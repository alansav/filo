package store

import (
	"context"
	"database/sql"
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
	Root  string // the root it is in, absolute
	Rel   string // the path relative to the root, '/' separators
	Size  int64
	Links int // other paths of the same file (hard links in the same root), not listed as files of their own
}

// AbsPath is the file's absolute path.
func (f FileRow) AbsPath() string { return absPath(f.Root, f.Rel) }

// Largest returns the n largest present regular files. Hard links (the same inode in the same root) are one file,
// listed by its first path with the number of other links, as the duplicates count them. The query takes the largest
// paths in order (cheap with LIMIT); links are merged here, fetching more rows if merging left fewer than n.
func (s *Store) Largest(ctx context.Context, n int) ([]FileRow, error) {
	type row struct {
		root, rel string
		rootID    int64
		size      int64
		ino       int64 // 0 = not known: never merged
	}
	type key struct{ root, ino int64 }
	for limit := n * 2; ; limit *= 2 {
		rows, err := queryAll(ctx, s.db, func(rs *sql.Rows) (row, error) {
			var r row
			return r, rs.Scan(&r.root, &r.rel, &r.rootID, &r.size, &r.ino)
		}, `
			SELECT r.path, p.path, p.root_id, f.size, coalesce(f.inode, 0)
			FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
			WHERE p.valid_to_scan IS NULL AND f.kind = 'file'
			ORDER BY f.size DESC, r.path, p.path LIMIT ?`, limit)
		if err != nil {
			return nil, fmt.Errorf("largest files: %w", err)
		}
		var out []FileRow
		first := map[key]int{} // the row a file's links join
		for _, r := range rows {
			if r.ino != 0 {
				k := key{r.rootID, r.ino}
				if i, ok := first[k]; ok {
					out[i].Links++
					continue
				}
				first[k] = len(out)
			}
			out = append(out, FileRow{Root: r.root, Rel: r.rel, Size: r.size})
		}
		if len(out) >= n || len(rows) < limit {
			// Links of the last files shown may lie beyond this batch; counting them exactly would need the whole
			// register, so a link count is a lower bound only at the very edge of a large batch.
			if len(out) > n {
				out = out[:n]
			}
			return out, nil
		}
	}
}

// DupGroup is a set of present regular files with the same content.
type DupGroup struct {
	Hash        string
	Size        int64       // of each copy
	Copies      []GroupCopy // ordered by absolute path
	Reclaimable int64       // Size × (copies − 1)
}

// GroupCopy is one copy in a report's duplicate group: its root (absolute), its path relative to it ('/'
// separators), and the other paths of the same file (hard links: one copy, freeing nothing if deleted), relative to
// the same root.
type GroupCopy struct {
	Root, Rel string
	Links     []string
}

// AbsPath is the copy's absolute path.
func (c GroupCopy) AbsPath() string { return absPath(c.Root, c.Rel) }

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
		for _, cp := range r.c.Copies { // already ordered by absolute path
			g.Copies = append(g.Copies, GroupCopy{Root: cp.RootPath, Rel: cp.Path, Links: cp.LinkRels()})
		}
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
	Root   string // the root it is in
	Rel    string // relative to the root
	Target string
}

// AbsPath is the symlink's absolute path.
func (l LinkRow) AbsPath() string { return absPath(l.Root, l.Rel) }

// BrokenLinks returns up to n present broken symlinks and how many there are in total.
func (s *Store) BrokenLinks(ctx context.Context, n int) ([]LinkRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' AND f.link_broken`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("broken links: %w", err)
	}
	out, err := queryAll(ctx, s.db, func(rows *sql.Rows) (LinkRow, error) {
		var l LinkRow
		return l, rows.Scan(&l.Root, &l.Rel, &l.Target)
	}, `
		SELECT r.path, p.path, coalesce(f.link_target, '')
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' AND f.link_broken
		ORDER BY r.path, p.path LIMIT ?`, n)
	if err != nil {
		return nil, 0, fmt.Errorf("broken links: %w", err)
	}
	return out, total, nil
}

// UnreadableRow is a present file flagged unreadable.
type UnreadableRow struct {
	Root  string // the root it is in
	Rel   string // relative to the root
	Since int64  // the first scan in a row that could not read it
}

// AbsPath is the file's absolute path.
func (u UnreadableRow) AbsPath() string { return absPath(u.Root, u.Rel) }

// UnreadableFiles returns up to n present files flagged unreadable, longest-flagged first, and how many there are in
// total.
func (s *Store) UnreadableFiles(ctx context.Context, n int) ([]UnreadableRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.unreadable_since_scan IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("unreadable files: %w", err)
	}
	out, err := queryAll(ctx, s.db, func(rows *sql.Rows) (UnreadableRow, error) {
		var u UnreadableRow
		return u, rows.Scan(&u.Root, &u.Rel, &u.Since)
	}, `
		SELECT r.path, p.path, f.unreadable_since_scan
		FROM paths p JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.unreadable_since_scan IS NOT NULL
		ORDER BY f.unreadable_since_scan, r.path, p.path LIMIT ?`, n)
	if err != nil {
		return nil, 0, fmt.Errorf("unreadable files: %w", err)
	}
	return out, total, nil
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
	out, err := queryAll(ctx, s.db, func(rows *sql.Rows) (EventRow, error) {
		var e EventRow
		err := rows.Scan(&e.Kind, &e.FileID, &e.Path, &e.OtherPath, &e.Detail, &e.ScanID, &e.RootPath, &e.FinishedUTC)
		return e, err
	}, `
		SELECT e.kind, coalesce(e.file_id, 0), coalesce(e.path, ''), coalesce(e.other_path, ''), coalesce(e.detail, ''),
		       sc.id, r.path, coalesce(sc.finished_utc, '')
		FROM events e JOIN scans sc ON sc.id = e.scan_id JOIN roots r ON r.id = sc.root_id
		WHERE e.kind IN ('moved', 'missing', 'changed', 'possible_move', 'error', 'deleted', 'same_as_deleted')
		ORDER BY e.id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("recent events: %w", err)
	}
	return out, nil
}
