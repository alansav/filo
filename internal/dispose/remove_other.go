//go:build !unix

package dispose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// removeFile removes a file or symlink, after checking it is not a directory. Untested: no Windows build is tested.
func removeFile(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s: is a directory", p)
	}
	return os.Remove(p)
}

// removeDir removes an empty directory. os.Remove refuses a non-empty one. Untested: no Windows build is tested.
func removeDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: %w", p, fs.ErrExist)
	}
	return os.Remove(p)
}

func isNotEmpty(err error) bool { return errors.Is(err, fs.ErrExist) }
