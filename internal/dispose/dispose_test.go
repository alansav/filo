package dispose

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/store"
)

// fakeRec records what Delete reported.
type fakeRec struct{ deleted, changed, unreadable []int64 }

func (f *fakeRec) Deleted(_ context.Context, c store.Candidate, _ time.Time) error {
	f.deleted = append(f.deleted, c.FileID)
	return nil
}

func (f *fakeRec) Changed(_ context.Context, c store.Candidate, _ store.FileUpdate) error {
	f.changed = append(f.changed, c.FileID)
	return nil
}

func (f *fakeRec) Unreadable(_ context.Context, c store.Candidate, _ string) error {
	f.unreadable = append(f.unreadable, c.FileID)
	return nil
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// candidate describes the file at root/rel as the register would after a scan, with its retention set on its current
// content.
func candidate(t *testing.T, root, rel string) store.Candidate {
	t.Helper()
	p := filepath.Join(root, rel)
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Candidate{FileItem: store.FileItem{FileID: 1, RootPath: root, Path: filepath.ToSlash(rel),
		Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano(), Retention: "2026-01-01", KeepUntil: "2026-01-01"}}
	if fi.Mode()&os.ModeSymlink != 0 {
		c.Kind = "symlink" // the register keeps no inode for a symlink
		if c.LinkTarget, err = os.Readlink(p); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c.Kind = "file"
	c.Ino = ino(fi)
	res, err := hasher.File(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	c.Hash, c.ReviewedHash = string(res.Sum), string(res.Sum)
	return c
}

func TestDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions and inodes differ on windows")
	}
	asRoot := os.Geteuid() == 0
	tests := []struct {
		name     string
		symlink  bool
		change   func(t *testing.T, p string) // after the candidate is read from the "register"
		during   func(p string)               // between the check and the removal
		needUser bool                         // permissions do not hold back root
		deleted  bool
		failed   bool
		reason   string
		rec      string // which recorder call is expected: deleted, changed, unreadable, or none
	}{
		{name: "unchanged", deleted: true, rec: "deleted"},
		{name: "symlink unchanged", symlink: true, deleted: true, rec: "deleted"},
		{name: "bigger", change: func(t *testing.T, p string) { write(t, p, "longer content") },
			reason: "changed since its retention was set; run filo scan", rec: "none"},
		{name: "same size and mtime, other content", change: func(t *testing.T, p string) {
			fi, _ := os.Stat(p)
			write(t, p, "tsetnoc")
			if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
				t.Fatal(err)
			}
		}, reason: "changed since its retention was set; it needs a new retention", rec: "changed"},
		{name: "moved", change: func(t *testing.T, p string) {
			if err := os.Rename(p, p+".moved"); err != nil {
				t.Fatal(err)
			}
		}, reason: "no longer at this path", rec: "none"},
		{name: "replaced, same content and mtime", change: func(t *testing.T, p string) {
			fi, _ := os.Stat(p)
			write(t, p+".new", "content")
			if err := os.Chtimes(p+".new", fi.ModTime(), fi.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(p+".new", p); err != nil { // created while the old one existed: another inode
				t.Fatal(err)
			}
		}, reason: "replaced by another file", rec: "none"},
		{name: "now a directory", change: func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}, reason: "no longer a regular file", rec: "none"},
		{name: "symlink retargeted", symlink: true, change: func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("elsewhere", p); err != nil {
				t.Fatal(err)
			}
		}, reason: "the symlink now points to elsewhere", rec: "none"},
		{name: "unreadable", needUser: true, change: func(t *testing.T, p string) {
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
		}, failed: true, reason: "cannot be read: permission denied", rec: "unreadable"},
		{name: "folder not writable", needUser: true, change: func(t *testing.T, p string) {
			if err := os.Chmod(filepath.Dir(p), 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Dir(p), 0o755) })
		}, failed: true, reason: "could not be deleted: permission denied", rec: "none"},
		{name: "changed during the check", during: func(p string) {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("more")
			f.Close()
		}, reason: "changed while it was being checked", rec: "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needUser && asRoot {
				t.Skip("running as root: permissions do not hold it back")
			}
			root := canonicalTemp(t)
			p := filepath.Join(root, "dir", "f")
			write(t, filepath.Join(root, "dir", "target"), "x")
			if tc.symlink {
				if err := os.Symlink("target", p); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, p, "content")
			}
			c := candidate(t, root, "dir/f")
			if tc.change != nil {
				tc.change(t, p)
			}
			if tc.during != nil {
				beforeRemove = tc.during
				t.Cleanup(func() { beforeRemove = nil })
			}
			rec := &fakeRec{}
			o, err := New(rec, time.Now).Delete(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if o.Deleted != tc.deleted || o.Failed != tc.failed || !strings.Contains(o.Reason, tc.reason) {
				t.Errorf("outcome = %+v, want deleted %v, failed %v, reason containing %q", o, tc.deleted, tc.failed,
					tc.reason)
			}
			_, statErr := os.Lstat(p)
			if exists := statErr == nil; exists == tc.deleted && !strings.Contains(tc.reason, "no longer at") {
				t.Errorf("file exists = %v after deleted = %v", exists, o.Deleted)
			}
			got := "none"
			switch {
			case len(rec.deleted) > 0:
				got = "deleted"
			case len(rec.changed) > 0:
				got = "changed"
			case len(rec.unreadable) > 0:
				got = "unreadable"
			}
			if got != tc.rec {
				t.Errorf("recorded %s, want %s", got, tc.rec)
			}
		})
	}
}

