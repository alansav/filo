package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Export kinds (exports.kind). Schema 7 also wrote 'separate', the first design's export of the contents marked
// separate; such exports stay in the register's history, but are not written any more.
const (
	ExportRetention  = "retention"
	ExportDuplicates = "duplicates"
)

// DupCopy is one copy in a duplicate group: a present regular file, with the other paths of the same file (hard
// links: the same inode in the same root) if it has any. Hard links are one copy, since deleting one frees nothing.
type DupCopy struct {
	Candidate           // the copy's first path; ReviewedHash is the hash its retention was set on
	Links      []string // the other paths of the same file, absolute
	Unreadable bool     // flagged unreadable: its hash is stale
	RecordSet  int64    // the set of records it is one of, on its current content; 0 for none
}

// DupContent is one content (hash) and its copies: present regular files of a size above zero, not deleted, not
// under an excluded directory.
type DupContent struct {
	Hash   string
	Size   int64
	Always bool      // the content belongs everywhere: never a duplicate group
	Copies []DupCopy // ordered by absolute path
}

// OneRecordSet reports whether every copy is a record of one and the same set: copies kept on purpose, not a
// duplicate group. A new copy of the content makes it a group again (scenario F3).
func (d DupContent) OneRecordSet() bool {
	if len(d.Copies) == 0 || d.Copies[0].RecordSet == 0 {
		return false
	}
	for _, c := range d.Copies[1:] {
		if c.RecordSet != d.Copies[0].RecordSet {
			return false
		}
	}
	return true
}

// IsGroup reports whether the content is a duplicate group (docs/design.md, Duplicates: "A group"): two or more
// copies, the content not always, and not all of them one set of records.
func (d DupContent) IsGroup() bool { return len(d.Copies) > 1 && !d.Always && !d.OneRecordSet() }

// hashesShared selects the contents held by more than one present path; hard links and excluded directories are
// told apart afterwards.
const hashesShared = `SELECT f2.hash FROM files f2 JOIN paths p2 ON p2.file_id = f2.id AND p2.valid_to_scan IS NULL
	WHERE f2.kind = 'file' AND f2.size > 0 AND f2.hash IS NOT NULL GROUP BY f2.hash HAVING count(*) > 1`

