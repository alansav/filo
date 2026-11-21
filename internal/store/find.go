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
}

// AbsPath is the file's absolute path.
func (f FoundFile) AbsPath() string { return absPath(f.Root, f.Rel) }

// FindFiles returns the regular files at their current paths in a root (0 for every root) at scope and below it
// ("" for all of it), ordered by root and then path. Missing files and files deleted by filo have no current path, so
// they are not among them, and nor are symlinks. Each path is a row: hard links are merged by the caller.
func (s *Store) FindFiles(ctx context.Context, rootID int64, scope string) ([]FoundFile, error) {
	cond, args := Filter{RootID: rootID, Scope: scope}.scopeSQL()
	out, err := queryAll(ctx, s.db, func(rs *sql.Rows) (FoundFile, error) {
		var f FoundFile
		return f, rs.Scan(&f.RootID, &f.Root, &f.Rel, &f.Size, &f.MtimeNS, &f.Inode, &f.Unreadable)
	}, `
		SELECT p.root_id, r.path, p.path, f.size, f.mtime_ns, coalesce(f.inode, 0), f.unreadable_since_scan IS NOT NULL
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'file' AND f.deleted_utc IS NULL`+cond+`
		ORDER BY r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("read files: %w", err)
	}
	return out, nil
}
