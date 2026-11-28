package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// DirReport is what importing found in one joined synced directory.
type DirReport struct {
	Dir      store.SyncedDir
	Problems []string             // each a line for the person: a journal stopped, the directory not found, ...
	Notes    []string             // done without asking: this device's journal or the mark written back
	Others   []journal.Entry      // names in .filo/ that are not Filo's
	Strange  []store.KnownDevice  // devices whose journal is there that this device does not trust
	Journals []store.JournalState // after the import, each device's
	// Status is each trusted device's status file (this device's too), read and checked: when it last ran there,
	// and the highest line it holds of each journal. A device with none, or one that does not check, is not in it.
	Status  map[string]journal.StatusBody
	Writing bool // this device writes there (false: retired there, or its journal is not its own)
	// Waiting counts the other devices' decisions not applied yet: their file not found here yet, or its content
	// not yet the one they were made on (the sync has not brought it). Conflicts counts the things decided (a file's
	// date, retention or a tag) with current decisions of different values.
	Waiting, Conflicts int
}

// Report is what Import found, a DirReport per joined synced directory.
type Report struct{ Dirs []DirReport }

// Problems counts the problems in all directories.
func (r Report) Problems() int {
	n := 0
	for _, d := range r.Dirs {
		n += len(d.Problems)
	}
	return n
}

