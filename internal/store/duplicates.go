package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Export kinds (exports.kind).
const (
	ExportRetention  = "retention"
	ExportDuplicates = "duplicates"
	// ExportSeparate is a duplicates CSV of the contents marked separate, to undo that decision.
	ExportSeparate = "separate"
)

// Decisions in a duplicates CSV's delete cell.
const (
	DecideNone   byte = 0
	DecideDelete byte = 'Y'
	DecideKeep   byte = 'N'
)

// Decisions in duplicate_reviews.
const (
	reviewDelete      = "delete"
	reviewKeep        = "keep"
	reviewUnmark      = "unmark"
	reviewSeparate    = "separate"
	reviewNotSeparate = "not separate"
)

// DupCopy is one copy in a duplicate group: a present regular file, with the other paths of the same file (hard
// links: the same inode in the same root) if it has any. Hard links are one copy, since deleting one frees nothing.
type DupCopy struct {
	Candidate           // the copy's first path; ReviewedHash is the hash its retention was set on
	Links      []string // the other paths of the same file, absolute
	Unreadable bool     // flagged unreadable: its hash is stale
	Marked     bool     // marked for deletion, on its current content
}

// DupContent is one content (hash) and its copies.
type DupContent struct {
	Hash     string
	Size     int64
	Separate bool      // marked separate: identical copies that are not duplicates
	Copies   []DupCopy // ordered by absolute path
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Which contents loadContents reads: their hashes are selected by one of these, which may take arguments.
const (
	// hashesShared: contents held by more than one present path (hard links still to be told apart).
	hashesShared = `SELECT f2.hash FROM files f2 JOIN paths p2 ON p2.file_id = f2.id AND p2.valid_to_scan IS NULL
		WHERE f2.kind = 'file' AND f2.size > 0 AND f2.hash IS NOT NULL GROUP BY f2.hash HAVING count(*) > 1`
	hashesSeparate = `SELECT hash FROM separate_hashes`
	// hashesMarked: contents with a copy marked on its current content.
	hashesMarked = `SELECT m.hash FROM duplicate_marks m JOIN files f2 ON f2.id = m.file_id
		WHERE f2.hash = m.hash AND f2.deleted_utc IS NULL`
)

// loadContents reads the present copies of the contents the hash query selects.
func loadContents(ctx context.Context, q querier, hashes string, args ...any) ([]DupContent, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT f.id, p.root_id, r.path, p.id, p.path, f.size, f.mtime_ns, f.ctime_ns, f.hash, coalesce(f.inode, 0),
		       coalesce(f.file_date, ''), coalesce(f.retention, ''), coalesce(f.keep_until, ''),
		       coalesce(f.reviewed_utc, ''), coalesce(f.reviewed_hash, ''), f.unreadable_since_scan IS NOT NULL,
		       coalesce(m.hash, '') = f.hash, sh.hash IS NOT NULL
		FROM paths p
		JOIN files f ON f.id = p.file_id
		JOIN roots r ON r.id = p.root_id
		LEFT JOIN duplicate_marks m ON m.file_id = f.id
		LEFT JOIN separate_hashes sh ON sh.hash = f.hash
		WHERE p.valid_to_scan IS NULL AND f.kind = 'file' AND f.size > 0 AND f.hash IS NOT NULL
		  AND f.deleted_utc IS NULL AND f.hash IN (`+hashes+`)
		ORDER BY f.hash, r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("read duplicates: %w", err)
	}
	defer rows.Close()
	var out []DupContent
	type key struct {
		root int64
		ino  uint64
	}
	var byIno map[key]int // index into the current content's copies
	for rows.Next() {
		var c DupCopy
		var ino int64
		var separate bool
		if err := rows.Scan(&c.FileID, &c.RootID, &c.RootPath, &c.PathID, &c.Path, &c.Size, &c.MtimeNS, &c.CtimeNS,
			&c.Hash, &ino, &c.FileDate, &c.Retention, &c.KeepUntil, &c.ReviewedUTC, &c.ReviewedHash, &c.Unreadable,
			&c.Marked, &separate); err != nil {
			return nil, fmt.Errorf("read duplicates: %w", err)
		}
		c.Kind = "file"
		c.Ino = uint64(ino)
		if len(out) == 0 || out[len(out)-1].Hash != c.Hash {
			out = append(out, DupContent{Hash: c.Hash, Size: c.Size, Separate: separate})
			byIno = map[key]int{}
		}
		cur := &out[len(out)-1]
		if c.Ino != 0 {
			k := key{c.RootID, c.Ino}
			if i, ok := byIno[k]; ok {
				cur.Copies[i].Links = append(cur.Copies[i].Links, c.AbsPath())
				continue
			}
			byIno[k] = len(cur.Copies)
		}
		cur.Copies = append(cur.Copies, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read duplicates: %w", err)
	}
	for i := range out {
		slices.SortFunc(out[i].Copies, func(a, b DupCopy) int { return strings.Compare(a.AbsPath(), b.AbsPath()) })
	}
	return out, nil
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
		for _, l := range c.Links {
			if rel, err := filepath.Rel(c.RootPath, l); err == nil && f.contains(c.RootID, filepath.ToSlash(rel)) {
				return true
			}
		}
	}
	return false
}

