//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockFile holds an exclusive advisory lock (flock) on a file. The kernel releases it if the process dies, so a
// crash never leaves a stale lock behind. The lock file itself is left in place; only the lock matters.
type lockFile struct {
	f *os.File
}

func acquireLock(path string) (*lockFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &lockFile{f: f}, nil
}

func (l *lockFile) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
