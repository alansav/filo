package dispose

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// fakeRenames records what Rename reported.
type fakeRenames struct{ names []string }

func (f *fakeRenames) Renamed(_ context.Context, c store.Candidate, name string, _ time.Time) error {
	f.names = append(f.names, c.Path+" -> "+name)
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
			if len(rec.names) != 1 || rec.names[0] != "d/Britsh Gas bill.pdf -> "+tc.to {
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
