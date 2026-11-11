//go:build linux

package hasher

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func atime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return time.Unix(st.Atim.Sec, st.Atim.Nsec)
}

// TestNoAtimeUpdate checks hashing leaves the access time alone. Times are set in the past so that even relatime
// would update atime on a normal read; a control file read normally shows whether this filesystem updates atime at
// all (it doesn't when mounted noatime, and then the test can't tell anything).
func TestNoAtimeUpdate(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	older := old.Add(-time.Hour)
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, older, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	control, hashed := mk("control"), mk("hashed")

	if _, err := os.ReadFile(control); err != nil {
		t.Fatal(err)
	}
	if atime(t, control).Equal(older) {
		t.Skip("this filesystem does not update atime on read (noatime?); nothing to check")
	}

	if _, err := File(context.Background(), hashed); err != nil {
		t.Fatal(err)
	}
	if got := atime(t, hashed); !got.Equal(older) {
		t.Errorf("hashing changed atime from %v to %v", older, got)
	}
}
