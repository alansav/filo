// Package share is sharing decisions between devices (docs/design.md, Sharing decisions between devices): marking a
// directory as synced, joining one marked on another device, and reading the journals there into the register. It
// holds the steps; package journal reads and writes .filo/, and package store keeps the register's side.
package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// Kinds of lines this package writes and reads besides decisions (docs/design.md, Files and format).
const (
	KindName   = "name"   // the device renamed itself
	KindTrust  = "trust"  // the device vouches for another
	KindRetire = "retire" // the device retires another
)

// NameBody is a name line's.
type NameBody struct {
	Name string `json:"name"`
}

// TrustBody is a trust line's: the device vouched for, with its key and name.
type TrustBody struct {
	Device string `json:"device"`
	Key    string `json:"key"`
	Name   string `json:"name"`
}

// RetireBody is a retire line's: the device retired, and its last line that still counts in this synced directory.
type RetireBody struct {
	Device string `json:"device"`
	Last   int64  `json:"last"`
}

// statusEvery is how often a device's status file is written at most.
const statusEvery = time.Hour

// timeText is how times are kept in the register.
func timeText(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// self is this device as the register's devices table keeps it: trusted, as it is this device.
func self(d store.Device, now time.Time) store.KnownDevice {
	return store.KnownDevice{Dev: d.ID, Key: device.EncodePublic(d.Public()), Name: d.Name, TrustedUTC: timeText(now)}
}

// Target is what `filo synced add` found at a directory.
type Target struct {
	Root store.Root
	Rel  string // relative to the root
	Abs  string
	// Exactly one of these: Plan (a mark: joining it), Moved (the mark of a synced directory joined elsewhere, whose
	// place has lost it: confirming the move), or neither (no .filo: marking it).
	Plan  *JoinPlan
	Moved *store.SyncedDir
}

// ErrNested is a directory inside a synced directory, or holding one.
var ErrNested = errors.New("a synced directory cannot hold another")

// Inspect looks at directory abs for `filo synced add`: whether it can be marked, joined, or confirmed as where a
// synced directory moved to.
func Inspect(ctx context.Context, s *store.Store, abs string) (Target, error) {
	root, rel, err := s.Resolve(ctx, abs)
	if err != nil {
		return Target{}, err
	}
	t := Target{Root: root, Rel: rel, Abs: store.AbsPath(root.Path, rel)}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return Target{}, err
	}
	d, err := journal.Open(t.Abs)
	if errors.Is(err, journal.ErrNoFiloDir) {
		for _, sd := range dirs {
			if sd.RootID == root.ID && sd.Dir == rel {
				return Target{}, fmt.Errorf("%s is a synced directory already (%s)", t.Abs, sd.State)
			}
		}
		if err := checkNesting(ctx, s, dirs, root.ID, rel); err != nil {
			return Target{}, err
		}
		return t, nil
	}
	if err != nil {
		return Target{}, err
	}
	defer d.Close()
	raw, err := d.Read(journal.MarkName, 0)
	if err != nil {
		return Target{}, fmt.Errorf("%s has a %s directory but no mark Filo can read (%v): it is not a synced "+
			"directory Filo can join", t.Abs, journal.FiloDir, err)
	}
	ml, err := journal.ReadRecord(raw, journal.KindMark)
	if err != nil {
		return Target{}, fmt.Errorf("%s: its mark is not one: %w", t.Abs, err)
	}
	mark, err := ml.Mark()
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", t.Abs, err)
	}
	for i, sd := range dirs {
		if sd.SyncID != mark.ID {
			continue
		}
		if sd.RootID == root.ID && sd.Dir == rel {
			if sd.State == store.SyncedJoined {
				return Target{}, fmt.Errorf("%s is joined already", t.Abs)
			}
			return Target{}, fmt.Errorf("%s was ignored: filo synced remove %s forgets that, then it can be "+
				"joined", t.Abs, t.Abs)
		}
		if sd.State == store.SyncedJoined {
			there, err := markAt(ctx, s, sd)
			if err != nil {
				return Target{}, fmt.Errorf("%s holds the mark of %s, which cannot be looked at (%v): whether it "+
					"moved cannot be told", t.Abs, sd.Abs(), err)
			}
			if !there {
				others := slices.Delete(slices.Clone(dirs), i, i+1)
				if err := checkNesting(ctx, s, others, root.ID, rel); err != nil {
					return Target{}, err
				}
				t.Moved = &dirs[i]
				return t, nil
			}
		}
		return Target{}, fmt.Errorf("%s holds the mark of %s, which is still there: a copy of it, not joined",
			t.Abs, sd.Abs())
	}
	if err := checkNesting(ctx, s, dirs, root.ID, rel); err != nil {
		return Target{}, err
	}
	p, err := planJoin(ctx, s, d, t, ml, mark)
	if err != nil {
		return Target{}, err
	}
	t.Plan = p
	return t, nil
}

