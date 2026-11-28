package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/share"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// filo synced and filo devices (docs/design.md, Sharing decisions between devices). synced lists the synced
// directories and the marks found; add marks a directory, joins a marked one (after showing the devices there and
// asking), or confirms one moved there; remove leaves one; ignore stops a found mark being reported. devices lists
// the devices of each synced directory; trust and retire act on one, after asking.

const syncedUsage = "filo synced | add DIR [-y] | remove DIR [-y] | ignore DIR"

const devicesUsage = "filo devices [DIR] | trust DEVICE [-y] | retire DEVICE [-y]"

// timeNow is the time a run acts at; tests replace it.
var timeNow = time.Now

func (e *env) synced(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	fl := e.flags("synced")
	db := dbFlag(fl, dbBefore)
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "synced", rest)
	if err != nil {
		return err
	}
	want := map[string]int{"": 0, "add": 1, "remove": 1, "ignore": 1}
	n, known := want[sub]
	switch {
	case !known:
		return usageError{fmt.Sprintf("unknown synced command %q; see filo help synced", sub)}
	case len(pos) != n:
		return usageError{"wrong number of arguments: " + syncedUsage}
	case *yes && sub != "add" && sub != "remove":
		return usageError{"-y is for synced add and synced remove"}
	}
	var abs string
	if n == 1 {
		if err := checkTilde("DIR", pos[0]); err != nil {
			return err
		}
		if abs, err = syncedDirArg(pos[0], sub != "remove"); err != nil {
			return err
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	switch sub {
	case "add":
		return e.syncedAdd(ctx, s, abs, *yes)
	case "remove":
		return e.syncedRemove(ctx, s, abs, *yes)
	case "ignore":
		return e.syncedIgnore(ctx, s, abs)
	}
	return e.syncedList(ctx, s)
}

// syncedDirArg is DIR made absolute, with symlinks resolved; it must be a directory on the disk if onDisk.
func syncedDirArg(arg string, onDisk bool) (string, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	} else if onDisk {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if onDisk {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("%s is not a directory", p)
		}
	}
	return p, nil
}

// importAll reads every joined synced directory's journals before the command goes on (docs/design.md, Timing).
func (e *env) importAll(ctx context.Context, s *store.Store) (share.Report, error) {
	return share.Import(ctx, s, timeNow())
}

func (e *env) syncedList(ctx context.Context, s *store.Store) error {
	rep, err := e.importAll(ctx, s)
	if err != nil {
		return err
	}
	found, err := share.FoundMarks(ctx, s)
	if err != nil {
		return err
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return err
	}
	r := e.view
	ignored := 0
	for _, d := range dirs {
		if d.State == store.SyncedIgnored {
			ignored++
		}
	}
	if len(rep.Dirs) == 0 && len(found) == 0 {
		r.Heading("Synced directories", view.Where{}, "none")
		r.Blank()
		r.Next("Next: filo synced add DIR marks a directory a sync service shares, or joins one marked on " +
			"another device")
		return nil
	}
	facts := []string{report.Count(int64(len(rep.Dirs))) + " joined"}
	if len(found) > 0 {
		facts = append(facts, report.Count(int64(len(found)))+" found")
	}
	if ignored > 0 {
		facts = append(facts, report.Count(int64(ignored))+" ignored")
	}
	r.Heading("Synced directories", view.Where{}, facts...)
	devs, err := s.KnownDevices(ctx)
	if err != nil {
		return err
	}
	for _, dr := range rep.Dirs {
		r.Blank()
		e.renderSyncedDir(dr, devs)
	}
	e.renderFound(found)
	return nil
}

// renderSyncedDir writes one joined synced directory: its path and devices, then its problems and notes.
func (e *env) renderSyncedDir(dr share.DirReport, devs []store.KnownDevice) {
	r := e.view
	names := map[string]string{}
	for _, d := range devs {
		names[d.Dev] = d.Name
	}
	var facts []string
	facts = append(facts, report.Plural(int64(len(dr.Journals)), "device", "devices"))
	if len(dr.Strange) > 0 {
		facts = append(facts, report.Count(int64(len(dr.Strange)))+" not trusted")
	}
	r.Section(r.Tilde(dr.Dir.Abs()), facts...)
	if len(dr.Problems) == 0 && len(dr.Strange) == 0 && len(dr.Others) == 0 {
		r.Count(view.Done, "no problems")
	}
	for _, p := range dr.Problems {
		r.Count(view.Attention, p)
	}
	for _, n := range dr.Notes {
		r.Count(view.Done, n)
	}
	for _, d := range dr.Strange {
		r.Count(view.Attention, fmt.Sprintf("%s (%s) shares decisions there but is not trusted: filo devices "+
			"trust %s", d.Name, d.Dev[:8], d.Name))
	}
	for _, o := range dr.Others {
		what := "not Filo's: left alone"
		if o.Note != "" {
			what = o.Note + ": left alone"
		}
		r.Count(view.Attention, filepath.Join(dr.Dir.Abs(), journal.FiloDir, o.Name)+": "+what)
	}
}

