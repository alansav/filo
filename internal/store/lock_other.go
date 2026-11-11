//go:build !unix

package store

import (
	"errors"
	"runtime"
)

// File locking is only implemented for unix so far. Refusing is the fail-safe choice: running without a lock could
// let two writers interleave.
type lockFile struct{}

func acquireLock(string) (*lockFile, error) {
	return nil, errors.New("file locking is not supported on " + runtime.GOOS + " yet")
}

func (*lockFile) release() error { return nil }
