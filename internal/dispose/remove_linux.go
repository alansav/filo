package dispose

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/alansav/filo/internal/walk"
)

// atRemoveDir is Linux's AT_REMOVEDIR, which the syscall package does not export.
const atRemoveDir = 0x200

// removeFile removes the file or symlink name in dir (path is its absolute path, for messages), never a directory:
// unlinkat relative to dir refuses one, and cannot reach outside dir whatever is swapped above it.
func removeFile(dir *os.Root, name, path string) error {
	if err := unlinkat(dir, name, 0); err != nil {
		return &os.PathError{Op: "unlink", Path: path, Err: err}
	}
	return nil
}

// removeDir removes the directory name in dir only if it is empty, never a file: unlinkat with AT_REMOVEDIR refuses
// both a non-empty directory and a file, so anything that appeared in it since it was listed keeps it.
func removeDir(dir *os.Root, name, path string) error {
	if err := unlinkat(dir, name, atRemoveDir); err != nil {
		return &os.PathError{Op: "rmdir", Path: path, Err: err}
	}
	return nil
}

// unlinkat calls unlinkat(2) on name relative to dir. The syscall package has it only with no flags, so it is made
// here, as golang.org/x/sys/unix makes it.
func unlinkat(dir *os.Root, name string, flags int) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, f.Fd(), uintptr(unsafe.Pointer(p)), uintptr(flags))
		runtime.KeepAlive(f)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

// renameNoReplace is Linux's RENAME_NOREPLACE: renameat2 fails with EEXIST rather than replace the target.
const renameNoReplace = 0x1

// afterPrivateMade, if set by a test, runs after the private directory is made, before it is opened.
var afterPrivateMade func(path string)

// sysRenameat2 is renameat2(2)'s number on each architecture (from golang.org/x/sys/unix's zsysnum_linux_*.go), since
// the syscall package has neither. On one not listed, a file is never moved back over a name (it stays where it is).
var sysRenameat2 = map[string]uintptr{
	"386": 353, "amd64": 316, "arm": 382, "arm64": 276, "loong64": 276, "mips": 4351, "mipsle": 4351,
	"mips64": 5311, "mips64le": 5311, "ppc64": 357, "ppc64le": 357, "riscv64": 276, "s390x": 347,
}[runtime.GOARCH]

// removeVerified removes the file name in dir (path is its absolute path, for messages) only if it is still the very
// file last saw (issue #21). The file is moved into a new private directory next to it (made by this run and checked
// to be that one), looked at there, where no other user can replace it, and only then unlinked; so a file put at its
// name in the instant after the last look is never the one deleted. If the file moved in is not the one checked, or
// cannot be unlinked there, it is moved back to its name, never over anything (a *changedError, or the unlink's error,
// says so); if that fails (the name is taken by then), it stays in the private directory and the error says where.
// The private directory is removed whenever it is empty.
//
// Where the private directory cannot be made for want of space or a writable filesystem, or the filesystem cannot
// make it private (no Unix modes: FAT, exFAT, NTFS, network shares), the file is unlinked at its name after the last
// look, as before issue #21.
func removeVerified(dir *os.Root, name, path string, last fs.FileInfo) error {
	pname, priv, err := makePrivate(dir, path)
	if errors.Is(err, errNoPrivate) {
		return removeFile(dir, name, path)
	}
	if err != nil {
		return err
	}
	ppath := filepath.Join(filepath.Dir(path), pname)
	defer func() {
		removePrivate(dir, pname, priv, ppath)
		priv.Close()
	}()

	if err := renameat(dir, name, priv, name, 0); err != nil {
		return &os.PathError{Op: "rename", Path: path, Err: err}
	}
	if inPrivate != nil {
		inPrivate(path)
	}
	left := filepath.Join(ppath, name)
	moved, err := priv.Lstat(name)
	if err == nil && stillChecked(moved, last) {
		err := removeInPrivate(priv, name, left)
		if err == nil {
			return nil
		}
		// Checked but not removable there (an append-only file): it goes back, as it was.
		if putBack(priv, dir, name) != nil {
			return &changedError{left: left, cause: err}
		}
		return &os.PathError{Op: "unlink", Path: path, Err: unwrapPath(err)}
	}

	// Not the file checked: put it back where it was, never over a file that has appeared there since.
	if putBack(priv, dir, name) != nil {
		return &changedError{left: left}
	}
	return &changedError{}
}

// removeInPrivate is removeFile, swappable in tests to fail as an append-only file does.
var removeInPrivate = removeFile

// putBack moves name from the private directory back into dir, never over anything there.
func putBack(priv, dir *os.Root, name string) error {
	if sysRenameat2 == 0 {
		return errors.New("renameat2 is not known on " + runtime.GOARCH)
	}
	return renameat(priv, name, dir, name, renameNoReplace)
}

// stillChecked reports whether moved, the file found in the private directory, is the file last saw at its name: the
// same file on disk, of the same kind, with the same size and modification time.
func stillChecked(moved, last fs.FileInfo) bool {
	return os.SameFile(moved, last) && moved.Mode().Type() == last.Mode().Type() && moved.Size() == last.Size() &&
		moved.ModTime().Equal(last.ModTime())
}