// markAt reports whether synced directory sd's place holds its mark: false when the place, its .filo or the mark is
// gone, or the mark is another's; an error when that cannot be told (its root's disk is not mounted, or the place
// cannot be read), which is never taken for a move.
func markAt(ctx context.Context, s *store.Store, sd store.SyncedDir) (bool, error) {
	root, err := s.RootAt(ctx, sd.RootPath)
	if err != nil {
		return false, err
	}
	if err := store.CheckRoot(root); err != nil {
		return false, err
	}
	abs := sd.Abs()
	if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	d, err := journal.Open(abs)
	if errors.Is(err, journal.ErrNoFiloDir) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer d.Close()
	if ok, err := d.Exists(journal.MarkName); err != nil || !ok {
		return false, err
	}
	raw, err := d.Read(journal.MarkName, 0)
	if err != nil {
		return false, err
	}
	l, err := journal.ReadRecord(raw, journal.KindMark)
	if err != nil {
		return false, nil
	}
	m, err := l.Mark()
	return err == nil && m.ID == sd.SyncID, nil
}

// holdsMark reports whether the directory abs holds the mark of synced directory id.
func holdsMark(abs, id string) bool {
	d, err := journal.Open(abs)
	if err != nil {
		return false
	}
	defer d.Close()
	raw, err := d.Read(journal.MarkName, 0)
	if err != nil {
		return false
	}
	l, err := journal.ReadRecord(raw, journal.KindMark)
	if err != nil {
		return false
	}
	m, err := l.Mark()
	return err == nil && m.ID == id
}

