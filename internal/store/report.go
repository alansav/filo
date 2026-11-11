package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
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
			WHERE root_id = ? AND finished_utc IS NOT NULL`, r.ID).Scan(&id)
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
	Groups      int64 // number of groups
	Copies      int64 // files in all groups
	Reclaimable int64 // across all groups
	Top         []DupGroup
}

// Duplicates finds present regular files (size > 0) that share a hash, across all roots, and returns the n groups
// with the most reclaimable space.
func (s *Store) Duplicates(ctx context.Context, n int) (Duplicates, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.hash, max(f.size), count(*)
		FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
		WHERE f.kind = 'file' AND f.size > 0 AND f.hash IS NOT NULL
		GROUP BY f.hash HAVING count(*) > 1`)
	if err != nil {
		return Duplicates{}, fmt.Errorf("duplicates: %w", err)
	}
	var d Duplicates
	var groups []DupGroup
	for rows.Next() {
		var g DupGroup
		var copies int64
		if err := rows.Scan(&g.Hash, &g.Size, &copies); err != nil {
			rows.Close()
			return Duplicates{}, fmt.Errorf("duplicates: %w", err)
		}
		g.Reclaimable = g.Size * (copies - 1)
		d.Groups++
		d.Copies += copies
		d.Reclaimable += g.Reclaimable
		groups = append(groups, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Duplicates{}, fmt.Errorf("duplicates: %w", err)
	}

	slices.SortFunc(groups, func(a, b DupGroup) int {
		if a.Reclaimable != b.Reclaimable {
			if a.Reclaimable > b.Reclaimable {
				return -1
			}
			return 1
		}
		if a.Hash < b.Hash {
			return -1
		}
		return 1
	})
	if len(groups) > n {
		groups = groups[:n]
	}
	for i := range groups {
		prow, err := s.db.QueryContext(ctx, `
			SELECT r.path, p.path FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
			JOIN roots r ON r.id = p.root_id
			WHERE f.hash = ? AND f.kind = 'file' AND f.size > 0`, groups[i].Hash)
		if err != nil {
			return Duplicates{}, fmt.Errorf("duplicate paths: %w", err)
		}
		for prow.Next() {
			var root, rel string
			if err := prow.Scan(&root, &rel); err != nil {
				prow.Close()
				return Duplicates{}, fmt.Errorf("duplicate paths: %w", err)
			}
			groups[i].Paths = append(groups[i].Paths, absPath(root, rel))
		}
		err = prow.Err()
		prow.Close()
		if err != nil {
			return Duplicates{}, fmt.Errorf("duplicate paths: %w", err)
		}
		slices.Sort(groups[i].Paths)
	}
	d.Top = groups
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

// RecentEvents returns the n most recent moved, missing, changed, possible_move and error events, newest first.
func (s *Store) RecentEvents(ctx context.Context, n int) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.kind, coalesce(e.file_id, 0), coalesce(e.path, ''), coalesce(e.other_path, ''), coalesce(e.detail, ''),
		       sc.id, r.path, coalesce(sc.finished_utc, '')
		FROM events e JOIN scans sc ON sc.id = e.scan_id JOIN roots r ON r.id = sc.root_id
		WHERE e.kind IN ('moved', 'missing', 'changed', 'possible_move', 'error')
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