// DuplicateGroups returns the duplicate groups with a copy under the filter: contents held by two or more copies,
// not marked separate, each with all of its copies (also those outside the filter). They are ordered by their first
// copy's path.
func (s *Store) DuplicateGroups(ctx context.Context, f Filter) ([]DupContent, error) {
	all, err := loadContents(ctx, s.db, hashesShared)
	if err != nil {
		return nil, err
	}
	var out []DupContent
	for _, d := range all {
		if len(d.Copies) > 1 && !d.Separate && d.touches(f) {
			out = append(out, d)
		}
	}
	sortContents(out)
	return out, nil
}

// SeparateContents returns the contents marked separate that have a copy under the filter, with all their copies.
func (s *Store) SeparateContents(ctx context.Context, f Filter) ([]DupContent, error) {
	all, err := loadContents(ctx, s.db, hashesSeparate)
	if err != nil {
		return nil, err
	}
	var out []DupContent
	for _, d := range all {
		if d.touches(f) {
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

// RecordDuplicatesExport stores a duplicates (or separate) export of groups, numbered from 1 in the order given, and
// calls write with its ID. Each copy's row keeps its hash, retention, group and path. As with RecordExport, the
// export is committed only if write succeeds.
func (s *Store) RecordDuplicatesExport(ctx context.Context, path string, now time.Time, kind string,
	groups []DupContent, write func(exportID int64) error) (int64, error) {
	return s.recordExport(ctx, path, now, kind, func(tx *sql.Tx, id int64) error {
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

// DupRow is one row of an edited duplicates CSV.
type DupRow struct {
	Line     int
	ExportID int64
	FileID   int64
	Group    int64 // the group cell
	Decision byte  // DecideDelete, DecideKeep or DecideNone (blank)
}

// DupResult is what an update of a duplicates CSV did (or, in a dry run or unconfirmed, would do).
type DupResult struct {
	Marked       int      // copies marked for deletion
	MarkedGroups int      // groups they are in
	Unmarked     int      // marks removed by an N
	Separate     []string // contents newly marked separate: each by its first copy's path
	NotSeparate  []string // contents no longer separate
	Appeared     int      // copies of decided contents that are not in the export (new since)
	Undecided    int      // groups left alone: every row blank
	Refusals     []Refusal
	RowsRefused  int  // rows the CSV reader refused before they reached the register
	Confirmed    bool // asked and confirmed
	NotRecorded  bool // something needed confirming and was not confirmed: nothing was recorded
}

// NeedsConfirm reports whether the update must be confirmed before it records anything: something was refused, or a
// content would be marked separate (which a fill-down of N would do to every group).
func (r DupResult) NeedsConfirm() bool {
	return len(r.Refusals) > 0 || r.RowsRefused > 0 || len(r.Separate) > 0
}

// exportRow is a duplicates export's record of one copy.
type exportRow struct {
	hash   string
	group  int64
	pathID int64
}

// dupGroupKey is one group of one export.
type dupGroupKey struct{ export, group int64 }

type decided struct {
	decision byte
	line     int
}

// UpdateDuplicates checks the decisions in a duplicates CSV group by group (docs/design.md, Duplicates) and records
// the groups that pass, in one transaction. If anything was refused or a content would be marked separate, confirm
// (given the result so far) must return true, or nothing is recorded; it is not called in a dry run. detail names
// the source for the history; rowsRefused is how many rows the CSV reader refused, which also need confirming.
func (s *Store) UpdateDuplicates(ctx context.Context, rows []DupRow, rowsRefused int, now time.Time, detail string,
	dryRun bool, confirm func(DupResult) bool) (DupResult, error) {
	res := DupResult{RowsRefused: rowsRefused}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	paths := map[int64]string{} // file -> current absolute path, "" if none
	pathOf := func(fileID int64) (string, error) {
		if p, ok := paths[fileID]; ok {
			return p, nil
		}
		var root, rel string
		err := tx.QueryRowContext(ctx, `SELECT r.path, p.path FROM paths p JOIN roots r ON r.id = p.root_id
			WHERE p.file_id = ? AND p.valid_to_scan IS NULL`, fileID).Scan(&root, &rel)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			paths[fileID] = ""
		case err != nil:
			return "", fmt.Errorf("read file %d: %w", fileID, err)
		default:
			paths[fileID] = absPath(root, rel)
		}
		return paths[fileID], nil
	}
	refuse := func(line int, fileID int64, reason string) error {
		p, err := pathOf(fileID)
		if err != nil {
			return err
		}
		res.Refusals = append(res.Refusals, Refusal{Line: line, FileID: fileID, Path: p, Reason: reason})
		return nil
	}

	// The exports the rows name, and their rows.
	type exportData struct {
		exportInfo
		rows   map[int64]exportRow // file -> row
		groups map[int64][]int64   // group -> files, in file order
	}
	exports := map[int64]*exportData{}
	loadExport := func(id int64) (*exportData, error) {
		if ex, ok := exports[id]; ok {
			return ex, nil
		}
		info, err := readExport(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		ex := &exportData{exportInfo: info, rows: map[int64]exportRow{}, groups: map[int64][]int64{}}
		exports[id] = ex
		if !info.found || info.kind == ExportRetention {
			return ex, nil
		}
		rs, err := tx.QueryContext(ctx, `SELECT file_id, coalesce(hash, ''), coalesce(dup_group, 0),
			coalesce(path_id, 0) FROM export_rows WHERE export_id = ? ORDER BY file_id`, id)
		if err != nil {
			return nil, fmt.Errorf("read export %d: %w", id, err)
		}
		defer rs.Close()
		for rs.Next() {
			var file int64
			var r exportRow
			if err := rs.Scan(&file, &r.hash, &r.group, &r.pathID); err != nil {
				return nil, fmt.Errorf("read export %d: %w", id, err)
			}
			ex.rows[file] = r
			ex.groups[r.group] = append(ex.groups[r.group], file)
		}
		return ex, rs.Err()
	}

	// Each row's decision, by group; a group with a row out of place is refused whole.
	decisions := map[dupGroupKey]map[int64]decided{}
	refused := map[dupGroupKey]string{}
	refusedLine := map[dupGroupKey]int{} // the row that caused the refusal, for a group with no rows of its own
	var order []dupGroupKey              // groups in the order their first row appears
	note := func(k dupGroupKey) {
		if _, ok := decisions[k]; !ok {
			decisions[k] = map[int64]decided{}
			order = append(order, k)
		}
	}
	for _, r := range rows {
		ex, err := loadExport(r.ExportID)
		if err != nil {
			return res, err
		}
		var why string
		er, inExport := ex.rows[r.FileID]
		switch {
		case !ex.found:
			why = fmt.Sprintf("export %d is not in this register", r.ExportID)
		case ex.kind == ExportRetention:
			why = fmt.Sprintf("export %d is a retention export, not a duplicates export", r.ExportID)
		case !inExport:
			why = fmt.Sprintf("file %d is not in export %d (rows cannot be added)", r.FileID, r.ExportID)
		case r.Group != er.group:
			why = fmt.Sprintf("its group is %d in export %d, not %d (a sorted or pasted row?)", er.group,
				r.ExportID, r.Group)
			for _, g := range []int64{er.group, r.Group} {
				k := dupGroupKey{r.ExportID, g}
				if _, ok := ex.groups[g]; ok && refused[k] == "" {
					note(k)
					refused[k] = fmt.Sprintf("a row of export %d (line %d) is in the wrong group", r.ExportID, r.Line)
					refusedLine[k] = r.Line
				}
			}
		}
		if why != "" {
			if err := refuse(r.Line, r.FileID, why); err != nil {
				return res, err
			}
			continue
		}
		k := dupGroupKey{r.ExportID, er.group}
		note(k)
		decisions[k][r.FileID] = decided{r.Decision, r.Line}
	}

	// What to record.
	type groupPlan struct {
		key           dupGroupKey
		kind          string
		hash          string
		del, keep     []int64
		separate      bool // mark the content separate
		notSeparate   bool // undo that
		alreadyMarked map[int64]bool
	}
	var plans []groupPlan
	separateNow := map[string]bool{}
	readSeparate := func(hash string) (bool, error) {
		if v, ok := separateNow[hash]; ok {
			return v, nil
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM separate_hashes WHERE hash = ?`, hash).
			Scan(&n); err != nil {
			return false, fmt.Errorf("read separate contents: %w", err)
		}
		separateNow[hash] = n > 0
		return n > 0, nil
	}

	for _, k := range order {
		ex := exports[k.export]
		files := ex.groups[k.group]
		dec := decisions[k]
		firstLine := 0
		for _, d := range dec {
			if firstLine == 0 || d.line < firstLine {
				firstLine = d.line
			}
		}
		if firstLine == 0 {
			firstLine = refusedLine[k]
		}
		var nDel, nKeep, nBlank int
		for _, f := range files {
			switch dec[f].decision {
			case DecideDelete:
				nDel++
			case DecideKeep:
				nKeep++
			default:
				nBlank++
			}
		}
		groupRefusal := func(reason string) error {
			return refuse(firstLine, files[0], fmt.Sprintf("group %d of export %d: %s", k.group, k.export, reason))
		}
		if why := refused[k]; why != "" {
			if err := groupRefusal(why); err != nil {
				return res, err
			}
			continue
		}
		hash := ex.rows[files[0]].hash
		if ex.kind != ExportSeparate && nDel == 0 && nKeep == 0 {
			res.Undecided++
			continue
		}

		// In an export of separate contents every cell starts as N: a blank one is the decision.
		if ex.kind == ExportSeparate {
			switch {
			case nDel > 0:
				if err := groupRefusal("a Y in an export of separate contents; blank the cells to make it a " +
					"duplicate again, then decide in a later filo export duplicates"); err != nil {
					return res, err
				}
			case !hasBlankRow(dec):
				res.Undecided++ // still all N (a row deleted from the CSV is no decision)
			default:
				sep, err := readSeparate(hash)
				if err != nil {
					return res, err
				}
				if sep {
					p, err := pathOf(files[0])
					if err != nil {
						return res, err
					}
					separateNow[hash] = false
					res.NotSeparate = append(res.NotSeparate, p)
					plans = append(plans, groupPlan{key: k, kind: ex.kind, hash: hash, notSeparate: true})
				}
			}
			continue
		}

		switch {
		case nBlank > 0:
			if err := groupRefusal(fmt.Sprintf("every copy of a decided group needs Y or N (%d %s)", nBlank,
				pluralWord(nBlank, "is blank or missing", "are blank or missing"))); err != nil {
				return res, err
			}
			continue
		case nKeep == 0:
			if err := groupRefusal("every copy is marked Y, which would leave none; to remove this content " +
				"entirely, give it a past retention"); err != nil {
				return res, err
			}
			continue
		}

		// Each copy as it is now.
		type copyState struct {
			present            bool
			pathID             int64
			hash, keep         string
			unreadable         bool
			links              int
			path               string
			alreadyMarkedOnNow bool
		}
		state := map[int64]copyState{}
		var problems []string
		for _, f := range files {
			var st copyState
			var err error
			if st.path, err = pathOf(f); err != nil {
				return res, err
			}
			err = tx.QueryRowContext(ctx, `
				SELECT p.id, coalesce(f.hash, ''), coalesce(f.keep_until, ''), f.unreadable_since_scan IS NOT NULL,
				       (SELECT count(*) FROM files f2 JOIN paths p2 ON p2.file_id = f2.id AND p2.valid_to_scan IS NULL
				        WHERE f2.id <> f.id AND f2.inode = f.inode AND p2.root_id = p.root_id AND f2.kind = 'file'
				          AND f2.deleted_utc IS NULL),
				       coalesce((SELECT m.hash FROM duplicate_marks m WHERE m.file_id = f.id), '') = coalesce(f.hash, '-')
				FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
				WHERE f.id = ? AND f.deleted_utc IS NULL`, f).
				Scan(&st.pathID, &st.hash, &st.keep, &st.unreadable, &st.links, &st.alreadyMarkedOnNow)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return res, fmt.Errorf("read file %d: %w", f, err)
			default:
				st.present = true
			}
			state[f] = st
			where := st.path
			if where == "" {
				where = fmt.Sprintf("file %d", f)
			}
			er := ex.rows[f]
			switch {
			case !st.present:
				problems = append(problems, where+" is no longer present")
			case st.pathID != er.pathID:
				problems = append(problems, where+" moved since the export")
			case st.hash != er.hash:
				problems = append(problems, where+" changed since the export")
			case st.unreadable:
				problems = append(problems, where+" is flagged unreadable")
			case dec[f].decision == DecideDelete && st.links > 0:
				problems = append(problems, fmt.Sprintf("%s has %d other %s, so deleting it frees nothing; "+
					"mark it N", where, st.links, pluralWord(st.links, "hard link", "hard links")))
			}
		}
		if len(problems) == 0 {
			// Never lose a decision: each copy deleted needs a kept copy that lasts at least as long.
			for _, f := range files {
				if dec[f].decision != DecideDelete {
					continue
				}
				ok := false
				for _, g := range files {
					if dec[g].decision == DecideKeep && LastsAtLeast(state[g].keep, state[f].keep) {
						ok = true
						break
					}
				}
				if !ok {
					problems = append(problems, fmt.Sprintf("%s is kept until %s, longer than every copy marked N; "+
						"keep that copy instead, or give a kept copy a longer retention first", state[f].path,
						state[f].keep))
				}
			}
		}
		if len(problems) > 0 {
			if err := groupRefusal(strings.Join(problems, "; ")); err != nil {
				return res, err
			}
			continue
		}

		gp := groupPlan{key: k, kind: ex.kind, hash: hash, alreadyMarked: map[int64]bool{}}
		for _, f := range files {
			if dec[f].decision == DecideDelete {
				gp.del = append(gp.del, f)
			} else {
				gp.keep = append(gp.keep, f)
				if state[f].alreadyMarkedOnNow {
					res.Unmarked++
				}
			}
		}
		res.Marked += len(gp.del)
		if len(gp.del) > 0 {
			res.MarkedGroups++
		}
		if nKeep >= 2 {
			sep, err := readSeparate(hash)
			if err != nil {
				return res, err
			}
			if !sep {
				gp.separate = true
				separateNow[hash] = true
				res.Separate = append(res.Separate, state[gp.keep[0]].path)
			}
		}
		// Copies of this content that the export did not have.
		args := []any{hash}
		ph := make([]string, len(files))
		for i, f := range files {
			ph[i] = "?"
			args = append(args, f)
		}
		// A hard link of an exported copy is the same copy (the export has one row per copy), so it is not new.
		var appeared int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM files f JOIN paths p ON p.file_id = f.id
			AND p.valid_to_scan IS NULL WHERE f.hash = ? AND f.kind = 'file' AND f.deleted_utc IS NULL
			AND f.id NOT IN (`+strings.Join(ph, ",")+`)
			AND NOT (f.inode IS NOT NULL AND EXISTS (SELECT 1 FROM files fx JOIN paths px ON px.file_id = fx.id
				AND px.valid_to_scan IS NULL WHERE fx.id IN (`+strings.Join(ph, ",")+`) AND fx.inode = f.inode
				AND px.root_id = p.root_id))`, append(args, args[1:]...)...).Scan(&appeared); err != nil {
			return res, fmt.Errorf("read copies: %w", err)
		}
		res.Appeared += appeared
		plans = append(plans, gp)
	}

	if dryRun {
		return res, nil
	}
	if res.NeedsConfirm() {
		if confirm == nil || !confirm(res) {
			res.NotRecorded = true
			return res, nil
		}
		res.Confirmed = true
	}
	if len(plans) == 0 {
		return res, nil
	}

	batch, err := newBatch(ctx, tx, now, "update", detail)
	if err != nil {
		return res, err
	}
	history := func(exportID int64, fileID sql.NullInt64, hash, decision string) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO duplicate_reviews (batch_id, export_id, file_id, hash, decision)
			VALUES (?, ?, ?, ?, ?)`, batch, exportID, fileID, hash, decision)
		return err
	}
	for _, gp := range plans {
		for _, f := range gp.del {
			if _, err := tx.ExecContext(ctx, `INSERT INTO duplicate_marks (file_id, hash, export_id, batch_id)
				VALUES (?, ?, ?, ?) ON CONFLICT (file_id) DO UPDATE SET hash = excluded.hash,
				export_id = excluded.export_id, batch_id = excluded.batch_id`, f, gp.hash, gp.key.export,
				batch); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
			if err := history(gp.key.export, nullInt(f), gp.hash, reviewDelete); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
		}
		for _, f := range gp.keep {
			if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_marks WHERE file_id = ?`, f); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
			if err := history(gp.key.export, nullInt(f), gp.hash, reviewKeep); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
		}
		switch {
		case gp.separate:
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO separate_hashes (hash, export_id, batch_id)
				VALUES (?, ?, ?)`, gp.hash, gp.key.export, batch); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
			if err := history(gp.key.export, sql.NullInt64{}, gp.hash, reviewSeparate); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
		case gp.notSeparate:
			if _, err := tx.ExecContext(ctx, `DELETE FROM separate_hashes WHERE hash = ?`, gp.hash); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
			if err := history(gp.key.export, sql.NullInt64{}, gp.hash, reviewNotSeparate); err != nil {
				return res, fmt.Errorf("record decisions: %w", err)
			}
		}
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("record decisions: %w", err)
	}
	return res, nil
}

// hasBlankRow reports whether a group's CSV rows include one with a blank delete cell.
func hasBlankRow(dec map[int64]decided) bool {
	for _, d := range dec {
		if d.decision == DecideNone {
			return true
		}
	}
	return false
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// MarkedGroup is a content with copies marked for deletion, as filo delete lists and checks it.
type MarkedGroup struct {
	Hash   string
	Size   int64
	Delete []Candidate // the marked copies under the filter; ReviewedHash is the hash they were marked on
	Keep   []Candidate // the unmarked copies, anywhere: the longest retention first
}

// MarkedGroups returns the contents with copies marked for deletion under the filter, ordered by their first marked
// copy, and how many marks under the filter no longer count because the copy's content changed since.
func (s *Store) MarkedGroups(ctx context.Context, f Filter) ([]MarkedGroup, int, error) {
	all, err := loadContents(ctx, s.db, hashesMarked)
	if err != nil {
		return nil, 0, err
	}
	var out []MarkedGroup
	for _, d := range all {
		g := MarkedGroup{Hash: d.Hash, Size: d.Size}
		for _, c := range d.Copies {
			switch {
			case c.Marked && f.contains(c.RootID, c.Path):
				cand := c.Candidate
				cand.ReviewedHash = d.Hash // what the check at the moment of deleting compares with
				g.Delete = append(g.Delete, cand)
			case !c.Marked:
				cand := c.Candidate
				cand.ReviewedHash = d.Hash
				g.Keep = append(g.Keep, cand)
			}
		}
		if len(g.Delete) == 0 {
			continue
		}
		slices.SortStableFunc(g.Keep, func(a, b Candidate) int {
			switch {
			case a.KeepUntil == b.KeepUntil:
				return 0
			case LastsAtLeast(a.KeepUntil, b.KeepUntil):
				return -1
			}
			return 1
		})
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b MarkedGroup) int { return strings.Compare(a.Delete[0].AbsPath(), b.Delete[0].AbsPath()) })

	cond, args := f.scopeSQL()
	var stale int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM duplicate_marks m JOIN files f ON f.id = m.file_id
		JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
		WHERE f.deleted_utc IS NULL AND f.hash IS NOT m.hash`+cond, args...).Scan(&stale); err != nil {
		return nil, 0, fmt.Errorf("read marks: %w", err)
	}
	return out, stale, nil
}

// Unmark removes a copy's mark for deletion, from filo delete's prompt.
func (s *Store) Unmark(ctx context.Context, fileID int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT hash FROM duplicate_marks WHERE file_id = ?`, fileID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unmark file %d: %w", fileID, err)
	}
	batch, err := newBatch(ctx, tx, now, "delete", "filo delete")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_marks WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("unmark file %d: %w", fileID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO duplicate_reviews (batch_id, file_id, hash, decision)
		VALUES (?, ?, ?, ?)`, batch, fileID, hash, reviewUnmark); err != nil {
		return fmt.Errorf("unmark file %d: %w", fileID, err)
	}
	return s.commit(tx)
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
	rows, err := s.db.QueryContext(ctx, `SELECT r.path, p.path, coalesce(f.link_target, '') FROM paths p
		JOIN files f ON f.id = p.file_id JOIN roots r ON r.id = p.root_id
		WHERE p.valid_to_scan IS NULL AND f.kind = 'symlink' ORDER BY r.path, p.path`)
	if err != nil {
		return nil, fmt.Errorf("read symlinks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var root, rel, target string
		if err := rows.Scan(&root, &rel, &target); err != nil {
			return nil, fmt.Errorf("read symlinks: %w", err)
		}
		if target == "" {
			continue
		}
		link := absPath(root, rel)
		t := filepath.FromSlash(target)
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(link), t)
		}
		t = filepath.Clean(t)
		if want[t] {
			out[t] = append(out[t], link)
		}
	}
	return out, rows.Err()
}

// SeparateTotals counts the contents marked separate that still have a present copy, and their files.
func (s *Store) SeparateTotals(ctx context.Context) (contents, files int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(DISTINCT f.hash), count(*) FROM separate_hashes sh
		JOIN files f ON f.hash = sh.hash JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
		WHERE f.kind = 'file' AND f.deleted_utc IS NULL`).Scan(&contents, &files)
	if err != nil {
		return 0, 0, fmt.Errorf("read separate contents: %w", err)
	}
	return contents, files, nil
}