// checkNesting refuses rel (of root rootID) when a synced directory, joined or ignored, or a mark found by a scan,
// is above it or below it.
func checkNesting(ctx context.Context, s *store.Store, dirs []store.SyncedDir, rootID int64, rel string) error {
	related := func(a, b string) bool {
		return a != b && (a == "" || b == "" || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/"))
	}
	for _, sd := range dirs {
		if sd.RootID == rootID && related(sd.Dir, rel) {
			return fmt.Errorf("%w: %s is a synced directory", ErrNested, sd.Abs())
		}
	}
	found, err := s.FoundMarks(ctx)
	if err != nil {
		return err
	}
	for _, m := range found {
		if m.RootID == rootID && related(m.Dir, rel) {
			return fmt.Errorf("%w: %s holds a mark", ErrNested, m.Abs())
		}
	}
	return nil
}

// JoinPlan is what joining a marked directory would trust and take in: shown to the person before they confirm.
type JoinPlan struct {
	SyncID   string
	Marked   string // when, as the mark says
	MarkedBy string // the device that marked it
	markRaw  []byte
	Devices  []PlanDevice
	Others   []journal.Entry // names in .filo/ that are not Filo's
	results  map[string]journal.Result
	statuses map[string]journal.StatusBody
}

// PlanDevice is one device whose journal is in the directory.
type PlanDevice struct {
	Dev         string
	Name        string
	Fingerprint string
	Created     string
	Seen        string // from its status file; "" if none
	Lines       int    // lines accepted from its journal
	Problem     string // why reading its journal stopped, if it did
	Self        bool   // this device's own journal (it was here before)
	Trusted     bool   // trusted already, from another synced directory
}

// MarkedByName is the name of the device that marked it.
func (p *JoinPlan) MarkedByName() string {
	for _, d := range p.Devices {
		if d.Dev == p.MarkedBy {
			return d.Name
		}
	}
	return p.MarkedBy
}

func planJoin(ctx context.Context, s *store.Store, d *journal.Dir, t Target, ml journal.Line,
	mark journal.MarkBody) (*JoinPlan, error) {
	me, err := s.Device(ctx)
	if err != nil {
		return nil, err
	}
	known, err := knownDevices(ctx, s)
	if err != nil {
		return nil, err
	}
	entries, err := d.List()
	if err != nil {
		return nil, err
	}
	p := &JoinPlan{SyncID: mark.ID, Marked: mark.Marked, MarkedBy: ml.Dev, markRaw: ml.Raw,
		results: map[string]journal.Result{}, statuses: map[string]journal.StatusBody{}}
	for _, e := range entries {
		switch e.Kind {
		case journal.Other:
			p.Others = append(p.Others, e)
		case journal.Journal:
			data, err := d.Read(e.Name, 0)
			if err != nil {
				return nil, err
			}
			res := journal.Check(data, e.Dev)
			if len(res.Lines) == 0 {
				p.Others = append(p.Others, journal.Entry{Name: e.Name, Kind: journal.Other, Dev: e.Dev,
					Note: "a journal whose first line cannot be read: " + stopText(res)})
				continue
			}
			pd := PlanDevice{Dev: e.Dev, Name: res.Device.Name, Fingerprint: device.Fingerprint(res.Key),
				Created: res.Device.Created, Lines: len(res.Lines), Problem: stopText(res), Self: e.Dev == me.ID}
			if pd.Self && !res.Key.Equal(me.Public()) {
				return nil, fmt.Errorf("%s holds a journal under this device's ID that is not signed with its key: "+
					"another machine uses this ID (filo device new gives this register a new one)", t.Abs)
			}
			if k, ok := known[e.Dev]; ok && k.Trusted() && k.Key == device.EncodePublic(res.Key) {
				pd.Trusted = true
			}
			p.results[e.Dev] = res
			p.Devices = append(p.Devices, pd)
		}
	}
	for _, e := range entries {
		if e.Kind != journal.Status {
			continue
		}
		res, ok := p.results[e.Dev]
		if !ok {
			continue
		}
		if st, ok := readStatus(d, e.Name, res.Key); ok {
			p.statuses[e.Dev] = st
			for i := range p.Devices {
				if p.Devices[i].Dev == e.Dev {
					p.Devices[i].Seen = st.Seen
				}
			}
		}
	}
	marker, ok := p.results[ml.Dev]
	if !ok || !ml.Verify(marker.Key) {
		return nil, fmt.Errorf("%s: its mark is not signed by a device whose journal is there", t.Abs)
	}
	slices.SortFunc(p.Devices, func(a, b PlanDevice) int { return strings.Compare(a.Name+a.Dev, b.Name+b.Dev) })
	return p, nil
}

func stopText(res journal.Result) string {
	if res.Stop == nil {
		return ""
	}
	return res.Stop.Error()
}

// readStatus reads and checks a status file with its device's key.
func readStatus(d *journal.Dir, name string, key []byte) (journal.StatusBody, bool) {
	raw, err := d.Read(name, 0)
	if err != nil {
		return journal.StatusBody{}, false
	}
	l, err := journal.ReadRecord(raw, journal.KindStatus)
	if err != nil || !l.Verify(key) {
		return journal.StatusBody{}, false
	}
	st, err := l.Status()
	return st, err == nil && l.Dev == strings.TrimPrefix(name, "status.")
}

func knownDevices(ctx context.Context, s *store.Store) (map[string]store.KnownDevice, error) {
	list, err := s.KnownDevices(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]store.KnownDevice{}
	for _, d := range list {
		m[d.Dev] = d
	}
	return m, nil
}

// Mark makes directory t (from Inspect, with neither Plan nor Moved) a new synced directory: its mark, this
// device's journal and status file, in the register first, then on the disk.
func Mark(ctx context.Context, s *store.Store, t Target, now time.Time) (store.SyncedDir, error) {
	if t.Plan != nil || t.Moved != nil {
		return store.SyncedDir{}, errors.New("not a directory to mark")
	}
	me, err := s.Device(ctx)
	if err != nil {
		return store.SyncedDir{}, err
	}
	id, err := device.NewID()
	if err != nil {
		return store.SyncedDir{}, err
	}
	markRaw, err := journal.NewMark(me.Key, me.ID, id, now)
	if err != nil {
		return store.SyncedDir{}, err
	}
	_, first, err := journal.Start(me.Key, me.ID, me.Name, now)
	if err != nil {
		return store.SyncedDir{}, err
	}
	sd := store.SyncedDir{SyncID: id, RootID: t.Root.ID, RootPath: t.Root.Path, Dir: t.Rel,
		State: store.SyncedJoined, Mark: markRaw, AddedUTC: timeText(now)}
	known, err := knownDevices(ctx, s)
	if err != nil {
		return store.SyncedDir{}, err
	}
	sd.ID, err = s.ApplySynced(ctx, store.SyncedChange{Add: &sd, Devices: []store.KnownDevice{selfFrom(known, me, now)},
		Journals: []store.JournalUpdate{{Dev: me.ID, Lines: []store.JournalLine{{Seq: 1, Raw: first}}, Seq: 1,
			Offset: int64(len(first) + 1)}}})
	if err != nil {
		return store.SyncedDir{}, err
	}
	d, err := journal.Make(t.Abs)
	if err != nil {
		return sd, err
	}
	defer d.Close()
	if err := d.Create(journal.MarkName, [][]byte{markRaw}); err != nil {
		return sd, err
	}
	if err := d.Create(journal.JournalName(me.ID), [][]byte{first}); err != nil {
		return sd, err
	}
	return sd, writeStatus(ctx, s, d, sd.ID, me, now)
}

// Join joins the marked directory t (from Inspect, with a Plan): every device whose journal is there is trusted
// (the person confirmed them), their lines are taken in, and this device starts its own journal there (or goes on
// with the one it left there before). It returns what the person should hear besides (this device was retired
// there, a vouch with another key).
func Join(ctx context.Context, s *store.Store, t Target, now time.Time) (store.SyncedDir, []string, error) {
	p := t.Plan
	if p == nil {
		return store.SyncedDir{}, nil, errors.New("not a directory to join")
	}
	me, err := s.Device(ctx)
	if err != nil {
		return store.SyncedDir{}, nil, err
	}
	known, err := knownDevices(ctx, s)
	if err != nil {
		return store.SyncedDir{}, nil, err
	}
	sd := store.SyncedDir{SyncID: p.SyncID, RootID: t.Root.ID, RootPath: t.Root.Path, Dir: t.Rel,
		State: store.SyncedJoined, Mark: p.markRaw, AddedUTC: timeText(now)}
	ch := store.SyncedChange{Add: &sd}
	devs := map[string]store.KnownDevice{}
	var reads []*read
	for _, pd := range p.Devices {
		res := p.results[pd.Dev]
		kd := known[pd.Dev]
		key := device.EncodePublic(res.Key)
		if kd.Trusted() && kd.Key != key {
			return store.SyncedDir{}, nil, fmt.Errorf("%s's journal in %s is signed with another key than the one "+
				"this device trusts for it: not joined (another device may be using its ID)", kd.Name, t.Abs)
		}
		kd.Dev, kd.Key, kd.Name, kd.CreatedUTC = pd.Dev, key, pd.Name, pd.Created
		if kd.TrustedUTC == "" {
			kd.TrustedUTC = timeText(now)
		}
		devs[pd.Dev] = kd
		reads = append(reads, &read{dev: pd.Dev, lines: res.Lines, problem: pd.Problem})
	}
	devs[me.ID] = selfFrom(known, me, now)
	retired, notes := settle(reads, map[string]store.JournalState{}, me.ID, devs, now)
	name := func(dev string) string { return nameOf(devs, dev) }
	for _, r := range reads {
		ch.Journals = append(ch.Journals, r.update(store.JournalState{Dev: r.dev}, retired[r.dev]))
		if r.dev != me.ID {
			notes = append(notes, fileDecisions(r.lines, 0, name, &ch)...)
		}
	}
	var first []byte
	if _, here := p.results[me.ID]; !here {
		_, first, err = journal.Start(me.Key, me.ID, me.Name, now)
		if err != nil {
			return store.SyncedDir{}, nil, err
		}
		ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: me.ID,
			Lines: []store.JournalLine{{Seq: 1, Raw: first}}, Seq: 1, Offset: int64(len(first) + 1),
			RetiredSeq: retired[me.ID]})
	}
	for dev, seq := range retired {
		if _, ok := p.results[dev]; !ok && dev != me.ID {
			ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: dev, RetiredSeq: seq})
		}
	}
	for _, kd := range devs {
		ch.Devices = append(ch.Devices, kd)
	}
	slices.SortFunc(ch.Devices, func(a, b store.KnownDevice) int { return strings.Compare(a.Dev, b.Dev) })
	if sd.ID, err = s.ApplySynced(ctx, ch); err != nil {
		return store.SyncedDir{}, nil, err
	}
	d, err := journal.Open(t.Abs)
	if err != nil {
		return sd, notes, err
	}
	defer d.Close()
	if first != nil {
		if err := d.Create(journal.JournalName(me.ID), [][]byte{first}); err != nil {
			return sd, notes, err
		}
	}
	if _, problems, err := applyDecisions(ctx, s, sd, now); err != nil {
		return sd, notes, err
	} else {
		notes = append(notes, problems...)
	}
	if retired[me.ID] > 0 {
		return sd, notes, nil
	}
	return sd, notes, writeStatus(ctx, s, d, sd.ID, me, now)
}

