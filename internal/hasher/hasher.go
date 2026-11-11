// Package hasher computes SHA-256 content hashes of regular files, without following symlinks, without blocking on
// special files, and only when the file stayed unchanged while it was read.
package hasher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"

	"github.com/alansav/filo/internal/fsid"
)

// Algorithm names the hash stored in the register (meta.hash_algorithm).
const Algorithm = "sha256"

// MaxWorkers caps HashAll's parallelism.
const MaxWorkers = 64

const bufSize = 1 << 20

var (
	// ErrNotRegular means the path is no longer a regular file (directory, FIFO, device, ...).
	ErrNotRegular = errors.New("not a regular file")
	// ErrChanged means the file changed while it was being read; the hash would not match any one state of it.
	ErrChanged = errors.New("file changed while it was being hashed")
)

// Sum is a lowercase hex SHA-256, the same text sha256sum prints.
type Sum string

// Result is a file's hash together with the size and mtime it had while it was read.
type Result struct {
	Sum     Sum
	Size    int64
	MtimeNS int64  // Unix nanoseconds
	CtimeNS int64  // status change time after the read, Unix nanoseconds; 0 = not known
	Ino     uint64 // inode of the open file; 0 = not known
}

// Unreadable reports whether err from File or HashAll means the file is there but cannot be opened or read
// (permission denied, I/O error, anything unexpected), as opposed to a file that vanished, stopped being a regular
// file or changed while it was read, which are retried as they are, or a cancelled scan. Unknown errors count as
// unreadable: the flag only ever holds a file back.
func Unreadable(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, fs.ErrNotExist),
		errors.Is(err, ErrNotRegular),
		errors.Is(err, ErrChanged),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return false
	}
	return true
}

// Progress counts work done, for a progress display. Safe to read while HashAll runs.
type Progress struct {
	Files atomic.Int64 // files finished, successfully or not
	Bytes atomic.Int64 // bytes read so far
}

// afterRead, if set by a test, runs between reading the file and the final fstat.
var afterRead func(path string)

// File hashes the regular file at path.
func File(ctx context.Context, path string) (Result, error) {
	return file(ctx, path, make([]byte, bufSize), nil)
}

func file(ctx context.Context, path string, buf []byte, p *Progress) (Result, error) {
	if p != nil {
		defer p.Files.Add(1)
	}
	f, err := openNoFollow(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	before, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if !before.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%s: %w", path, ErrNotRegular)
	}

	h := sha256.New()
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		m, err := f.Read(buf)
		h.Write(buf[:m])
		n += int64(m)
		if p != nil {
			p.Bytes.Add(int64(m))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Result{}, err
		}
	}

	if afterRead != nil {
		afterRead(path)
	}
	after, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || n != after.Size() {
		return Result{}, fmt.Errorf("%s: %w", path, ErrChanged)
	}
	return Result{
		Sum:     Sum(hex.EncodeToString(h.Sum(nil))),
		Size:    after.Size(),
		MtimeNS: after.ModTime().UnixNano(),
		CtimeNS: fsid.ChangeTimeNS(after),
		Ino:     inode(after),
	}, nil
}

func inode(fi fs.FileInfo) uint64 {
	id, _ := fsid.FromFileInfo(fi)
	return id.Ino
}

// Job is one file to hash.
type Job struct {
	Path string // absolute
}

// Outcome is one job's result or error.
type Outcome struct {
	Result
	Err error
}

// HashAll hashes jobs with the given number of workers. out[i] belongs to jobs[i]; one file's error never stops the
// others. If ctx is cancelled it returns ctx.Err(), and jobs that did not run carry that error. p, if not nil, is
// updated as files are read.
func HashAll(ctx context.Context, jobs []Job, workers int, p *Progress) ([]Outcome, error) {
	if workers < 1 || workers > MaxWorkers {
		return nil, fmt.Errorf("hash workers must be between 1 and %d, got %d", MaxWorkers, workers)
	}
	out := make([]Outcome, len(jobs))
	idx := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, bufSize)
			for i := range idx {
				if err := ctx.Err(); err != nil {
					out[i].Err = err
					continue
				}
				r, err := file(ctx, jobs[i].Path, buf, p)
				out[i] = Outcome{Result: r, Err: err}
			}
		}()
	}

feed:
	for i := range jobs {
		select {
		case idx <- i:
		case <-ctx.Done():
			for j := i; j < len(jobs); j++ {
				out[j].Err = ctx.Err()
			}
			break feed
		}
	}
	close(idx)
	wg.Wait()
	return out, ctx.Err()
}