// Import reads the journals of every joined synced directory into the register: the new lines of each trusted
// device, the trust, retire and name lines among them, the status files; it writes this device's journal back if
// the file is not what the register holds, the mark back if it is lost, and this device's status file at most
// hourly. It writes to the register only when something changed. A problem with one directory is reported in its
// DirReport, never stops the others.
func Import(ctx context.Context, s *store.Store, now time.Time) (Report, error) {
	me, err := s.Device(ctx)
	if err != nil {
		return Report{}, err
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	for _, sd := range dirs {
		if sd.State != store.SyncedJoined {
			continue
		}
		dr, err := importDir(ctx, s, me, sd, now)
		if err != nil {
			return rep, err
		}
		if dr.Journals, err = s.Journals(ctx, sd.ID); err != nil {
			return rep, err
		}
		rep.Dirs = append(rep.Dirs, dr)
	}
	return rep, nil
}

func importDir(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir, now time.Time) (DirReport,
	error) {
	dr := DirReport{Dir: sd, Status: map[string]journal.StatusBody{}}
	abs := sd.Abs()
	if root, err := s.RootAt(ctx, sd.RootPath); err != nil {
		return dr, err
	} else if err := store.CheckRoot(root); err != nil {
		dr.Problems = append(dr.Problems, "not read: "+err.Error())
		return dr, nil
	}
	if !dirExists(abs) {
		dr.Problems = append(dr.Problems, "not found at "+abs+": moved, or not synced here yet (a scan that finds it "+
			"elsewhere says so)")
		return dr, nil
	}
	// Its mark (or all of .filo) gone from its place, while a scan found it elsewhere: it may have moved, so nothing
	// is written here until the person says.
	if there, err := markAt(ctx, s, sd); err != nil {
		dr.Problems = append(dr.Problems, "not read: "+err.Error())
		return dr, nil
	} else if !there {
		if elsewhere, err := foundElsewhere(ctx, s, sd); err != nil {
			return dr, err
		} else if elsewhere != "" {
			dr.Problems = append(dr.Problems, "its mark is gone from here and was found at "+elsewhere+": if it "+
				"moved, filo synced add "+elsewhere+" says so; nothing is written here meanwhile")
			return dr, nil
		}
	}
	d, err := journal.Make(abs) // a .filo lost as a whole is made again: the mark and this device's journal follow
	if err != nil {
		dr.Problems = append(dr.Problems, err.Error())
		return dr, nil
	}
	defer d.Close()

	// The mark: written back if lost; another synced directory's mark here stops everything.
	if ok, err := d.Exists(journal.MarkName); err != nil {
		dr.Problems = append(dr.Problems, err.Error())
		return dr, nil
	} else if !ok {
		if err := d.Create(journal.MarkName, [][]byte{sd.Mark}); err != nil {
			dr.Problems = append(dr.Problems, err.Error())
			return dr, nil
		}
		dr.Notes = append(dr.Notes, "its mark was lost: written back")
	} else if !holdsMark(abs, sd.SyncID) {
		dr.Problems = append(dr.Problems, abs+" holds another synced directory's mark, or one Filo cannot read: "+
			"nothing read or written there")
		return dr, nil
	}

	known, err := knownDevices(ctx, s)
	if err != nil {
		return dr, err
	}
	states, err := s.Journals(ctx, sd.ID)
	if err != nil {
		return dr, err
	}
	state := map[string]store.JournalState{}
	for _, j := range states {
		state[j.Dev] = j
	}
	entries, err := d.List()
	if err != nil {
		dr.Problems = append(dr.Problems, err.Error())
		return dr, nil
	}
	ch := store.SyncedChange{SyncedID: sd.ID}
	var reads []*read
	devs := map[string]store.KnownDevice{}
	for k, v := range known {
		devs[k] = v
	}
	present := map[string]bool{}

	// This device's journal first: it must be what the register holds.
	mine := state[me.ID]
	dr.Writing = mine.RetiredSeq == 0
	if !dr.Writing {
		dr.Problems = append(dr.Problems, fmt.Sprintf("%s retired this device here: it writes nothing more; to "+
			"share decisions again, filo device new, then join again", nameOf(devs, devs[me.ID].RetiredBy)))
	}
	if dr.Writing {
		note, problem, err := checkOwn(ctx, s, d, sd.ID, me)
		if err != nil {
			return dr, err
		}
		if note != "" {
			dr.Notes = append(dr.Notes, note)
		}
		if problem != "" {
			dr.Problems = append(dr.Problems, problem)
			dr.Writing = false
		}
	}

	for _, e := range entries {
		switch {
		case e.Kind == journal.Other:
			dr.Others = append(dr.Others, e)
			continue
		case e.Kind != journal.Journal || e.Dev == me.ID:
			continue
		}
		present[e.Dev] = true
		kd, isKnown := devs[e.Dev]
		if !isKnown || !kd.Trusted() {
			if nd, ok := firstLine(d, e); ok {
				if !isKnown || kd.Key != nd.Key || kd.Name != nd.Name {
					devs[e.Dev] = nd
				}
				dr.Strange = append(dr.Strange, nd)
			} else {
				dr.Others = append(dr.Others, journal.Entry{Name: e.Name, Kind: journal.Other, Dev: e.Dev,
					Note: "a journal whose first line cannot be read"})
			}
			continue
		}
		if r, problem := readJournal(ctx, s, d, sd.ID, e, kd, state[e.Dev]); r != nil {
			reads = append(reads, r)
		} else if problem != "" {
			dr.Problems = append(dr.Problems, nameOf(devs, e.Dev)+"'s journal: "+problem)
		}
	}
	// The retirements among what was read, whichever journal holds them; each journal cut at its own; then the
	// register's change for each journal read, and for each device newly retired here, from its stored state.
	retired, notes := settle(reads, state, me.ID, devs, now)
	dr.Problems = append(dr.Problems, notes...)
	name := func(dev string) string { return nameOf(devs, dev) }
	for _, r := range reads {
		dr.Problems = append(dr.Problems, fileDecisions(r.lines, sd.ID, name, &ch)...)
		ps, err := settingDecisions(ctx, s, r.lines, name, &ch, now)
		if err != nil {
			return dr, err
		}
		dr.Problems = append(dr.Problems, ps...)
	}
	updated := map[string]bool{}
	for _, r := range reads {
		if r.problem != "" {
			dr.Problems = append(dr.Problems, nameOf(devs, r.dev)+"'s journal: "+r.problem)
		}
		st := state[r.dev]
		if len(r.lines) > 0 || r.problem != st.Problem || retired[r.dev] != st.RetiredSeq {
			ch.Journals = append(ch.Journals, r.update(st, retired[r.dev]))
			updated[r.dev] = true
		}
	}
	for dev, seq := range retired {
		if st := state[dev]; !updated[dev] && seq != st.RetiredSeq {
			ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: dev, Seq: st.Seq, Offset: st.Offset,
				Problem: st.Problem, RetiredSeq: seq})
		}
	}
	// A trusted device's journal kept but gone from the directory.
	for dev, j := range state {
		if dev != me.ID && j.Seq > 0 && !present[dev] && devs[dev].Trusted() {
			dr.Problems = append(dr.Problems, nameOf(devs, dev)+"'s journal is missing from "+d.Path())
		}
	}
	// The status files of trusted devices, this one's too: when each last ran there.
	for _, e := range entries {
		if e.Kind != journal.Status || !devs[e.Dev].Trusted() {
			continue
		}
		key, err := device.DecodePublic(devs[e.Dev].Key)
		if err != nil {
			continue
		}
		if st, ok := readStatus(d, e.Name, key); ok {
			dr.Status[e.Dev] = st
		}
	}
	for dev, kd := range devs {
		if old, ok := known[dev]; !ok || old != kd {
			ch.Devices = append(ch.Devices, kd)
		}
	}
	slices.SortFunc(ch.Devices, func(a, b store.KnownDevice) int { return strings.Compare(a.Dev, b.Dev) })
	if _, err := s.ApplySynced(ctx, ch); err != nil {
		return dr, err
	}
	if retired[me.ID] > 0 {
		dr.Writing = false
	}
	waiting, problems, err := applyDecisions(ctx, s, sd, now)
	if err != nil {
		return dr, err
	}
	dr.Waiting, dr.Problems = waiting, append(dr.Problems, problems...)
	if c, err := s.Conflicts(ctx, sd.ID); err != nil {
		return dr, err
	} else {
		dr.Conflicts = len(c)
	}
	if dr.Writing && (len(ch.Journals) > 0 || statusDue(dr.Status[me.ID].Seen, now)) {
		if err := writeStatus(ctx, s, d, sd.ID, me, now); err != nil {
			dr.Problems = append(dr.Problems, err.Error())
		} else if st, ok := readStatus(d, journal.StatusName(me.ID), me.Public()); ok {
			dr.Status[me.ID] = st
		}
	}
	return dr, nil
}