// read is one trusted device's journal as read this run: the lines new to the register (from offset base), and
// why reading stopped, if it did.
type read struct {
	dev     string
	base    int64
	lines   []journal.Line
	problem string
}

// update is the register's change for r, from the journal's stored state st, with r's lines cut at retiredSeq (> 0:
// the device is retired there, and its lines after that one are refused).
func (r *read) update(st store.JournalState, retiredSeq int64) store.JournalUpdate {
	ju := store.JournalUpdate{Dev: r.dev, Seq: st.Seq, Offset: r.base, Problem: r.problem, RetiredSeq: retiredSeq}
	if len(r.lines) == 0 {
		ju.Offset = st.Offset
	}
	for _, l := range r.lines {
		ju.Lines = append(ju.Lines, store.JournalLine{Seq: l.Seq, Raw: l.Raw})
		ju.Seq, ju.Offset = l.Seq, ju.Offset+int64(len(l.Raw)+1)
	}
	return ju
}

// cut drops r's lines after seq (0: none dropped); it reports whether it dropped any.
func (r *read) cut(seq int64) bool {
	if seq == 0 {
		return false
	}
	keep := r.lines[:0:0]
	for _, l := range r.lines {
		if l.Seq <= seq {
			keep = append(keep, l)
		}
	}
	dropped := len(keep) < len(r.lines)
	r.lines = keep
	if dropped {
		r.problem = "" // what follows the retirement is refused, not a problem
	}
	return dropped
}

