package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

var (
	// ErrRootExists means the directory is already a root, under its own path or an alias (symlink, bind mount).
	ErrRootExists = errors.New("already a root")
	// ErrRootOverlap means the directory is inside, or contains, a root on the same filesystem.
	ErrRootOverlap = errors.New("overlaps an existing root")
	// ErrNoRoot means a path is not inside any registered root.
	ErrNoRoot = errors.New("not inside any root")
	// ErrNotMounted means the mount a root lives on is no longer mounted.
	ErrNotMounted = errors.New("root looks unmounted")
)

// statID is fsid.Stat, swappable in tests to simulate other filesystems and bind mounts.
var statID = fsid.Stat

// Root is a registered root directory.
type Root struct {
	ID        int64
	Path      string // absolute, symlinks resolved
	MountPath string // the mount point the root lives under when added; "" = the filesystem of /
	AddedUTC  string
}

// AddRoot registers a directory as a root. It refuses an alias of an existing root, and a root inside or around
// another root on the same filesystem.
func (s *Store) AddRoot(ctx context.Context, path string) (Root, error) {
	p, err := canonicalDir(path)
	if err != nil {
		return Root{}, err
	}
	id, err := statID(p)
	if err != nil {
		return Root{}, fmt.Errorf("root %s: %w", p, err)
	}
	mountPath, err := mountPathOf(p, id)
	if err != nil {
		return Root{}, err
	}

	existing, err := s.Roots(ctx)
	if err != nil {
		return Root{}, err
	}
	for _, e := range existing {
		if err := checkNoOverlap(p, id, e); err != nil {
			return Root{}, err
		}
	}

	r := Root{Path: p, MountPath: mountPath, AddedUTC: time.Now().UTC().Format(time.RFC3339)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Root{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO roots (path, mount_path, added_utc) VALUES (?, ?, ?)`,
		r.Path, nullString(r.MountPath), r.AddedUTC)
	if err != nil {
		return Root{}, fmt.Errorf("add root %s: %w", p, err)
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return Root{}, err
	}
	if err := tx.Commit(); err != nil {
		return Root{}, err
	}
	return r, nil
}

// checkNoOverlap compares a candidate root p (identity id) with an existing root e.
func checkNoOverlap(p string, id fsid.ID, e Root) error {
	if p == e.Path {
		return fmt.Errorf("%w: %s", ErrRootExists, e.Path)
	}
	eid, reachable := reachableID(e)
	if reachable && eid == id {
		return fmt.Errorf("%w: %s is the same directory as root %s", ErrRootExists, p, e.Path)
	}

	inside := isWithin(p, e.Path) || (reachable && hasAncestor(p, eid))
	around := isWithin(e.Path, p) || hasAncestor(e.Path, id)
	if !inside && !around {
		return nil
	}
	if !reachable {
		return fmt.Errorf("%w: %s and root %s are nested, and %s cannot be checked now (not reachable or not mounted)",
			ErrRootOverlap, p, e.Path, e.Path)
	}
	if eid.Dev == id.Dev {
		return fmt.Errorf("%w: %s and root %s are nested on the same filesystem", ErrRootOverlap, p, e.Path)
	}
	return nil
}

// reachableID returns a root's identity if it can be checked now: it exists and its mount, if any, is mounted.
func reachableID(r Root) (fsid.ID, bool) {
	if checkRoot(r) != nil {
		return fsid.ID{}, false
	}
	id, err := statID(r.Path)
	return id, err == nil
}

// Roots lists the registered roots, ordered by path.
func (s *Store) Roots(ctx context.Context) ([]Root, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, coalesce(mount_path, ''), added_utc FROM roots ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list roots: %w", err)
	}
	defer rows.Close()
	var out []Root
	for rows.Next() {
		var r Root
		if err := rows.Scan(&r.ID, &r.Path, &r.MountPath, &r.AddedUTC); err != nil {
			return nil, fmt.Errorf("list roots: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Resolve maps a scan target to its root and a scope relative to that root ('/'-separated, "" for the whole root).
// The deepest root containing the target by path wins; failing that, a root reached through an alias (bind mount) of
// one of the target's ancestors. The target must be a directory on the root's filesystem, and the root must pass
// CheckRoot.
func (s *Store) Resolve(ctx context.Context, target string) (Root, string, error) {
	p, err := canonicalDir(target)
	if err != nil {
		return Root{}, "", err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return Root{}, "", err
	}

	var best Root
	var base string // the directory standing for best.Path: best.Path itself, or an alias of it
	for _, r := range roots {
		if (p == r.Path || isWithin(p, r.Path)) && len(r.Path) > len(base) {
			best, base = r, r.Path
		}
	}
	if base == "" {
		for _, r := range roots {
			rid, ok := reachableID(r)
			if !ok {
				continue
			}
			if d, ok := ancestorWithID(p, rid, true); ok && len(d) > len(base) {
				best, base = r, d
			}
		}
	}
	if base == "" {
		return Root{}, "", fmt.Errorf("%w: %s; add it with `filo roots add`", ErrNoRoot, p)
	}

	if err := CheckRoot(best); err != nil {
		return Root{}, "", err
	}
	tid, err := statID(p)
	if err != nil {
		return Root{}, "", fmt.Errorf("%s: %w", p, err)
	}
	rid, err := statID(best.Path)
	if err != nil {
		return Root{}, "", fmt.Errorf("root %s: %w", best.Path, err)
	}
	if tid.Dev != rid.Dev {
		return Root{}, "", fmt.Errorf("%s is on a different filesystem from root %s; add it as its own root", p, best.Path)
	}

	rel, err := filepath.Rel(base, p)
	if err != nil {
		return Root{}, "", err
	}
	if rel == "." {
		rel = ""
	}
	return best, filepath.ToSlash(rel), nil
}

// CheckRoot verifies a root can be scanned: it must still be a directory and, if it lives on a mounted filesystem,
// that mount point must still be mounted (on a different device from its parent) with the root on it. A scan must
// call this before walking, so an unmounted disk never looks like deleted files.
func CheckRoot(r Root) error {
	return checkRoot(r)
}

func checkRoot(r Root) error {
	fi, err := os.Lstat(r.Path)
	if err != nil {
		return fmt.Errorf("root %s: %w", r.Path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("root %s is no longer a directory", r.Path)
	}
	if r.MountPath == "" {
		return nil
	}
	notMounted := fmt.Errorf("%w: %s is not mounted (root %s); mount it and retry", ErrNotMounted, r.MountPath, r.Path)
	mid, err := statID(r.MountPath)
	if err != nil {
		return notMounted
	}
	pid, err := statID(filepath.Dir(r.MountPath))
	if err != nil {
		return fmt.Errorf("parent of %s: %w", r.MountPath, err)
	}
	if pid.Dev == mid.Dev {
		return notMounted
	}
	rid, err := statID(r.Path)
	if err != nil {
		return fmt.Errorf("root %s: %w", r.Path, err)
	}
	if rid.Dev != mid.Dev {
		return fmt.Errorf("%w: root %s is no longer on the filesystem mounted at %s", ErrNotMounted, r.Path, r.MountPath)
	}
	return nil
}

// mountPathOf returns the mount point p (identity id) lives under: the deepest directory, p itself or an ancestor,
// whose device differs from its parent's. "" means p is on the same filesystem as /, which cannot be unmounted.
func mountPathOf(p string, id fsid.ID) (string, error) {
	d := p
	for {
		parent := filepath.Dir(d)
		if parent == d {
			return "", nil
		}
		pid, err := statID(parent)
		if err != nil {
			return "", fmt.Errorf("%s: %w", parent, err)
		}
		if pid.Dev != id.Dev {
			return d, nil
		}
		d, id = parent, pid
	}
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// canonicalDir makes path absolute, resolves symlinks and requires a directory.
func canonicalDir(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return resolved, nil
}

// isWithin reports whether child is strictly below parent, by path.
func isWithin(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// hasAncestor reports whether a strict ancestor of p has identity id.
func hasAncestor(p string, id fsid.ID) bool {
	_, ok := ancestorWithID(p, id, false)
	return ok
}

// ancestorWithID returns the deepest directory among p's ancestors (and p itself if includeSelf) with identity id.
func ancestorWithID(p string, id fsid.ID, includeSelf bool) (string, bool) {
	d := p
	if !includeSelf {
		d = filepath.Dir(p)
		if d == p {
			return "", false
		}
	}
	for {
		if got, err := statID(d); err == nil && got == id {
			return d, true
		}
		next := filepath.Dir(d)
		if next == d {
			return "", false
		}
		d = next
	}
}
