//go:build unix

package walk

import (
	"errors"
	"syscall"
)

// isLoop reports whether err means a symlink chain loops (ELOOP), which makes the link broken.
func isLoop(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}
