// Package fsid identifies files and directories by device and inode, so Filo can tell when two paths are the same
// directory (a bind mount) or when a directory is on a different filesystem.
package fsid

// ID is a file's identity on this machine at this moment. Device numbers can change across reboots or remounts, so
// IDs are compared live and never stored.
type ID struct {
	Dev uint64
	Ino uint64
}

var impl = lstatID

// Stat returns the identity of path itself (lstat: a symlink is not followed).
func Stat(path string) (ID, error) { return impl(path) }

// SetForTest makes Stat call fake, which receives the real lookup, until restore is called. Tests use it to simulate
// mounts and bind mounts, which cannot be created without privileges. Not safe for parallel tests.
func SetForTest(fake func(path string, real func(string) (ID, error)) (ID, error)) (restore func()) {
	impl = func(p string) (ID, error) { return fake(p, lstatID) }
	return func() { impl = lstatID }
}
