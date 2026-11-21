package store

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/rules"
)

// DirShown is what the register holds under a directory of a root, for filo show DIR (docs/design.md, Showing and
// setting a file's details): every file at it and below, present or missing, and the rules that decide for it.
type DirShown struct {
	RootID      int64
	RootPath    string
	Dir         string     // relative to the root, '/'-separated; "" for the root itself
	Files       []DirEntry // present and missing files at Dir and below, ordered by path
	Deleted     []string   // the last paths, relative to the root, of the files filo deleted at Dir and below
	Rules       []Rule     // the rules tried for a file directly in Dir, in the order they are tried
	RulesBelow  []Rule     // the rules set on directories below Dir, a parent before the directories inside it
	Ties        []RuleTie  // the present files at Dir and below that two or more rules match equally, by path
	LastScanUTC string     // when the latest finished scan covering Dir finished; "" = none
}

// DirEntry is one file under a directory: present, with its status, or missing, with its last path.
type DirEntry struct {
	FileItem          // Status set only while Present
	Present  bool     // it has a current path; otherwise Path is its last one
	GoneUTC  string   // missing: when the scan that closed that path ran
	Dup      DupState // where it stands among copies of its content
}

// DupState is a present file's place among the copies of its content.
type DupState int

const (
	DupNone      DupState = iota // no other copy (hard links are one copy), or not counted (under an excluded directory)
	DupCopies                    // it has copies: a duplicate group
	DupRecord                    // it is one of a set of separate records
	DupOnPurpose                 // its content is always: copies on purpose
)

// IsAtOrUnder reports whether a '/'-separated path inside a root is dir or below it ("" is the root itself).
func IsAtOrUnder(rel, dir string) bool { return isAtOrUnder(rel, dir) }

