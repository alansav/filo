package share

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// fileDecisions reads the file decision lines among lines (of trusted devices, cut at any retirement) into ch:
// each kept with the lines it replaces. A line that does not check is refused (a problem), never applied.
func fileDecisions(lines []journal.Line, syncedID int64, name func(dev string) string,
	ch *store.SyncedChange) []string {
	var problems []string
	for _, l := range lines {
		if !IsFileKind(l.Kind) {
			continue
		}
		d, refs, err := readDecision(syncedID, l)
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d of %s's journal is not applied: %v", l.Seq,
				name(l.Dev), err))
			continue
		}
		d.Decided = l.Time
		ch.Decisions = append(ch.Decisions, d)
		ch.Replaces = append(ch.Replaces, refs)
	}
	return problems
}

// applyDecisions applies the current decisions of synced directory sd not applied yet: for each file and thing
// decided, the file is found (by its shared ID, or the first time by content and path: store.MatchFile, never a
// guess) and given the value in effect, or the safe one in a conflict. A date or a retention applies only to the
// content it was decided on: until that content is here, it waits (an expectation), and so does a decision whose
// file is not found yet. Applied changes are recorded with command "journal", so they are never written to this
// device's journal again. It returns how many decisions wait, and the problems met.
func applyDecisions(ctx context.Context, s *store.Store, sd store.SyncedDir, now time.Time) (int, []string, error) {
	un, err := s.Unapplied(ctx, sd.ID)
	if err != nil {
		return 0, nil, err
	}
	type group struct {
		fid, key string
		refs     []store.LineRef
		last     store.Decision
	}
	var groups []*group
	byKey := map[[2]string]*group{}
	for _, d := range un {
		k := [2]string{d.FID, d.Key}
		g, ok := byKey[k]
		if !ok {
			g = &group{fid: d.FID, key: d.Key}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.refs = append(g.refs, store.LineRef{Dev: d.Dev, Seq: d.Seq})
		g.last = d
	}
	waiting := 0
	var problems []string
	today := retention.Today(now)
	for _, g := range groups {
		fileID, err := matchShared(ctx, s, sd, g.fid, g.last)
		if err != nil {
			return waiting, problems, err
		}
		if fileID == 0 {
			waiting += len(g.refs)
			continue
		}
		cur, err := s.CurrentDecisions(ctx, sd.ID, g.fid, g.key)
		if err != nil {
			return waiting, problems, err
		}
		v, ok, err := s.Values(ctx, fileID)
		if err != nil {
			return waiting, problems, err
		}
		if !ok {
			waiting += len(g.refs) // deleted, or gone: nothing to apply to
			continue
		}
		done, problem, err := applyOne(ctx, s, fileID, g.key, cur, v, now, today)
		if err != nil {
			return waiting, problems, err
		}
		if problem != "" {
			problems = append(problems, problem)
		}
		if !done {
			waiting += len(g.refs)
			continue
		}
		if err := s.MarkApplied(ctx, sd.ID, g.refs); err != nil {
			return waiting, problems, err
		}
	}
	return waiting, problems, nil
}

// matchShared returns the file a shared ID is here, matching it the first time from decision d's content and path
// (0: not found, or not one).
func matchShared(ctx context.Context, s *store.Store, sd store.SyncedDir, fid string, d store.Decision) (int64, error) {
	fileID, _, err := s.SharedFile(ctx, sd.ID, fid)
	if err != nil || fileID != 0 {
		return fileID, err
	}
	fileID, err = s.MatchFile(ctx, sd, d.Hash, d.Path)
	if err != nil || fileID == 0 {
		return 0, err
	}
	if other, err := s.SharedID(ctx, sd.ID, fileID); err != nil || other != "" {
		return 0, err // the file has another shared ID here: never two
	}
	_, err = s.ApplySynced(ctx, store.SyncedChange{SyncedID: sd.ID, Shared: map[string]int64{fid: fileID}})
	if err != nil {
		return 0, err
	}
	return fileID, nil
}

// applyOne gives file fileID (values v) the value in effect for key from its current decisions. It reports whether
// the decisions are applied (false: they wait for their content), and a problem for the person.
func applyOne(ctx context.Context, s *store.Store, fileID int64, key string, cur []store.Decision,
	v store.FileValues, now time.Time, today string) (bool, string, error) {
	keepUntil := func(k, value string) string {
		date, ret := v.FileDate, v.Retention
		if k == keyDate {
			date = value
		} else {
			ret = value
		}
		if ret == "" {
			return ""
		}
		if store.IsPolicyRef(ret) {
			p, err := s.Policy(ctx, ret)
			if err != nil || p.Value == "" {
				return ""
			}
			ret = p.Value
		}
		u, err := store.KeepUntil(date, ret)
		if err != nil {
			return ""
		}
		return u
	}
	value, _ := effective(key, cur, keepUntil)
	if strings.HasPrefix(key, keyTagPrefix) {
		var t tags.Tag
		for _, d := range cur {
			if tg, err := tagOf(d); err == nil {
				t = tg
				if d.Value != "" {
					break
				}
			}
		}
		if t.Value == "" && t.Key == "" {
			return true, "", nil
		}
		if value != "" {
			if tg, err := tags.Parse(value); err == nil {
				t = tg
			}
		}
		_, err := s.JournalTag(ctx, fileID, t, value != "", now)
		return err == nil, "", err
	}
	// A date or a retention: only on the content it was decided on.
	matches := false
	for _, d := range cur {
		matches = matches || d.Hash == v.Hash
	}
	if !matches || v.Hash == "" {
		return false, "", nil
	}
	sv := store.SetValues{}
	switch key {
	case keyDate:
		if value == v.FileDate {
			return true, "", nil
		}
		sv.FileDate, sv.SetFileDate = value, true
	case keyRetention:
		if value == v.Retention {
			return true, "", nil
		}
		sv.Retention, sv.SetRetention = value, true
	default:
		return true, "", nil
	}
	res, err := s.SetFiles(ctx, []int64{fileID}, sv, now, today, store.SetOptions{Command: store.CommandJournal,
		Confirm: func(store.SetResult) bool { return true }})
	if err != nil {
		return false, "", err
	}
	if len(res.Refusals) > 0 {
		return false, fmt.Sprintf("%s: %s %s not applied yet: %s", res.Refusals[0].Path, key, value,
			res.Refusals[0].Reason), nil
	}
	return true, "", nil
}

// tagOf is the tag a tag decision is about, from its key: "tag:" key ":" folded value. The spelling comes from the
// value when it is added.
func tagOf(d store.Decision) (tags.Tag, error) {
	if d.Value != "" {
		return tags.Parse(d.Value)
	}
	rest := strings.TrimPrefix(d.Key, keyTagPrefix)
	k, folded, ok := strings.Cut(rest, ":")
	if !ok {
		return tags.Tag{}, fmt.Errorf("not a tag's key: %s", d.Key)
	}
	return tags.Tag{Key: k, Value: folded}, nil
}
