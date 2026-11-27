package dispose

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// Rename and CheckRename are a Move and a CheckMove in the file's own directory, as the rename tests use them.
func Rename(ctx context.Context, rec RenameRecorder, c store.Candidate, name string, now time.Time) (Outcome,
	error) {
	return NewMover(rec, func() time.Time { return now }).Move(ctx, store.Rename{Candidate: c,
		Dir: dirOf(c.Path), Name: name})
}

func CheckRename(c store.Candidate, name string) string {
	return CheckMove(store.Rename{Candidate: c, Dir: dirOf(c.Path), Name: name})
}

func dirOf(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// fakeRenames records what Rename reported.
type fakeRenames struct{ names []string }

func (f *fakeRenames) Renamed(_ context.Context, c store.Candidate, newPath string, _ time.Time) error {
	f.names = append(f.names, c.Path+" -> "+newPath)
	return nil
}

func TestRename(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("renames need renameat2 (Linux)")
	}
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, root string) // after the candidate is taken, as a change since the last scan
		to     string
		done   bool
		failed bool
		want   string // a word the reason holds
	}{
		{name: "renamed", to: "British Gas bill.pdf", done: true},
		{name: "case only", to: "Britsh gas bill.pdf", done: true},
		{name: "a space removed", to: "Britsh Gas bill.pdf ", done: true},
		{name: "a name taken on disk", to: "taken.pdf", want: "already exists",
			setup: func(t *testing.T, root string) { write(t, filepath.Join(root, "d", "taken.pdf"), "other") }},
		{name: "edited since the last scan", to: "x.pdf", want: "changed since the last scan",
			setup: func(t *testing.T, root string) {
				write(t, filepath.Join(root, "d", "Britsh Gas bill.pdf"), "longer content")
			}},
		{name: "gone", to: "x.pdf", want: "changed since the last scan",
			setup: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "d", "Britsh Gas bill.pdf")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "replaced by a new file of the same size", to: "x.pdf", want: "changed since the last scan",
			setup: func(t *testing.T, root string) {
				p := filepath.Join(root, "d", "Britsh Gas bill.pdf")
				fi, _ := os.Lstat(p)
				write(t, p+".new", "bill")
				if err := os.Chtimes(p+".new", fi.ModTime(), fi.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(p+".new", p); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a directory on the way is now a symlink", to: "x.pdf", want: "symlink",
			setup: func(t *testing.T, root string) {
				if err := os.Rename(filepath.Join(root, "d"), filepath.Join(root, "elsewhere")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("elsewhere", filepath.Join(root, "d")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "not one name", to: "../x.pdf", failed: true, want: "not a file name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalTemp(t)
			write(t, filepath.Join(root, "d", "Britsh Gas bill.pdf"), "bill")
			c := candidate(t, root, filepath.Join("d", "Britsh Gas bill.pdf"))
			if tc.setup != nil {
				tc.setup(t, root)
			}
			rec := &fakeRenames{}
			o, err := Rename(ctx, rec, c, tc.to, now)
			if err != nil {
				t.Fatal(err)
			}
			if o.Done != tc.done || o.Failed != tc.failed || !strings.Contains(o.Reason, tc.want) {
				t.Fatalf("outcome %+v, want done %v failed %v reason %q", o, tc.done, tc.failed, tc.want)
			}
			if !tc.done {
				if len(rec.names) != 0 {
					t.Errorf("recorded %v for a rename not done", rec.names)
				}
				return
			}
			if got, err := os.ReadFile(filepath.Join(root, "d", tc.to)); err != nil || string(got) != "bill" {
				t.Errorf("new name: %q, %v", got, err)
			}
			entries, _ := os.ReadDir(filepath.Join(root, "d"))
			if len(entries) != 1 {
				t.Errorf("directory holds %d entries, want the one file under its new name", len(entries))
			}
			if len(rec.names) != 1 || rec.names[0] != "d/Britsh Gas bill.pdf -> d/"+tc.to {
				t.Errorf("recorded %v", rec.names)
			}
		})
	}
}

func TestRenameNeverReplaces(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("renames need renameat2 (Linux)")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "a.txt"), "a")
	c := candidate(t, root, "a.txt")
	// A file put at the new name after the checks, just before the rename, is never replaced.
	beforeRename = func(string) { write(t, filepath.Join(root, "b.txt"), "someone else's") }
	t.Cleanup(func() { beforeRename = nil })
	rec := &fakeRenames{}
	o, err := Rename(context.Background(), rec, c, "b.txt", time.Now())
	if err != nil || o.Done || !strings.Contains(o.Reason, "already exists") || len(rec.names) != 0 {
		t.Fatalf("outcome %+v, %v, recorded %v", o, err, rec.names)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "someone else's" {
		t.Errorf("b.txt = %q, want it untouched", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "a" {
		t.Errorf("a.txt = %q, want it where it was", got)
	}
}

func TestRenameSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("renames need renameat2 (Linux)")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "target.txt"), "t")
	if err := os.Symlink("target.txt", filepath.Join(root, "lnk")); err != nil {
		t.Fatal(err)
	}
	c := candidate(t, root, "lnk")
	o, err := Rename(context.Background(), &fakeRenames{}, c, "link", time.Now())
	if err != nil || !o.Done {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "link")); err != nil || target != "target.txt" {
		t.Errorf("link -> %q, %v; want the link itself renamed", target, err)
	}
	if _, err := os.Stat(filepath.Join(root, "target.txt")); err != nil {
		t.Errorf("target: %v, want it untouched", err)
	}
}

