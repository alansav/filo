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
	return d.delete(ctx, c, nil)
}

// delete is Delete, with guard (if not nil) asked about the file's absolute path at the last look before the
// removal; false stops it.
func (d *Disposer) delete(ctx context.Context, c store.Candidate, guard func(p string) (string, bool)) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	p, o, ok := within(c)
	if !ok {
		return o, nil
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
			if c.DuplicateOf != "" {
				return skip("changed since it was marked for deletion; run filo scan"), nil
			}
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
			if c.DuplicateOf != "" {
				return skip("changed since it was marked for deletion; its mark no longer counts"), nil
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
	// The folders are checked again: hashing a large file takes long enough for one to be swapped for a symlink.
	if why, ok := realFolders(c.RootPath, filepath.FromSlash(c.Path)); !ok || why != "" {
		return skip("changed while it was being checked; run filo scan"), nil
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
	if guard != nil {
		if why, ok := guard(p); !ok {
			return skip(why), nil
		}
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

// within returns the candidate's absolute path if it is inside its root, or else the outcome that stops its
// deletion. The register only ever records clean paths below canonical roots, and a walk never follows a symlink, so
// anything else means the register was altered (a path such as ../../.ssh/id_ed25519; refused, exit 1) or a folder
// from the root down was replaced by a symlink since the scan (an ordinary change: skipped, a scan clears it up).
// Folders above the root are not checked: the root was resolved when it was added, and a symlink above it (a home
// directory moved to another disk) moves the whole root, which scans handle.
func within(c store.Candidate) (string, Outcome, bool) {
	altered := Outcome{Reason: "its recorded path is not inside its root; the register may have been altered, so " +
		"it is not deleted", Failed: true}
	rel := filepath.FromSlash(c.Path)
	if c.Path == "" || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel || !filepath.IsAbs(c.RootPath) ||
		filepath.Clean(c.RootPath) != c.RootPath {
		return "", altered, false
	}
	p := filepath.Join(c.RootPath, rel)
	if !inside(p, c.RootPath) {
		return "", altered, false
	}
	if why, _ := realFolders(c.RootPath, rel); why != "" {
		return "", skip(why), false
	}
	return p, Outcome{}, true
}

// realFolders checks, with lstat, that the root and every folder below it down to the file at rel are real folders,
// not symlinks. It returns why not, or ok false if a folder could not be looked at (gone or unreadable; the caller's
// lstat of the file reports that).
func realFolders(root, rel string) (why string, ok bool) {
	dir := root
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	if filepath.Dir(rel) == "." {
		parts = nil
	}
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			dir = filepath.Join(dir, parts[i])
		}
		fi, err := os.Lstat(dir)
		if err != nil {
			return "", false
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(dir)
			return "a folder on its path is now a symlink (" + dir + " to " + target + "); run filo scan", true
		}
		if !fi.IsDir() {
			return "a folder on its path is no longer a folder (" + dir + "); run filo scan", true
		}
	}
	return "", true
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

// Kept is a copy checked on disk to stay while copies of the same content are deleted (docs/design.md, Duplicates:
// never the last copy).
type Kept struct {
	c     store.Candidate
	path  string
	size  int64
	mtime int64
	ino   uint64
}

// Path is the kept copy's absolute path.
func (k *Kept) Path() string { return k.path }

// Candidate is the kept copy as the register has it.
func (k *Kept) Candidate() store.Candidate { return k.c }

// CheckKept checks, without changing anything, that c is still at its path as the same file, can be read, and
// re-hashes to c.ReviewedHash. It returns the kept copy, or why it cannot be one ("" with a nil Kept never happens).
// The error is for a cancelled run.
func CheckKept(ctx context.Context, c store.Candidate) (*Kept, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	p, o, ok := within(c)
	if !ok {
		return nil, o.Reason, nil
	}
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "no longer at this path", nil
	case err != nil:
		return nil, "cannot be looked at: " + store.ErrDetail(err), nil
	}
	if o, bad := sameKind(c, fi); bad {
		return nil, o.Reason, nil
	}
	if !sameIno(c.Ino, ino(fi)) {
		return nil, "replaced by another file since the last scan", nil
	}
	if fi.Size() != c.Size || fi.ModTime().UnixNano() != c.MtimeNS {
		return nil, "changed since the last scan", nil
	}
	res, err := hasher.File(ctx, p)
	switch {
	case ctx.Err() != nil:
		return nil, "", ctx.Err()
	case errors.Is(err, fs.ErrNotExist):
		return nil, "no longer at this path", nil
	case errors.Is(err, hasher.ErrChanged), errors.Is(err, hasher.ErrNotRegular):
		return nil, "changed while it was being checked", nil
	case err != nil:
		return nil, "cannot be read: " + store.ErrDetail(err), nil
	}
	if !sameIno(c.Ino, res.Ino) {
		return nil, "replaced by another file since the last scan", nil
	}
	if c.ReviewedHash == "" || string(res.Sum) != c.ReviewedHash {
		return nil, "its content is no longer the same", nil
	}
	return &Kept{c: c, path: p, size: res.Size, mtime: res.MtimeNS, ino: res.Ino}, "", nil
}

// still looks at the kept copy again: still a real file at its path, with the inode, size and modification time it
// had when it was read.
func (k *Kept) still() (string, bool) {
	gone := "the copy kept, " + k.path + ", changed while this one was being checked; run filo scan"
	if why, ok := realFolders(k.c.RootPath, filepath.FromSlash(k.c.Path)); !ok || why != "" {
		return gone, false
	}
	fi, err := os.Lstat(k.path)
	if err != nil || !fi.Mode().IsRegular() || !sameIno(k.ino, ino(fi)) || fi.Size() != k.size ||
		fi.ModTime().UnixNano() != k.mtime {
		return gone, false
	}
	return "", true
}

// DeleteDuplicate deletes c, a copy marked for deletion, through the same check as Delete (against the content it
// was marked on), as long as kept still holds the same content: kept is looked at again right before the removal.
func (d *Disposer) DeleteDuplicate(ctx context.Context, c store.Candidate, kept *Kept) (Outcome, error) {
	if kept == nil || c.ReviewedHash == "" || c.ReviewedHash != kept.c.ReviewedHash {
		return skip("no copy of the same content is kept"), nil
	}
	if p, _, ok := within(c); ok {
		if why, ok := notSameFile(p, kept.path); !ok {
			return skip(why), nil
		}
	}
	c.DuplicateOf = kept.path
	return d.delete(ctx, c, func(p string) (string, bool) {
		if why, ok := kept.still(); !ok {
			return why, false
		}
		return notSameFile(p, kept.path)
	})
}

// notSameFile checks, live, that two paths are not one file: a hard link in the same root, or the same file seen
// through a bind mount in two roots (the register cannot tell, since it keeps no device numbers). Unlinking one
// would then remove the other. If either cannot be looked at, they are not taken to be different.
func notSameFile(p, kept string) (string, bool) {
	a, errA := fsid.Stat(p)
	b, errB := fsid.Stat(kept)
	switch {
	case errA != nil || errB != nil:
		return "could not be compared with the copy kept, " + kept + "; run filo scan", false
	case a.Ino != 0 && a == b:
		return "is the same file as the copy kept, " + kept + " (a hard link or a bind mount), so deleting it " +
			"would delete both", false
	}
	return "", true
}

// SameFile reports whether two paths are one file on disk (same device and inode), looked at now. An error means
// one of them could not be looked at.
func SameFile(a, b string) (bool, error) {
	ia, err := fsid.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := fsid.Stat(b)
	if err != nil {
		return false, err
	}
	return ia.Ino != 0 && ia == ib, nil
}
