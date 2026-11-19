package dispose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The private directory a file passes through to be deleted is Linux only (remove_linux.go).

// swapIn puts a new file with content at p, by rename, as an editor or a sync tool saves: another inode.
func swapIn(t *testing.T, p, content string) {
	t.Helper()
	write(t, p+".tmp", content)
	if err := os.Rename(p+".tmp", p); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteFileSwappedBeforeUnlink: a file put at the name in the instant after the last look, before the removal,
// is never the one deleted (issue #21). The entry is moved into a private directory and looked at there; what was
// moved is not the file checked, so it is put back at its name, and nothing is deleted. Moving a file into place, as an
// editor or Syncthing saves, must never lose that new version.
func TestDeleteFileSwappedBeforeUnlink(t *testing.T) {
	tests := []struct {
		name      string
		before    func(t *testing.T, p string) // right before the removal, after every check
		inPrivate func(t *testing.T, p string) // once the file is in the private directory
		failed    bool
		reason    string
		atPath    string // content at the path afterwards
		left      string // content left in the private directory; "" = it is gone
	}{
		{name: "swapped before the move: put back",
			before: func(t *testing.T, p string) { swapIn(t, p, "new version") },
			reason: "changed as it was being deleted, so it was put back", atPath: "new version"},
		{name: "swapped, then the name taken again: left in the private directory",
			before:    func(t *testing.T, p string) { swapIn(t, p, "new version") },
			inPrivate: func(t *testing.T, p string) { write(t, p, "newer still") },
			failed:    true, reason: "could not be put back: it is now ", atPath: "newer still", left: "new version"},
		{name: "written to in the private directory: put back",
			inPrivate: func(t *testing.T, p string) {
				priv := privateDirs(t, filepath.Dir(p))
				if len(priv) != 1 {
					t.Fatalf("private directories = %v, want one", priv)
				}
				f, err := os.OpenFile(filepath.Join(filepath.Dir(p), priv[0], "f"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString(" and more")
				f.Close()
			},
			reason: "changed as it was being deleted, so it was put back", atPath: "content and more"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalTemp(t)
			p := filepath.Join(root, "dir", "f")
			write(t, p, "content")
			c := candidate(t, root, "dir/f")
			if tc.before != nil {
				beforeUnlink = func(string) { tc.before(t, p) }
				t.Cleanup(func() { beforeUnlink = nil })
			}
			if tc.inPrivate != nil {
				inPrivate = func(string) { tc.inPrivate(t, p) }
				t.Cleanup(func() { inPrivate = nil })
			}
			rec := &fakeRec{}
			o, err := New(rec, time.Now).Delete(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if o.Deleted || o.Failed != tc.failed || !strings.Contains(o.Reason, tc.reason) {
				t.Errorf("outcome = %+v, want not deleted, failed %v, reason containing %q", o, tc.failed, tc.reason)
			}
			if len(rec.deleted) > 0 {
				t.Error("recorded as deleted")
			}
			if got, err := os.ReadFile(p); err != nil || string(got) != tc.atPath {
				t.Errorf("at its path: %q, %v; want %q", got, err, tc.atPath)
			}
			priv := privateDirs(t, filepath.Dir(p))
			if tc.left == "" {
				if len(priv) > 0 {
					t.Errorf("private directories left behind: %v", priv)
				}
				return
			}
			if len(priv) != 1 {
				t.Fatalf("private directories = %v, want the one left", priv)
			}
			leftPath := filepath.Join(filepath.Dir(p), priv[0], "f")
			if got, err := os.ReadFile(leftPath); err != nil || string(got) != tc.left {
				t.Errorf("in the private directory: %q, %v; want %q", got, err, tc.left)
			}
			if !strings.Contains(o.Reason, leftPath) {
				t.Errorf("reason %q does not say where it was left (%s)", o.Reason, leftPath)
			}
			if fi, err := os.Stat(filepath.Dir(leftPath)); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("private directory mode = %v, %v; want 0700", fi, err)
			}
		})
	}
}

// TestPrivateDirSwapped: a directory put in place of the private directory between its creation and its use, with
// something in it, is never used: the file stays where it is, not deleted, and that directory is left alone.
func TestPrivateDirSwapped(t *testing.T) {
	root := canonicalTemp(t)
	p := filepath.Join(root, "dir", "f")
	write(t, p, "content")
	c := candidate(t, root, "dir/f")
	var swappedIn string
	afterPrivateMade = func(path string) {
		swappedIn = path
		if err := os.Rename(path, path+".aside"); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(path, "f"), "someone else's")
	}
	t.Cleanup(func() { afterPrivateMade = nil })
	o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Deleted || !o.Failed || !strings.Contains(o.Reason, "changed before it was used") {
		t.Errorf("outcome = %+v, want not deleted, failed, the private directory refused", o)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != "content" {
		t.Errorf("at its path: %q, %v; want the file untouched", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(swappedIn, "f")); err != nil || string(got) != "someone else's" {
		t.Errorf("in the swapped-in directory: %q, %v; want it untouched", got, err)
	}
}

// TestNoPrivateDir: where no private directory can be had (a full disk, a used-up quota, a read-only filesystem, or a
// filesystem whose modes let others write to the directory just made: FAT, exFAT, NTFS, network shares), the file
// checked is still deleted, at its name, as before issue #21, and no private directory is left behind.
func TestNoPrivateDir(t *testing.T) {
	tests := []struct {
		name  string
		mkdir func(dir *os.Root, name string) error
		made  func(t *testing.T, path string)
	}{
		{name: "disk full", mkdir: func(*os.Root, string) error { return &fs.PathError{Op: "mkdirat", Err: syscall.ENOSPC} }},
		{name: "quota used up", mkdir: func(*os.Root, string) error { return &fs.PathError{Op: "mkdirat", Err: syscall.EDQUOT} }},
		{name: "made writable by all", made: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o777); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "made writable by the group", made: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o770); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mkdir != nil {
				old := mkdirPrivate
				mkdirPrivate = tc.mkdir
				t.Cleanup(func() { mkdirPrivate = old })
			}
			if tc.made != nil {
				afterPrivateMade = func(path string) { tc.made(t, path) }
				t.Cleanup(func() { afterPrivateMade = nil })
			}
			root := canonicalTemp(t)
			p := filepath.Join(root, "dir", "f")
			write(t, p, "content")
			c := candidate(t, root, "dir/f")
			o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if !o.Deleted {
				t.Errorf("outcome = %+v, want deleted", o)
			}
			if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the file is still there (%v)", err)
			}
			if left := privateDirs(t, filepath.Dir(p)); len(left) > 0 {
				t.Errorf("private directories left behind: %v", left)
			}
		})
	}
	t.Run("readable by others: still private", func(t *testing.T) {
		root := canonicalTemp(t)
		p := filepath.Join(root, "dir", "f")
		write(t, p, "content")
		c := candidate(t, root, "dir/f")
		afterPrivateMade = func(path string) { os.Chmod(path, 0o755) }
		t.Cleanup(func() { afterPrivateMade = nil })
		used := false
		inPrivate = func(string) { used = true }
		t.Cleanup(func() { inPrivate = nil })
		if o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), c); err != nil || !o.Deleted {
			t.Fatalf("outcome = %+v, %v; want deleted", o, err)
		}
		if !used {
			t.Error("a directory others can only read was not used as the private directory")
		}
	})
}