// foundElsewhere returns where a scan found sd's mark other than at its place ("" if nowhere).
func foundElsewhere(ctx context.Context, s *store.Store, sd store.SyncedDir) (string, error) {
	found, err := s.FoundMarks(ctx)
	if err != nil {
		return "", err
	}
	for _, m := range found {
		if m.SyncID == sd.SyncID && !(m.RootID == sd.RootID && m.Dir == sd.Dir) {
			return m.Abs(), nil
		}
	}
	return "", nil
}

// statusDue reports whether this device's status file is to be written when nothing new was read: never written,
// or an hour ago or more.
func statusDue(seen string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, seen)
	return err != nil || now.Sub(t) >= statusEvery
}

// firstLine reads a journal's line 1: the device it introduces, not trusted.
func firstLine(d *journal.Dir, e journal.Entry) (store.KnownDevice, bool) {
	data, err := d.Read(e.Name, 0)
	if err != nil {
		return store.KnownDevice{}, false
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	}
	res := journal.Check(data, e.Dev)
	if len(res.Lines) == 0 {
		return store.KnownDevice{}, false
	}
	return store.KnownDevice{Dev: e.Dev, Key: device.EncodePublic(res.Key), Name: res.Device.Name,
		CreatedUTC: res.Device.Created}, true
}

// readJournal reads the new lines of trusted device kd's journal: from where reading stopped, or from the start if
// the journal changed under it (then every line kept must still be there as it was). It returns what was read (nil:
// nothing new, and the same problem as before, which it returns), with any problem in it.
func readJournal(ctx context.Context, s *store.Store, d *journal.Dir, syncedID int64, e journal.Entry,
	kd store.KnownDevice, st store.JournalState) (*read, string) {
	key, err := device.DecodePublic(kd.Key)
	if err != nil {
		return problemRead(st, e.Dev, "its trusted key cannot be read")
	}
	if st.Seq > 0 && len(st.Last) > 0 {
		if last, err := journal.Parse(st.Last); err == nil {
			if start := st.Offset - int64(len(st.Last)) - 1; start >= 0 {
				if tail, err := d.Read(e.Name, start); err == nil {
					res, err := journal.CheckFrom(tail, journal.Point{Offset: st.Offset, Last: last, Key: key})
					if err == nil {
						return newRead(st, e.Dev, st.Offset, res.Lines, stopText(res))
					}
				}
			}
		}
	}
	data, err := d.Read(e.Name, 0)
	if err != nil {
		return problemRead(st, e.Dev, err.Error())
	}
	res := journal.Check(data, e.Dev)
	if len(res.Lines) > 0 && !res.Key.Equal(key) {
		return problemRead(st, e.Dev, "it is signed with another key than the one trusted: not read")
	}
	kept, err := s.JournalLines(ctx, syncedID, e.Dev)
	if err != nil {
		return problemRead(st, e.Dev, err.Error())
	}
	if len(res.Lines) < len(kept) {
		return problemRead(st, e.Dev, fmt.Sprintf("it holds %d lines, fewer than the %d read before: its device "+
			"writes it back when it next runs", len(res.Lines), len(kept)))
	}
	base := int64(0)
	for i, k := range kept {
		if !bytes.Equal(res.Lines[i].Raw, k.Raw) {
			return problemRead(st, e.Dev, fmt.Sprintf("line %d is not the one read before: nothing more read", k.Seq))
		}
		base += int64(len(k.Raw) + 1)
	}
	return newRead(st, e.Dev, base, res.Lines[len(kept):], stopText(res))
}

// newRead is what a read found: nil when it found nothing new and the problem is the one kept.
func newRead(st store.JournalState, dev string, base int64, lines []journal.Line, problem string) (*read, string) {
	if len(lines) == 0 && problem == st.Problem && st.RetiredSeq == 0 {
		return nil, problem
	}
	return &read{dev: dev, base: base, lines: lines, problem: problem}, problem
}

