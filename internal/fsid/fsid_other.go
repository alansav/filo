//go:build !unix

package fsid

import (
	"errors"
	"io/fs"
	"runtime"
)

// lstatID is not implemented on this OS yet. Callers refuse rather than guess identities.
func lstatID(string) (ID, error) {
	return ID{}, errors.New("device and inode lookup is not supported on " + runtime.GOOS + " yet")
}

// FromFileInfo is not implemented on this OS yet: the identity is unknown.
func FromFileInfo(fs.FileInfo) (ID, bool) { return ID{}, false }