func TestDeleteCancelled(t *testing.T) {
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "f"), "content")
	c := candidate(t, root, "f")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(&fakeRec{}, time.Now).Delete(ctx, c); err == nil {
		t.Fatal("Delete with a cancelled context succeeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "f")); err != nil {
		t.Errorf("file gone after a cancelled run: %v", err)
	}
}

func TestPruneDirs(t *testing.T) {
	root := canonicalTemp(t)
	files := []string{
		"a/b/f",          // a/b and then a are emptied
		"c/f", "c/.keep", // a hidden file keeps c
		"d/f", "d/g", // only f is deleted, so d keeps g
		"top", // a file in the root: the root itself stays
	}
	for _, f := range files {
		write(t, filepath.Join(root, f), "x"+f)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil { // already empty: never touched
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "a", "sibling"), 0o755); err != nil { // keeps a, though empty itself
		t.Fatal(err)
	}
	d := New(&fakeRec{}, time.Now)
	for _, f := range []string{"a/b/f", "c/f", "d/f", "top"} {
		o, err := d.Delete(context.Background(), candidate(t, root, f))
		if err != nil || !o.Deleted {
			t.Fatalf("delete %s: %+v, %v", f, o, err)
		}
	}
	removed, problems := d.PruneDirs()
	if len(problems) > 0 {
		t.Errorf("problems: %v", problems)
	}
	want := []string{filepath.Join(root, "a", "b")}
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	for _, dir := range []string{"a", "a/sibling", "c", "d", "empty", "."} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Errorf("%s was removed: %v", dir, err)
		}
	}

	// Once the sibling goes too, a deletion in a/ empties it up to the root.
	write(t, filepath.Join(root, "a", "g"), "g")
	if err := os.Remove(filepath.Join(root, "a", "sibling")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "x", "y", "z", "f"), "deep")
	d = New(&fakeRec{}, time.Now)
	for _, f := range []string{"a/g", "x/y/z/f"} {
		if o, err := d.Delete(context.Background(), candidate(t, root, f)); err != nil || !o.Deleted {
			t.Fatalf("delete %s: %+v, %v", f, o, err)
		}
	}
	removed, _ = d.PruneDirs()
	want = []string{filepath.Join(root, "x", "y", "z"), filepath.Join(root, "x", "y"), filepath.Join(root, "a"),
		filepath.Join(root, "x")}
	slices.Sort(removed)
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root removed: %v", err)
	}
}

