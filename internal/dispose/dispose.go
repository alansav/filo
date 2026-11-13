// Package dispose deletes deletion candidates, one at a time, each only after the check at the moment of deleting
// (docs/design.md, Disposal): the file is still at its recorded path as the same file, it can be read, and its
// content is the content its retention was set on. It then removes the folders the run emptied. This is the only
// package that deletes the user's files.
package dispose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/store"
)

// Recorder writes each outcome to the register as it happens (store.DeleteRun).
type Recorder interface {
	Deleted(ctx context.Context, c store.Candidate, now time.Time) error
	Changed(ctx context.Context, c store.Candidate, u store.FileUpdate) error
	Unreadable(ctx context.Context, c store.Candidate, why string) error
}

// Outcome is what happened to one candidate.
type Outcome struct {
	Deleted bool
	Reason  string // why it was not deleted; "" if it was
	Failed  bool   // it could not be read or deleted (the command then exits 1)
}

// Disposer deletes candidates and remembers the folders it took entries from.
type Disposer struct {
	rec  Recorder
	now  func() time.Time
	dirs map[string]string // folder a file was deleted from -> its root
}

// New returns a Disposer that records through rec and stamps deletions with now.
func New(rec Recorder, now func() time.Time) *Disposer {
	return &Disposer{rec: rec, now: now, dirs: map[string]string{}}
}

// beforeRemove, if set by a test, runs after the check and before the final lstat.
var beforeRemove func(path string)

// Delete checks c and deletes it if it passes. A file that fails the check is left alone; one whose content changed
// has its new content recorded, and one that cannot be read is flagged unreadable. The error is for a cancelled run or
// a register that could not be written; the run should stop.
func (d *Disposer) Delete(ctx context.Context, c store.Candidate) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	p, reason, altered := within(c)
	if reason != "" {
		return Outcome{Reason: reason, Failed: altered}, nil
	}
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return skip("no longer at this path; run filo scan"), nil
	case err != nil:
		return d.unreadable(ctx, c, err)
	}
	if o, bad := sameKind(c, fi); bad {
		return o, nil
	}
	if !sameIno(c.Ino, ino(fi)) {
		return skip("replaced by another file since the last scan; run filo scan"), nil
	}

	if c.Kind == "symlink" {
		target, err := os.Readlink(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return skip("no longer at this path; run filo scan"), nil
			}
			return d.unreadable(ctx, c, err)
		}
		if target != c.LinkTarget {
			return skip(fmt.Sprintf("the symlink now points to %s, not %s; run filo scan", target, c.LinkTarget)), nil
		}
	} else {
		// The quick check: a different size or modification time means the content changed, without reading it.
		if fi.Size() != c.Size || fi.ModTime().UnixNano() != c.MtimeNS {
			return skip("changed since its retention was set; run filo scan, then give it a new retention"), nil
		}
		res, err := hasher.File(ctx, p)
		switch {
		case ctx.Err() != nil:
			return Outcome{}, ctx.Err()
		case errors.Is(err, fs.ErrNotExist):
			return skip("no longer at this path; run filo scan"), nil
		case errors.Is(err, hasher.ErrChanged), errors.Is(err, hasher.ErrNotRegular):
			return skip("changed while it was being checked; run filo scan"), nil
		case err != nil:
			return d.unreadable(ctx, c, err)
		}
		if !sameIno(c.Ino, res.Ino) {
			return skip("replaced by another file since the last scan; run filo scan"), nil
		}
		if string(res.Sum) != c.ReviewedHash {
			if err := d.rec.Changed(ctx, c, store.FileUpdate{FileID: c.FileID, Size: res.Size, MtimeNS: res.MtimeNS,
				CtimeNS: res.CtimeNS, Hash: string(res.Sum), Ino: res.Ino}); err != nil {
				return Outcome{}, err
			}
			return skip("changed since its retention was set; it needs a new retention"), nil
		}
		fi = nil // compare the final lstat with what was read
		c.Size, c.MtimeNS = res.Size, res.MtimeNS
	}

	// The last look, as close to the removal as possible: still the same file, unchanged since it was checked.
	if beforeRemove != nil {
		beforeRemove(p)
	}
	last, err := os.Lstat(p)
	if err != nil {
		return skip("changed while it was being checked; run filo scan"), nil
	}
	if _, bad := sameKind(c, last); bad || !sameIno(c.Ino, ino(last)) ||
		(c.Kind == "file" && (last.Size() != c.Size || last.ModTime().UnixNano() != c.MtimeNS)) ||
		(fi != nil && !last.ModTime().Equal(fi.ModTime())) {
		return skip("changed while it was being checked; run filo scan"), nil
	}

	if err := removeFile(p); err != nil {
		return Outcome{Reason: "could not be deleted: " + store.ErrDetail(err), Failed: true}, nil
	}
	if err := d.rec.Deleted(ctx, c, d.now()); err != nil {
		return Outcome{Deleted: true}, fmt.Errorf("%w (%s was deleted; the next scan will report it missing)", err, p)
	}
	d.dirs[filepath.Dir(p)] = c.RootPath
	return Outcome{Deleted: true}, nil
}