// settle works out the retirements among the lines read this run, whichever journal holds them: a retire line counts
// only while its own device's lines still count there, so it starts from the retirements kept (state) and cuts and
// looks again until nothing changes. Then it applies the name, trust and retire lines left to devs. It returns each
// retired device's last line that counts (the kept ones and the new), and what the person should hear.
func settle(reads []*read, state map[string]store.JournalState, me string, devs map[string]store.KnownDevice,
	now time.Time) (map[string]int64, []string) {
	retired := map[string]int64{}
	by := map[string]string{}
	for dev, st := range state {
		if st.RetiredSeq > 0 {
			retired[dev] = st.RetiredSeq
		}
	}
	for range len(reads) + 2 {
		changed := false
		for _, r := range reads {
			r.cut(retired[r.dev])
		}
		for _, r := range reads {
			for _, l := range r.lines {
				var b RetireBody
				if l.Kind != KindRetire || l.Body(&b) != nil || !device.ValidID(b.Device) || b.Last < 0 {
					continue
				}
				if retired[b.Device] == 0 {
					retired[b.Device], by[b.Device], changed = max(b.Last, 1), l.Dev, true
				}
			}
		}
		if !changed {
			break
		}
	}
	var notes []string
	for _, r := range reads {
		notes = append(notes, applyLines(r.lines, devs, now)...)
	}
	for dev, who := range by {
		kd, ok := devs[dev]
		if !ok {
			kd = store.KnownDevice{Dev: dev, Name: dev}
		}
		if !kd.Retired() {
			kd.RetiredUTC, kd.RetiredBy = timeText(now), who
			devs[dev] = kd
		}
		if dev == me {
			notes = append(notes, fmt.Sprintf("%s retired this device: it writes nothing more there; to share "+
				"decisions again, filo device new, then join again", nameOf(devs, who)))
		}
	}
	return retired, notes
}

