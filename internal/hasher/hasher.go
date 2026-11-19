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
	"os"
	"path"
	"path/filepath"
	"strings"
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
	f, err := openNoFollow(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	return file(ctx, f, path, make([]byte, bufSize), nil)
}

// FileIn hashes the regular file name in dir, opened through dir (see OpenIn).
func FileIn(ctx context.Context, dir *os.Root, name string) (Result, error) {
	f, err := OpenIn(dir, name)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	return file(ctx, f, name, make([]byte, bufSize), nil)
}

// OpenIn opens the regular file name (a path below dir, '/'-separated) for reading through dir, so it is inside dir
// whatever is swapped meanwhile, and only if it is the regular file lstat sees there: dir would follow a symlink in
// the file's place if it stays inside dir, so one swapped in since the lstat is caught by comparing identities
// (ErrChanged). A directory on a longer name that is a symlink staying inside dir is followed by both the lstat and
// the open; callers that must not follow one open the file's own directory first (as HashAll does) and pass a bare
// name.
func OpenIn(dir *os.Root, name string) (*os.File, error) {
	fi, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, ErrNotRegular)
	}
	f, err := openInRoot(dir, name)
	if err != nil {
		return nil, err
	}
	got, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(fi, got) {
		f.Close()
		return nil, fmt.Errorf("%s: %w", name, ErrChanged)
	}
	return f, nil
}

// file hashes f, open on the file at path (named in errors).
func file(ctx context.Context, f *os.File, path string, buf []byte, p *Progress) (Result, error) {

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
	// Root, if set, is the directory Path is below; the file is then opened through it (OpenIn), so a directory
	// swapped for a symlink since the walk cannot lead outside it.
	Root string
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
			roots := &dirCache{roots: map[string]*os.Root{}}
			defer roots.close()
			for i := range idx {
				if err := ctx.Err(); err != nil {
					out[i].Err = err
					continue
				}
				r, err := hashJob(ctx, jobs[i], roots, buf, p)
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

// hashJob opens and hashes one job's file, through its root if it has one (opened once into roots).
func hashJob(ctx context.Context, j Job, roots *dirCache, buf []byte, p *Progress) (Result, error) {
	if p != nil {
		defer p.Files.Add(1)
	}
	var f *os.File
	var err error
	if j.Root == "" {
		f, err = openNoFollow(j.Path)
	} else {
		f, err = openJob(j, roots)
	}
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	return file(ctx, f, j.Path, buf, p)
}

// dirCache holds one worker's open roots, and the directory of its last job: jobs come in walk order, so most
// files are in the directory of the one before, and each is then opened with a single lookup by its bare name.
type dirCache struct {
	roots   map[string]*os.Root // root path -> open root
	key     string              // root and directory of dir
	dir     *os.Root
	ownsDir bool // dir is not one of roots
}

func (c *dirCache) close() {
	if c.ownsDir {
		c.dir.Close()
	}
	for _, r := range c.roots {
		r.Close()
	}
}

// open returns the directory rel (relative to root, '/'-separated, "." for the root itself), open through root.
func (c *dirCache) open(root, rel string) (*os.Root, error) {
	if key := root + "\x00" + rel; c.dir != nil && key == c.key {
		return c.dir, nil
	}
	r, ok := c.roots[root]
	if !ok {
		var err error
		if r, err = os.OpenRoot(root); err != nil {
			return nil, err
		}
		c.roots[root] = r
	}
	d, owns := r, false
	if rel != "." {
		var err error
		if d, err = r.OpenRoot(rel); err != nil {
			return nil, err
		}
		owns = true
	}
	if c.ownsDir {
		c.dir.Close()
	}
	c.key, c.dir, c.ownsDir = root+"\x00"+rel, d, owns
	return d, nil
}

func openJob(j Job, roots *dirCache) (*os.File, error) {
	rel, err := filepath.Rel(j.Root, j.Path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("%s is not inside %s", j.Path, j.Root)
	}
	rel = filepath.ToSlash(rel)
	dir, err := roots.open(j.Root, path.Dir(rel))
	if err != nil {
		return nil, asPathError(err, filepath.Dir(j.Path))
	}
	f, err := OpenIn(dir, path.Base(rel))
	return f, asPathError(err, j.Path)
}

// asPathError names the absolute path in an error from a Root, which names the path relative to it ("openat
// docs/a.txt"), so messages read as they do for a file opened by path ("open /home/a/docs/a.txt").
func asPathError(err error, path string) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: strings.TrimSuffix(pe.Op, "at"), Path: path, Err: pe.Err}
	}
	return err
}