// loadContents reads the present copies of the contents the hash query selects. A path under an excluded directory
// is left out, so a file counts only through its other paths, if it has any.
func loadContents(ctx context.Context, q querier, hashes string, args ...any) ([]DupContent, error) {
	excluded, err := exclusions(ctx, q)
	if err != nil {
		return nil, err
	}
	type row struct {
		c      DupCopy
		always bool
	}
	rows, err := queryAll(ctx, q, func(rows *sql.Rows) (row, error) {
		var r row
		c := &r.c
		var ino int64
		err := rows.Scan(&c.FileID, &c.RootID, &c.RootPath, &c.PathID, &c.Path, &c.Size, &c.MtimeNS, &c.CtimeNS,
			&c.Hash, &ino, &c.FileDate, &c.Retention, &c.KeepUntil, &c.ReviewedUTC, &c.ReviewedHash, &c.Unreadable,
			&c.RecordSet, &r.always)
		c.Kind = "file"
		c.Ino = uint64(ino)
		return r, err
	}, `
		SELECT f.id, p.root_id, r.path, p.id, p.path, f.size, f.mtime_ns, f.ctime_ns, f.hash, coalesce(f.inode, 0),
		       coalesce(f.file_date, ''), coalesce(f.retention, ''), coalesce(f.keep_until, ''),
		       coalesce(f.reviewed_utc, ''), coalesce(f.reviewed_hash, ''), f.unreadable_since_scan IS NOT NULL,
		       coalesce(dr.set_id, 0), ah.hash IS NOT NULL
		FROM paths p
		JOIN files f ON f.id = p.file_id
		JOIN roots r ON r.id = p.root_id
		LEFT JOIN duplicate_records dr ON dr.file_id = f.id AND dr.hash = f.hash
		LEFT JOIN always_hashes ah ON ah.hash = f.hash
		WHERE p.valid_to_scan IS NULL AND f.kind = 'file' AND f.size > 0 AND f.hash IS NOT NULL
		  AND f.deleted_utc IS NULL AND f.hash IN (`+hashes+`)
		ORDER BY f.hash, r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("read duplicates: %w", err)
	}
	// Rows come by content; hard links (the same inode in the same root) join their copy's first path. Each path may
	// be a file row of its own, so the copy is a record if any of its paths is.
	var out []DupContent
	type key struct {
		root int64
		ino  uint64
	}
	var byIno map[key]int // index into the current content's copies
	for _, r := range rows {
		c := r.c
		if len(out) == 0 || out[len(out)-1].Hash != c.Hash {
			out = append(out, DupContent{Hash: c.Hash, Size: c.Size, Always: r.always})
			byIno = map[key]int{}
		}
		if excluded.covers(c.RootID, c.Path) {
			continue
		}
		cur := &out[len(out)-1]
		if c.Ino != 0 {
			k := key{c.RootID, c.Ino}
			if i, ok := byIno[k]; ok {
				cur.Copies[i].Links = append(cur.Copies[i].Links, c.AbsPath())
				if cur.Copies[i].RecordSet == 0 {
					cur.Copies[i].RecordSet = c.RecordSet
				}
				continue
			}
			byIno[k] = len(cur.Copies)
		}
		cur.Copies = append(cur.Copies, c)
	}
	for i := range out {
		slices.SortFunc(out[i].Copies, func(a, b DupCopy) int { return strings.Compare(a.AbsPath(), b.AbsPath()) })
	}
	return out, nil
}

// excludedDirs is the excluded directories by root: paths relative to it, '/'-separated, "" for the whole root.
type excludedDirs map[int64][]string

func exclusions(ctx context.Context, q querier) (excludedDirs, error) {
	type row struct {
		root int64
		path string
	}
	rows, err := queryAll(ctx, q, func(rows *sql.Rows) (row, error) {
		var r row
		err := rows.Scan(&r.root, &r.path)
		return r, err
	}, `SELECT root_id, path FROM excluded_dirs`)
	if err != nil {
		return nil, fmt.Errorf("read excluded directories: %w", err)
	}
	out := excludedDirs{}
	for _, r := range rows {
		out[r.root] = append(out[r.root], r.path)
	}
	return out, nil
}

// covers reports whether the path rel in root rootID is under an excluded directory: the directory itself or
// anything in its subtree (backup/a, not backups/a).
func (x excludedDirs) covers(rootID int64, rel string) bool {
	for _, dir := range x[rootID] {
		if dir == "" || rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// contains reports whether the filter covers the path rel in root rootID.
func (f Filter) contains(rootID int64, rel string) bool {
	if f.RootID == 0 {
		return true
	}
	if rootID != f.RootID {
		return false
	}
	return f.Scope == "" || rel == f.Scope || strings.HasPrefix(rel, f.Scope+"/")
}

// touches reports whether any path of the content's copies is under the filter.
func (d DupContent) touches(f Filter) bool {
	for _, c := range d.Copies {
		if f.contains(c.RootID, c.Path) {
			return true
		}
		for _, rel := range c.LinkRels() {
			if f.contains(c.RootID, rel) {
				return true
			}
		}
	}
	return false
}

// LinkRels returns the copy's other paths (hard links, in the same root) relative to the root, '/' separators.
func (c DupCopy) LinkRels() []string {
	var out []string
	for _, l := range c.Links {
		if rel, err := filepath.Rel(c.RootPath, l); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// DuplicateGroups returns the duplicate groups with a copy under the filter (see IsGroup), each with all of its
// copies, also those outside the filter. They are ordered by their first copy's path.
func (s *Store) DuplicateGroups(ctx context.Context, f Filter) ([]DupContent, error) {
	all, err := loadContents(ctx, s.db, hashesShared)
	if err != nil {
		return nil, err
	}
	var out []DupContent
	for _, d := range all {
		if d.IsGroup() && d.touches(f) {
			out = append(out, d)
		}
	}
	sortContents(out)
	return out, nil
}

func sortContents(cs []DupContent) {
	slices.SortFunc(cs, func(a, b DupContent) int {
		return strings.Compare(a.Copies[0].AbsPath(), b.Copies[0].AbsPath())
	})
}

// RecordDuplicatesExport stores a duplicates export of groups, numbered from 1 in the order given, and calls write
// with its ID. Each copy's row keeps its hash, retention, group and path. As with RecordExport, the export is
// committed only if write succeeds.
func (s *Store) RecordDuplicatesExport(ctx context.Context, path string, now time.Time, groups []DupContent,
	write func(exportID int64) error) (int64, error) {
	return s.recordExport(ctx, path, now, ExportDuplicates, func(tx *sql.Tx, id int64) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO export_rows (export_id, file_id, hash, file_date, retention,
			keep_until, dup_group, path_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, g := range groups {
			for _, c := range g.Copies {
				if _, err := stmt.ExecContext(ctx, id, c.FileID, c.Hash, nullString(c.FileDate),
					nullString(c.Retention), nullString(c.KeepUntil), i+1, c.PathID); err != nil {
					return err
				}
			}
		}
		return nil
	}, write)
}

// ExportedGroups returns, for each of the given exports that is a duplicates export, each file's group as exported:
// what a duplicates CSV's rows are checked against. An export that is not one (a retention export, the first design's
// separate export, or no export of this register) is left out.
func (s *Store) ExportedGroups(ctx context.Context, exportIDs []int64) (map[int64]map[int64]int64, error) {
	out := map[int64]map[int64]int64{}
	for _, id := range exportIDs {
		rows, err := queryAll(ctx, s.db, func(rows *sql.Rows) ([2]int64, error) {
			var r [2]int64
			err := rows.Scan(&r[0], &r[1])
			return r, err
		}, `SELECT er.file_id, er.dup_group FROM export_rows er JOIN exports e ON e.id = er.export_id
			WHERE er.export_id = ? AND e.kind = ? AND er.dup_group IS NOT NULL`, id, ExportDuplicates)
		if err != nil {
			return nil, fmt.Errorf("read export %d: %w", id, err)
		}
		if len(rows) == 0 {
			continue
		}
		out[id] = map[int64]int64{}
		for _, r := range rows {
			out[id][r[0]] = r[1]
		}
	}
	return out, nil
}

// LastsAtLeast reports whether a copy kept until kept (YYYY-MM-DD, Forever or "" for no retention) lasts at least as
// long as one kept until other, so other can go without a decision being lost. No retention lasts no time.
func LastsAtLeast(kept, other string) bool {
	switch {
	case other == "" || kept == Forever:
		return true
	case other == Forever || kept == "":
		return false
	}
	return kept >= other
}

// NotCounted is what is kept out of the duplicate groups on purpose (docs/design.md, Duplicates), for the report's
// "Not counted" line.
type NotCounted struct {
	Dirs       int64 // excluded directories
	Always     int64 // contents always with two or more copies (would otherwise be a group)
	RecordSets int64 // sets of records with two or more members present (would otherwise be a group)
}

// SymlinksTo returns, for each of the given absolute paths, the present symlinks in the register that point at it
// (absolute paths). A relative target counts from the link's folder.
func (s *Store) SymlinksTo(ctx context.Context, targets []string) (map[string][]string, error) {
	want := map[string]bool{}
	for _, t := range targets {
		want[t] = true
	}
	out := map[string][]string{}
	if len(want) == 0 {
		return out, nil
	}
	links, err := queryAll(ctx, s.db, func(rows *sql.Rows) ([2]string, error) {
		var root, rel, target string
		err := rows.Scan(&root, &rel, &target)
		return [2]string{absPath(root, rel), target}, err
	}, `SELECT r.path, p.path, coalesce(f.link_target, '') FROM paths p
		JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' ORDER BY r.path, p.path`)
	if err != nil {
		return nil, fmt.Errorf("read symlinks: %w", err)
	}
	for _, l := range links {
		link, target := l[0], l[1]
		if target == "" {
			continue
		}
		t := filepath.FromSlash(target)
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(link), t)
		}
		t = filepath.Clean(t)
		if want[t] {
			out[t] = append(out[t], link)
		}
	}
	return out, nil
}
