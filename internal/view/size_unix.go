//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package view

import (
	"os"
	"syscall"
	"unsafe"
)

// CanCheck is true where Size can tell a terminal apart from anything else.
const CanCheck = true

// Size returns the width in columns of the terminal f is connected to, and whether f is a terminal at all: the call
// that reads the size fails on anything else (a file, a pipe, a character device that is not a terminal, such as
// /dev/null). It goes through SyscallConn, so f stays as it was (f.Fd would switch it to blocking mode).
func Size(f *os.File) (cols int, terminal bool) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, false
	}
	var ws struct{ rows, cols, x, y uint16 }
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	}); err != nil || errno != 0 {
		return 0, false
	}
	return int(ws.cols), true
}