func TestCheckRename(t *testing.T) {
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "a.txt"), "a")
	write(t, filepath.Join(root, "b.txt"), "b")
	c := candidate(t, root, "a.txt")
	if why := CheckRename(c, "c.txt"); why != "" {
		t.Errorf("CheckRename to a free name = %q", why)
	}
	if why := CheckRename(c, "b.txt"); !strings.Contains(why, "already exists") {
		t.Errorf("CheckRename to a taken name = %q", why)
	}
	write(t, filepath.Join(root, "a.txt"), "edited")
	if why := CheckRename(c, "c.txt"); !strings.Contains(why, "changed since the last scan") {
		t.Errorf("CheckRename of an edited file = %q", why)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 2 {
		t.Errorf("the directory holds %d entries after CheckRename, want it untouched", len(entries))
	}
}

// ctxRenames fails, as the register does, when given a cancelled context.
type ctxRenames struct{ recorded bool }

func (f *ctxRenames) Renamed(ctx context.Context, _ store.Candidate, _ string, _ time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.recorded = true
	return nil
}

// TestRenameRecordedAfterCtrlC: a Ctrl-C that lands once the checks are done still leaves the rename recorded.
func TestRenameRecordedAfterCtrlC(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("renames need renameat2 (Linux)")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "a.txt"), "a")
	c := candidate(t, root, "a.txt")
	ctx, cancel := context.WithCancel(context.Background())
	beforeRename = func(string) { cancel() }
	t.Cleanup(func() { beforeRename = nil })
	rec := &ctxRenames{}
	o, err := Rename(ctx, rec, c, "b.txt", time.Now())
	if err != nil || !o.Done || !rec.recorded {
		t.Errorf("outcome %+v, %v, recorded %v; want renamed and recorded", o, err, rec.recorded)
	}
}