// errNoPrivate means no private directory can be had here, so the file is unlinked at its name.
var errNoPrivate = errors.New("no private directory on this filesystem")

// mkdirPrivate makes the private directory; swappable in tests to fail as a full disk does.
var mkdirPrivate = func(dir *os.Root, name string) error { return dir.Mkdir(name, 0o700) }

// makePrivate makes a new private directory in dir and opens it. Once open it is checked to be the very directory
// lstat sees at its name (not a symlink) and empty: one someone swapped in at its name is never used. It must also be
// owned by this user, and nobody else may write to it (only write access lets anyone replace an entry in it); a
// filesystem that gives the directory just made other owners or modes cannot make one private (errNoPrivate, with
// the directory removed), and nor can a full disk, a used-up quota or a read-only filesystem.
func makePrivate(dir *os.Root, path string) (string, *os.Root, error) {
	var name string
	for range 5 {
		name = walk.DeletingPrefix + rand.Text()
		err := mkdirPrivate(dir, name)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.EROFS) {
			return "", nil, errNoPrivate
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, &os.PathError{Op: "mkdir", Path: filepath.Join(filepath.Dir(path), name), Err: unwrapPath(err)}
		}
		name = ""
	}
	ppath := filepath.Join(filepath.Dir(path), name)
	if name == "" {
		return "", nil, &os.PathError{Op: "mkdir", Path: ppath, Err: fs.ErrExist}
	}
	if afterPrivateMade != nil {
		afterPrivateMade(ppath)
	}
	priv, err := dir.OpenRoot(name)
	if err != nil {
		return "", nil, &os.PathError{Op: "open", Path: ppath, Err: unwrapPath(err)}
	}
	switch privacy(dir, name, priv) {
	case private:
		return name, priv, nil
	case notPrivate:
		removePrivate(dir, name, priv, ppath)
		priv.Close()
		return "", nil, errNoPrivate
	}
	priv.Close()
	return "", nil, fmt.Errorf("%s: the private directory made to delete the file changed before it was used", ppath)
}

type privateness int

const (
	swapped    privateness = iota // not the directory made, or not empty: never used, left alone
	notPrivate                    // the directory made, but the filesystem lets others write to it, or gave it another owner
	private
)

// privacy says whether priv, opened from name in dir, is a real, empty directory, the one lstat sees at name, owned
// by this user and not writable by anyone else.
func privacy(dir *os.Root, name string, priv *os.Root) privateness {
	opened, err := priv.Stat(".")
	if err != nil {
		return swapped
	}
	seen, err := dir.Lstat(name)
	if err != nil || !seen.IsDir() || !os.SameFile(opened, seen) {
		return swapped
	}
	f, err := priv.Open(".")
	if err != nil {
		return swapped
	}
	entries, err := f.ReadDir(1)
	f.Close()
	if len(entries) != 0 || !errors.Is(err, io.EOF) {
		return swapped
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() || opened.Mode().Perm()&0o022 != 0 {
		return notPrivate
	}
	return private
}

// removePrivate removes the private directory if it is empty and still the one at its name; one that is not empty
// (the file could not be put back) stays, and a scan reports it.
func removePrivate(dir *os.Root, name string, priv *os.Root, path string) {
	opened, err := priv.Stat(".")
	if err != nil {
		return
	}
	if seen, err := dir.Lstat(name); err != nil || !os.SameFile(opened, seen) {
		return
	}
	_ = removeDir(dir, name, path) // refuses a directory that is not empty
}

// renameat moves from's entry oldname to to's entry newname, both relative to their open directories, never by a
// path; with flags (renameNoReplace) it is renameat2.
func renameat(from *os.Root, oldname string, to *os.Root, newname string, flags int) error {
	ff, err := from.Open(".")
	if err != nil {
		return err
	}
	defer ff.Close()
	tf, err := to.Open(".")
	if err != nil {
		return err
	}
	defer tf.Close()
	if flags == 0 {
		return ignoringEINTR(func() error {
			return syscall.Renameat(int(ff.Fd()), oldname, int(tf.Fd()), newname)
		})
	}
	op, err := syscall.BytePtrFromString(oldname)
	if err != nil {
		return err
	}
	np, err := syscall.BytePtrFromString(newname)
	if err != nil {
		return err
	}
	return ignoringEINTR(func() error {
		_, _, errno := syscall.Syscall6(sysRenameat2, ff.Fd(), uintptr(unsafe.Pointer(op)), tf.Fd(),
			uintptr(unsafe.Pointer(np)), uintptr(flags), 0)
		runtime.KeepAlive(ff)
		runtime.KeepAlive(tf)
		if errno != 0 {
			return errno
		}
		return nil
	})
}

func ignoringEINTR(f func() error) error {
	for {
		if err := f(); !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// unwrapPath returns the error inside a *fs.PathError, so a message names the absolute path, not the relative one.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// renameInPlace renames old to name in dir, never replacing a file at name (renameat2 with RENAME_NOREPLACE).
func renameInPlace(dir *os.Root, old, name string) error {
	if sysRenameat2 == 0 {
		return errNoReplace
	}
	return renameat(dir, old, dir, name, renameNoReplace)
}
