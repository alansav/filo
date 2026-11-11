package reconcile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/store"
)

// env is one register with one root in a temp dir.
type env struct {
	t    *testing.T
	s    *store.Store
	base string // the root directory, canonical
	root store.Root
}

// fakeDevs makes every path report device 1 except those under an override, so tests do not depend on how the
// machine is mounted and can simulate a root on a mounted filesystem.
func fakeDevs(t *testing.T, devs map[string]uint64) {
	t.Helper()
	restore := fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
		id, err := real(p)
		if err != nil {
			return id, err
		}
		id.Dev = 1
		best := ""
		for prefix, dev := range devs {
			if (p == prefix || strings.HasPrefix(p, prefix+string(filepath.Separator))) && len(prefix) > len(best) {
				best, id.Dev = prefix, dev
			}
		}
		return id, nil
	})
	t.Cleanup(restore)
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// newEnv opens a register in its own temp dir and adds a temp dir as root. devs as in fakeDevs, relative to the root
// ("." for the root itself).
func newEnv(t *testing.T, devs map[string]uint64) *env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("device and inode lookup not implemented on windows")
	}
	base := canonicalTemp(t)
	abs := map[string]uint64{}
	for k, v := range devs {
		abs[filepath.Join(base, filepath.FromSlash(k))] = v
	}
	fakeDevs(t, abs)
	s, err := store.Open(context.Background(), store.Options{DBPath: filepath.Join(t.TempDir(), "filo.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r, err := s.AddRoot(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, s: s, base: base, root: r}
}

func (e *env) abs(rel string) string { return filepath.Join(e.base, filepath.FromSlash(rel)) }

func (e *env) write(rel, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.abs(rel), []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) appendTo(rel, content string) {
	e.t.Helper()
	f, err := os.OpenFile(e.abs(rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) mv(from, to string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.abs(to)), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(e.abs(from), e.abs(to)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) rm(rel string) {
	e.t.Helper()
	if err := os.RemoveAll(e.abs(rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) symlink(target, rel string) {
	e.t.Helper()
	if err := os.Symlink(target, e.abs(rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) link(from, to string) {
	e.t.Helper()
	if err := os.Link(e.abs(from), e.abs(to)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) chmod(rel string, mode os.FileMode) {
	e.t.Helper()
	if err := os.Chmod(e.abs(rel), mode); err != nil {
		e.t.Fatal(err)
	}
	if mode == 0 {
		e.t.Cleanup(func() { os.Chmod(e.abs(rel), 0o700) })
	}
}

func (e *env) touch(rel string, when time.Time) {
	e.t.Helper()
	if err := os.Chtimes(e.abs(rel), when, when); err != nil {
		e.t.Fatal(err)
	}
}

// known returns the register's current paths for the root.
func (e *env) known() map[string]store.Known {
	e.t.Helper()
	ks, err := e.s.Known(context.Background(), e.root.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]store.Known{}
	for _, k := range ks {
		m[k.Path] = k
	}
	return m
}

func (e *env) events(scanID int64) []store.Event {
	e.t.Helper()
	evs, err := e.s.Events(context.Background(), scanID)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func (e *env) rows() map[string]int64 {
	e.t.Helper()
	m, err := e.s.RowCounts(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

type counts struct{ New, Changed, Moved, Missing, Possible, Errors int }

func countsOf(s Summary) counts {
	return counts{s.New, s.Changed, s.Moved, s.Missing, s.PossibleMoves, s.Errors}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
}

type step struct {
	do    func(e *env)
	opt   Options // Target defaults to the root
	want  counts
	check func(t *testing.T, e *env, sum Summary, prev map[string]store.Known)
}

func TestScenarios(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)

	tests := []struct {
		name  string
		steps []step
	}{
		{"first scan sees everything as new, a rescan changes nothing", []step{
			{do: func(e *env) {
				e.write("a.txt", "a")
				e.write("d/b.txt", "b")
				e.symlink("a.txt", "link")
			}, want: counts{New: 3}},
			{want: counts{}, check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
				if len(e.events(sum.ScanID)) != 0 {
					t.Errorf("events on an unchanged rescan: %v", e.events(sum.ScanID))
				}
				if r := e.rows(); r["files"] != 3 || r["paths"] != 3 {
					t.Errorf("rows = %v, want 3 files and 3 paths", r)
				}
			}},
		}},
		{"touched file is updated quietly", []step{
			{do: func(e *env) { e.write("a.txt", "a"); e.touch("a.txt", old) }, want: counts{New: 1}},
			{do: func(e *env) { e.touch("a.txt", old.Add(time.Hour)) }, want: counts{},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if got := e.known()["a.txt"].MtimeNS; got != old.Add(time.Hour).UnixNano() {
						t.Errorf("mtime not updated: %d", got)
					}
				}},
		}},
		{"edited file is changed, same identity", []step{
			{do: func(e *env) { e.write("a.txt", "a") }, want: counts{New: 1}},
			{do: func(e *env) { e.appendTo("a.txt", "more") }, want: counts{Changed: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					now := e.known()["a.txt"]
					if now.FileID != prev["a.txt"].FileID || now.Hash == prev["a.txt"].Hash || now.Size != 5 {
						t.Errorf("after edit: %+v (before %+v)", now, prev["a.txt"])
					}
				}},
		}},
		{"rename keeps the identity and records history", []step{
			{do: func(e *env) { e.write("a.txt", "content") }, want: counts{New: 1}},
			{do: func(e *env) { e.mv("a.txt", "sub/b.txt") }, want: counts{Moved: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					id := prev["a.txt"].FileID
					if e.known()["sub/b.txt"].FileID != id {
						t.Fatalf("moved file has a new identity")
					}
					h, err := e.s.History(context.Background(), id)
					if err != nil {
						t.Fatal(err)
					}
					if len(h) != 2 || h[0].Path != "a.txt" || h[0].To != sum.ScanID ||
						h[1].Path != "sub/b.txt" || h[1].From != sum.ScanID || h[1].To != 0 {
						t.Errorf("history = %+v", h)
					}
				}},
		}},
		{"a path freed by one move is reused by another in the same scan", []step{
			// Distinct mtimes: same-size files with the same mtime are indistinguishable without hashing (the
			// size-and-mtime limit in the design), and this test is about path reuse.
			{do: func(e *env) {
				e.write("a", "content A")
				e.write("c", "content C")
				e.touch("a", old)
				e.touch("c", old.Add(time.Hour))
			}, want: counts{New: 2}},
			{do: func(e *env) { e.mv("a", "b"); e.mv("c", "a") }, want: counts{Moved: 2},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					k := e.known()
					if k["b"].FileID != prev["a"].FileID || k["a"].FileID != prev["c"].FileID {
						t.Errorf("identities not carried: before %v, after %v", prev, k)
					}
				}},
		}},
		{"a move that overwrites a file: the move is kept, the overwritten file is missing", []step{
			{do: func(e *env) {
				e.write("a", "content A")
				e.write("c", "content C")
				e.touch("a", old)
				e.touch("c", old.Add(time.Hour))
			}, want: counts{New: 2}},
			{do: func(e *env) { e.mv("c", "a") }, want: counts{Moved: 1, Missing: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if e.known()["a"].FileID != prev["c"].FileID {
						t.Error("a should now be c's identity")
					}
				}},
		}},
		{"a backup copy then an edit stays an edit", []step{
			{do: func(e *env) { e.write("a", "original") }, want: counts{New: 1}},
			{do: func(e *env) { e.write("a.bak", "original"); e.appendTo("a", " edited") }, want: counts{Changed: 1, New: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if e.known()["a"].FileID != prev["a"].FileID {
						t.Error("the edited file lost its identity")
					}
				}},
		}},
		{"a file gone in this scan wins over one gone earlier", []step{
			{do: func(e *env) { e.write("x", "same"); e.write("y", "same") }, want: counts{New: 2}},
			{do: func(e *env) { e.rm("x") }, want: counts{Missing: 1}},
			{do: func(e *env) { e.mv("y", "z") }, want: counts{Moved: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if e.known()["z"].FileID != prev["y"].FileID {
						t.Error("z should be y moved, not x returned")
					}
				}},
		}},
		// Same size and the same mtime on purpose: only the inode tells these files apart without hashing.
		{"path reuse with identical size and mtime", []step{
			{do: func(e *env) {
				e.write("a", "content A")
				e.write("c", "content C")
				e.touch("a", old)
				e.touch("c", old)
			}, want: counts{New: 2}},
			{do: func(e *env) { e.mv("a", "b"); e.mv("c", "a") }, want: counts{Moved: 2},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					k := e.known()
					if k["b"].FileID != prev["a"].FileID || k["a"].FileID != prev["c"].FileID {
						t.Errorf("identities not carried: before %v, after %v", prev, k)
					}
				}},
		}},
		{"a move overwriting a file with identical size and mtime", []step{
			{do: func(e *env) {
				e.write("a", "content A")
				e.write("c", "content C")
				e.touch("a", old)
				e.touch("c", old)
			}, want: counts{New: 2}},
			{do: func(e *env) { e.mv("c", "a") }, want: counts{Moved: 1, Missing: 1}},
		}},
		{"an atomic save of identical content is quiet, and records the new inode", []step{
			{do: func(e *env) { e.write("f", "same"); e.touch("f", old) }, want: counts{New: 1}},
			{do: func(e *env) { e.write("f.tmp", "same"); e.touch("f.tmp", old); e.mv("f.tmp", "f") }, want: counts{},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					now := e.known()["f"]
					if now.FileID != prev["f"].FileID || now.Ino == prev["f"].Ino || now.Ino == 0 {
						t.Errorf("before %+v, after %+v: same identity, new inode expected", prev["f"], now)
					}
				}},
		}},
		{"an atomic save of new content keeping size and mtime is a change", []step{
			{do: func(e *env) { e.write("f", "content A"); e.touch("f", old) }, want: counts{New: 1}},
			{do: func(e *env) { e.write("f.tmp", "content B"); e.touch("f.tmp", old); e.mv("f.tmp", "f") },
				want: counts{Changed: 1}},
		}},
		{"a hard link is a new file; the original is unchanged", []step{
			{do: func(e *env) { e.write("a", "content") }, want: counts{New: 1}},
			{do: func(e *env) { e.link("a", "b") }, want: counts{New: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					k := e.known()
					if k["a"] != prev["a"] || k["b"].Ino != k["a"].Ino {
						t.Errorf("a %+v -> %+v, b %+v", prev["a"], k["a"], k["b"])
					}
				}},
		}},
		{"ambiguous duplicates are not paired", []step{
			{do: func(e *env) { e.write("x1", "same"); e.write("x2", "same") }, want: counts{New: 2}},
			{do: func(e *env) { e.mv("x1", "y1"); e.mv("x2", "y2") }, want: counts{New: 2, Missing: 2}},
		}},
		{"duplicates moved with their names are paired by name", []step{
			{do: func(e *env) { e.write("d/a", "same"); e.write("d/b", "same") }, want: counts{New: 2}},
			{do: func(e *env) { e.mv("d/a", "e/a"); e.mv("d/b", "e/b") }, want: counts{Moved: 2},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					k := e.known()
					if k["e/a"].FileID != prev["d/a"].FileID || k["e/b"].FileID != prev["d/b"].FileID {
						t.Error("paired the wrong way round")
					}
				}},
		}},
		{"one moved plus a copy: the one with its name is the move", []step{
			{do: func(e *env) { e.write("a.txt", "c") }, want: counts{New: 1}},
			{do: func(e *env) { e.mv("a.txt", "x/a.txt"); e.write("x/b.txt", "c") }, want: counts{Moved: 1, New: 1}},
		}},
		{"one moved as two copies under new names: not guessed", []step{
			{do: func(e *env) { e.write("a.txt", "c") }, want: counts{New: 1}},
			{do: func(e *env) { e.mv("a.txt", "x/p.txt"); e.write("x/q.txt", "c") }, want: counts{New: 2, Missing: 1}},
		}},
		{"a copy of a present file is new, not a move", []step{
			{do: func(e *env) { e.write("a.txt", "c") }, want: counts{New: 1}},
			{do: func(e *env) { e.write("copy/a.txt", "c") }, want: counts{New: 1}},
		}},
		{"empty files are never paired", []step{
			{do: func(e *env) { e.write("e1", "") }, want: counts{New: 1}},
			{do: func(e *env) { e.mv("e1", "e2") }, want: counts{New: 1, Missing: 1}},
		}},
		{"edited and moved is a possible move, not applied", []step{
			{do: func(e *env) { e.write("a.txt", "v1") }, want: counts{New: 1}},
			{do: func(e *env) { e.rm("a.txt"); e.write("x/a.txt", "v2") }, want: counts{New: 1, Missing: 1, Possible: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if e.known()["x/a.txt"].FileID == prev["a.txt"].FileID {
						t.Error("a possible move was applied")
					}
				}},
		}},
		{"file moved away and replaced by a symlink", []step{
			{do: func(e *env) { e.write("f.txt", "content") }, want: counts{New: 1}},
			{do: func(e *env) { e.mv("f.txt", "g.txt"); e.symlink("g.txt", "f.txt") }, want: counts{Moved: 1, New: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					k := e.known()
					if k["g.txt"].FileID != prev["f.txt"].FileID || k["f.txt"].Kind != "symlink" {
						t.Errorf("after: %+v", k)
					}
				}},
		}},
		{"symlink retargeted is changed", []step{
			{do: func(e *env) { e.write("a", "a"); e.write("b", "b"); e.symlink("a", "l") }, want: counts{New: 3}},
			{do: func(e *env) { e.rm("l"); e.symlink("b", "l") }, want: counts{Changed: 1}},
		}},
		{"symlink whose target goes is changed (broken)", []step{
			{do: func(e *env) { e.write("a", "a"); e.symlink("a", "l") }, want: counts{New: 2}},
			{do: func(e *env) { e.rm("a") }, want: counts{Changed: 1, Missing: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if !e.known()["l"].LinkBroken {
						t.Error("link not marked broken")
					}
				}},
		}},
		{"deleted directory: its files are missing", []step{
			{do: func(e *env) { e.write("d/x", "x"); e.write("d/sub/y", "y") }, want: counts{New: 2}},
			{do: func(e *env) { e.rm("d") }, want: counts{Missing: 2}},
		}},
		{"scoped scan leaves files outside the scope alone", []step{
			{do: func(e *env) { e.write("a.txt", "a"); e.write("d/b.txt", "b"); e.write("d/c.txt", "c") }, want: counts{New: 3}},
			{do: func(e *env) { e.rm("a.txt"); e.rm("d/b.txt") }, opt: Options{Target: "d"}, want: counts{Missing: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if _, ok := e.known()["a.txt"]; !ok || sum.Scope != "d" {
						t.Errorf("a.txt marked missing by a scan of d (scope %q)", sum.Scope)
					}
				}},
		}},
		{"hidden files are untouched by a scan without --include-hidden", []step{
			{do: func(e *env) { e.write(".h/x", "x"); e.write("v", "v") }, opt: Options{IncludeHidden: true}, want: counts{New: 2}},
			{do: func(e *env) { e.rm(".h/x") }, want: counts{}},
			{opt: Options{IncludeHidden: true}, want: counts{Missing: 1}},
		}},
		{"unreadable directory: its files are not missing", []step{
			{do: func(e *env) { e.write("d/x", "x"); e.write("y", "y") }, want: counts{New: 2}},
			{do: func(e *env) { skipIfRoot(e.t); e.chmod("d", 0) }, want: counts{Errors: 1}},
		}},
		{"a file that can't be hashed is left untouched, then caught up", []step{
			{do: func(e *env) { e.write("f", "a") }, want: counts{New: 1}},
			{do: func(e *env) { skipIfRoot(e.t); e.appendTo("f", "bc"); e.chmod("f", 0) }, want: counts{Errors: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if now := e.known()["f"]; now != prev["f"] {
						t.Errorf("row changed after a failed hash: %+v -> %+v", prev["f"], now)
					}
				}},
			{do: func(e *env) { e.chmod("f", 0o600) }, want: counts{Changed: 1}},
		}},
		{"a new file that can't be hashed is recorded without a hash, then filled in", []step{
			{do: func(e *env) { skipIfRoot(e.t); e.write("f", "a"); e.chmod("f", 0) }, want: counts{New: 1, Errors: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if h := e.known()["f"].Hash; h != "" {
						t.Errorf("hash = %q, want none", h)
					}
				}},
			{do: func(e *env) { e.chmod("f", 0o600) }, want: counts{},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					if e.known()["f"].Hash == "" {
						t.Error("hash not filled in")
					}
				}},
		}},
		{"a file that comes back is relinked to its old identity", []step{
			{do: func(e *env) { e.write("a.txt", "content") }, want: counts{New: 1}},
			{do: func(e *env) { e.rm("a.txt") }, want: counts{Missing: 1}},
			{want: counts{}},
			{do: func(e *env) { e.write("restored/a.txt", "content") }, want: counts{Moved: 1},
				check: func(t *testing.T, e *env, sum Summary, prev map[string]store.Known) {
					evs := e.events(sum.ScanID)
					if len(evs) != 1 || evs[0].OtherPath != "a.txt" || !strings.Contains(evs[0].Detail, "returned") {
						t.Errorf("events = %+v", evs)
					}
				}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, nil)
			for i, st := range tc.steps {
				prev := e.known()
				if st.do != nil {
					st.do(e)
				}
				opt := st.opt
				if opt.Target == "" {
					opt.Target = e.base
				} else if !filepath.IsAbs(opt.Target) {
					opt.Target = e.abs(opt.Target)
				}
				sum, err := Scan(context.Background(), e.s, opt)
				if err != nil {
					t.Fatalf("step %d: Scan: %v", i, err)
				}
				if got := countsOf(sum); got != st.want {
					t.Fatalf("step %d: counts = %+v, want %+v; events %+v", i, got, st.want, e.events(sum.ScanID))
				}
				if st.check != nil {
					st.check(t, e, sum, prev)
				}
			}
		})
	}
}

func TestMoveBetweenRoots(t *testing.T) {
	e := newEnv(t, nil)
	other := canonicalTemp(t)
	r2, err := e.s.AddRoot(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	e.write("doc.pdf", "pdf bytes")
	if _, err := Scan(context.Background(), e.s, Options{Target: e.base}); err != nil {
		t.Fatal(err)
	}
	id := e.known()["doc.pdf"].FileID

	if err := os.Rename(e.abs("doc.pdf"), filepath.Join(other, "doc.pdf")); err != nil {
		t.Fatal(err)
	}
	if sum, err := Scan(context.Background(), e.s, Options{Target: e.base}); err != nil || sum.Missing != 1 {
		t.Fatalf("first root: %+v, %v", sum, err)
	}
	sum, err := Scan(context.Background(), e.s, Options{Target: other})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Moved != 1 || sum.New != 0 {
		t.Fatalf("second root: %+v", sum)
	}
	ks, err := e.s.Known(context.Background(), r2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ks) != 1 || ks[0].FileID != id {
		t.Errorf("file in the second root = %+v, want identity %d", ks, id)
	}
	evs := e.events(sum.ScanID)
	if len(evs) != 1 || !strings.Contains(evs[0].Detail, e.base) {
		t.Errorf("event should name the old root: %+v", evs)
	}
}

func TestEmptyGuard(t *testing.T) {
	tests := []struct {
		name       string
		mounted    bool
		allowEmpty bool
		wantErr    bool
	}{
		{"root on a mount, emptied: refused", true, false, true},
		{"root on a mount, emptied, AllowEmpty: recorded", true, true, false},
		{"root not on a mount, emptied: recorded", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var devs map[string]uint64
			if tc.mounted {
				devs = map[string]uint64{".": 2}
			}
			e := newEnv(t, devs)
			if (e.root.MountPath != "") != tc.mounted {
				t.Fatalf("mount path = %q", e.root.MountPath)
			}
			e.write("a", "a")
			e.write("b/c", "c")
			if _, err := Scan(context.Background(), e.s, Options{Target: e.base}); err != nil {
				t.Fatal(err)
			}
			e.rm("a")
			e.rm("b")
			before := e.rows()

			sum, err := Scan(context.Background(), e.s, Options{Target: e.base, AllowEmpty: tc.allowEmpty})
			if tc.wantErr {
				if !errors.Is(err, ErrEmptyWalk) {
					t.Fatalf("err = %v, want ErrEmptyWalk", err)
				}
				if after := e.rows(); after["scans"] != before["scans"] || len(e.known()) != 2 {
					t.Errorf("a refused scan wrote something: %v -> %v", before, after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sum.Missing != 2 {
				t.Errorf("missing = %d, want 2", sum.Missing)
			}
		})
	}
}

func TestScanAllSkipsUnmountedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	base := canonicalTemp(t)
	disk := filepath.Join(base, "mnt", "disk")
	home := filepath.Join(base, "home")
	for _, d := range []string{disk, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "h"), []byte("h"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeDevs(t, map[string]uint64{disk: 2})
	s, err := store.Open(context.Background(), store.Options{DBPath: filepath.Join(t.TempDir(), "filo.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, r := range []string{disk, home} {
		if _, err := s.AddRoot(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}

	fakeDevs(t, nil) // the disk is no longer mounted
	results, err := ScanAll(context.Background(), s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byRoot := map[string]RootResult{}
	for _, r := range results {
		byRoot[r.Root.Path] = r
	}
	if !errors.Is(byRoot[disk].Err, store.ErrNotMounted) {
		t.Errorf("disk: err = %v, want ErrNotMounted", byRoot[disk].Err)
	}
	if r := byRoot[home]; r.Err != nil || r.Summary.New != 1 {
		t.Errorf("home: %+v", r)
	}
}

func TestDatabaseInsideRootNotRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	base := canonicalTemp(t)
	fakeDevs(t, nil)
	if err := os.WriteFile(filepath.Join(base, "keep"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), store.Options{DBPath: filepath.Join(base, "register.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.AddRoot(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	sum, err := Scan(context.Background(), s, Options{Target: base})
	if err != nil {
		t.Fatal(err)
	}
	if sum.New != 1 {
		t.Errorf("new = %d, want 1 (only keep); samples %v", sum.New, sum.Samples)
	}
}

func TestCancelledScanWritesNothing(t *testing.T) {
	e := newEnv(t, nil)
	e.write("a", "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, e.s, Options{Target: e.base}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if r := e.rows(); r["scans"] != 0 || r["files"] != 0 {
		t.Errorf("rows after a cancelled scan: %v", r)
	}
}

// TestTestTree runs scripts/testtree.sh and checks the result the script itself states.
func TestTestTree(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	e := newEnv(t, nil)
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "testtree.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := e.abs("FiloTest")
	run := func(cmd string) {
		t.Helper()
		if out, err := exec.Command(bash, script, cmd, dir).CombinedOutput(); err != nil {
			t.Fatalf("testtree.sh %s: %v\n%s", cmd, err, out)
		}
	}

	run("init")
	first, err := Scan(context.Background(), e.s, Options{Target: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := countsOf(first); got != (counts{New: 13}) {
		t.Fatalf("first scan = %+v, want 13 new", got)
	}

	run("mutate")
	sum, err := Scan(context.Background(), e.s, Options{Target: dir})
	if err != nil {
		t.Fatal(err)
	}
	// "expected: moved 2, changed 2 (todo.md, link-to-report), missing 2, new 3, possible move 1"
	if got := countsOf(sum); got != (counts{Moved: 2, Changed: 2, Missing: 2, New: 3, Possible: 1}) {
		t.Fatalf("after mutate = %+v; events %+v", got, e.events(sum.ScanID))
	}
	changed := map[string]bool{}
	for _, ev := range e.events(sum.ScanID) {
		if ev.Kind == Changed {
			changed[ev.Path] = true
		}
	}
	if !changed["FiloTest/notes/todo.md"] || !changed["FiloTest/link-to-report"] {
		t.Errorf("changed = %v, want todo.md and link-to-report", changed)
	}
}

// TestScanTiming records how long a first scan of a generated tree takes. It is a measurement, not a limit.
func TestScanTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	e := newEnv(t, nil)
	const n = 20000
	for i := range n {
		e.write(filepath.Join("d"+string(rune('a'+i%26)), "f"+strconv.Itoa(i)), "content "+strconv.Itoa(i))
	}
	start := time.Now()
	sum, err := Scan(context.Background(), e.s, Options{Target: e.base})
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if sum.New != n {
		t.Fatalf("new = %d, want %d", sum.New, n)
	}
	t.Logf("first scan of %d files: %v (%.0f files/s)", n, took, float64(n)/took.Seconds())
	start = time.Now()
	if _, err := Scan(context.Background(), e.s, Options{Target: e.base}); err != nil {
		t.Fatal(err)
	}
	t.Logf("unchanged rescan: %v", time.Since(start))
}

// TestProgressCounters runs real scans with progress switched on and checks the counters match what each scan did.
func TestProgressCounters(t *testing.T) {
	e := newEnv(t, nil)
	e.write("a.txt", "hello")     // 5 bytes
	e.write("d/b.txt", "goodbye") // 7 bytes
	e.symlink("a.txt", "link")    // no hash

	type want struct{ walked, hashFiles, hashBytes int64 }
	steps := []struct {
		name string
		do   func()
		want want
	}{
		// The root lists a.txt, d and link; d lists b.txt. Directories count as listed entries too.
		{"first scan hashes every regular file", nil, want{walked: 4, hashFiles: 2, hashBytes: 12}},
		{"unchanged rescan hashes nothing", nil, want{walked: 4}},
		{"a touched file is rehashed", func() { e.touch("d/b.txt", time.Now().Add(-time.Hour)) },
			want{walked: 4, hashFiles: 1, hashBytes: 7}},
	}
	p := &Progress{}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			if st.do != nil {
				st.do()
			}
			if _, err := Scan(context.Background(), e.s, Options{Target: e.base, Progress: p}); err != nil {
				t.Fatal(err)
			}
			got := want{p.Walked.Load(), p.HashFiles.Load(), p.HashBytes.Load()}
			if got != st.want {
				t.Errorf("counters = %+v, want %+v", got, st.want)
			}
			// Everything that was due to be hashed was hashed, byte for byte.
			if p.Hash.Files.Load() != st.want.hashFiles || p.Hash.Bytes.Load() != st.want.hashBytes {
				t.Errorf("hashed %d files, %d bytes; want %d, %d",
					p.Hash.Files.Load(), p.Hash.Bytes.Load(), st.want.hashFiles, st.want.hashBytes)
			}
			if p.Root() != e.base {
				t.Errorf("Root() = %q, want %q", p.Root(), e.base)
			}
			if ph := p.Phase.Load(); ph != PhaseIdle {
				t.Errorf("phase after the scan = %d, want idle", ph)
			}
		})
	}
}
