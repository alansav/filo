package store

import (
	"context"
	"database/sql"
	"fmt"
)

// FoundFile is one regular file at its current path, as filo find lists it (docs/design.md, Finding files).
type FoundFile struct {
	RootID     int64
	Root       string // the root it is in, absolute
	Rel        string // the path relative to the root, '/' separators
	Size       int64
	MtimeNS    int64
	Inode      int64 // 0 = not known
	Unreadable bool  // it could not be read the last time it was hashed
	// Links is how many other current paths in its root are the same file, hard links: the same inode, size and
	// modified time (a stale inode, reused by another file, is not enough to make two files one). Counted over the
	// whole root, not only the scope.
	Links int
}

// AbsPath is the file's absolute path.
func (f FoundFile) AbsPath() string { return absPath(f.Root, f.Rel) }

// FindFiles returns the regular files at their current paths in a root (0 for every root) at scope and below it
// ("" for all of it), ordered by root and then path. Missing files and files deleted by filo have no current path, so
// they are not among them, and nor are symlinks. Each path is a row, with its count of Links: hard links are merged by
// the caller.
func (s *Store) FindFiles(ctx context.Context, rootID int64, scope string) ([]FoundFile, error) {
	cond, args := Filter{RootID: rootID, Scope: scope}.scopeSQL()
	out, err := queryAll(ctx, s.db, func(rs *sql.Rows) (FoundFile, error) {
		var f FoundFile
		return f, rs.Scan(&f.RootID, &f.Root, &f.Rel, &f.Size, &f.MtimeNS, &f.Inode, &f.Unreadable, &f.Links)
	}, `
		SELECT p.root_id, r.path, p.path, f.size, f.mtime_ns, coalesce(f.inode, 0), f.unreadable_since_scan IS NOT NULL,
			CASE WHEN f.inode IS NULL THEN 0 ELSE (
				SELECT count(*) FROM files lf JOIN paths lp ON lp.file_id = lf.id
				WHERE lf.inode = f.inode AND lf.size = f.size AND lf.mtime_ns = f.mtime_ns AND lf.kind = 'file'
					AND lf.deleted_utc IS NULL AND lp.root_id = p.root_id AND lp.valid_to_scan IS NULL AND lp.id <> p.id)
			END
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'file' AND f.deleted_utc IS NULL`+cond+`
		ORDER BY r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("read files: %w", err)
	}
	return out, nil
}
