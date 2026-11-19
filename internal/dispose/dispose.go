// Package dispose deletes deletion candidates, one at a time, each only after the check at the moment of deleting
// (docs/design.md, Disposal): the file is still at its recorded path as the same file, it can be read, and its
// content is the content its retention was set on. It then removes the directories the run emptied. This is the only
// package that deletes the user's files.
package dispose

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Disposer deletes candidates and remembers the directories it took entries from.
type Disposer struct {
	rec  Recorder
	now  func() time.Time
	dirs map[string]string // directory a file was deleted from -> its root
}

// New returns a Disposer that records through rec and stamps deletions with now.
func New(rec Recorder, now func() time.Time) *Disposer {
	return &Disposer{rec: rec, now: now, dirs: map[string]string{}}
}

// beforeRemove, if set by a test, runs after the check and before the final lstat; beforeUnlink, right before the
// removal, after every check.
var beforeRemove, beforeUnlink func(path string)

// afterKeptLook, if set by a test, runs in DeleteDuplicate's last look, after the kept copy is looked at again.
var afterKeptLook func(path string)

// Delete checks c and deletes it if it passes. A file that fails the check is left alone; one whose content changed
// has its new content recorded, and one that cannot be read is flagged unreadable. The error is for a cancelled run or
// a register that could not be written; the run should stop.
func (d *Disposer) Delete(ctx context.Context, c store.Candidate) (Outcome, error) {
	return d.delete(ctx, c, nil)
}

// delete is Delete, with guard (if not nil) asked about the file (its absolute path, and its name in the open
// directory that holds it) at the last look before the removal; false stops it.
func (d *Disposer) delete(ctx context.Context, c store.Candidate,
	guard func(p string, dir *os.Root, name string) (string, bool)) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	p, o, ok := within(c)
	if !ok {
		return o, nil
	}
	// Everything from here on goes through the directory that holds the file, opened from the root down: a directory
	// swapped for a symlink meanwhile can then never redirect a look, a read or the removal outside the root.
	dir, why, err := openDir(c.RootPath, filepath.FromSlash(c.Path))
	switch {
	case why != "":
		return skip(why), nil
	case errors.Is(err, fs.ErrNotExist):
		return skip("no longer at this path; run filo scan"), nil
	case err != nil:
		return d.unreadable(ctx, c, err)
	}
	defer dir.Close()
	name := dir.name
	fi, err := dir.Lstat(name)
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
		target, err := dir.Readlink(name)
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
				return skip("changed since the export; export again"), nil
			}
			return skip("changed since its retention was set; run filo scan, then give it a new retention"), nil
		}
		res, err := hasher.FileIn(ctx, dir.Root, name)
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
				return skip("changed since the export; export again"), nil
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
	// The directories are checked again: hashing a large file takes long enough for one to be swapped for a symlink,
	// or moved out of the root with the open directory in it. Either way the file is no longer at its recorded path.
	if !dir.stillAt() {
		return skip("changed while it was being checked; run filo scan"), nil
	}
	last, err := dir.Lstat(name)
	if err != nil {
		return skip("changed while it was being checked; run filo scan"), nil
	}
	if _, bad := sameKind(c, last); bad || !sameIno(c.Ino, ino(last)) ||
		(c.Kind == "file" && (last.Size() != c.Size || last.ModTime().UnixNano() != c.MtimeNS)) ||
		(fi != nil && !last.ModTime().Equal(fi.ModTime())) {
		return skip("changed while it was being checked; run filo scan"), nil
	}
	if guard != nil {
		if why, ok := guard(p, dir.Root, name); !ok {
			return skip(why), nil
		}
	}

	if beforeUnlink != nil {
		beforeUnlink(p)
	}
	if err := removeFile(dir.Root, name, p); err != nil {
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
// anything else means the register was altered (a path such as ../../.ssh/id_ed25519; refused, exit 1) or a directory
// from the root down was replaced by a symlink since the scan (an ordinary change: skipped, a scan clears it up).
// Directories above the root are not checked: the root was resolved when it was added, and a symlink above it (a home
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
	if why, _ := realDirs(c.RootPath, rel); why != "" {
		return "", skip(why), false
	}
	return p, Outcome{}, true
}

