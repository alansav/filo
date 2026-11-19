package dispose

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// atRemoveDir is Linux's AT_REMOVEDIR, which the syscall package does not export.
const atRemoveDir = 0x200

// removeFile removes the file or symlink name in dir (path is its absolute path, for messages), never a directory:
// unlinkat relative to dir refuses one, and cannot reach outside dir whatever is swapped above it.
func removeFile(dir *os.Root, name, path string) error {
	if err := unlinkat(dir, name, 0); err != nil {
		return &os.PathError{Op: "unlink", Path: path, Err: err}
	}
	return nil
}

// removeDir removes the directory name in dir only if it is empty, never a file: unlinkat with AT_REMOVEDIR refuses
// both a non-empty directory and a file, so anything that appeared in it since it was listed keeps it.
func removeDir(dir *os.Root, name, path string) error {
	if err := unlinkat(dir, name, atRemoveDir); err != nil {
		return &os.PathError{Op: "rmdir", Path: path, Err: err}
	}
	return nil
}

// unlinkat calls unlinkat(2) on name relative to dir. The syscall package has it only with no flags, so it is made
// here, as golang.org/x/sys/unix makes it.
func unlinkat(dir *os.Root, name string, flags int) error {
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, f.Fd(), uintptr(unsafe.Pointer(p)), uintptr(flags))
		runtime.KeepAlive(f)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}