// renderFound writes the marks found that this device has neither joined nor ignored, each with what to do.
func (e *env) renderFound(found []share.Found) {
	if len(found) == 0 {
		return
	}
	r := e.view
	r.Blank()
	r.Section("Found", report.Count(int64(len(found))))
	for _, f := range found {
		p := r.Tilde(f.Abs())
		switch f.Kind {
		case share.FoundNew:
			r.Count(view.Attention, p+": a synced directory this device has not joined: filo synced add "+
				shellQuote(f.Abs())+" joins it, filo synced ignore "+shellQuote(f.Abs())+" stops this")
		case share.FoundMoved:
			r.Count(view.Attention, p+" holds the mark of "+r.Tilde(f.Of.Abs())+", which is gone from there: "+
				"if it moved, filo synced add "+shellQuote(f.Abs())+" says so")
		case share.FoundCopy:
			if f.Problem != "" {
				r.Count(view.Attention, p+" holds the mark of "+r.Tilde(f.Of.Abs())+": "+f.Problem)
				continue
			}
			r.Count(view.Attention, p+" is a copy of "+r.Tilde(f.Of.Abs())+" (its mark): not joined")
		case share.FoundNested:
			r.Count(view.Attention, p+" holds a mark, inside or around "+r.Tilde(f.Of.Abs())+": a synced "+
				"directory cannot hold another")
		default:
			r.Count(view.Attention, p+": "+f.Problem+"; look inside its "+journal.FiloDir+", and remove it if it "+
				"is not a synced directory's")
		}
	}
}

func (e *env) syncedAdd(ctx context.Context, s *store.Store, abs string, yes bool) error {
	if _, err := e.importAll(ctx, s); err != nil {
		return err
	}
	t, err := share.Inspect(ctx, s, abs)
	if err != nil {
		return err
	}
	r := e.view
	switch {
	case t.Moved != nil:
		r.Heading("Synced directory moved?", view.Where{}, r.Tilde(t.Abs))
		r.Count(view.Neutral, "it holds the mark of "+r.Tilde(t.Moved.Abs())+", which is gone from there")
		if err := e.outputErr(); err != nil {
			return err
		}
		ok, err := e.confirmChange(ctx, "Record that "+r.Tilde(t.Moved.Abs())+" moved to "+r.Tilde(t.Abs)+"?",
			yes, nil)
		if err != nil || !ok {
			return err
		}
		if err := s.MoveSynced(ctx, t.Moved.ID, t.Root.ID, t.Rel); err != nil {
			return err
		}
		if _, err := e.importAll(ctx, s); err != nil {
			return err
		}
		r.Outcome("Moved", r.Tilde(t.Moved.Abs())+" → "+r.Tilde(t.Abs))
		return nil
	case t.Plan != nil:
		return e.syncedJoin(ctx, s, t, yes)
	}
	sd, err := share.Mark(ctx, s, t, timeNow())
	if err != nil {
		return err
	}
	r.Heading("Synced directory marked", view.Where{}, r.Tilde(sd.Abs()))
	r.Count(view.Neutral, "its decisions are shared through "+r.Tilde(filepath.Join(sd.Abs(), journal.FiloDir))+
		"/, which scans skip")
	r.Blank()
	r.Next("Next: once the sync service has copied it, filo synced add DIR on each other device joins it")
	return nil
}

