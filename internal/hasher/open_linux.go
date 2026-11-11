//go:build linux

package hasher

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow opens path read-only without following a final symlink (ELOOP instead), without blocking if it has
// become a FIFO, and without updating its access time where the kernel allows it (O_NOATIME needs file ownership).
func openNoFollow(path string) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(path, flags|syscall.O_NOATIME, 0)
	if errors.Is(err, syscall.EPERM) {
		f, err = os.OpenFile(path, flags, 0)
	}
	return f, err
}