func TestMove(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("moves need renameat2 (Linux)")
	}
	ctx := context.Background()
	exists := func(t *testing.T, p string) bool {
		t.Helper()
		_, err := os.Lstat(p)
		return err == nil
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
		dir   string
		to    string
		done  bool
		want  string   // a word the reason holds
		made  int      // directories made
		there []string // paths (relative) that must exist after
		gone  []string // paths that must not
	}{
		{name: "into an existing directory", dir: "archive", to: "bill.pdf", done: true,
			setup: func(t *testing.T, root string) { mkdir(t, filepath.Join(root, "archive")) },
			there: []string{"archive/bill.pdf"}, gone: []string{"inbox/bill.pdf"}},
		{name: "into new directories, under a new name", dir: "received/2026/11", to: "Bill (paid).pdf", done: true,
			made: 3, there: []string{"received/2026/11/Bill (paid).pdf"}},
		{name: "to the top of the root", dir: "", to: "bill.pdf", done: true, there: []string{"bill.pdf"}},
		{name: "a name taken in the new directory", dir: "archive", to: "bill.pdf", want: "already exists",
			setup: func(t *testing.T, root string) { write(t, filepath.Join(root, "archive", "bill.pdf"), "other") },
			there: []string{"inbox/bill.pdf", "archive/bill.pdf"}},
		{name: "a symlink on the new path", dir: "link/x", to: "bill.pdf", want: "symlink",
			setup: func(t *testing.T, root string) {
				mkdir(t, filepath.Join(root, "real"))
				if err := os.Symlink("real", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			},
			there: []string{"inbox/bill.pdf"}, gone: []string{"real/x"}},
		{name: "a file where a new directory would go", dir: "plain/x", to: "bill.pdf", want: "no longer a directory",
			setup: func(t *testing.T, root string) { write(t, filepath.Join(root, "plain"), "a file") },
			there: []string{"inbox/bill.pdf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalTemp(t)
			write(t, filepath.Join(root, "inbox", "bill.pdf"), "bill")
			write(t, filepath.Join(root, "inbox", "keep.txt"), "stays")
			if tc.setup != nil {
				tc.setup(t, root)
			}
			c := candidate(t, root, filepath.Join("inbox", "bill.pdf"))
			rec := &fakeRenames{}
			m := NewMover(rec, time.Now)
			o, err := m.Move(ctx, store.Rename{Candidate: c, Dir: tc.dir, Name: tc.to})
			if err != nil {
				t.Fatal(err)
			}
			if o.Done != tc.done || !strings.Contains(o.Reason, tc.want) || len(o.Made) != tc.made {
				t.Fatalf("outcome %+v, want done %v, reason %q, %d made", o, tc.done, tc.want, tc.made)
			}
			for _, p := range tc.there {
				if !exists(t, filepath.Join(root, p)) {
					t.Errorf("%s missing", p)
				}
			}
			for _, p := range tc.gone {
				if exists(t, filepath.Join(root, p)) {
					t.Errorf("%s still there", p)
				}
			}
			if tc.done != (len(rec.names) == 1) {
				t.Errorf("recorded %v", rec.names)
			}
		})
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestMoveUnmakesDirs: a move that fails after making its directories removes them again.
func TestMoveUnmakesDirs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("moves need renameat2 (Linux)")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "inbox", "bill.pdf"), "bill")
	c := candidate(t, root, filepath.Join("inbox", "bill.pdf"))
	// The file changes once the checks are done: the rename finds it gone.
	beforeRename = func(string) { os.Remove(filepath.Join(root, "inbox", "bill.pdf")) }
	t.Cleanup(func() { beforeRename = nil })
	o, err := NewMover(&fakeRenames{}, time.Now).Move(context.Background(), store.Rename{Candidate: c,
		Dir: "new/deeper", Name: "bill.pdf"})
	if err != nil || o.Done {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); err == nil {
		t.Error("the directories made for the move are still there")
	}
}

// TestMovePrunes: the directories the moves emptied are removed, as filo delete does; one with a hidden file stays,
// and so does the root.
func TestMovePrunes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("moves need renameat2 (Linux)")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "inbox", "a", "x.pdf"), "x")
	write(t, filepath.Join(root, "other", "y.pdf"), "y")
	write(t, filepath.Join(root, "other", ".DS_Store"), "")
	write(t, filepath.Join(root, "top.pdf"), "t")
	m := NewMover(&fakeRenames{}, time.Now)
	for _, mv := range []struct{ from, dir string }{{"inbox/a/x.pdf", "sorted"}, {"other/y.pdf", "sorted"},
		{"top.pdf", "sorted"}} {
		c := candidate(t, root, filepath.FromSlash(mv.from))
		if o, err := m.Move(context.Background(), store.Rename{Candidate: c, Dir: mv.dir,
			Name: filepath.Base(mv.from)}); err != nil || !o.Done {
			t.Fatalf("move %s: %+v, %v", mv.from, o, err)
		}
	}
	removed, problems := m.PruneDirs()
	if len(problems) != 0 || len(removed) != 2 {
		t.Errorf("removed %v, problems %v; want inbox/a and inbox", removed, problems)
	}
	for p, want := range map[string]bool{"inbox": false, "other": true, ".": true, "sorted": true} {
		if _, err := os.Lstat(filepath.Join(root, p)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", p, err == nil, want)
		}
	}
}
