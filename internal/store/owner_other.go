//go:build !unix

package store

import "io/fs"

// ownedByUser cannot check ownership on this platform. Untested: no non-Unix build is tested.
func ownedByUser(fs.FileInfo) bool { return true }

// writableByOthers cannot check permissions on this platform. Untested: no non-Unix build is tested.
func writableByOthers(fs.FileInfo) bool { return false }
