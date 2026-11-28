package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// TakeOwn takes in the lines this device's journal in sd holds past what the register does: the register was
// restored from a snapshot or a backup, and those are decisions this device made after it (docs/design.md, Identity
// cases). They are kept, and applied here like another device's, so the register catches up. It returns how many
// lines it took in.
func TakeOwn(ctx context.Context, s *store.Store, sd store.SyncedDir, now time.Time) (int, error) {
	me, err := s.Device(ctx)
	if err != nil {
		return 0, err
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return 0, err
	}
	defer d.Close()
	data, err := d.Read(journal.JournalName(me.ID), 0)
	if err != nil {
		return 0, err
	}
	res := journal.Check(data, me.ID)
	if len(res.Lines) == 0 || !res.Key.Equal(me.Public()) {
		return 0, errors.New("this device's journal there is not signed with its key")
	}
	kept, err := s.JournalLines(ctx, sd.ID, me.ID)
	if err != nil {
		return 0, err
	}
	if len(res.Lines) <= len(kept) {
		return 0, nil
	}
	for i, k := range kept {
		if !bytes.Equal(res.Lines[i].Raw, k.Raw) {
			return 0, fmt.Errorf("line %d of this device's journal is not the register's: two machines write as "+
				"this device (filo device new gives this one a new ID)", k.Seq)
		}
	}
	known, err := knownDevices(ctx, s)
	if err != nil {
		return 0, err
	}
	states, err := s.Journals(ctx, sd.ID)
	if err != nil {
		return 0, err
	}
	state := map[string]store.JournalState{}
	for _, j := range states {
		state[j.Dev] = j
	}
	base := int64(0)
	for _, k := range kept {
		base += int64(len(k.Raw) + 1)
	}
	r := &read{dev: me.ID, base: base, lines: res.Lines[len(kept):]}
	devs := map[string]store.KnownDevice{}
	for k, v := range known {
		devs[k] = v
	}
	retired, _ := settle([]*read{r}, state, me.ID, devs, now)
	ch := store.SyncedChange{SyncedID: sd.ID, Journals: []store.JournalUpdate{r.update(state[me.ID], 0)}}
	for dev, seq := range retired {
		if dev != me.ID && seq != state[dev].RetiredSeq {
			st := state[dev]
			ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: dev, Seq: st.Seq, Offset: st.Offset,
				Problem: st.Problem, RetiredSeq: seq})
		}
	}
	name := func(dev string) string { return nameOf(devs, dev) }
	fileDecisions(r.lines, sd.ID, name, &ch) // this device's own, not applied here yet: applied below
	if _, err := settingDecisions(ctx, s, r.lines, name, &ch, now, true); err != nil {
		return 0, err
	}
	for dev, kd := range devs {
		if old, ok := known[dev]; !ok || old != kd {
			ch.Devices = append(ch.Devices, kd)
		}
	}
	if _, err := s.ApplySynced(ctx, ch); err != nil {
		return 0, err
	}
	if _, _, err := applyDecisions(ctx, s, sd, now); err != nil {
		return 0, err
	}
	return len(r.lines), nil
}

// Restore writes back the journal of device kd (not this one) in each synced directory in where it is missing, or
// holds fewer good lines than the register keeps of it (cut, or damaged): the same lines, byte for byte, so their
// signatures still check (docs/design.md, Losing a journal). A journal with more good lines than the register's, or
// one that differs from them, is left alone. It returns the directories written to.
func Restore(ctx context.Context, s *store.Store, kd store.KnownDevice, in []store.SyncedDir) ([]store.SyncedDir,
	error) {
	me, err := s.Device(ctx)
	if err != nil {
		return nil, err
	}
	if kd.Dev == me.ID {
		return nil, errors.New("this device's own journal is written back by every run: nothing to restore")
	}
	var done []store.SyncedDir
	for _, sd := range in {
		ok, err := restoreOne(ctx, s, sd, kd.Dev)
		if err != nil {
			return done, fmt.Errorf("%s: %w", sd.Abs(), err)
		}
		if ok {
			done = append(done, sd)
		}
	}
	return done, nil
}

// NeedsRestore reports whether device dev's journal in sd is missing or short of the lines the register keeps.
func NeedsRestore(ctx context.Context, s *store.Store, sd store.SyncedDir, dev string) (bool, error) {
	kept, data, missing, err := restoreState(ctx, s, sd, dev)
	if err != nil || len(kept) == 0 {
		return false, err
	}
	if missing {
		return true, nil
	}
	res := journal.Check(data, dev)
	return shortOf(res.Lines, kept), nil
}

func restoreState(ctx context.Context, s *store.Store, sd store.SyncedDir, dev string) ([]store.JournalLine, []byte,
	bool, error) {
	kept, err := s.JournalLines(ctx, sd.ID, dev)
	if err != nil || len(kept) == 0 {
		return nil, nil, false, err
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return nil, nil, false, err
	}
	defer d.Close()
	name := journal.JournalName(dev)
	if ok, err := d.Exists(name); err != nil {
		return nil, nil, false, err
	} else if !ok {
		return kept, nil, true, nil
	}
	data, err := d.Read(name, 0)
	return kept, data, false, err
}

// shortOf reports whether lines are a beginning of kept, and fewer.
func shortOf(lines []journal.Line, kept []store.JournalLine) bool {
	if len(lines) >= len(kept) {
		return false
	}
	for i, l := range lines {
		if !bytes.Equal(l.Raw, kept[i].Raw) {
			return false
		}
	}
	return true
}

func restoreOne(ctx context.Context, s *store.Store, sd store.SyncedDir, dev string) (bool, error) {
	kept, data, missing, err := restoreState(ctx, s, sd, dev)
	if err != nil || len(kept) == 0 {
		return false, err
	}
	if !missing && !shortOf(journal.Check(data, dev).Lines, kept) {
		return false, nil
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return false, err
	}
	defer d.Close()
	if missing {
		return true, d.Create(journal.JournalName(dev), toLines(kept))
	}
	return true, d.Replace(journal.JournalName(dev), toLines(kept))
}