// realDirs checks, with lstat, that the root and every directory below it down to the file at rel are real directories,
// not symlinks. It returns why not, or ok false if a directory could not be looked at (gone or unreadable; the caller's
// lstat of the file reports that).
func realDirs(root, rel string) (why string, ok bool) {
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
		if why := notRealDir(dir, fi); why != "" {
			return why, true
		}
	}
	return "", true
}

// heldDir is the open directory that holds a file, with what lstat saw of each directory on the way down to it.
type heldDir struct {
	*os.Root
	name  string  // the file's name in it
	chain []dirAt // the root first, then each directory down to this one
}

type dirAt struct {
	path string
	fi   fs.FileInfo
}

// stillAt reports whether the open directory is still where the path says: every directory from the root down is
// still, at its path, a real directory and the very one opened. A directory moved out of the root after it was
// opened (renamed elsewhere, a new one made in its place) leaves the handle pointing outside the root; this catches it.
func (h *heldDir) stillAt() bool {
	for _, d := range h.chain {
		fi, err := os.Lstat(d.path)
		if err != nil || notRealDir(d.path, fi) != "" || !os.SameFile(fi, d.fi) {
			return false
		}
	}
	return true
}

// openDir opens the directory that holds the file at rel below root: the root, then each directory down, each
// through the one above it, and each only if it is a real directory (lstat, never a symlink) and the very directory
// that lstat saw (same device and inode; opening it follows a symlink that stays inside the one above, so one
// swapped in after the lstat is caught here). It returns the open directory; why says what changed on the path (a
// skip, as realDirs words it), and err that a directory is gone or cannot be opened.
func openDir(root, rel string) (*heldDir, string, error) {
	fi, err := os.Lstat(root)
	if err != nil {
		return nil, "", err
	}
	if why := notRealDir(root, fi); why != "" {
		return nil, why, nil
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	if got, err := dir.Stat("."); err != nil || !os.SameFile(fi, got) {
		dir.Close()
		return nil, "a directory on its path changed (" + root + "); run filo scan", err
	}
	chain := []dirAt{{root, fi}}
	parts := strings.Split(rel, string(filepath.Separator))
	path := root
	for _, part := range parts[:len(parts)-1] {
		path = filepath.Join(path, part)
		sub, fi, why, err := openSub(dir, part, path)
		dir.Close()
		if why != "" || err != nil {
			return nil, why, err
		}
		dir = sub
		chain = append(chain, dirAt{path, fi})
	}
	return &heldDir{Root: dir, name: parts[len(parts)-1], chain: chain}, "", nil
}

// openSub opens the directory name in parent (path is its absolute path, for messages) if it is a real directory,
// and the one lstat saw (returned with it).
func openSub(parent *os.Root, name, path string) (*os.Root, fs.FileInfo, string, error) {
	fi, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, "", err
	}
	if why := notRealDir(path, fi); why != "" {
		return nil, nil, why, nil
	}
	sub, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, "", err
	}
	if got, err := sub.Stat("."); err != nil || !os.SameFile(fi, got) {
		sub.Close()
		return nil, nil, "a directory on its path changed (" + path + "); run filo scan", err
	}
	return sub, fi, "", nil
}

// notRealDir says why the directory at path, as lstat saw it (fi), is not a real directory, or "".
func notRealDir(path string, fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(path)
		return "a directory on its path is now a symlink (" + path + " to " + target + "); run filo scan"
	case !fi.IsDir():
		return "a directory on its path is no longer a directory (" + path + "); run filo scan"
	}
	return ""
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