func (e *env) syncedJoin(ctx context.Context, s *store.Store, t share.Target, yes bool) error {
	r := e.view
	p := t.Plan
	r.Heading("Join", view.Where{}, r.Tilde(t.Abs))
	r.Count(view.Neutral, fmt.Sprintf("a synced directory (ID %s), marked by %s on %s", p.SyncID[:8],
		p.MarkedByName(), shortTime(p.Marked)))
	r.Blank()
	r.Section("Devices sharing decisions there", report.Count(int64(len(p.Devices))))
	fields := make([]view.Field, 0, len(p.Devices))
	for _, d := range p.Devices {
		facts := []string{"first seen " + shortTime(d.Created)}
		if d.Seen != "" {
			facts = append(facts, "last seen "+shortTime(d.Seen))
		}
		facts = append(facts, report.Plural(int64(d.Lines), "line", "lines"))
		switch {
		case d.Self:
			facts = append(facts, "this device, before")
		case d.Trusted:
			facts = append(facts, "trusted already")
		}
		fields = append(fields, view.Field{Label: d.Name, Value: d.Fingerprint, Note: r.Join(facts...),
			Attention: d.Problem})
	}
	r.Fields(0, fields)
	for _, o := range p.Others {
		what := "not Filo's: left alone"
		if o.Note != "" {
			what = o.Note
		}
		r.Count(view.Attention, filepath.Join(t.Abs, journal.FiloDir, o.Name)+": "+what)
	}
	r.Blank()
	r.Paragraph(0, "Compare each fingerprint with filo device on that device. Joining trusts these devices: "+
		"the decisions they share there are taken in here.")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Trust these devices and share decisions in "+r.Tilde(t.Abs)+"?", yes, nil)
	if err != nil || !ok {
		return err
	}
	sd, notes, err := share.Join(ctx, s, t, timeNow())
	if err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	r.Outcome("Joined", r.Tilde(sd.Abs()), report.Plural(int64(len(p.Devices)), "device", "devices")+" trusted")
	for _, n := range notes {
		r.Count(view.Attention, n)
	}
	return nil
}

func (e *env) syncedRemove(ctx context.Context, s *store.Store, abs string, yes bool) error {
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return err
	}
	var sd *store.SyncedDir
	for i, d := range dirs {
		if d.Abs() == abs {
			sd = &dirs[i]
		}
	}
	if sd == nil {
		return fmt.Errorf("%s is not a synced directory this device knows (filo synced lists them)", abs)
	}
	r := e.view
	if sd.State == store.SyncedIgnored {
		if err := s.RemoveSynced(ctx, sd.ID); err != nil {
			return err
		}
		r.Heading("No longer ignored", view.Where{}, r.Tilde(abs))
		return nil
	}
	r.Heading("Leave", view.Where{}, r.Tilde(abs))
	r.Count(view.Neutral, "this device stops sharing decisions there; its files and every journal stay as they are")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Stop sharing decisions in "+r.Tilde(abs)+"?", yes, nil)
	if err != nil || !ok {
		return err
	}
	if err := s.RemoveSynced(ctx, sd.ID); err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	r.Outcome("Left", r.Tilde(abs))
	return nil
}

func (e *env) syncedIgnore(ctx context.Context, s *store.Store, abs string) error {
	if err := share.Ignore(ctx, s, abs, timeNow()); err != nil {
		return err
	}
	r := e.view
	r.Heading("Ignored", view.Where{}, r.Tilde(abs))
	r.Count(view.Neutral, "its mark is no longer reported, here or in a copy; filo synced remove "+shellQuote(abs)+
		" undoes this")
	return nil
}

// shortTime is an RFC 3339 time as a date and minute, or as it is if it is not one.
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func (e *env) devices(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	if sub != "" && sub != "trust" && sub != "retire" {
		sub, rest = "", args // filo devices DIR
	}
	fl := e.flags("devices")
	db := dbFlag(fl, dbBefore)
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "devices", rest)
	if err != nil {
		return err
	}
	switch {
	case sub == "" && len(pos) > 1, sub != "" && len(pos) != 1:
		return usageError{"wrong number of arguments: " + devicesUsage}
	case *yes && sub == "":
		return usageError{"-y is for devices trust and devices retire"}
	}
	var dir string
	if sub == "" && len(pos) == 1 {
		if err := checkTilde("DIR", pos[0]); err != nil {
			return err
		}
		if dir, err = syncedDirArg(pos[0], false); err != nil {
			return err
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	rep, err := e.importAll(ctx, s)
	if err != nil {
		return err
	}
	switch sub {
	case "trust":
		return e.devicesTrust(ctx, s, pos[0], *yes)
	case "retire":
		return e.devicesRetire(ctx, s, pos[0], *yes)
	}
	return e.devicesList(ctx, s, rep, dir)
}

func (e *env) devicesList(ctx context.Context, s *store.Store, rep share.Report, dir string) error {
	me, err := s.Device(ctx)
	if err != nil {
		return err
	}
	devs, err := s.KnownDevices(ctx)
	if err != nil {
		return err
	}
	byDev := map[string]store.KnownDevice{}
	for _, d := range devs {
		byDev[d.Dev] = d
	}
	r := e.view
	var shown []share.DirReport
	for _, dr := range rep.Dirs {
		if dir == "" || dr.Dir.Abs() == dir {
			shown = append(shown, dr)
		}
	}
	if dir != "" && len(shown) == 0 {
		return fmt.Errorf("%s is not a joined synced directory (filo synced lists them)", dir)
	}
	if len(shown) == 0 {
		r.Heading("Devices", view.Where{}, "no synced directory joined")
		r.Blank()
		r.Next("Next: filo synced add DIR")
		return nil
	}
	r.Heading("Devices", view.Where{}, report.Plural(int64(len(shown)), "synced directory", "synced directories"))
	for _, dr := range shown {
		r.Blank()
		r.Section(r.Tilde(dr.Dir.Abs()), report.Plural(int64(len(dr.Journals)+len(dr.Strange)), "device", "devices"))
		mine := int64(0)
		for _, j := range dr.Journals {
			if j.Dev == me.ID {
				mine = j.Seq
			}
		}
		var fields []view.Field
		for _, j := range dr.Journals {
			kd := byDev[j.Dev]
			facts := []string{j.Dev[:8]}
			switch {
			case j.Dev == me.ID:
				facts = append(facts, "this device")
			case j.RetiredSeq > 0:
				facts = append(facts, "retired by "+nameIn(byDev, kd.RetiredBy))
			case kd.VouchedBy != "":
				facts = append(facts, "trusted, vouched for by "+nameIn(byDev, kd.VouchedBy))
			case kd.Trusted():
				facts = append(facts, "trusted")
			}
			st, seen := dr.Status[j.Dev]
			if seen {
				facts = append(facts, "last seen "+shortTime(st.Seen))
			}
			facts = append(facts, report.Plural(j.Seq, "line", "lines"))
			if j.Dev != me.ID && j.RetiredSeq == 0 && seen {
				switch got := st.Applied[me.ID]; {
				case got >= mine:
					facts = append(facts, "has this device's latest")
				case got == 0:
					facts = append(facts, "has none of this device's lines yet")
				default:
					facts = append(facts, fmt.Sprintf("has this device's lines to %d of %d", got, mine))
				}
			}
			fp := ""
			if k, err := decodeKey(kd.Key); err == nil {
				fp = k
			}
			fields = append(fields, view.Field{Label: kd.Name, Value: fp, Note: r.Join(facts...), Attention: j.Problem})
		}
		for _, d := range dr.Strange {
			fp, _ := decodeKey(d.Key)
			fields = append(fields, view.Field{Label: d.Name, Value: fp, Note: d.Dev[:8],
				Attention: "not trusted: filo devices trust " + d.Name})
		}
		r.Fields(0, fields)
	}
	return nil
}

func nameIn(devs map[string]store.KnownDevice, dev string) string {
	if d, ok := devs[dev]; ok {
		return d.Name
	}
	return dev
}

func (e *env) devicesTrust(ctx context.Context, s *store.Store, arg string, yes bool) error {
	kd, err := share.FindDevice(ctx, s, arg)
	if err != nil {
		return err
	}
	if kd.Trusted() {
		return fmt.Errorf("%s is trusted already", kd.Name)
	}
	in, err := share.Where(ctx, s, kd.Dev)
	if err != nil {
		return err
	}
	r := e.view
	fp, err := decodeKey(kd.Key)
	if err != nil {
		return err
	}
	r.Heading("Trust", view.Where{}, kd.Name)
	r.Fields(0, []view.Field{{Label: "ID", Value: kd.Dev}, {Label: "fingerprint", Value: fp,
		Note: "compare it with filo device on " + kd.Name}})
	for _, sd := range in {
		r.Count(view.Neutral, "shares decisions in "+r.Tilde(sd.Abs()))
	}
	r.Paragraph(0, "Trusting it takes in the decisions it shares, and vouches for it: the other devices that "+
		"trust this one trust it too.")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Trust "+kd.Name+"?", yes, nil)
	if err != nil || !ok {
		return err
	}
	terr := share.Trust(ctx, s, kd, in, timeNow())
	if _, err := e.importAll(ctx, s); err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	if terr != nil {
		r.Outcome("Trusted", kd.Name)
		return fmt.Errorf("the vouch was not written everywhere: %w", terr)
	}
	r.Outcome("Trusted", kd.Name, report.Plural(int64(len(in)), "synced directory", "synced directories"))
	return nil
}

func (e *env) devicesRetire(ctx context.Context, s *store.Store, arg string, yes bool) error {
	kd, err := share.FindDevice(ctx, s, arg)
	if err != nil {
		return err
	}
	me, err := s.Device(ctx)
	if err != nil {
		return err
	}
	switch {
	case kd.Dev == me.ID:
		return errors.New("this device cannot retire itself: retire it from another device")
	case kd.Retired():
		return fmt.Errorf("%s is retired already", kd.Name)
	}
	in, err := share.Where(ctx, s, kd.Dev)
	if err != nil {
		return err
	}
	r := e.view
	r.Heading("Retire", view.Where{}, kd.Name, kd.Dev[:8])
	r.Paragraph(0, "Its decisions so far stay. Anything it writes from now on is refused, here and on every "+
		"device that trusts this one; its journal is never deleted. If it is still in use, it must join again as "+
		"a new device (filo device new there).")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Retire "+kd.Name+"?", yes, nil)
	if err != nil || !ok {
		return err
	}
	if err := share.Retire(ctx, s, kd, in, timeNow()); err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	r.Outcome("Retired", kd.Name, report.Plural(int64(len(in)), "synced directory", "synced directories"))
	return nil
}

// syncedAfterScan notes the marks the scans found and reads the journals, then says in one line what needs the
// person, if anything (filo synced has the rest). A problem here never fails the scan.
func (e *env) syncedAfterScan(ctx context.Context, s *store.Store, sums []scannedRoot) {
	now := timeNow()
	for _, sr := range sums {
		root, err := s.RootAt(ctx, sr.root)
		if err != nil {
			continue
		}
		if err := share.NoteMarks(ctx, s, root, sr.scope, sr.filoDirs, now); err != nil {
			e.errorLine("warning: noting the synced directories found: " + err.Error())
		}
	}
	rep, err := e.importAll(ctx, s)
	if err != nil {
		e.errorLine("warning: reading the synced directories' journals: " + err.Error())
		return
	}
	found, err := share.FoundMarks(ctx, s)
	if err != nil {
		return
	}
	strange := 0
	for _, d := range rep.Dirs {
		strange += len(d.Strange)
	}
	var facts []string
	if n := rep.Problems(); n > 0 {
		facts = append(facts, report.Plural(int64(n), "problem", "problems"))
	}
	if strange > 0 {
		facts = append(facts, report.Plural(int64(strange), "device not trusted", "devices not trusted"))
	}
	if len(found) > 0 {
		facts = append(facts, report.Plural(int64(len(found)), "mark found", "marks found"))
	}
	if len(facts) == 0 {
		return
	}
	r := e.view
	r.Blank()
	r.Outcome("Synced", facts...)
	r.Next("Next: filo synced")
}

// scannedRoot is what syncedAfterScan needs of one scan: where it was, and the directories holding .filo.
type scannedRoot struct {
	root, scope string
	filoDirs    []string
}

// syncedReport writes the report's Synced part: each joined synced directory's problems, and the marks found.
func (e *env) syncedReport(ctx context.Context, s *store.Store) error {
	rep, err := e.importAll(ctx, s)
	if err != nil {
		return err
	}
	found, err := share.FoundMarks(ctx, s)
	if err != nil {
		return err
	}
	if len(rep.Dirs) == 0 && len(found) == 0 {
		return nil
	}
	devs, err := s.KnownDevices(ctx)
	if err != nil {
		return err
	}
	r := e.view
	r.Blank()
	r.Section("Synced", report.Count(int64(len(rep.Dirs)))+" joined")
	for _, dr := range rep.Dirs {
		e.renderSyncedDir(dr, devs)
	}
	e.renderFound(found)
	return nil
}

// decodeKey is a device's key fingerprint from its stored public key.
func decodeKey(key string) (string, error) { return share.FingerprintOf(key) }
