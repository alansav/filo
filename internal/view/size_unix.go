//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package view

import (
	"os"
	"syscall"
	"unsafe"
)

// Size returns the width in columns of the terminal f is connected to, and whether f is a terminal at all: the call
// that reads the size fails on anything else (a file, a pipe, a character device that is not a terminal, such as
// /dev/null).
func Size(f *os.File) (cols int, terminal bool) {
	var ws struct{ rows, cols, x, y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0, false
	}
	return int(ws.cols), true
}
