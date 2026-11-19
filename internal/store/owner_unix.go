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

// writableByOthers reports whether the group or others may write to the file (a directory: create, rename and
// delete entries in it).
func writableByOthers(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o022 != 0 }