// HasFilesUnder reports whether the register has a file below dir in a root, as ShowDir finds them: at its current
// path, or, for one with none (missing, deleted by filo), at its last path. Whether dir is, or was, a directory filo
// knows with something to show; a file since moved out of it does not count.
func (s *Store) HasFilesUnder(ctx context.Context, rootID int64, dir string) (bool, error) {
	cond, args := Filter{RootID: rootID, Scope: dir}.scopeSQL()
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.path <> ?`+cond+` AND (p.valid_to_scan IS NULL OR (
		  NOT EXISTS (SELECT 1 FROM paths cp WHERE cp.file_id = f.id AND cp.valid_to_scan IS NULL)
		  AND p.id = (SELECT lp.id FROM paths lp WHERE lp.file_id = f.id
		              ORDER BY lp.valid_to_scan DESC, lp.id DESC LIMIT 1)))
		LIMIT 1)`, append([]any{dir}, args...)...).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", dir, err)
	}
	return n > 0, nil
}

// ShowDir returns what the register holds at dir and below it in a root. today (YYYY-MM-DD) decides the statuses;
// now is for working out the rule ties, which writes nothing.
func (s *Store) ShowDir(ctx context.Context, rootID int64, dir, today string, now time.Time) (DirShown, error) {
	d := DirShown{RootID: rootID, Dir: dir}
	if err := s.db.QueryRowContext(ctx, `SELECT path FROM roots WHERE id = ?`, rootID).Scan(&d.RootPath); err != nil {
		return d, fmt.Errorf("read root %d: %w", rootID, err)
	}
	f := Filter{RootID: rootID, Scope: dir}
	present, err := s.Files(ctx, today, f)
	if err != nil {
		return d, err
	}
	at := map[string]bool{} // the current paths
	for _, it := range present {
		if it.Path == dir {
			continue // a file at the directory's own path: not under it
		}
		at[it.Path] = true
		d.Files = append(d.Files, DirEntry{FileItem: it, Present: true})
	}

	// Files with no current path, under their last path: missing, or deleted by filo.
	cond, args := f.scopeSQL()
	type gone struct {
		e       DirEntry
		deleted bool
	}
	gones, err := queryAll(ctx, s.db, func(rows *sql.Rows) (gone, error) {
		var g gone
		it := &g.e.FileItem
		err := rows.Scan(&it.FileID, &it.Path, &it.Kind, &it.Size, &it.MtimeNS, &it.Hash, &it.FileDate, &it.Retention,
			&it.Policy, &it.PolicyValue, &it.KeepUntil, &g.e.GoneUTC, &g.deleted)
		it.RootPath = d.RootPath
		return g, err
	}, `
		SELECT f.id, p.path, f.kind, f.size, f.mtime_ns, coalesce(f.hash, ''), coalesce(f.file_date, ''),
		       `+retentionColumns+`, coalesce(f.keep_until, ''), coalesce(`+scanTime("st")+`, ''),
		       f.deleted_utc IS NOT NULL
		FROM paths p
		JOIN files f ON f.id = p.file_id`+policyJoin+`
		LEFT JOIN scans st ON st.id = p.valid_to_scan
		WHERE p.valid_to_scan IS NOT NULL AND p.path <> ?`+cond+`
		  AND NOT EXISTS (SELECT 1 FROM paths cp WHERE cp.file_id = f.id AND cp.valid_to_scan IS NULL)
		  AND p.id = (SELECT lp.id FROM paths lp WHERE lp.file_id = f.id
		              ORDER BY lp.valid_to_scan DESC, lp.id DESC LIMIT 1)`, append([]any{dir}, args...)...)
	if err != nil {
		return d, fmt.Errorf("read the files gone from %s: %w", dir, err)
	}
	for _, g := range gones {
		switch {
		case g.deleted:
			d.Deleted = append(d.Deleted, g.e.Path)
		case !at[g.e.Path]: // a path another file has taken since shows that file only
			d.Files = append(d.Files, g.e)
		}
	}
	slices.SortFunc(d.Files, func(a, b DirEntry) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(d.Deleted)

	if err := s.dupStates(ctx, f, d.Files); err != nil {
		return d, err
	}
	if d.Rules, d.RulesBelow, err = s.dirRules(ctx, rootID, dir); err != nil {
		return d, err
	}
	ties, err := s.RuleTies(ctx, rootID, today, now)
	if err != nil {
		return d, err
	}
	for _, t := range ties {
		if t.Path != dir && isAtOrUnder(t.Path, dir) {
			d.Ties = append(d.Ties, t)
		}
	}
	d.LastScanUTC, err = s.LastScanCovering(ctx, rootID, dir)
	return d, err
}

// dupStates sets the duplicate state of the present files among entries, from their contents as the duplicate
// groups see them (hard links one copy, copies under an excluded directory left out).
func (s *Store) dupStates(ctx context.Context, f Filter, entries []DirEntry) error {
	cond, args := f.scopeSQL()
	contents, err := loadContents(ctx, s.db, `SELECT f.hash FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.hash IS NOT NULL`+cond+` AND f.hash IN (`+hashesShared+`)`, args...)
	if err != nil {
		return err
	}
	state := map[string]DupState{} // by absolute path
	for _, c := range contents {
		if len(c.Copies) < 2 {
			continue
		}
		for _, cp := range c.Copies {
			st := DupCopies
			switch {
			case c.Always:
				st = DupOnPurpose
			case c.OneRecordSet():
				st = DupRecord
			}
			state[cp.AbsPath()] = st
			for _, l := range cp.Links {
				state[l] = st
			}
		}
	}
	for i := range entries {
		if entries[i].Present {
			entries[i].Dup = state[entries[i].AbsPath()]
		}
	}
	return nil
}

// dirRules returns the rules tried for a file directly in dir, in the order they are tried (its own directory's
// first, then each parent's up to the root; within one directory the longest pattern first), and the rules set on
// directories below it.
func (s *Store) dirRules(ctx context.Context, rootID int64, dir string) (tried, below []Rule, err error) {
	all, err := readRules(ctx, s.db, rootID, "")
	if err != nil {
		return nil, nil, err
	}
	for _, r := range all {
		switch {
		case r.Dir == dir || r.Dir == "" || strings.HasPrefix(dir, r.Dir+"/"):
			tried = append(tried, r)
		case isAtOrUnder(r.Dir, dir):
			below = append(below, r)
		}
	}
	depth := func(d string) int {
		if d == "" {
			return 0
		}
		return strings.Count(d, "/") + 1
	}
	slices.SortStableFunc(tried, func(a, b Rule) int {
		if c := cmp.Compare(depth(b.Dir), depth(a.Dir)); c != 0 {
			return c
		}
		return rules.Compare(a.Matcher(), b.Matcher())
	})
	return tried, below, nil
}