// applyLines applies the name and trust lines among lines (of trusted devices, cut at any retirement) to devs; it
// returns what the person should hear (a vouch with another key).
func applyLines(lines []journal.Line, devs map[string]store.KnownDevice, now time.Time) []string {
	var notes []string
	for _, l := range lines {
		by := l.Dev
		switch l.Kind {
		case KindName:
			var b NameBody
			if l.Body(&b) == nil && device.CheckName(b.Name) == nil {
				if kd, ok := devs[by]; ok {
					kd.Name = b.Name
					devs[by] = kd
				}
			}
		case KindTrust:
			var b TrustBody
			if l.Body(&b) != nil || !device.ValidID(b.Device) || device.CheckName(b.Name) != nil {
				continue
			}
			if _, err := device.DecodePublic(b.Key); err != nil {
				continue
			}
			kd, ok := devs[b.Device]
			switch {
			case ok && kd.Key != "" && kd.Key != b.Key:
				notes = append(notes, fmt.Sprintf("%s vouched for %s with another key than its journal's: not "+
					"trusted by that", nameOf(devs, by), b.Name))
			case ok && kd.Trusted():
			default:
				kd.Dev, kd.Key, kd.Name, kd.TrustedUTC, kd.VouchedBy = b.Device, b.Key, b.Name, timeText(now), by
				devs[b.Device] = kd
			}
		}
	}
	return notes
}

// selfFrom is this device's row in the devices table: as kept (its first trust time, a retirement), with its key
// and name now.
func selfFrom(known map[string]store.KnownDevice, me store.Device, now time.Time) store.KnownDevice {
	kd, ok := known[me.ID]
	if !ok {
		return self(me, now)
	}
	kd.Key, kd.Name = device.EncodePublic(me.Public()), me.Name
	if kd.TrustedUTC == "" {
		kd.TrustedUTC = timeText(now)
	}
	return kd
}

func nameOf(devs map[string]store.KnownDevice, dev string) string {
	if kd, ok := devs[dev]; ok && kd.Name != "" {
		return kd.Name
	}
	return dev
}

// writeStatus writes this device's status file in d: now, and the highest line it holds of each journal there. It
// writes nothing to the register: status files are read from .filo/ on every run.
func writeStatus(ctx context.Context, s *store.Store, d *journal.Dir, syncedID int64, me store.Device,
	now time.Time) error {
	js, err := s.Journals(ctx, syncedID)
	if err != nil {
		return err
	}
	applied := map[string]int64{}
	for _, j := range js {
		if j.Seq > 0 {
			applied[j.Dev] = j.Seq
		}
	}
	raw, err := journal.NewStatus(me.Key, me.ID, applied, now)
	if err != nil {
		return err
	}
	return d.Replace(journal.StatusName(me.ID), [][]byte{raw})
}

// ownBytes is this device's journal as the register keeps it: what the file must hold.
func ownBytes(lines []store.JournalLine) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l.Raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// dirExists reports whether abs is a directory.
func dirExists(abs string) bool {
	fi, err := os.Stat(abs)
	return err == nil && fi.IsDir()
}
