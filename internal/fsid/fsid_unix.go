//go:build unix

package fsid

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// lstatID returns the identity of path itself (lstat: a symlink is not followed).
func lstatID(path string) (ID, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return ID{}, err
	}
	id, ok := FromFileInfo(fi)
	if !ok {
		return ID{}, fmt.Errorf("%s: no device and inode information", path)
	}
	return id, nil
}

// FromFileInfo reads the identity from a FileInfo the caller already has (from Lstat, Stat, File.Stat or
// DirEntry.Info), without another system call. It reports false if the FileInfo carries no device and inode.
func FromFileInfo(fi fs.FileInfo) (ID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ID{}, false
	}
	return ID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}, true // Dev and Ino types vary by OS
}
