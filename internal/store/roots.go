package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	Settings
}

// Settings are a root's scan settings. Every scan of the root, whole or partial, uses them.
type Settings struct {
	IncludeHidden bool     // also scan hidden entries
	Ignore        []string // entry names skipped at any depth, besides the built-in noise
}

// Equal reports whether two settings scan the same entries.
func (a Settings) Equal(b Settings) bool {
	if a.IncludeHidden != b.IncludeHidden || len(a.Ignore) != len(b.Ignore) {
		return false
	}
	for i := range a.Ignore {
		if a.Ignore[i] != b.Ignore[i] {
			return false
		}
	}
	return true
}

// normal returns the settings with duplicate ignore names removed, in the order given.
func (a Settings) normal() Settings {
	out := Settings{IncludeHidden: a.IncludeHidden}
	seen := map[string]bool{}
	for _, n := range a.Ignore {
		if !seen[n] {
			seen[n] = true
			out.Ignore = append(out.Ignore, n)
		}
	}
	return out
}

func (a Settings) ignoresJSON() (string, error) {
	if len(a.Ignore) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(a.Ignore)
	return string(b), err
}

// Absorbed is an existing root that a new root took in.
type Absorbed struct {
	Path     string   // the old root, absolute
	Prefix   string   // its place in the new root, '/'-separated
	Settings Settings // its settings before; the new root's settings apply from now on
}

// AddRoot registers a directory as a root with default settings; see AddRootAbsorb.
func (s *Store) AddRoot(ctx context.Context, path string) (Root, error) {
	r, _, err := s.AddRootAbsorb(ctx, path, Settings{})
	return r, err
}

