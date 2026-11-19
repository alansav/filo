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
// file last saw (issue #21). The file is moved into a new private directory next to it (0700, made by this run and
// checked to be that one), looked at there, where no other user can replace it, and only then unlinked; so a file put
// at its name in the instant after the last look is never the one deleted. If the file moved in is not the one
// checked, it is moved back to its name, never over anything (a *changedError says so); if that name is taken by then,
// it stays in the private directory and the error says where. The private directory is removed whenever it is empty.
func removeVerified(dir *os.Root, name, path string, last fs.FileInfo) error {
	pname, priv, err := makePrivate(dir, path)
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
	moved, err := priv.Lstat(name)
	if err == nil && stillChecked(moved, last) {
		return removeFile(priv, name, filepath.Join(ppath, name))
	}

	// Not the file checked: put it back where it was, never over a file that has appeared there since.
	back := errors.New("renameat2 is not known on " + runtime.GOARCH)
	if sysRenameat2 != 0 {
		back = renameat(priv, name, dir, name, renameNoReplace)
	}
	if back != nil {
		return &changedError{left: filepath.Join(ppath, name)}
	}
	return &changedError{}
}

// stillChecked reports whether moved, the file found in the private directory, is the file last saw at its name: the same
// file on disk, of the same kind, with the same size and modification time.
func stillChecked(moved, last fs.FileInfo) bool {
	return os.SameFile(moved, last) && moved.Mode().Type() == last.Mode().Type() && moved.Size() == last.Size() &&
		moved.ModTime().Equal(last.ModTime())
}

// makePrivate makes a new private directory in dir and opens it. It is checked, once open, to be the very directory
// lstat sees at its name (not a symlink), owned by this user, with no access for anyone else, and empty: a directory
// someone swapped in at its name is never used.
func makePrivate(dir *os.Root, path string) (string, *os.Root, error) {
	var name string
	for range 5 {
		name = walk.DeletingPrefix + rand.Text()
		err := dir.Mkdir(name, 0o700)
		if err == nil {
			break
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
	if !isPrivate(dir, name, priv) {
		priv.Close()
		return "", nil, fmt.Errorf("%s: the private directory made to delete the file changed before it was used", ppath)
	}
	return name, priv, nil
}

// isPrivate reports whether priv, opened from name in dir, is a real, empty directory, the one lstat sees at name,
// owned by this user and closed to everyone else.
func isPrivate(dir *os.Root, name string, priv *os.Root) bool {
	opened, err := priv.Stat(".")
	if err != nil {
		return false
	}
	seen, err := dir.Lstat(name)
	if err != nil || !seen.IsDir() || !os.SameFile(opened, seen) || opened.Mode().Perm()&0o077 != 0 {
		return false
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	f, err := priv.Open(".")
	if err != nil {
		return false
	}
	defer f.Close()
	entries, err := f.ReadDir(1)
	return len(entries) == 0 && errors.Is(err, io.EOF)
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
