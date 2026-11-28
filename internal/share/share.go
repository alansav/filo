// Package share is sharing decisions between devices (docs/design.md, Sharing decisions between devices): marking a
// directory as synced, joining one marked on another device, and reading the journals there into the register. It
// holds the steps; package journal reads and writes .filo/, and package store keeps the register's side.
package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
		if sd.State == store.SyncedJoined && !holdsMark(sd.Abs(), mark.ID) {
			t.Moved = &dirs[i]
			return t, nil
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
	sd.ID, err = s.ApplySynced(ctx, store.SyncedChange{Add: &sd, Devices: []store.KnownDevice{self(me, now)},
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
	for _, pd := range p.Devices {
		res := p.results[pd.Dev]
		kd := known[pd.Dev]
		kd.Dev, kd.Key, kd.Name, kd.CreatedUTC = pd.Dev, device.EncodePublic(res.Key), pd.Name, pd.Created
		if kd.TrustedUTC == "" {
			kd.TrustedUTC = timeText(now)
		}
		devs[pd.Dev] = kd
		st := p.statuses[pd.Dev]
		ju := store.JournalUpdate{Dev: pd.Dev, Seq: res.End.Last.Seq, Offset: res.End.Offset, Problem: pd.Problem,
			SeenUTC: st.Seen, Applied: st.Applied}
		for _, l := range res.Lines {
			ju.Lines = append(ju.Lines, store.JournalLine{Seq: l.Seq, Raw: l.Raw})
		}
		ch.Journals = append(ch.Journals, ju)
	}
	var first []byte
	if _, here := p.results[me.ID]; !here {
		_, first, err = journal.Start(me.Key, me.ID, me.Name, now)
		if err != nil {
			return store.SyncedDir{}, nil, err
		}
		ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: me.ID,
			Lines: []store.JournalLine{{Seq: 1, Raw: first}}, Seq: 1, Offset: int64(len(first) + 1)})
	}
	devs[me.ID] = self(me, now)
	var notes []string
	for _, pd := range p.Devices {
		if pd.Dev == me.ID {
			continue
		}
		notes = append(notes, applyLines(p.results[pd.Dev].Lines, me.ID, devs, &ch, now)...)
	}
	for i := range ch.Journals {
		cutRetired(&ch.Journals[i])
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
	if retiredHere(ch, me.ID) {
		return sd, notes, nil
	}
	return sd, notes, writeStatus(ctx, s, d, sd.ID, me, now)
}

// cutRetired drops from ju the lines after its retirement line (a retired device's later lines are refused),
// moving its state back to the last line kept.
func cutRetired(ju *store.JournalUpdate) {
	if ju.RetiredSeq == 0 || ju.Seq <= ju.RetiredSeq || len(ju.Lines) == 0 {
		return
	}
	off := ju.Offset
	keep := ju.Lines[:0]
	for _, l := range ju.Lines {
		if l.Seq <= ju.RetiredSeq {
			keep = append(keep, l)
		} else {
			off -= int64(len(l.Raw) + 1)
		}
	}
	ju.Lines, ju.Offset = keep, off
	ju.Seq = 0
	if len(keep) > 0 {
		ju.Seq = keep[len(keep)-1].Seq
	}
}

// retiredHere reports whether ch retires this device.
func retiredHere(ch store.SyncedChange, me string) bool {
	for _, j := range ch.Journals {
		if j.Dev == me && j.RetiredSeq > 0 {
			return true
		}
	}
	return false
}

// applyLines applies the trust, retire and name lines among lines (of a trusted device in the synced directory
// ch is about) to devs, and to ch's journal states for a retirement; it returns what the person should hear (this
// device retired, a vouch with another key).
func applyLines(lines []journal.Line, me string, devs map[string]store.KnownDevice, ch *store.SyncedChange,
	now time.Time) []string {
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
			case ok && kd.Key != b.Key:
				notes = append(notes, fmt.Sprintf("%s vouched for %s with another key than its journal's: not "+
					"trusted by that", nameOf(devs, by), b.Name))
			case ok && kd.Trusted():
			default:
				kd.Dev, kd.Key, kd.Name, kd.TrustedUTC, kd.VouchedBy = b.Device, b.Key, b.Name, timeText(now), by
				devs[b.Device] = kd
			}
		case KindRetire:
			var b RetireBody
			if l.Body(&b) != nil || !device.ValidID(b.Device) || b.Last < 0 {
				continue
			}
			kd, ok := devs[b.Device]
			if !ok {
				kd = store.KnownDevice{Dev: b.Device, Name: b.Device}
			}
			if !kd.Retired() {
				kd.RetiredUTC, kd.RetiredBy = timeText(now), by
			}
			devs[b.Device] = kd
			found := false
			for i := range ch.Journals {
				if ch.Journals[i].Dev == b.Device {
					ch.Journals[i].RetiredSeq = max(b.Last, 1)
					found = true
				}
			}
			if !found {
				ch.Journals = append(ch.Journals, store.JournalUpdate{Dev: b.Device, RetiredSeq: max(b.Last, 1)})
			}
			if b.Device == me {
				notes = append(notes, fmt.Sprintf("%s retired this device: it writes nothing more there; to "+
					"share decisions again, filo device new, then join again", nameOf(devs, by)))
			}
		}
	}
	return notes
}

func nameOf(devs map[string]store.KnownDevice, dev string) string {
	if kd, ok := devs[dev]; ok && kd.Name != "" {
		return kd.Name
	}
	return dev
}

// writeStatus writes this device's status file in d: now, and the highest line it holds of each journal there.
func writeStatus(ctx context.Context, s *store.Store, d *journal.Dir, syncedID int64, me store.Device,
	now time.Time) error {
	js, err := s.Journals(ctx, syncedID)
	if err != nil {
		return err
	}
	applied := map[string]int64{}
	var mine store.JournalState
	for _, j := range js {
		if j.Seq > 0 {
			applied[j.Dev] = j.Seq
		}
		if j.Dev == me.ID {
			mine = j
		}
	}
	raw, err := journal.NewStatus(me.Key, me.ID, applied, now)
	if err != nil {
		return err
	}
	if err := d.Replace(journal.StatusName(me.ID), [][]byte{raw}); err != nil {
		return err
	}
	mine.Dev, mine.SeenUTC = me.ID, timeText(now)
	_, err = s.ApplySynced(ctx, store.SyncedChange{SyncedID: syncedID, Journals: []store.JournalUpdate{{
		Dev: me.ID, Seq: mine.Seq, Offset: mine.Offset, Problem: mine.Problem, SeenUTC: mine.SeenUTC,
		Applied: mine.Applied, RetiredSeq: mine.RetiredSeq}}})
	return err
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