// TestUnlinkInPrivateFails: a file checked in the private directory that then cannot be unlinked (an append-only
// file) is put back at its name, not left hidden; if it cannot be put back either, the run says where it is.
func TestUnlinkInPrivateFails(t *testing.T) {
	for _, takeName := range []bool{false, true} {
		t.Run(fmt.Sprintf("name taken %v", takeName), func(t *testing.T) {
			root := canonicalTemp(t)
			p := filepath.Join(root, "dir", "f")
			write(t, p, "content")
			c := candidate(t, root, "dir/f")
			old := removeInPrivate
			removeInPrivate = func(_ *os.Root, _, path string) error {
				if takeName {
					write(t, p, "newer")
				}
				return &os.PathError{Op: "unlink", Path: path, Err: syscall.EPERM}
			}
			t.Cleanup(func() { removeInPrivate = old })
			o, err := New(&fakeRec{}, time.Now).Delete(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if o.Deleted || !o.Failed || !strings.Contains(o.Reason, "could not be deleted: operation not permitted") {
				t.Errorf("outcome = %+v, want failed, not deleted", o)
			}
			priv := privateDirs(t, filepath.Dir(p))
			if !takeName {
				if got, err := os.ReadFile(p); err != nil || string(got) != "content" {
					t.Errorf("at its path: %q, %v; want the file put back", got, err)
				}
				if len(priv) > 0 {
					t.Errorf("private directories left behind: %v", priv)
				}
				return
			}
			if len(priv) != 1 || !strings.Contains(o.Reason, filepath.Join(filepath.Dir(p), priv[0], "f")) {
				t.Errorf("reason %q does not say where it was left (%v)", o.Reason, priv)
			}
		})
	}
}
