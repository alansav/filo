//go:build unix

package walk

import "syscall"

func mkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }
