package share

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

var t0 = time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

// node is one device in a test: its register, a root, and its synced directory there (root/Sync).
type node struct {
	t    *testing.T
	s    *store.Store
	root string
	sync string
	name string
}

func newNode(t *testing.T, name string) *node {
	t.Helper()
	ctx := context.Background()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, store.Options{DBPath: filepath.Join(dbDir, "filo.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetDeviceName(ctx, name); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	sync := filepath.Join(root, "Sync")
	if err := os.Mkdir(sync, 0o755); err != nil {
		t.Fatal(err)
	}
	return &node{t: t, s: s, root: root, sync: sync, name: name}
}

func (n *node) dev() store.Device {
	d, err := n.s.Device(context.Background())
	if err != nil {
		n.t.Fatal(err)
	}
	return d
}

func (n *node) mark() store.SyncedDir {
	n.t.Helper()
	tg, err := Inspect(context.Background(), n.s, n.sync)
	if err != nil {
		n.t.Fatal(err)
	}
	sd, problems, err := Mark(context.Background(), n.s, tg, t0)
	if err != nil || len(problems) > 0 {
		n.t.Fatal(err)
	}
	return sd
}

func (n *node) join() (Target, []string) {
	n.t.Helper()
	tg, err := Inspect(context.Background(), n.s, n.sync)
	if err != nil {
		n.t.Fatal(err)
	}
	if tg.Plan == nil {
		n.t.Fatalf("%s: nothing to join", n.name)
	}
	_, notes, err := Join(context.Background(), n.s, tg, t0)
	if err != nil {
		n.t.Fatal(err)
	}
	return tg, notes
}

func (n *node) imp(at time.Time) DirReport {
	n.t.Helper()
	rep, err := Import(context.Background(), n.s, at)
	if err != nil {
		n.t.Fatal(err)
	}
	if len(rep.Dirs) != 1 {
		n.t.Fatalf("%s: %d synced directories", n.name, len(rep.Dirs))
	}
	return rep.Dirs[0]
}

func (n *node) known(dev string) store.KnownDevice {
	list, _ := n.s.KnownDevices(context.Background())
	for _, d := range list {
		if d.Dev == dev {
			return d
		}
	}
	return store.KnownDevice{}
}

func (n *node) lines(dev string) int {
	dirs, _ := n.s.SyncedDirs(context.Background())
	ls, _ := n.s.JournalLines(context.Background(), dirs[0].ID, dev)
	return len(ls)
}

// syncFilo is the sync service: every file of from's .filo/ copied into to's, over what is there, unless what is
// there is newer (the same journal with more lines).
func syncFilo(t *testing.T, from, to *node) {
	t.Helper()
	src := filepath.Join(from.sync, journal.FiloDir)
	dst := filepath.Join(to.sync, journal.FiloDir)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		b, err := os.ReadFile(filepath.Join(src, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// A journal the destination has more of (its device wrote on there) is newer: the sync keeps it.
		if have, err := os.ReadFile(filepath.Join(dst, de.Name())); err == nil && len(have) > len(b) &&
			bytes.HasPrefix(have, b) {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, de.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMarkJoinAndTrust(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	for _, name := range []string{journal.MarkName, journal.JournalName(a.dev().ID), journal.StatusName(a.dev().ID)} {
		if _, err := os.Stat(filepath.Join(a.sync, journal.FiloDir, name)); err != nil {
			t.Fatalf("after marking: %v", err)
		}
	}
	if dr := a.imp(t0); len(dr.Problems) != 0 || !dr.Writing {
		t.Fatalf("a's import after marking: %+v", dr)
	}

	syncFilo(t, a, b)
	tg, err := Inspect(ctx, b.s, b.sync)
	if err != nil || tg.Plan == nil {
		t.Fatalf("Inspect on b: %+v, %v", tg, err)
	}
	p := tg.Plan
	if len(p.Devices) != 1 || p.Devices[0].Name != "laptop" || p.MarkedByName() != "laptop" ||
		p.Devices[0].Fingerprint != a.dev().Fingerprint() || p.Devices[0].Seen == "" {
		t.Fatalf("plan = %+v", p)
	}
	b.join()
	if !b.known(a.dev().ID).Trusted() || b.lines(a.dev().ID) != 1 || b.lines(b.dev().ID) != 1 {
		t.Fatal("after joining, b does not trust a or keep the journals")
	}

	// a sees b's journal but does not trust it until the person says so.
	syncFilo(t, b, a)
	dr := a.imp(t0.Add(time.Minute))
	if len(dr.Strange) != 1 || dr.Strange[0].Name != "desktop" {
		t.Fatalf("a's import: strange = %+v", dr.Strange)
	}
	kd, err := FindDevice(ctx, a.s, "Desktop")
	if err != nil {
		t.Fatal(err)
	}
	in, err := Where(ctx, a.s, kd.Dev)
	if err != nil || len(in) != 1 {
		t.Fatalf("Where = %v, %v", in, err)
	}
	if err := Trust(ctx, a.s, kd, in, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	dr = a.imp(t0.Add(3 * time.Minute))
	if len(dr.Strange) != 0 || len(dr.Problems) != 0 || a.lines(b.dev().ID) != 1 || a.lines(a.dev().ID) != 2 {
		t.Fatalf("after trusting: %+v, lines %d / %d", dr, a.lines(b.dev().ID), a.lines(a.dev().ID))
	}

	// A third device joins later and trusts both; then a vouches for nothing new, and c reads a's trust line.
	c := newNode(t, "tablet")
	syncFilo(t, a, c)
	tg, _ = Inspect(ctx, c.s, c.sync)
	if len(tg.Plan.Devices) != 2 {
		t.Fatalf("c's plan: %+v", tg.Plan.Devices)
	}
	c.join()
	if !c.known(a.dev().ID).Trusted() || !c.known(b.dev().ID).Trusted() {
		t.Fatal("c does not trust both")
	}
}

func TestVouching(t *testing.T) {
	ctx := context.Background()
	a, b, c := newNode(t, "laptop"), newNode(t, "desktop"), newNode(t, "tablet")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	syncFilo(t, a, c) // c joins before b's journal reaches it: it trusts a only
	c.join()
	// a trusts b, which writes a trust line; c then trusts b through it, without asking.
	syncFilo(t, b, a)
	a.imp(t0)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	if err := Trust(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	syncFilo(t, a, c)
	c.imp(t0) // takes in a's trust line
	dr := c.imp(t0.Add(time.Minute))
	if k := c.known(b.dev().ID); !k.Trusted() || k.VouchedBy != a.dev().ID {
		t.Fatalf("c's view of b: %+v (%+v)", k, dr)
	}
	if c.lines(b.dev().ID) != 1 {
		t.Fatal("c did not read b's journal once b was vouched for")
	}
}

func TestForgedAndDamagedJournals(t *testing.T) {
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	aj := filepath.Join(b.sync, journal.FiloDir, journal.JournalName(a.dev().ID))

	// Someone appends a line to a's journal on b, signed with another key: refused, reported, nothing taken in.
	x := newNode(t, "intruder")
	kept, _ := b.s.JournalLines(context.Background(), mustDir(t, b).ID, a.dev().ID)
	last, _ := journal.Parse(kept[len(kept)-1].Raw)
	forged, err := journal.Encode(x.dev().Key, journal.Header{Dev: a.dev().ID, Seq: last.Seq + 1, Prev: last.Hash(),
		Kind: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(aj, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(forged, '\n'))
	f.Close()
	dr := b.imp(t0)
	if len(dr.Problems) != 1 || !strings.Contains(dr.Problems[0], "signature") || b.lines(a.dev().ID) != 1 {
		t.Fatalf("a forged line: problems %q, lines %d", dr.Problems, b.lines(a.dev().ID))
	}
	// a's own copy is rewritten from its register when it next runs.
	syncFilo(t, b, a)
	dr = a.imp(t0)
	if len(dr.Notes) == 0 {
		t.Fatalf("a did not write its journal back: %+v", dr)
	}
	want := ownBytes(mustLines(t, a, a.dev().ID))
	if got, _ := os.ReadFile(filepath.Join(a.sync, journal.FiloDir, journal.JournalName(a.dev().ID))); !bytes.Equal(got, want) {
		t.Fatal("a's journal is not as its register holds it")
	}

	// A journal deleted, the mark deleted: both written back.
	os.Remove(filepath.Join(a.sync, journal.FiloDir, journal.JournalName(a.dev().ID)))
	os.Remove(filepath.Join(a.sync, journal.FiloDir, journal.MarkName))
	dr = a.imp(t0)
	if len(dr.Notes) != 2 || len(dr.Problems) != 0 {
		t.Fatalf("after deleting the journal and mark: %+v", dr)
	}
	// .filo deleted as a whole.
	os.RemoveAll(filepath.Join(a.sync, journal.FiloDir))
	if dr = a.imp(t0); len(dr.Problems) != 0 || !holdsMark(a.sync, mustDir(t, a).SyncID) {
		t.Fatalf("after deleting .filo: %+v", dr)
	}
}

func TestOwnJournalAhead(t *testing.T) {
	ctx := context.Background()
	a := newNode(t, "laptop")
	sd := a.mark()
	// a writes a line, then its register goes back to before it (a restored snapshot): the journal is ahead.
	kept := mustLines(t, a, a.dev().ID)
	last, _ := journal.Parse(kept[0].Raw)
	w, _ := journal.Resume(a.dev().Key, last)
	raw, _ := w.Add(KindName, NameBody{Name: "laptop"}, t0)
	p := filepath.Join(a.sync, journal.FiloDir, journal.JournalName(a.dev().ID))
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write(append(raw, '\n'))
	f.Close()
	dr := a.imp(t0)
	if dr.Writing || len(dr.Problems) != 1 || !strings.Contains(dr.Problems[0], "holds lines this register does not") {
		t.Fatalf("a journal ahead: %+v", dr)
	}
	before, _ := os.ReadFile(p)
	if err := AppendOwn(ctx, a.s, a.dev(), sd, KindName, NameBody{Name: "x"}, t0); err == nil {
		t.Fatal("AppendOwn wrote to a journal ahead of the register")
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Fatal("AppendOwn changed a journal ahead of the register")
	}
	if len(mustLines(t, a, a.dev().ID)) != 1 {
		t.Fatal("AppendOwn kept a line in the register for a journal ahead of it")
	}
}

func TestRetire(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	syncFilo(t, b, a)
	a.imp(t0)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	Trust(ctx, a.s, kd, in, t0)
	a.imp(t0)
	kd, _ = FindDevice(ctx, a.s, "desktop")
	if err := Retire(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	if err := Retire(ctx, a.s, store.KnownDevice{Dev: a.dev().ID}, in, t0); err == nil {
		t.Fatal("a retired itself")
	}
	// b writes a line after it was retired: a refuses it.
	if err := Rename(ctx, b.s, t0); err != nil {
		t.Fatal(err)
	}
	syncFilo(t, b, a)
	a.imp(t0)
	if a.lines(b.dev().ID) != 1 {
		t.Fatalf("a took in %d of b's lines after retiring it", a.lines(b.dev().ID))
	}
	// b reads a's retire line and stops writing.
	syncFilo(t, a, b)
	b.imp(t0)
	dr := b.imp(t0.Add(time.Minute))
	if dr.Writing || len(dr.Problems) == 0 {
		t.Fatalf("b after being retired: %+v", dr)
	}
}

func TestNoteMarksAndInspect(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	syncFilo(t, a, b)
	rootB, _ := b.s.RootAt(ctx, b.root)
	if err := NoteMarks(ctx, b.s, rootB, "", []string{"Sync"}, t0); err != nil {
		t.Fatal(err)
	}
	found, _ := FoundMarks(ctx, b.s)
	if len(found) != 1 || found[0].Kind != FoundNew {
		t.Fatalf("found = %+v", found)
	}
	// Nested: a directory inside the marked one cannot be marked.
	inner := filepath.Join(b.sync, "inner")
	os.Mkdir(inner, 0o755)
	if _, err := Inspect(ctx, b.s, inner); err == nil {
		t.Fatal("a directory inside a found mark was offered")
	}
	b.join()
	if found, _ := FoundMarks(ctx, b.s); len(found) != 0 {
		t.Fatalf("found after joining: %+v", found)
	}
	// A copy of the synced directory elsewhere in the root: reported as a copy, not joinable.
	cp := filepath.Join(b.root, "Copy")
	os.MkdirAll(filepath.Join(cp, journal.FiloDir), 0o700)
	src := filepath.Join(b.sync, journal.FiloDir, journal.MarkName)
	data, _ := os.ReadFile(src)
	os.WriteFile(filepath.Join(cp, journal.FiloDir, journal.MarkName), data, 0o600)
	NoteMarks(ctx, b.s, rootB, "", []string{"Sync", "Copy"}, t0)
	found, _ = FoundMarks(ctx, b.s)
	if len(found) != 1 || found[0].Kind != FoundCopy {
		t.Fatalf("a copy: %+v", found)
	}
	if _, err := Inspect(ctx, b.s, cp); err == nil {
		t.Fatal("a copy was offered for joining")
	}
	// The synced directory moves: the copy's place is now where it is; Inspect offers the move.
	os.Rename(b.sync, filepath.Join(b.root, "Gone"))
	os.RemoveAll(filepath.Join(b.root, "Gone"))
	found, _ = FoundMarks(ctx, b.s)
	if len(found) != 1 || found[0].Kind != FoundMoved {
		t.Fatalf("moved: %+v", found)
	}
	tg, err := Inspect(ctx, b.s, cp)
	if err != nil || tg.Moved == nil {
		t.Fatalf("Inspect of the new place: %+v, %v", tg, err)
	}
	if dr := b.imp(t0); len(dr.Problems) != 1 {
		t.Fatalf("import while it is not at its place: %+v", dr)
	}
}

func mustDir(t *testing.T, n *node) store.SyncedDir {
	t.Helper()
	dirs, err := n.s.SyncedDirs(context.Background())
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no synced directory: %v", err)
	}
	return dirs[0]
}

func mustLines(t *testing.T, n *node, dev string) []store.JournalLine {
	t.Helper()
	ls, err := n.s.JournalLines(context.Background(), mustDir(t, n).ID, dev)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestAppendOwnAfterATornAppend(t *testing.T) {
	ctx := context.Background()
	a := newNode(t, "laptop")
	sd := a.mark()
	p := filepath.Join(a.sync, journal.FiloDir, journal.JournalName(a.dev().ID))
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte(`{"v":1,"dev":"part`)) // a power cut part way through a line
	f.Close()
	if err := AppendOwn(ctx, a.s, a.dev(), sd, KindName, NameBody{Name: "laptop"}, t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if res := journal.Check(data, a.dev().ID); res.Stop != nil || res.Partial || len(res.Lines) != 2 {
		t.Fatalf("after a torn append: stop %v, partial %v, %d lines", res.Stop, res.Partial, len(res.Lines))
	}
}

// TestImportWithNothingNewWritesNothing: a run that reads nothing new changes nothing in the register (so a timer's
// scan takes no snapshot), while this device's status file is still written at most hourly.
func TestImportWithNothingNewWritesNothing(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	b.imp(t0)
	db := b.s.DBPath()
	b.s.Close()
	s, err := store.Open(ctx, store.Options{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b.s = s
	status := filepath.Join(b.sync, journal.FiloDir, journal.StatusName(b.dev().ID))
	before, _ := os.ReadFile(status)
	b.imp(t0.Add(10 * time.Minute))
	if s.Changed() {
		t.Fatal("an import that read nothing new wrote to the register")
	}
	if after, _ := os.ReadFile(status); !bytes.Equal(before, after) {
		t.Fatal("the status file was written again within the hour")
	}
	dr := b.imp(t0.Add(2 * time.Hour))
	if s.Changed() {
		t.Fatal("writing the hourly status file wrote to the register")
	}
	if after, _ := os.ReadFile(status); bytes.Equal(before, after) || dr.Status[b.dev().ID].Seen == "" {
		t.Fatal("the status file was not written after an hour")
	}
	if dr.Status[a.dev().ID].Seen == "" {
		t.Fatal("the other device's status file was not read")
	}
}

// trio is three devices that all trust each other in one synced directory.
func trio(t *testing.T) (a, b, c *node) {
	t.Helper()
	ctx := context.Background()
	a, b, c = newNode(t, "laptop"), newNode(t, "desktop"), newNode(t, "tablet")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	syncFilo(t, b, c)
	c.join()
	for _, n := range []*node{a, b} {
		syncFilo(t, c, n)
		n.imp(t0)
		for _, other := range []string{"desktop", "tablet"} {
			if kd, err := FindDevice(ctx, n.s, other); err == nil && !kd.Trusted() {
				in, _ := Where(ctx, n.s, kd.Dev)
				if err := Trust(ctx, n.s, kd, in, t0); err != nil {
					t.Fatal(err)
				}
			}
		}
		n.imp(t0)
	}
	syncFilo(t, a, c)
	syncFilo(t, b, c)
	c.imp(t0)
	c.imp(t0)
	return a, b, c
}

// A retire line read while the retired device wrote nothing new keeps what was read of its journal.
func TestRetireKeepsTheReadPosition(t *testing.T) {
	ctx := context.Background()
	a, b, c := trio(t)
	c.imp(t0)
	before := c.lines(b.dev().ID)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	if err := Retire(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	syncFilo(t, a, c)
	c.imp(t0)
	js, _ := c.s.Journals(ctx, mustDir(t, c).ID)
	for _, j := range js {
		if j.Dev == b.dev().ID && (j.Seq == 0 || j.RetiredSeq == 0 || int(j.Seq) != before) {
			t.Fatalf("b's journal on c after its retirement: %+v (had %d lines)", j, before)
		}
	}
}

// A retire line and the retired device's later lines read in one import: the later lines are refused, whichever
// journal is listed first.
func TestRetireInOneImportRefusesLaterLines(t *testing.T) {
	ctx := context.Background()
	a, b, c := trio(t)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	keep := c.lines(b.dev().ID)
	if err := Retire(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	// b, not yet knowing, vouches for an intruder.
	x := newNode(t, "intruder")
	kx := store.KnownDevice{Dev: x.dev().ID, Name: "intruder", Key: deviceKey(x)}
	if err := AppendOwn(ctx, b.s, b.dev(), mustDir(t, b), KindTrust, TrustBody{Device: kx.Dev, Key: kx.Key,
		Name: kx.Name}, t0); err != nil {
		t.Fatal(err)
	}
	syncFilo(t, a, c)
	syncFilo(t, b, c)
	c.imp(t0)
	if c.lines(b.dev().ID) != keep {
		t.Fatalf("c kept %d of b's lines, want the %d before its retirement", c.lines(b.dev().ID), keep)
	}
	if c.known(x.dev().ID).Trusted() {
		t.Fatal("a line written after the retirement made a device trusted")
	}
}

// Joining refuses a journal signed with another key than the one trusted for its device.
func TestJoinRefusesAnotherKey(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	syncFilo(t, a, b)
	b.join()
	// Elsewhere, a directory holding a journal under a's ID, signed with an intruder's key.
	x := newNode(t, "intruder")
	other := filepath.Join(b.root, "Other")
	os.MkdirAll(filepath.Join(other, journal.FiloDir), 0o700)
	_, first, err := journal.Start(x.dev().Key, a.dev().ID, "laptop", t0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := device.NewID()
	mark, err := journal.NewMark(x.dev().Key, a.dev().ID, id, t0)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(other, journal.FiloDir, journal.MarkName), append(mark, '\n'), 0o600)
	os.WriteFile(filepath.Join(other, journal.FiloDir, journal.JournalName(a.dev().ID)), append(first, '\n'), 0o600)
	tg, err := Inspect(ctx, b.s, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Join(ctx, b.s, tg, t0); err == nil {
		t.Fatal("joined a directory holding a journal under a trusted device's ID with another key")
	}
	if k := b.known(a.dev().ID); k.Key != deviceKey(a) {
		t.Fatal("a's trusted key was replaced")
	}
}

// A synced directory moved: its old place, left empty, gets no mark written back while the scan found it elsewhere.
func TestMovedDirectoryIsNotWrittenBack(t *testing.T) {
	ctx := context.Background()
	a := newNode(t, "laptop")
	a.mark()
	moved := filepath.Join(a.root, "Files")
	if err := os.Rename(a.sync, moved); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(a.sync, 0o755) // the sync service left the old directory, empty
	root, _ := a.s.RootAt(ctx, a.root)
	NoteMarks(ctx, a.s, root, "", []string{"Files"}, t0)
	dr := a.imp(t0)
	if len(dr.Problems) != 1 || !strings.Contains(dr.Problems[0], "was found at") {
		t.Fatalf("import of a moved directory: %+v", dr)
	}
	if _, err := os.Stat(filepath.Join(a.sync, journal.FiloDir)); err == nil {
		t.Fatal("the old place got a .filo")
	}
	found, _ := FoundMarks(ctx, a.s)
	if len(found) != 1 || found[0].Kind != FoundMoved {
		t.Fatalf("found = %+v", found)
	}
}

// Renaming a device retired in one synced directory does not fail, and keeps its row.
func TestRenameWhereRetired(t *testing.T) {
	ctx := context.Background()
	a, b, _ := trio(t)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	Retire(ctx, a.s, kd, in, t0)
	syncFilo(t, a, b)
	b.imp(t0)
	created := b.known(b.dev().ID).TrustedUTC
	if err := b.s.SetDeviceName(ctx, "desk2"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(ctx, b.s, t0.Add(time.Hour)); err != nil {
		t.Fatalf("renaming where retired: %v", err)
	}
	if k := b.known(b.dev().ID); k.Name != "desk2" || k.TrustedUTC != created {
		t.Fatalf("this device's row after renaming: %+v", k)
	}
}

// A mark that cannot be joined (its device's journal not there) can still be ignored.
func TestIgnoreAMarkThatCannotBeJoined(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	a.mark()
	os.MkdirAll(filepath.Join(b.sync, journal.FiloDir), 0o700)
	data, _ := os.ReadFile(filepath.Join(a.sync, journal.FiloDir, journal.MarkName))
	os.WriteFile(filepath.Join(b.sync, journal.FiloDir, journal.MarkName), data, 0o600)
	if _, err := Inspect(ctx, b.s, b.sync); err == nil {
		t.Fatal("a mark without its device's journal was offered for joining")
	}
	if err := Ignore(ctx, b.s, b.sync, t0); err != nil {
		t.Fatal(err)
	}
	root, _ := b.s.RootAt(ctx, b.root)
	NoteMarks(ctx, b.s, root, "", []string{"Sync"}, t0)
	if found, _ := FoundMarks(ctx, b.s); len(found) != 0 {
		t.Fatalf("an ignored mark is still found: %+v", found)
	}
}

func deviceKey(n *node) string {
	k, _ := n.s.Device(context.Background())
	return deviceEncode(k)
}

func deviceEncode(d store.Device) string { return selfFrom(nil, d, t0).Key }
