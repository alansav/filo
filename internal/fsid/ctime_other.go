//go:build !linux && !openbsd && !darwin && !freebsd && !netbsd

package fsid

import "io/fs"

// ChangeTimeNS is not implemented on this OS (Windows has no status change time in its FileInfo): 0 means not known,
// and callers then do not compare change times.
func ChangeTimeNS(fs.FileInfo) int64 { return 0 }
