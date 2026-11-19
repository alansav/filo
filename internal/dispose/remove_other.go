//go:build !unix

package dispose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// removeFile removes the file or symlink name in dir, after checking it is not a directory. Untested: no Windows
// build is tested.
func removeFile(dir *os.Root, name, path string) error {
	fi, err := dir.Lstat(name)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s: is a directory", path)
	}
	return dir.Remove(name)
}

// removeDir removes the empty directory name in dir. Remove refuses a non-empty one. Untested: no Windows build is
// tested.
func removeDir(dir *os.Root, name, path string) error {
	fi, err := dir.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: %w", path, fs.ErrExist)
	}
	return dir.Remove(name)
}

func isNotEmpty(err error) bool { return errors.Is(err, fs.ErrExist) }
