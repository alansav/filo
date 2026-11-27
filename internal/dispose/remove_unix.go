//go:build unix && !linux

package dispose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// removeFile removes the file or symlink name in dir (path is its absolute path, for messages), never a directory.
// This OS's syscall package has no unlinkat, so dir's Remove is used, after an lstat through dir: Remove would also
// remove an empty directory swapped in between the two, never anything outside dir. Untested: only Linux builds are
// tested.
func removeFile(dir *os.Root, name, path string) error {
	fi, err := dir.Lstat(name)
	if err != nil {
		return &os.PathError{Op: "lstat", Path: path, Err: unwrap(err)}
	}
	if fi.IsDir() {
		return &os.PathError{Op: "unlink", Path: path, Err: syscall.EISDIR}
	}
	if err := dir.Remove(name); err != nil {
		return &os.PathError{Op: "unlink", Path: path, Err: unwrap(err)}
	}
	return nil
}

// removeDir removes the empty directory name in dir, never a file, after an lstat through dir (see removeFile: a
// file swapped in between the two would be removed; it is inside dir). Untested: only Linux builds are tested.
func removeDir(dir *os.Root, name, path string) error {
	fi, err := dir.Lstat(name)
	if err != nil {
		return &os.PathError{Op: "lstat", Path: path, Err: unwrap(err)}
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: %w", path, fs.ErrExist)
	}
	if err := dir.Remove(name); err != nil {
		return &os.PathError{Op: "rmdir", Path: path, Err: unwrap(err)}
	}
	return nil
}

func unwrap(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

// removeVerified removes the file name in dir, as removeFile does. Only Linux moves it into a private directory first
// to make sure it is still the file checked (last), since only there does Filo have the calls to move it back without
// replacing anything (renameat2). Untested: only Linux builds are tested.
func removeVerified(dir *os.Root, name, path string, _ fs.FileInfo) error {
	return removeFile(dir, name, path)
}

// renameInPlace refuses: these systems' syscall package has no rename that never replaces its target. Untested:
// only Linux builds are tested.
func renameInPlace(dir *os.Root, old, name string) error { return errNoReplace }
