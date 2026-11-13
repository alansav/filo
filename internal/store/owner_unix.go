//go:build unix

package store

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByUser reports whether the file belongs to the user running filo.
func ownedByUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
