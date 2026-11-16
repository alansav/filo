package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

// What is not counted as duplicates on purpose (docs/design.md, Duplicates, "filo duplicates"): excluded
// directories, contents always and sets of records; listing them, and undoing each.

// AbsPath is the absolute path of rel ('/'-separated, "" for the root itself) in root.
func AbsPath(root, rel string) string { return absPath(root, rel) }

// atOrUnder is the one definition, in SQL, of "the path in column col is dir or under it" (dir "" is the whole
// root), with its arguments. Names are compared exactly, never as LIKE patterns: a folder may be called 50%_off.
// The Go side of the same rule is excludedDirs.covers.
func atOrUnder(col, dir string) (string, []any) {
	return `(? = '' OR ` + col + ` = ? OR substr(` + col + `, 1, length(?) + 1) = ? || '/')`,
		[]any{dir, dir, dir, dir}
}

// FilesUnder counts the present regular files under dir in a root (dir "" is the whole root), and their size.
func (s *Store) FilesUnder(ctx context.Context, rootID int64, dir string) (files, bytes int64, err error) {
	under, args := atOrUnder("p.path", dir)
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(f.size), 0) FROM paths p
		JOIN files f ON f.id = p.file_id
		WHERE p.root_id = ? AND p.valid_to_scan IS NULL AND f.kind = 'file' AND `+under,
		append([]any{rootID}, args...)...).Scan(&files, &bytes)
	if err != nil {
		return 0, 0, fmt.Errorf("count files under %s: %w", dir, err)
	}
	return files, bytes, nil
}

// ExcludedDir is a directory whose files never count as duplicates, with what is under it now.
type ExcludedDir struct {
	RootID       int64
	RootPath     string
	Path         string // relative to the root, '/'-separated; "" for the whole root
	Files, Bytes int64
}

// ExcludedDirs returns the excluded directories, ordered by root and path, with the files under each. Exclusions
// are few (mainly backups), so each is counted on its own.
func (s *Store) ExcludedDirs(ctx context.Context) ([]ExcludedDir, error) {
	dirs, err := queryAll(ctx, s.db, func(rows *sql.Rows) (ExcludedDir, error) {
		var d ExcludedDir
		err := rows.Scan(&d.RootID, &d.RootPath, &d.Path)
		return d, err
	}, `SELECT x.root_id, r.path, x.path FROM excluded_dirs x JOIN roots r ON r.id = x.root_id ORDER BY r.path, x.path`)
	if err != nil {
		return nil, fmt.Errorf("read excluded directories: %w", err)
	}
	for i := range dirs {
		if dirs[i].Files, dirs[i].Bytes, err = s.FilesUnder(ctx, dirs[i].RootID, dirs[i].Path); err != nil {
			return nil, err
		}
	}
	return dirs, nil
}

// ExclusionsUnder returns the excluded directories strictly under dir in a root, relative to dir.
func (s *Store) ExclusionsUnder(ctx context.Context, rootID int64, dir string) ([]string, error) {
	under, args := atOrUnder("path", dir)
	paths, err := queryAll(ctx, s.db, func(rows *sql.Rows) (string, error) {
		var p string
		err := rows.Scan(&p)
		return p, err
	}, `SELECT path FROM excluded_dirs WHERE root_id = ? AND path != ? AND `+under+` ORDER BY path`,
		append([]any{rootID, dir}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("read excluded directories: %w", err)
	}
	for i, p := range paths {
		if dir != "" {
			paths[i] = p[len(dir)+1:]
		}
	}
	return paths, nil
}

// Exclusion returns the excluded directory that covers path in a root: path itself or the nearest directory above
// it, and whether there is one.
func (s *Store) Exclusion(ctx context.Context, rootID int64, path string) (string, bool, error) {
	x, err := exclusions(ctx, s.db)
	if err != nil {
		return "", false, err
	}
	best, found := "", false
	for _, dir := range x[rootID] {
		if (excludedDirs{rootID: {dir}}).covers(rootID, path) && (!found || len(dir) > len(best)) {
			best, found = dir, true
		}
	}
	return best, found, nil
}

// Exclude stores dir in a root as excluded, replacing the exclusions under it, in one transaction. It returns those
// replaced, relative to dir. The caller checks first that dir is not covered already.
func (s *Store) Exclude(ctx context.Context, rootID int64, dir string, now time.Time) ([]string, error) {
	replaced, err := s.ExclusionsUnder(ctx, rootID, dir)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	under, args := atOrUnder("path", dir)
	if _, err := tx.ExecContext(ctx, `DELETE FROM excluded_dirs WHERE root_id = ? AND `+under,
		append([]any{rootID}, args...)...); err != nil {
		return nil, fmt.Errorf("exclude %s: %w", dir, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (?, ?, ?)`,
		rootID, dir, utc(now)); err != nil {
		return nil, fmt.Errorf("exclude %s: %w", dir, err)
	}
	return replaced, s.commit(tx)
}

