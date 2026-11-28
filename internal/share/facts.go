package share

import (
	"context"
	"strings"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// Identity facts (docs/design.md, Sharing decisions between devices, What is shared): what this device's scans paired
// for a file with a shared ID (moved, renamed, edited) and its deletes by Filo, so another device follows the same
// file where its own scan could not (copies and a rename; an edit and a move), and closes a missing file as deleted
// by Filo instead of missing. A fact is kept like a decision (key fact:place or fact:deleted), so it waits until it
// can apply; two facts that disagree are ignored, and each device's own pairing stands.
const KindFact = "fact"

// Fact keys and what they say.
const (
	factPlace   = "fact:place"   // the file is now at its path, with its content
	factDeleted = "fact:deleted" // Filo deleted it on the device that wrote the line
)

// publishFacts adds the facts of this device's events after the events cursor to the pubs of the synced directories
// holding their files; it returns the last event dealt with: an edit not yet seen on two scans in a row stops it
// there (the file may still be arriving or being written), to be looked at again on a later run.
func publishFacts(ctx context.Context, s *store.Store, me string, after int64, dirs []store.SyncedDir,
	at func(store.SyncedDir) *pub, now time.Time) (int64, error) {
	evs, err := s.FileEventsSince(ctx, after)
	if err != nil {
		return after, err
	}
	upto := after
	for _, e := range evs {
		for _, sd := range dirs {
			rel, ok := relIn(sd, e.RootID, e.Path)
			if !ok {
				continue
			}
			fid, err := s.SharedID(ctx, sd.ID, e.FileID)
			if err != nil {
				return upto, err
			}
			if fid == "" {
				break // no decision about it was ever shared: nothing to follow
			}
			v, _, err := s.Values(ctx, e.FileID)
			if err != nil {
				return upto, err
			}
			key, value := factPlace, v.Hash+" "+rel
			skip := journal.CheckPath(rel) != nil // a name no line may carry: not shared, as for decisions
			switch e.Kind {
			case store.EventDeleted:
				key, value = factDeleted, "1"
			case "changed":
				seen, err := s.SeenSince(ctx, e.FileID, e.ScanID)
				if err != nil {
					return upto, err
				}
				if !seen {
					later, err := s.ScannedSince(ctx, e.RootID, e.ScanID)
					if err != nil {
						return upto, err
					}
					if !later && v.Present && !v.Deleted {
						return upto, nil // seen once only: maybe still being written; looked at after the next scan
					}
					skip = true // edited again since, or gone: a later event, or none, says where it is
				}
				arriving, err := expected(ctx, s, sd.ID, fid, v.Hash, me)
				if err != nil {
					return upto, err
				}
				skip = skip || arriving // a content another device decided about: the sync bringing it, not an edit
			}
			if pb := at(sd); pb != nil && !skip {
				if err := pb.fact(ctx, s, fid, key, value, v.Hash, rel, now); err != nil {
					return upto, err
				}
			}
			break
		}
		upto = e.ID
	}
	return upto, nil
}

// expected reports whether another device's current decision about fid names content hash (applied here or not):
// that content here is the sync bringing it, not an edit of this device's.
func expected(ctx context.Context, s *store.Store, syncedID int64, fid, hash, me string) (bool, error) {
	for _, key := range []string{keyDate, keyRetention, factPlace} {
		ds, err := s.CurrentDecisions(ctx, syncedID, fid, key)
		if err != nil {
			return false, err
		}
		for _, d := range ds {
			if d.Dev != me && (d.Hash == hash || (key == factPlace && strings.HasPrefix(d.Value, hash+" "))) {
				return true, nil
			}
		}
	}
	return false, nil
}

// fact adds a fact line about fid, replacing the facts of its key in effect (or the one this run wrote), unless one
// says the same already.
func (pb *pub) fact(ctx context.Context, s *store.Store, fid, key, value, hash, rel string, now time.Time) error {
	k := [2]string{fid, key}
	var refs []Ref
	var replaces []store.LineRef
	if ref, ok := pb.mine[k]; ok {
		if pb.value[k] == value {
			return nil
		}
		refs, replaces = []Ref{{Dev: ref.Dev, Seq: ref.Seq}}, []store.LineRef{ref}
	} else {
		cur, err := s.CurrentDecisions(ctx, pb.sd.ID, fid, key)
		if err != nil {
			return err
		}
		same := len(cur) > 0
		for _, d := range cur {
			same = same && d.Value == value
			refs = append(refs, Ref{Dev: d.Dev, Seq: d.Seq})
			replaces = append(replaces, store.LineRef{Dev: d.Dev, Seq: d.Seq})
		}
		if same {
			return nil
		}
	}
	raw, err := pb.w.Add(KindFact, FileBody{File: fid, Hash: hash, Path: rel, Replaces: refs, Value: value,
		What: strings.TrimPrefix(key, "fact:")}, now)
	if err != nil {
		return err
	}
	l := pb.w.Last()
	pb.addLine(raw, l)
	pb.ch.Decisions = append(pb.ch.Decisions, store.Decision{Dev: l.Dev, Seq: l.Seq, FID: fid, Key: key, Hash: hash,
		Path: rel, Value: value, Decided: l.Time, Current: true, Applied: true})
	pb.ch.Replaces = append(pb.ch.Replaces, replaces)
	pb.mine[k] = store.LineRef{Dev: l.Dev, Seq: l.Seq}
	pb.value[k] = value
	return nil
}

// applyFact acts on the facts in effect about fid (key): the file a place fact names becomes the shared ID's here
// when the one it was is missing (another device's scan paired what this one could not); a deleted fact closes the
// file as deleted by Filo once it is missing here (the sync deleted it). Facts that disagree are ignored. It reports
// whether the facts are done with (false: they wait, as for the sync to delete the file).
func applyFact(ctx context.Context, s *store.Store, sd store.SyncedDir, fid, key string, cur []store.Decision,
	now time.Time, again *bool) (bool, error) {
	if len(cur) == 0 {
		return true, nil
	}
	for _, d := range cur[1:] {
		if d.Value != cur[0].Value {
			return true, nil // two devices disagree: neither is followed
		}
	}
	mapped, _, err := s.SharedFile(ctx, sd.ID, fid)
	if err != nil {
		return false, err
	}
	var v store.FileValues
	if mapped != 0 {
		if v, _, err = s.Values(ctx, mapped); err != nil {
			return false, err
		}
	}
	switch key {
	case factDeleted:
		switch {
		case mapped == 0, v.Deleted:
			return true, nil
		case v.Present:
			return false, nil // the sync has not deleted it here yet
		}
		_, err := s.MarkDeletedElsewhere(ctx, mapped, now)
		return err == nil, err
	case factPlace:
		if mapped != 0 && v.Present {
			return true, nil // this device's own scan followed it
		}
		d := cur[0]
		id, err := s.MatchFile(ctx, sd, d.Hash, d.Path)
		if err != nil || id == 0 {
			return false, err // not here yet
		}
		remapped, err := s.RemapShared(ctx, sd.ID, fid, id)
		if remapped {
			*again = true
		}
		return err == nil, err
	}
	return true, nil
}