func TestInside(t *testing.T) {
	tests := []struct {
		dir, root string
		want      bool
	}{
		{"/r/a", "/r", true},
		{"/r/a/b", "/r", true},
		{"/r", "/r", false},
		{"/", "/r", false},
		{"/rx", "/r", false},
		{"/r/..a", "/r", true},
	}
	for _, tc := range tests {
		if got := inside(filepath.FromSlash(tc.dir), filepath.FromSlash(tc.root)); got != tc.want {
			t.Errorf("inside(%s, %s) = %v, want %v", tc.dir, tc.root, got, tc.want)
		}
	}
}

// TestDeleteOnlyInsideRoot checks that a register path pointing outside its root (an altered register) or through a
// folder that is now a symlink is never deleted.
func TestDeleteOnlyInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	base := canonicalTemp(t)
	root := filepath.Join(base, "root")
	write(t, filepath.Join(base, "outside.txt"), "secret")
	write(t, filepath.Join(base, "elsewhere", "f"), "content")
	write(t, filepath.Join(root, "keep"), "x")
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	unclean := candidate(t, root, "keep")
	unclean.RootPath = root + "/../root"
	tests := []struct {
		name   string
		c      store.Candidate
		failed bool
		reason string
		kept   string
	}{
		{"escapes the root", candidate(t, root, "../outside.txt"), true, "not inside its root",
			filepath.Join(base, "outside.txt")},
		{"root path not clean", unclean, true, "not inside its root", filepath.Join(root, "keep")},
		{"through a symlinked folder", candidate(t, root, "link/f"), false, "a folder on its path is now a symlink",
			filepath.Join(base, "elsewhere", "f")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeRec{}
			o, err := New(rec, time.Now).Delete(context.Background(), tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if o.Deleted || o.Failed != tc.failed || !strings.Contains(o.Reason, tc.reason) {
				t.Errorf("outcome = %+v, want failed %v and %q", o, tc.failed, tc.reason)
			}
			if _, err := os.Stat(tc.kept); err != nil {
				t.Errorf("%s is gone: %v", tc.kept, err)
			}
			if len(rec.deleted)+len(rec.changed)+len(rec.unreadable) > 0 {
				t.Errorf("recorded %+v", rec)
			}
		})
	}
}

// TestDeleteSymlinkAboveRoot: a symlink above the root (a home directory moved to another disk) does not block
// deletion; only folders from the root down are checked.
func TestDeleteSymlinkAboveRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	base := canonicalTemp(t)
	write(t, filepath.Join(base, "disk", "home", "docs", "f"), "content")
	if err := os.Symlink(filepath.Join(base, "disk", "home"), filepath.Join(base, "home")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "home", "docs") // reached through the symlink
	o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), candidate(t, root, "f"))
	if err != nil || !o.Deleted {
		t.Errorf("Delete = %+v, %v; want deleted", o, err)
	}
}

// TestDeleteFolderSwappedDuringCheck: a folder replaced by a symlink while the file is being checked (hashing a
// large file takes a while) stops the deletion at the last look.
func TestDeleteFolderSwappedDuringCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := canonicalTemp(t)
	write(t, filepath.Join(root, "dir", "f"), "content")
	c := candidate(t, root, "dir/f")
	beforeRemove = func(string) {
		os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "moved"))
		os.Symlink(filepath.Join(root, "moved"), filepath.Join(root, "dir"))
	}
	t.Cleanup(func() { beforeRemove = nil })
	o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), c)
	if err != nil || o.Deleted || !strings.Contains(o.Reason, "changed while it was being checked") {
		t.Errorf("Delete = %+v, %v; want skipped as changed", o, err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved", "f")); err != nil {
		t.Errorf("the file was deleted through the swapped folder: %v", err)
	}
}