// within returns the candidate's absolute path if it is inside its root, or why it is not and whether that means
// the register was altered. The register only ever
// records clean paths below canonical roots, and a walk never follows a symlink, so anything else means the register
// was altered (a path such as ../../.ssh/id_ed25519) or a folder on the path was replaced by a symlink since the scan.
// Either way nothing is deleted; only the second is an ordinary change that a scan clears up.
func within(c store.Candidate) (path, reason string, altered bool) {
	const notInside = "its recorded path is not inside its root; the register may have been altered, so it is not deleted"
	rel := filepath.FromSlash(c.Path)
	if c.Path == "" || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || !filepath.IsAbs(c.RootPath) ||
		filepath.Clean(c.RootPath) != c.RootPath {
		return "", notInside, true
	}
	p := filepath.Join(c.RootPath, rel)
	if !inside(p, c.RootPath) {
		return "", notInside, true
	}
	// Every folder from the root down to the file must still be a real folder, not a symlink to somewhere else.
	dir := filepath.Dir(p)
	real, err := filepath.EvalSymlinks(dir)
	switch {
	case err != nil:
		return p, "", false // the lstat below reports it gone or unreadable
	case real != dir:
		return "", "a folder on its path is now a symlink (to " + real + "); run filo scan", false
	}
	return p, "", false
}

func (d *Disposer) unreadable(ctx context.Context, c store.Candidate, err error) (Outcome, error) {
	why := "cannot be read: " + store.ErrDetail(err)
	if rerr := d.rec.Unreadable(ctx, c, why); rerr != nil {
		return Outcome{}, rerr
	}
	return Outcome{Reason: why, Failed: true}, nil
}

func skip(reason string) Outcome { return Outcome{Reason: reason} }

// sameKind reports a candidate that is no longer the kind of file it was.
func sameKind(c store.Candidate, fi fs.FileInfo) (Outcome, bool) {
	switch {
	case c.Kind == "symlink" && fi.Mode()&fs.ModeSymlink == 0:
		return skip("no longer a symlink; run filo scan"), true
	case c.Kind != "symlink" && !fi.Mode().IsRegular():
		return skip("no longer a regular file; run filo scan"), true
	}
	return Outcome{}, false
}

func ino(fi fs.FileInfo) uint64 {
	id, _ := fsid.FromFileInfo(fi)
	return id.Ino
}

// sameIno compares inodes only when both are known, as a scan does.
func sameIno(a, b uint64) bool { return a == 0 || b == 0 || a == b }

// PruneDirs removes the folders this run emptied: a folder a file was deleted from, if it now has no entries at all
// (hidden ones included), then each parent that removal emptied, stopping below the root. A folder that was already
// empty, or that the run deleted nothing from, is never touched. It returns the folders removed, deepest first, and
// any folder that looked empty but could not be removed.
func (d *Disposer) PruneDirs() (removed []string, problems []error) {
	byDepth := map[int][]string{}
	root := map[string]string{}
	maxDepth := 0
	add := func(dir, r string) {
		if _, ok := root[dir]; ok || !inside(dir, r) {
			return
		}
		root[dir] = r
		n := depth(dir)
		byDepth[n] = append(byDepth[n], dir)
		maxDepth = max(maxDepth, n)
	}
	for dir, r := range d.dirs {
		add(dir, r)
	}
	for n := maxDepth; n >= 0; n-- {
		for i := 0; i < len(byDepth[n]); i++ {
			dir := byDepth[n][i]
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				continue
			}
			if err := removeDir(dir); err != nil {
				// Something appeared in it, or it cannot be removed: either way it stays.
				if !errors.Is(err, fs.ErrExist) && !isNotEmpty(err) {
					problems = append(problems, fmt.Errorf("could not remove the empty folder %s: %s", dir, store.ErrDetail(err)))
				}
				continue
			}
			removed = append(removed, dir)
			add(filepath.Dir(dir), root[dir])
		}
	}
	return removed, problems
}

// inside reports whether dir is strictly below root.
func inside(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

func depth(dir string) int { return strings.Count(filepath.Clean(dir), string(filepath.Separator)) }
