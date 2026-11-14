package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// absPath joins a root and a register path for display.
func absPath(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// RootTotal is one root's present files and its latest finished scan.
type RootTotal struct {
	Root     Root
	Files    int64 // present regular files
	Links    int64 // present symlinks
	Bytes    int64 // total size of present regular files
	LastScan *ScanInfo
}

// RootTotals returns every root with its totals, ordered by path.
func (s *Store) RootTotals(ctx context.Context) ([]RootTotal, error) {
	roots, err := s.Roots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RootTotal, 0, len(roots))
	for _, r := range roots {
		t := RootTotal{Root: r}
		if err := s.db.QueryRowContext(ctx, `
			SELECT coalesce(sum(f.kind = 'file'), 0), coalesce(sum(f.kind = 'symlink'), 0),
			       coalesce(sum(CASE WHEN f.kind = 'file' THEN f.size END), 0)
			FROM paths p JOIN files f ON f.id = p.file_id
			WHERE p.root_id = ? AND p.valid_to_scan IS NULL`, r.ID).Scan(&t.Files, &t.Links, &t.Bytes); err != nil {
			return nil, fmt.Errorf("totals for %s: %w", r.Path, err)
		}
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM scans
			WHERE root_id = ? AND kind = 'scan' AND finished_utc IS NOT NULL`, r.ID).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("last scan for %s: %w", r.Path, err)
		}
		if id != 0 {
			si, ok, err := s.Scan(ctx, id)
			if err != nil {
				return nil, err
			}
			if ok {
				t.LastScan = &si
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// FileRow is one present file for display.
type FileRow struct {
	Path string // absolute
	Size int64
}

// Largest returns the n largest present regular files.
func (s *Store) Largest(ctx context.Context, n int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.path, p.path, f.size
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'file'
		ORDER BY f.size DESC, r.path, p.path LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("largest files: %w", err)
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var root, rel string
		var fr FileRow
		if err := rows.Scan(&root, &rel, &fr.Size); err != nil {
			return nil, fmt.Errorf("largest files: %w", err)
		}
		fr.Path = absPath(root, rel)
		out = append(out, fr)
	}
	return out, rows.Err()
}

// DupGroup is a set of present regular files with the same content.
type DupGroup struct {
	Hash        string
	Size        int64    // of each copy
	Paths       []string // absolute, sorted
	Reclaimable int64    // Size × (copies − 1)
}

// Duplicates is every duplicate group's total, plus the largest groups in detail.
type Duplicates struct {
	Groups        int64 // number of groups
	Copies        int64 // copies in all groups (hard links to one file are one copy)
	Reclaimable   int64 // across all groups
	Top           []DupGroup
	Separate      int64 // contents marked separate (identical copies that are not duplicates)
	SeparateFiles int64 // their present files
	Marked        int64 // copies marked for deletion, on their current content
}

// Duplicates finds the duplicate groups across all roots (DuplicateGroups: two or more copies of a content, not
// marked separate) and returns the n groups with the most reclaimable space.
func (s *Store) Duplicates(ctx context.Context, n int) (Duplicates, error) {
	var d Duplicates
	all, err := s.DuplicateGroups(ctx, Filter{})
	if err != nil {
		return d, err
	}
	// Sort by reclaimable space and keep the top n before building any path list.
	type ranked struct {
		c           DupContent
		reclaimable int64
	}
	rs := make([]ranked, 0, len(all))
	for _, c := range all {
		r := ranked{c, c.Size * int64(len(c.Copies)-1)}
		d.Groups++
		d.Copies += int64(len(c.Copies))
		d.Reclaimable += r.reclaimable
		rs = append(rs, r)
	}
	slices.SortFunc(rs, func(a, b ranked) int {
		if a.reclaimable != b.reclaimable {
			if a.reclaimable > b.reclaimable {
				return -1
			}
			return 1
		}
		return strings.Compare(a.c.Hash, b.c.Hash)
	})
	if len(rs) > n {
		rs = rs[:n]
	}
	for _, r := range rs {
		g := DupGroup{Hash: r.c.Hash, Size: r.c.Size, Reclaimable: r.reclaimable}
		for _, cp := range r.c.Copies {
			g.Paths = append(g.Paths, cp.AbsPath())
			g.Paths = append(g.Paths, cp.Links...)
		}
		slices.Sort(g.Paths)
		d.Top = append(d.Top, g)
	}
	if d.Separate, d.SeparateFiles, err = s.SeparateTotals(ctx); err != nil {
		return d, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM duplicate_marks m JOIN files f ON f.id = m.file_id
		JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
		WHERE f.hash = m.hash AND f.deleted_utc IS NULL`).Scan(&d.Marked); err != nil {
		return d, fmt.Errorf("read marks: %w", err)
	}
	return d, nil
}

