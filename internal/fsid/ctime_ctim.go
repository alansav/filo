//go:build linux || openbsd

package fsid

import (
	"io/fs"
	"syscall"
)

// ChangeTimeNS returns the file's status change time (ctime) in Unix nanoseconds from a FileInfo the caller already
// has, or 0 if it carries none. The ctime changes on any content or metadata change (permissions, owner, links) and
// cannot be set by the user.
func ChangeTimeNS(fi fs.FileInfo) int64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return st.Ctim.Nano()
}
