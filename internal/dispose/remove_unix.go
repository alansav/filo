//go:build unix

package dispose

import (
	"errors"
	"os"
	"syscall"
)

// removeFile removes a file or symlink, never a directory (unlink refuses one).
func removeFile(p string) error {
	if err := syscall.Unlink(p); err != nil {
		return &os.PathError{Op: "unlink", Path: p, Err: err}
	}
	return nil
}

// removeDir removes a directory only if it is empty, never a file (rmdir refuses both a non-empty directory and a
// file), so anything that appeared in it since it was listed keeps it.
func removeDir(p string) error {
	if err := syscall.Rmdir(p); err != nil {
		return &os.PathError{Op: "rmdir", Path: p, Err: err}
	}
	return nil
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