// LinkRow is one present symlink.
type LinkRow struct {
	Path   string // absolute
	Target string
}

// BrokenLinks returns up to n present broken symlinks and how many there are in total.
func (s *Store) BrokenLinks(ctx context.Context, n int) ([]LinkRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' AND f.link_broken`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("broken links: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.path, p.path, coalesce(f.link_target, '')
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' AND f.link_broken
		ORDER BY r.path, p.path LIMIT ?`, n)
	if err != nil {
		return nil, 0, fmt.Errorf("broken links: %w", err)
	}
	defer rows.Close()
	var out []LinkRow
	for rows.Next() {
		var root, rel string
		var l LinkRow
		if err := rows.Scan(&root, &rel, &l.Target); err != nil {
			return nil, 0, fmt.Errorf("broken links: %w", err)
		}
		l.Path = absPath(root, rel)
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// UnreadableRow is a present file flagged unreadable.
type UnreadableRow struct {
	Path  string // absolute
	Since int64  // the first scan in a row that could not read it
}

// UnreadableFiles returns up to n present files flagged unreadable, longest-flagged first, and how many there are in
// total.
func (s *Store) UnreadableFiles(ctx context.Context, n int) ([]UnreadableRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.unreadable_since_scan IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("unreadable files: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.path, p.path, f.unreadable_since_scan
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.unreadable_since_scan IS NOT NULL
		ORDER BY f.unreadable_since_scan, r.path, p.path LIMIT ?`, n)
	if err != nil {
		return nil, 0, fmt.Errorf("unreadable files: %w", err)
	}
	defer rows.Close()
	var out []UnreadableRow
	for rows.Next() {
		var root, rel string
		var u UnreadableRow
		if err := rows.Scan(&root, &rel, &u.Since); err != nil {
			return nil, 0, fmt.Errorf("unreadable files: %w", err)
		}
		u.Path = absPath(root, rel)
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// EventRow is a recorded event with its scan's root and time.
type EventRow struct {
	Event
	ScanID      int64
	RootPath    string
	FinishedUTC string
}

// RecentEvents returns the n most recent moved, missing, changed, possible_move, error, deleted and same_as_deleted
// events, newest first.
func (s *Store) RecentEvents(ctx context.Context, n int) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.kind, coalesce(e.file_id, 0), coalesce(e.path, ''), coalesce(e.other_path, ''), coalesce(e.detail, ''),
		       sc.id, r.path, coalesce(sc.finished_utc, '')
		FROM events e JOIN scans sc ON sc.id = e.scan_id JOIN roots r ON r.id = sc.root_id
		WHERE e.kind IN ('moved', 'missing', 'changed', 'possible_move', 'error', 'deleted', 'same_as_deleted')
		ORDER BY e.id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.Kind, &e.FileID, &e.Path, &e.OtherPath, &e.Detail, &e.ScanID, &e.RootPath,
			&e.FinishedUTC); err != nil {
			return nil, fmt.Errorf("recent events: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
