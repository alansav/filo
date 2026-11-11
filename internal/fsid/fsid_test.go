package fsid

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	hard := filepath.Join(dir, "hard")
	link := filepath.Join(dir, "link")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(a, hard); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		x, y string
		same bool
	}{
		{"same path", a, a, true},
		{"different files", a, b, false},
		{"hard link is the same file", a, hard, true},
		{"symlink is not followed", a, link, false},
		{"file and its directory", a, dir, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, err := Stat(tc.x)
			if err != nil {
				t.Fatal(err)
			}
			y, err := Stat(tc.y)
			if err != nil {
				t.Fatal(err)
			}
			if (x == y) != tc.same {
				t.Errorf("Stat(%s) == Stat(%s) is %v, want %v", tc.x, tc.y, x == y, tc.same)
			}
			if x.Dev != y.Dev {
				t.Errorf("devices differ inside one temp dir: %d vs %d", x.Dev, y.Dev)
			}
		})
	}

	if _, err := Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("Stat(missing) err = %v, want not-exist", err)
	}
}

func TestFromFileInfo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, dir} {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := FromFileInfo(fi)
		want, err := Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != want || got.Ino == 0 {
			t.Errorf("FromFileInfo(%s) = %+v, %v; Stat = %+v", p, got, ok, want)
		}
	}
}
