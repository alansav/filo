package share

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Publish writes the decisions this device made since it last published (the review and tag histories, past the
// cursors) to its journal in each joined synced directory holding the file: one line per date, retention or tag that
// changed, each naming the decisions it replaces (those in effect for it here). A decision that applied another
// device's (command "journal") is never written again. The lines go to the register first, with the cursors, then to
// the disk. A directory where this device does not write (retired there, its journal ahead of the register) gets
// nothing, and the cursors still move on: seeding (on joining again) carries what is in effect. A directory that
// cannot be reached now keeps the cursors where they were, so the next run tries again (a decision written already is
// in effect, so it is not written twice). It returns the problems met, one line each.
func Publish(ctx context.Context, s *store.Store, now time.Time) ([]string, error) {
	cur, err := s.Cursors(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.Latest(ctx)
	if err != nil {
		return nil, err
	}
	reviews, err := s.ReviewsSince(ctx, cur.Reviews)
	if err != nil {
		return nil, err
	}
	tagEvs, err := s.TagChangesSince(ctx, cur.TagChanges)
	if err != nil {
		return nil, err
	}
	policyEvs, err := s.PolicyChangesSince(ctx, cur.PolicyChanges)
	if err != nil {
		return nil, err
	}
	dirs, err := joinedDirs(ctx, s)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, s.Publish(ctx, nil, latest)
	}
	me, err := s.Device(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, e := range reviews {
		ids = append(ids, e.FileID)
	}
	for _, e := range tagEvs {
		ids = append(ids, e.FileID)
	}
	places, err := s.FilePlaces(ctx, ids)
	if err != nil {
		return nil, err
	}
	var problems []string
	failed := false // a directory not written for a passing reason (not reachable): the cursors stay, to retry
	pubs := map[int64]*pub{}
	var order []*pub
	at := func(fileID int64) *pub {
		p, ok := places[fileID]
		if !ok {
			return nil
		}
		for _, sd := range dirs {
			if !sd.Contains(p.RootID, p.Path) {
				continue
			}
			if pb, ok := pubs[sd.ID]; ok {
				return pb
			}
			pb, problem, err := startPub(ctx, s, me, sd)
			if err != nil {
				problems = append(problems, sd.Abs()+": "+err.Error()+" (tried again on the next run)")
				failed = true
			} else if problem != "" {
				problems = append(problems, sd.Abs()+": "+problem)
				failed = true // its journal ahead or forked: written once that is settled
			}
			pubs[sd.ID] = pb // nil: this device does not write there
			if pb != nil {
				order = append(order, pb)
			}
			return pb
		}
		return nil
	}
	for _, e := range reviews {
		pb := at(e.FileID)
		if pb == nil || e.Hash == "" {
			continue
		}
		p := places[e.FileID]
		for _, kv := range []struct{ kind, key, value string }{
			{KindDate, keyDate, e.FileDate}, {KindRetention, keyRetention, e.Retention},
		} {
			if err := pb.decide(ctx, s, e.FileID, p, kv.kind, kv.key, kv.value, "", e.Hash, now); err != nil {
				return problems, err
			}
		}
	}
	for _, e := range tagEvs {
		pb := at(e.FileID)
		if pb == nil {
			continue
		}
		t := tags.Tag{Key: e.Key, Value: e.Value}
		value := ""
		if e.Added {
			value = t.String()
		}
		p := places[e.FileID]
		if err := pb.decide(ctx, s, e.FileID, p, KindTag, tagKey(t), value, t.String(), p.Hash, now); err != nil {
			return problems, err
		}
	}
	// Identity facts: what this device's scans paired, and its deletes by Filo, for files with a shared ID.
	pubFor := func(sd store.SyncedDir) *pub {
		if pb, ok := pubs[sd.ID]; ok {
			return pb
		}
		pb, problem, err := startPub(ctx, s, me, sd)
		if err != nil {
			problems = append(problems, sd.Abs()+": "+err.Error()+" (tried again on the next run)")
			failed = true
		} else if problem != "" {
			problems = append(problems, sd.Abs()+": "+problem)
			failed = true
		}
		pubs[sd.ID] = pb
		if pb != nil {
			order = append(order, pb)
		}
		return pb
	}
	upto, err := publishFacts(ctx, s, me.ID, cur.Events, dirs, pubFor, now)
	if err != nil {
		return problems, err
	}
	latest.Events = upto
	// Settings (policies, rules, duplicates decisions, tag indexes): what each synced directory's journal has in
	// effect, against what this device holds; and the policies moved, in every one.
	var moves []store.PolicyEvent
	for _, e := range policyEvs {
		if e.Kind == "rename" || e.Kind == "move" {
			moves = append(moves, e)
		}
	}
	for _, sd := range dirs {
		if !s.Changed() && len(moves) == 0 {
			break // a run that changed nothing has no setting of its own to write
		}
		pb, started := pubs[sd.ID]
		if !started {
			// Settings only: a directory not reachable now is left quietly; what differs is written when it is.
			pb, _, _ = startPub(ctx, s, me, sd)
			pubs[sd.ID] = pb
			if pb != nil {
				order = append(order, pb)
			}
		}
		if pb == nil {
			continue
		}
		if err := pb.publishMoves(moves, now); err != nil {
			return problems, err
		}
		if err := pb.publishSettings(ctx, s, me, now); err != nil {
			return problems, err
		}
	}
	changes := make([]store.SyncedChange, 0, len(order))
	for _, pb := range order {
		changes = append(changes, pb.ch)
	}
	if failed {
		latest = cur // what was written now is not written again: a decision in effect already is skipped
	}
	if err := s.Publish(ctx, changes, latest); err != nil {
		return problems, err
	}
	for _, pb := range order {
		for _, sv := range pb.seen {
			if err := s.SetSeen(ctx, sv.id, sv.key, sv.value); err != nil {
				return problems, err
			}
		}
	}
	for _, pb := range order {
		if err := pb.write(); err != nil {
			problems = append(problems, pb.sd.Abs()+": "+err.Error()+" (written back on the next run)")
		}
	}
	return problems, nil
}

// joinedDirs returns the joined synced directories.
func joinedDirs(ctx context.Context, s *store.Store) ([]store.SyncedDir, error) {
	all, err := s.SyncedDirs(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.SyncedDir
	for _, sd := range all {
		if sd.State == store.SyncedJoined {
			out = append(out, sd)
		}
	}
	return out, nil
}

// pub is what one Publish writes to one synced directory: this device's new lines, and their decisions.
type pub struct {
	sd    store.SyncedDir
	w     *journal.Writer
	kept  []store.JournalLine // this device's lines as kept before
	ch    store.SyncedChange
	ju    store.JournalUpdate
	mine  map[[2]string]store.LineRef // the line this run wrote for a (fid, key), which the next one replaces
	value map[[2]string]string        // and its value
	fids  map[int64]string
	fresh map[string]int64 // shared IDs made this run, not recorded until a line about the file is written
	// seeding: the lines are decisions made before this device shared the directory: they replace nothing
	seeding bool
	seen    []seenValue // setting values written, recorded as seen once committed
}

// startPub checks this device's journal in sd (written back if it fell short) and starts writing after its last
// line; nil, with why, where this device writes nothing.
func startPub(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir) (*pub, string, error) {
	js, err := s.Journals(ctx, sd.ID)
	if err != nil {
		return nil, "", err
	}
	for _, j := range js {
		if j.Dev == me.ID && j.RetiredSeq > 0 {
			return nil, "", nil // retired there: Import says so
		}
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	if _, problem, err := checkOwn(ctx, s, d, sd.ID, me); err != nil || problem != "" {
		return nil, problem, err
	}
	kept, err := s.JournalLines(ctx, sd.ID, me.ID)
	if err != nil || len(kept) == 0 {
		return nil, "", err
	}
	last, err := journal.Parse(kept[len(kept)-1].Raw)
	if err != nil {
		return nil, "", err
	}
	w, err := journal.Resume(me.Key, last)
	if err != nil {
		return nil, "", err
	}
	size := int64(len(ownBytes(kept)))
	return &pub{sd: sd, w: w, kept: kept, ch: store.SyncedChange{SyncedID: sd.ID, Shared: map[string]int64{}},
		ju:   store.JournalUpdate{Dev: me.ID, Seq: last.Seq, Offset: size},
		mine: map[[2]string]store.LineRef{}, value: map[[2]string]string{}, fids: map[int64]string{},
		fresh: map[string]int64{}}, "", nil
}

// decide adds a line for (the file's) key if value differs from what is in effect for it here.
func (pb *pub) decide(ctx context.Context, s *store.Store, fileID int64, p store.FilePlace, kind, key, value, tag,
	hash string, now time.Time) error {
	rel := strings.TrimPrefix(strings.TrimPrefix(p.Path, pb.sd.Dir), "/")
	if journal.CheckPath(rel) != nil {
		return nil // a name no line may carry (a control character in it): not shared
	}
	fid, err := pb.fid(ctx, s, fileID, hash, rel)
	if err != nil {
		return err
	}
	k := [2]string{fid, key}
	var replaces []store.LineRef
	if ref, ok := pb.mine[k]; ok {
		if pb.value[k] == value {
			return nil
		}
		replaces = []store.LineRef{ref}
	} else {
		cur, err := s.CurrentDecisions(ctx, pb.sd.ID, fid, key)
		if err != nil {
			return err
		}
		same := len(cur) > 0
		for _, d := range cur {
			same = same && d.Value == value
			replaces = append(replaces, store.LineRef{Dev: d.Dev, Seq: d.Seq})
		}
		if same || (len(cur) == 0 && value == "") {
			return nil // already in effect here; or nothing decided, and nothing to say
		}
		if pb.seeding {
			if value == "" {
				return nil // seeding writes what this device decided; nothing decided is not a decision
			}
			replaces = ownRefs(replaces, pb.ju.Dev) // another device's: a conflict; this one's own: replaced
		}
	}
	refs := make([]Ref, len(replaces))
	for i, r := range replaces {
		refs[i] = Ref{Dev: r.Dev, Seq: r.Seq}
	}
	raw, err := pb.w.Add(kind, FileBody{File: fid, Hash: hash, Path: rel, Replaces: refs, Value: value, Tag: tag}, now)
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
	if f, ok := pb.fresh[fid]; ok {
		pb.ch.Shared[fid] = f
		delete(pb.fresh, fid)
	}
	return nil
}

// ownRefs keeps of refs those of device me.
func ownRefs(refs []store.LineRef, me string) []store.LineRef {
	var out []store.LineRef
	for _, r := range refs {
		if r.Dev == me {
			out = append(out, r)
		}
	}
	return out
}

// addLine adds a line just made to what pb writes.
func (pb *pub) addLine(raw []byte, l journal.Line) {
	pb.ju.Lines = append(pb.ju.Lines, store.JournalLine{Seq: l.Seq, Raw: raw})
	pb.ju.Seq, pb.ju.Offset = l.Seq, pb.ju.Offset+int64(len(raw)+1)
	pb.ch.Journals = []store.JournalUpdate{pb.ju}
}

// fid is the file's shared ID in the synced directory: the one it has, or a new one, made from the synced directory,
// the file's content and its path, so two devices deciding about the same file before either has read the other
// give it the same ID; a random one if that is another file's already (a later file with the same content at the
// same path).
func (pb *pub) fid(ctx context.Context, s *store.Store, fileID int64, hash, rel string) (string, error) {
	if fid, ok := pb.fids[fileID]; ok {
		return fid, nil
	}
	fid, err := s.SharedID(ctx, pb.sd.ID, fileID)
	if err != nil {
		return "", err
	}
	if fid == "" {
		fid = derivedID(pb.sd.SyncID, hash, rel)
		other, known, err := s.SharedFile(ctx, pb.sd.ID, fid)
		if err != nil {
			return "", err
		}
		if known && other != 0 && other != fileID {
			if fid, err = device.NewID(); err != nil {
				return "", err
			}
		}
		pb.fresh[fid] = fileID // recorded with the first line written about it, not before
	}
	pb.fids[fileID] = fid
	return fid, nil
}

// derivedID is the shared ID a file first decided about gets: the first 128 bits of a SHA-256 of the synced
// directory's ID, the file's content hash and its path, in a device ID's form.
func derivedID(syncID, hash, rel string) string {
	sum := sha256.Sum256([]byte("filo file\x00" + syncID + "\x00" + hash + "\x00" + rel))
	return idEncoding.EncodeToString(sum[:16])
}

// idEncoding is a device ID's: lower-case base32, no padding.
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// write appends the lines to this device's journal on the disk (written whole again if the file is not where the
// register left it).
func (pb *pub) write() error {
	if len(pb.ju.Lines) == 0 {
		return nil
	}
	d, err := journal.Open(pb.sd.Abs())
	if err != nil {
		return err
	}
	defer d.Close()
	name := journal.JournalName(pb.ju.Dev)
	add := make([][]byte, len(pb.ju.Lines))
	for i, l := range pb.ju.Lines {
		add[i] = l.Raw
	}
	err = d.Append(name, int64(len(ownBytes(pb.kept))), add)
	if errors.Is(err, journal.ErrNotAt) {
		err = d.Replace(name, append(toLines(pb.kept), add...))
	}
	return err
}