// AddRootAbsorb registers a directory as a root. It refuses an alias of an existing root, and a directory inside an
// existing root on the same filesystem (it is already covered). An existing root inside the new one on the same
// filesystem is absorbed: its paths, scans and events move into the new root under its folder, in the same
// transaction, so file identities, history and review decisions are kept. A root that cannot be checked now (not
// reachable, or its disk not mounted) is never absorbed: the add is refused instead. The new root's settings apply to
// everything it absorbs.
func (s *Store) AddRootAbsorb(ctx context.Context, path string, set Settings) (Root, []Absorbed, error) {
	p, err := canonicalDir(path)
	if err != nil {
		return Root{}, nil, err
	}
	id, err := statID(p)
	if err != nil {
		return Root{}, nil, fmt.Errorf("root %s: %w", p, err)
	}
	mountPath, err := mountPathOf(p, id)
	if err != nil {
		return Root{}, nil, err
	}

	existing, err := s.Roots(ctx)
	if err != nil {
		return Root{}, nil, err
	}
	type take struct {
		e      Root
		prefix string
	}
	var takes []take
	for _, e := range existing {
		prefix, err := overlap(p, id, e)
		if err != nil {
			return Root{}, nil, err
		}
		if prefix != "" {
			takes = append(takes, take{e, prefix})
		}
	}

	r := Root{Path: p, MountPath: mountPath, AddedUTC: time.Now().UTC().Format(time.RFC3339), Settings: set.normal()}
	ignores, err := r.ignoresJSON()
	if err != nil {
		return Root{}, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Root{}, nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO roots (path, mount_path, added_utc, include_hidden, ignores)
		VALUES (?, ?, ?, ?, ?)`, r.Path, nullString(r.MountPath), r.AddedUTC, r.IncludeHidden, ignores)
	if err != nil {
		return Root{}, nil, fmt.Errorf("add root %s: %w", p, err)
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return Root{}, nil, err
	}
	var absorbed []Absorbed
	for _, t := range takes {
		if err := absorb(ctx, tx, t.e.ID, r.ID, t.prefix); err != nil {
			return Root{}, nil, fmt.Errorf("absorb root %s into %s: %w", t.e.Path, p, err)
		}
		absorbed = append(absorbed, Absorbed{Path: t.e.Path, Prefix: t.prefix, Settings: t.e.Settings})
	}
	if err := s.commit(tx); err != nil {
		return Root{}, nil, err
	}
	return r, absorbed, nil
}

// absorb moves root old into root into, under prefix: its paths, scans (a scan of the old root is a scan of the new
// one scoped to prefix) and events, then deletes the old root. Files keep their IDs, so their history and review
// decisions are untouched.
func absorb(ctx context.Context, tx *sql.Tx, old, into int64, prefix string) error {
	under := func(col string) string {
		return `CASE WHEN ` + col + ` IS NULL OR ` + col + ` = '' THEN ? ELSE ? || '/' || ` + col + ` END`
	}
	stmts := []struct {
		q    string
		args []any
	}{
		// Event paths are relative to their scan's root. A returned file's other_path is relative to the root it
		// came from, which the detail names ("... from root /x"): that one stays as it is.
		{`UPDATE events SET path = ` + under("path") + `,
			other_path = CASE WHEN other_path IS NULL OR other_path = '' OR detail LIKE '% from root %' THEN other_path
			                  ELSE ? || '/' || other_path END
			WHERE scan_id IN (SELECT id FROM scans WHERE root_id = ?)`, []any{prefix, prefix, prefix, old}},
		{`UPDATE paths SET root_id = ?, path = ? || '/' || path WHERE root_id = ?`, []any{into, prefix, old}},
		{`UPDATE scans SET root_id = ?, scope = ` + under("scope") + ` WHERE root_id = ?`,
			[]any{into, prefix, prefix, old}},
		{`DELETE FROM roots WHERE id = ?`, []any{old}},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	return nil
}

// overlap compares a candidate root p (identity id) with an existing root e. It returns e's place inside p
// ('/'-separated) if p should absorb e, "" if they do not overlap or are nested across filesystems, or an error if p
// must be refused.
func overlap(p string, id fsid.ID, e Root) (string, error) {
	if p == e.Path {
		return "", fmt.Errorf("%w: %s", ErrRootExists, e.Path)
	}
	eid, reachable := reachableID(e)
	if reachable && eid == id {
		return "", fmt.Errorf("%w: %s is the same directory as root %s", ErrRootExists, p, e.Path)
	}

	inside := isWithin(p, e.Path) || (reachable && hasAncestor(p, eid))
	around := isWithin(e.Path, p) || hasAncestor(e.Path, id)
	if !inside && !around {
		return "", nil
	}
	if !reachable {
		return "", fmt.Errorf("%w: %s and root %s are nested, and %s cannot be checked now (not reachable or not "+
			"mounted); make it reachable and try again", ErrRootOverlap, p, e.Path, e.Path)
	}
	if eid.Dev != id.Dev {
		return "", nil // nested across filesystems: both stay roots
	}
	if inside {
		return "", fmt.Errorf("%w: %s is already inside root %s; scan just that folder with: filo scan %s",
			ErrRootOverlap, p, e.Path, p)
	}
	// e is inside p: by path, or below an alias of p (a bind mount).
	base := p
	if !isWithin(e.Path, p) {
		d, ok := ancestorWithID(e.Path, id, false)
		if !ok {
			return "", fmt.Errorf("%w: cannot place root %s inside %s", ErrRootOverlap, e.Path, p)
		}
		base = d
	}
	rel, err := filepath.Rel(base, e.Path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: cannot place root %s inside %s", ErrRootOverlap, e.Path, p)
	}
	return filepath.ToSlash(rel), nil
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
	out, err := queryAll(ctx, s.db, func(rows *sql.Rows) (Root, error) {
		var r Root
		var ignores string
		if err := rows.Scan(&r.ID, &r.Path, &r.MountPath, &r.AddedUTC, &r.IncludeHidden, &ignores); err != nil {
			return r, err
		}
		if err := json.Unmarshal([]byte(ignores), &r.Ignore); err != nil {
			return r, fmt.Errorf("root %s: ignore names: %w", r.Path, err)
		}
		if len(r.Ignore) == 0 {
			r.Ignore = nil
		}
		return r, nil
	}, `SELECT id, path, coalesce(mount_path, ''), added_utc, include_hidden, ignores FROM roots ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list roots: %w", err)
	}
	return out, nil
}

// SetSettings replaces the settings of the root at path (the root itself, not a folder inside it) and returns the
// root with its settings before the change. The next scan uses the new settings.
func (s *Store) SetSettings(ctx context.Context, path string, set Settings) (Root, error) {
	p, err := canonicalDir(path)
	if err != nil {
		return Root{}, err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return Root{}, err
	}
	for _, r := range roots {
		if r.Path != p {
			continue
		}
		ignores, err := set.normal().ignoresJSON()
		if err != nil {
			return Root{}, err
		}
		if _, err := s.exec(ctx, `UPDATE roots SET include_hidden = ?, ignores = ? WHERE id = ?`,
			set.IncludeHidden, ignores, r.ID); err != nil {
			return Root{}, fmt.Errorf("set settings of root %s: %w", p, err)
		}
		return r, nil
	}
	return Root{}, fmt.Errorf("%w: %s is not a root (filo roots lists them)", ErrNoRoot, p)
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
