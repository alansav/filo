package hasher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

func write(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func randomBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sumOf(b []byte) Sum {
	s := sha256.Sum256(b)
	return Sum(hex.EncodeToString(s[:]))
}

func TestFile(t *testing.T) {
	dir := t.TempDir()
	big := randomBytes(t, 3*bufSize+bufSize/2) // crosses buffer boundaries
	unix := runtime.GOOS != "windows"

	tests := []struct {
		name    string
		setup   func(t *testing.T) string // returns the path to hash
		want    Sum                       // checked when no error is expected
		wantErr func(error) bool          // nil = must succeed
	}{
		{
			name:  "empty file",
			setup: func(t *testing.T) string { return write(t, filepath.Join(dir, "empty"), nil) },
			want:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:  "abc",
			setup: func(t *testing.T) string { return write(t, filepath.Join(dir, "abc"), []byte("abc")) },
			want:  "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		},
		{
			name:  "larger than the buffer",
			setup: func(t *testing.T) string { return write(t, filepath.Join(dir, "big"), big) },
			want:  sumOf(big),
		},
		{
			name: "symlink to a valid file is not followed",
			setup: func(t *testing.T) string {
				target := write(t, filepath.Join(dir, "target"), []byte("t"))
				link := filepath.Join(dir, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink: %v", err)
				}
				return link
			},
			wantErr: func(err error) bool { return err != nil },
		},
		{
			name:    "directory",
			setup:   func(t *testing.T) string { return dir },
			wantErr: func(err error) bool { return errors.Is(err, ErrNotRegular) || (!unix && err != nil) },
		},
		{
			name:    "missing",
			setup:   func(t *testing.T) string { return filepath.Join(dir, "missing") },
			wantErr: func(err error) bool { return errors.Is(err, fs.ErrNotExist) },
		},
		{
			name: "unreadable",
			setup: func(t *testing.T) string {
				if !unix || os.Geteuid() == 0 {
					t.Skip("permissions not enforced here")
				}
				p := write(t, filepath.Join(dir, "locked"), []byte("x"))
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: func(err error) bool { return errors.Is(err, fs.ErrPermission) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.setup(t)
			r, err := File(context.Background(), p)
			if tc.wantErr != nil {
				if !tc.wantErr(err) {
					t.Fatalf("err = %v (result %+v), not the expected error", err, r)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Sum != tc.want {
				t.Errorf("sum = %s, want %s", r.Sum, tc.want)
			}
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if r.Size != fi.Size() || r.MtimeNS != fi.ModTime().UnixNano() {
				t.Errorf("size/mtime = %d/%d, want %d/%d", r.Size, r.MtimeNS, fi.Size(), fi.ModTime().UnixNano())
			}
		})
	}
}

func TestFileFIFODoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pipe")
	if err := mkfifo(p); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := File(context.Background(), p)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v, want ErrNotRegular", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("File blocked on a FIFO")
	}
}

func TestFileChangedWhileReading(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, path string)
	}{
		{"appended", func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString("more"); err != nil {
				t.Fatal(err)
			}
		}},
		{"mtime only", func(t *testing.T, path string) {
			when := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, when, when); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, filepath.Join(t.TempDir(), "f"), []byte("original"))
			afterRead = func(string) { tc.change(t, p) }
			t.Cleanup(func() { afterRead = nil })
			if _, err := File(context.Background(), p); !errors.Is(err, ErrChanged) {
				t.Fatalf("err = %v, want ErrChanged", err)
			}
		})
	}
}

func TestCancelled(t *testing.T) {
	p := write(t, filepath.Join(t.TempDir(), "f"), []byte("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := File(ctx, p); !errors.Is(err, context.Canceled) {
		t.Errorf("File err = %v, want context.Canceled", err)
	}
	out, err := HashAll(ctx, []Job{{p}, {p}, {p}}, 2, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("HashAll err = %v, want context.Canceled", err)
	}
	for i, o := range out {
		if !errors.Is(o.Err, context.Canceled) {
			t.Errorf("outcome %d err = %v, want context.Canceled", i, o.Err)
		}
	}
}

func TestHashAll(t *testing.T) {
	dir := t.TempDir()
	var jobs []Job
	var want []Sum
	for i := range 200 {
		data := []byte(fmt.Sprintf("file %d", i))
		jobs = append(jobs, Job{write(t, filepath.Join(dir, fmt.Sprintf("f%03d", i)), data)})
		want = append(want, sumOf(data))
	}
	for _, workers := range []int{1, 4, 16} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			out, err := HashAll(context.Background(), jobs, workers, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != len(jobs) {
				t.Fatalf("%d outcomes, want %d", len(out), len(jobs))
			}
			for i, o := range out {
				if o.Err != nil || o.Sum != want[i] {
					t.Errorf("outcome %d = %+v, want sum %s", i, o, want[i])
				}
			}
		})
	}

	t.Run("per-file errors do not stop the others", func(t *testing.T) {
		mixed := []Job{jobs[0], {filepath.Join(dir, "missing")}, {dir}, jobs[1]}
		out, err := HashAll(context.Background(), mixed, 3, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantErr := []bool{false, true, true, false}
		for i, o := range out {
			if (o.Err != nil) != wantErr[i] {
				t.Errorf("outcome %d err = %v, want error %v", i, o.Err, wantErr[i])
			}
		}
		if out[0].Sum != want[0] || out[3].Sum != want[1] {
			t.Errorf("good files hashed wrongly: %s, %s", out[0].Sum, out[3].Sum)
		}
	})

	for _, w := range []int{0, -1, MaxWorkers + 1} {
		t.Run(fmt.Sprintf("bad workers=%d", w), func(t *testing.T) {
			if _, err := HashAll(context.Background(), jobs, w, nil); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func BenchmarkFile(b *testing.B) {
	p := filepath.Join(b.TempDir(), "big")
	data := randomBytes(b, 64<<20)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := File(context.Background(), p); err != nil {
			b.Fatal(err)
		}
	}
}

func TestFileInode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	p := write(t, filepath.Join(t.TempDir(), "f"), []byte("x"))
	r, err := File(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fsid.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ino == 0 || r.Ino != want.Ino {
		t.Errorf("Ino = %d, want %d", r.Ino, want.Ino)
	}
}

func TestUnreadable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"permission denied", &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}, true},
		{"unexpected error", errors.New("input/output error"), true},
		{"vanished", &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrNotExist}, false},
		{"no longer a regular file", fmt.Errorf("/x: %w", ErrNotRegular), false},
		{"changed while read", fmt.Errorf("/x: %w", ErrChanged), false},
		{"cancelled", context.Canceled, false},
		{"timed out", context.DeadlineExceeded, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unreadable(tc.err); got != tc.want {
				t.Errorf("Unreadable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFileCtime(t *testing.T) {
	p := write(t, filepath.Join(t.TempDir(), "f"), []byte("x"))
	r, err := File(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	want := fsid.ChangeTimeNS(fi)
	if r.CtimeNS != want {
		t.Errorf("CtimeNS = %d, want %d", r.CtimeNS, want)
	}
	if want == 0 && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
		t.Errorf("no ctime on %s", runtime.GOOS)
	}
}
