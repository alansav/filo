//go:build !unix

package hasher

import (
	"fmt"
	"os"
)

// openNoFollow refuses a symlink, then opens. Best effort only: the path could be swapped between the check and the
// open. The fstat in file still rejects anything that is not a regular file.
func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	return os.Open(path)
}

// openInRoot opens name in root read-only. OpenIn checks the file opened is the one lstat saw. Untested: no
// non-Unix build is tested.
func openInRoot(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