// problemRead is a journal that could not be read on: nothing read, only its problem.
func problemRead(st store.JournalState, dev, problem string) (*read, string) {
	if problem == st.Problem {
		return nil, problem
	}
	return &read{dev: dev, base: st.Offset, problem: problem}, problem
}

// ErrRetiredHere is a synced directory where this device was retired: it writes nothing more there.
var ErrRetiredHere = errors.New("this device was retired there")

// ErrAhead is this device's journal holding lines its register does not: the register was restored from a
// snapshot or a backup, or copied from another machine (docs/design.md, Identity cases).
var ErrAhead = errors.New("this device's journal holds lines this register does not")

// checkOwn compares this device's journal with what the register holds and writes it back when it falls short
// (lost, cut, a line added or changed by something else). A journal holding more of this device's own signed lines
// than the register, or another line under one of its numbers, is a problem: nothing is written there.
func checkOwn(ctx context.Context, s *store.Store, d *journal.Dir, syncedID int64, me store.Device) (note,
	problem string, err error) {
	kept, err := s.JournalLines(ctx, syncedID, me.ID)
	if err != nil {
		return "", "", err
	}
	want := ownBytes(kept)
	name := journal.JournalName(me.ID)
	data, rerr := d.Read(name, 0)
	if rerr == nil && bytes.Equal(data, want) {
		return "", "", nil
	}
	if rerr == nil {
		res := journal.Check(data, me.ID)
		if len(res.Lines) > 0 && res.Key.Equal(me.Public()) {
			n := 0
			for n < len(res.Lines) && n < len(kept) && bytes.Equal(res.Lines[n].Raw, kept[n].Raw) {
				n++
			}
			switch {
			case n < len(res.Lines) && n < len(kept):
				return "", fmt.Sprintf("this device's journal holds another line %d than this register: two "+
					"machines write as this device (filo device new gives this one a new ID)", n+1), nil
			case len(res.Lines) > len(kept):
				return "", ErrAhead.Error() + ": the register was restored, or copied from another machine; " +
					"nothing is written there until that is settled", nil
			}
		}
	}
	if len(kept) == 0 {
		return "", "", nil
	}
	if err := d.Replace(name, toLines(kept)); err != nil {
		return "", err.Error(), nil
	}
	return "this device's journal was not what the register holds: written back", "", nil
}

func toLines(kept []store.JournalLine) [][]byte {
	out := make([][]byte, len(kept))
	for i, l := range kept {
		out[i] = l.Raw
	}
	return out
}

// AppendOwn adds one line of kind with body to this device's journal in synced directory sd: in the register
// first, then on the disk. The journal is checked first, as Import does: written back if it fell short, and left
// alone, with nothing added, if it holds lines the register does not (ErrAhead) or another line under a number.
func AppendOwn(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir, kind string, body any,
	now time.Time) error {
	js, err := s.Journals(ctx, sd.ID)
	if err != nil {
		return err
	}
	var mine store.JournalState
	for _, j := range js {
		if j.Dev == me.ID {
			mine = j
		}
	}
	if mine.RetiredSeq > 0 {
		return fmt.Errorf("%s: %w", sd.Abs(), ErrRetiredHere)
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return err
	}
	defer d.Close()
	if _, problem, err := checkOwn(ctx, s, d, sd.ID, me); err != nil {
		return err
	} else if problem != "" {
		return fmt.Errorf("%s: %s", sd.Abs(), problem)
	}
	kept, err := s.JournalLines(ctx, sd.ID, me.ID)
	if err != nil {
		return err
	}
	if len(kept) == 0 {
		return fmt.Errorf("%s: this device has no journal there", sd.Abs())
	}
	last, err := journal.Parse(kept[len(kept)-1].Raw)
	if err != nil {
		return err
	}
	w, err := journal.Resume(me.Key, last)
	if err != nil {
		return err
	}
	raw, err := w.Add(kind, body, now)
	if err != nil {
		return err
	}
	size := int64(len(ownBytes(kept)))
	if _, err := s.ApplySynced(ctx, store.SyncedChange{SyncedID: sd.ID, Journals: []store.JournalUpdate{{
		Dev: me.ID, Lines: []store.JournalLine{{Seq: last.Seq + 1, Raw: raw}}, Seq: last.Seq + 1,
		Offset: size + int64(len(raw)+1), Problem: mine.Problem}}}); err != nil {
		return err
	}
	err = d.Append(journal.JournalName(me.ID), size, [][]byte{raw})
	if errors.Is(err, journal.ErrNotAt) {
		err = d.Replace(journal.JournalName(me.ID), append(toLines(kept), raw))
	}
	return err
}