// PruneDirs removes the directories this run emptied: a directory a file was deleted from, if it now has no entries at
// all (hidden ones included), then each parent that removal emptied, stopping below the root. A directory that was
// already empty, or that the run deleted nothing from, is never touched. It returns the directories removed, deepest
// first, and any directory that looked empty but could not be removed.
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
			if err := pruneDir(root[dir], dir); err != nil {
				// Something appeared in it, or it cannot be removed: either way it stays.
				if !errors.Is(err, errKept) && !errors.Is(err, fs.ErrExist) && !isNotEmpty(err) {
					problems = append(problems,
						fmt.Errorf("could not remove the empty directory %s: %s", dir, store.ErrDetail(err)))
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

// errKept means pruneDir left the directory: it has entries, or it or a directory above changed or cannot be read.
var errKept = errors.New("kept")

// pruneDir removes dir, below root, if it is still a real directory with no entries, reaching it only through the
// directories above it, opened from the root down (openDir), so the removal can never land outside the root.
func pruneDir(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return errKept
	}
	parent, why, err := openDir(root, rel)
	if why != "" || err != nil {
		return errKept
	}
	defer parent.Close()
	name := parent.name
	sub, _, why, err := openSub(parent.Root, name, dir)
	if why != "" || err != nil {
		return errKept
	}
	f, err := sub.Open(".")
	sub.Close()
	if err != nil {
		return errKept
	}
	entries, err := f.ReadDir(1)
	f.Close()
	if len(entries) > 0 || !errors.Is(err, io.EOF) || !parent.stillAt() {
		return errKept
	}
	return removeDir(parent.Root, name, dir)
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
	dir, why, err := openDir(c.RootPath, filepath.FromSlash(c.Path))
	switch {
	case why != "":
		return nil, why, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, "no longer at this path", nil
	case err != nil:
		return nil, "cannot be looked at: " + store.ErrDetail(err), nil
	}
	defer dir.Close()
	name := dir.name
	fi, err := dir.Lstat(name)
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
	res, err := hasher.FileIn(ctx, dir.Root, name)
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
	if why, ok := realDirs(k.c.RootPath, filepath.FromSlash(k.c.Path)); !ok || why != "" {
		return gone, false
	}
	fi, err := os.Lstat(k.path)
	if err != nil || !fi.Mode().IsRegular() || !sameIno(k.ino, ino(fi)) || fi.Size() != k.size ||
		fi.ModTime().UnixNano() != k.mtime {
		return gone, false
	}
	return "", true
}

// DeleteDuplicate deletes c, a copy decided on in a duplicates CSV, through the same check as Delete (against the
// content exported), as long as kept still holds the same content: kept is looked at again right before the removal.
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
	return d.delete(ctx, c, func(p string, dir *os.Root, name string) (string, bool) {
		if why, ok := kept.still(); !ok {
			return why, false
		}
		if afterKeptLook != nil {
			afterKeptLook(p)
		}
		// Live, after the kept copy's look: a hard link made meanwhile is seen.
		fi, err := dir.Lstat(name)
		if err != nil {
			return "could not be looked at: " + store.ErrDetail(err) + "; run filo scan", false
		}
		if why, ok := onlyLink(fi); !ok {
			return why, false
		}
		return notSameFile(p, kept.path)
	})
}

// OnlyLink checks, live, that the file at p has no other hard link: deleting one of two links frees nothing, so a
// copy with hard links can be kept but not deleted (docs/design.md, Duplicates). Where the link count is not known
// (an OS filo cannot read it on), it is not taken to be one.
func OnlyLink(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "could not be looked at: " + store.ErrDetail(err) + "; run filo scan", false
	}
	return onlyLink(fi)
}

// onlyLink is OnlyLink on what an lstat of the file saw.
func onlyLink(fi fs.FileInfo) (string, bool) {
	switch {
	case fsid.Links(fi) == 0:
		return "its hard links cannot be counted on this system, so it is not deleted", false
	case fsid.Links(fi) > 1:
		return "has another hard link, so deleting it frees nothing: a copy with hard links can be kept but not " +
			"deleted", false
	}
	return "", true
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