// Include removes dir's own exclusion in a root. It reports false if dir was not excluded itself.
func (s *Store) Include(ctx context.Context, rootID int64, dir string) (bool, error) {
	res, err := s.exec(ctx, `DELETE FROM excluded_dirs WHERE root_id = ? AND path = ?`, rootID, dir)
	if err != nil {
		return false, fmt.Errorf("include %s: %w", dir, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Locate finds the root a path is in by the path alone, for a path that is no longer on disk (an excluded folder
// since deleted, a record whose folder went): the deepest root that is the path or holds it. It returns the root
// and the path relative to it, '/'-separated.
func (s *Store) Locate(ctx context.Context, p string) (Root, string, error) {
	p = filepath.Clean(p)
	roots, err := s.Roots(ctx)
	if err != nil {
		return Root{}, "", err
	}
	var best Root
	for _, r := range roots {
		if (p == r.Path || isWithin(p, r.Path)) && len(r.Path) > len(best.Path) {
			best = r
		}
	}
	if best.Path == "" {
		return Root{}, "", fmt.Errorf("%w: %s", ErrNoRoot, p)
	}
	rel, err := filepath.Rel(best.Path, p)
	if err != nil {
		return Root{}, "", err
	}
	if rel == "." {
		rel = ""
	}
	return best, filepath.ToSlash(rel), nil
}

// RecordSetCopies is one set of records with its copies.
type RecordSetCopies struct {
	ID     int64
	Hash   string
	Size   int64
	Copies []DupCopy // ordered by path
}

// AlwaysAndRecords returns what is kept out of the duplicate groups on purpose, counted as the report's "not
// counted" line counts it (groupsAndNotCounted): contents always with two or more copies, and contents whose copies,
// two or more, are all records of one set. A content always is listed as always only, though some of its copies are
// records; a set with a new copy is a duplicate group again (F3), so not listed. Copies under an excluded directory
// are left out.
func (s *Store) AlwaysAndRecords(ctx context.Context) ([]DupContent, []RecordSetCopies, error) {
	contents, err := loadContents(ctx, s.db, hashesShared)
	if err != nil {
		return nil, nil, err
	}
	var always, sets []DupContent
	for _, c := range contents {
		switch {
		case c.IsGroup(), len(c.Copies) < 2:
		case c.Always:
			always = append(always, c)
		case c.OneRecordSet():
			sets = append(sets, c)
		}
	}
	sortContents(always)
	sortContents(sets)
	records := make([]RecordSetCopies, len(sets))
	for i, c := range sets {
		records[i] = RecordSetCopies{ID: c.Copies[0].RecordSet, Hash: c.Hash, Size: c.Size, Copies: c.Copies}
	}
	return always, records, nil
}

// GroupCount counts the duplicate groups of the whole register, as the report does.
func (s *Store) GroupCount(ctx context.Context) (int, error) {
	groups, _, err := s.groupsAndNotCounted(ctx)
	return len(groups), err
}

// PresentFile is a present file at a path, with what keeps it out of the duplicate groups.
type PresentFile struct {
	FileID    int64
	Hash      string
	Size      int64
	RecordSet int64 // the set of records its copy is one of (any of the copy's paths, as the list has it); 0 for none
	Always    bool  // its content is always
	Copies    int64 // its content's copies, as the list counts them (hard links one copy, excluded ones left out)
	SetSize   int64 // the copies of its set of records, counted the same way (its own included)
}

// ErrNoFile means no present file is registered at a path.
var ErrNoFile = errors.New("not a file in the register, nor an excluded directory: check the path, or run filo scan")

// FileAt returns the present regular file at path in a root, with its copy as the list sees it: the content's
// copies merged as loadContents merges them (hard links one copy), so a hard link of a record is a record too. A file
// under an excluded directory is not one of the copies; its own row says what it is.
func (s *Store) FileAt(ctx context.Context, rootID int64, path string) (PresentFile, error) {
	var f PresentFile
	err := s.db.QueryRowContext(ctx, `
		SELECT f.id, coalesce(f.hash, ''), f.size, coalesce(dr.set_id, 0), ah.hash IS NOT NULL
		FROM paths p JOIN files f ON f.id = p.file_id
		LEFT JOIN duplicate_records dr ON dr.file_id = f.id AND dr.hash = f.hash
		LEFT JOIN always_hashes ah ON ah.hash = f.hash
		WHERE p.root_id = ? AND p.path = ? AND p.valid_to_scan IS NULL AND f.kind = 'file'
		  AND f.deleted_utc IS NULL`, rootID, path).
		Scan(&f.FileID, &f.Hash, &f.Size, &f.RecordSet, &f.Always)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return f, ErrNoFile
	case err != nil:
		return f, fmt.Errorf("read %s: %w", path, err)
	}
	if f.Hash == "" || f.Size == 0 {
		return f, nil
	}
	contents, err := loadContents(ctx, s.db, `SELECT ?`, f.Hash)
	if err != nil || len(contents) == 0 {
		return f, err
	}
	c := contents[0]
	f.Copies = int64(len(c.Copies))
	var abs string
	if err := s.db.QueryRowContext(ctx, `SELECT path FROM roots WHERE id = ?`, rootID).Scan(&abs); err != nil {
		return f, fmt.Errorf("read root %d: %w", rootID, err)
	}
	abs = absPath(abs, path)
	counted := false // the file is one of the copies (not under an excluded directory)
	for _, cp := range c.Copies {
		if cp.AbsPath() == abs || slices.Contains(cp.Links, abs) {
			f.RecordSet, counted = cp.RecordSet, true
			break
		}
	}
	if f.RecordSet != 0 {
		for _, cp := range c.Copies {
			if cp.RecordSet == f.RecordSet {
				f.SetSize++
			}
		}
		if !counted {
			f.SetSize++
		}
	}
	return f, nil
}

// RemoveAlways makes a content count as a duplicate again.
func (s *Store) RemoveAlways(ctx context.Context, hash string) error {
	if _, err := s.exec(ctx, `DELETE FROM always_hashes WHERE hash = ?`, hash); err != nil {
		return fmt.Errorf("remove a content always: %w", err)
	}
	return nil
}

// RemoveRecordSet removes a whole set of records: its copies count as duplicates again.
func (s *Store) RemoveRecordSet(ctx context.Context, set int64) error {
	if _, err := s.exec(ctx, `DELETE FROM duplicate_records WHERE set_id = ?`, set); err != nil {
		return fmt.Errorf("remove a set of records: %w", err)
	}
	return nil
}
