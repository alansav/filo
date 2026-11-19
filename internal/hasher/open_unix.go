//go:build unix && !linux

package hasher

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a final symlink and without blocking if it has become a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// openInRoot opens name in root read-only, without blocking. OpenIn checks the file opened is the one lstat saw.
func openInRoot(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
