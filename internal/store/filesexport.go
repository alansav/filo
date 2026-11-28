package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/tags"
)

// ExportFiles is the combined CSV's export kind (exports.kind): every column a person edits, file_date, retention
// and tags, in one CSV (docs/design.md, Tags, "The combined CSV").
const ExportFiles = "files"

// KindName is an export kind as messages name it: "combined" for ExportFiles, as the help and design call it, the
// kind itself otherwise.
func KindName(kind string) string {
	if kind == ExportFiles {
		return "combined"
	}
	return kind
}

// FilesItem is one present file for the combined CSV: its retention and its tags. A symlink has no tags.
type FilesItem struct {
	FileItem
	Tags     []tags.Tag
	NameCell string // the name cell the CSV shows (package filecsv fills it in), recorded so an edit can be told
	// Conflict names what devices sharing the file disagree on: "date", "retention", "tags", space-separated; "" for
	// nothing (docs/design.md, Sharing decisions between devices).
	Conflict string
}

// FilesItems returns every present file (regular files and symlinks) under the filter with its status and its tags,
// ordered by root and path, as Files does. today is the local date, YYYY-MM-DD.
func (s *Store) FilesItems(ctx context.Context, today string, f Filter) ([]FilesItem, error) {
	fs, err := s.Files(ctx, today, f)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(fs))
	for i, it := range fs {
		ids[i] = it.FileID
	}
	ts, err := s.FileTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.ConflictedFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FilesItem, len(fs))
	for i, it := range fs {
		out[i] = FilesItem{FileItem: it, Tags: ts[it.FileID], Conflict: conflictCell(conflicts[it.FileID])}
	}
	return out, nil
}

// RecordFilesExport stores a combined export of items (made at now, written to path) and calls write with its ID.
// Each row keeps the file's hash, the file_date (as filled in from the name, if it was), retention and keep_until it
// was exported with, and its tags cell, so the update can tell an untouched cell in either half, and its name cell,
// so a changed one can ask for a rename. The export is committed only if write succeeds.
func (s *Store) RecordFilesExport(ctx context.Context, path string, now time.Time, items []FilesItem,
	write func(exportID int64) error) (int64, error) {
	return s.recordExport(ctx, path, now, ExportFiles, func(tx *sql.Tx, id int64) error {
		// path_id is the file's current path, so a rename can tell a file moved since the export.
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO export_rows (export_id, file_id, hash, file_date, retention,
			keep_until, tags, name, path_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?,
			(SELECT id FROM paths WHERE file_id = ?2 AND valid_to_scan IS NULL))`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, it := range items {
			if _, err := stmt.ExecContext(ctx, id, it.FileID, nullString(it.Hash), nullString(it.FileDate),
				nullString(it.Retention), nullString(it.KeepUntil), JoinTags(it.Tags), nullString(it.NameCell)); err != nil {
				return err
			}
		}
		return nil
	}, write)
}

// FilesRow is one row of an edited combined CSV: its retention half, and the tags its cell holds, each once.
type FilesRow struct {
	RetentionRow
	Tags     []tags.Tag
	HasName  bool   // the CSV has a name column
	NameCell string // the name cell as read, to tell it from the one exported
	Name     string // the name it asks for (the formula guard dropped)

	HasDirectory  bool   // the CSV has a directory column
	DirectoryCell string // the directory cell as read, to tell it from the one exported (and the directory it asks for)
}

// FilesPending is what the combined update asks about before it records anything: the retentions blank cells clear,
// the changes that remove a tag, the policies the rows name that do not exist yet, and the renames.
type FilesPending struct {
	Clears      []Clear
	Removals    []TagChange
	NewPolicies []NewPolicy
	Renames     []Rename
}

// Rename is a file a row asks to rename or move within its root: the file as the register knows it, the directory it
// goes to ('/'-separated, relative to the root, "" for the root itself) and its name there.
type Rename struct {
	Candidate
	Dir      string
	Name     string
	Symlinks []string // the registered symlinks that point at the file (absolute), which the rename or move breaks
}

// NewPath is where the file goes, relative to its root.
func (r Rename) NewPath() string { return path.Join(r.Dir, r.Name) }

// Moves reports whether the file goes to another directory (a move), not only to another name.
func (r Rename) Moves() bool { return r.Dir != relDir(r.Path) }

// relDir is the directory of rel ('/'-separated), "" for the root.
func relDir(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// Empty reports whether there is nothing to ask about.
func (p FilesPending) Empty() bool {
	return len(p.Clears)+len(p.Removals)+len(p.NewPolicies)+len(p.Renames) == 0
}

// FilesUpdateOptions are how a combined update runs: a dry run, its one question (nil declines, as with no
// terminal), and the checks of a rename or move that are not the register's: the new path's (CheckPath, package
// dispose's rules for a root with these settings, given the old and new paths relative to the root) and the disk's
// (CheckDisk: the file is still the one the register knows, and it can go there). Each says why not, or "". Moves
// says whether a changed directory cell moves the file; without it the cell is display only, as before moves.
type FilesUpdateOptions struct {
	DryRun    bool
	Confirm   func(FilesPending) bool // given what is pending, once, when there is any
	CheckPath func(oldRel, newRel string, includeHidden bool, ignore []string) string
	CheckDisk func(r Rename) string
	Moves     bool
}

// FilesUpdateResult is what a combined update did (or, in a dry run, would do). The renames, in Pending, are not done
// by the update: once it returns with nothing declined, the caller makes each (package dispose) and records it.
type FilesUpdateResult struct {
	Pending         FilesPending
	Declined        bool        // the question was not confirmed: nothing was recorded
	Set             int         // files whose file_date or retention changed, clears not included
	NoValue         int         // of Set: files put on a policy with no value yet
	Cleared         int         // retentions cleared by a blank cell
	PoliciesCreated int         // new policies created
	NowPast         []Past      // of Set: files whose keep_until is today or earlier
	TagsChanged     []TagChange // the files whose tags changed
	Unchanged       int         // rows with nothing to change in either half
	Refusals        []Refusal
}

// filesRowCheck is what checking one row of a combined CSV found, before the rows are compared with each other.
type filesRowCheck struct {
	r       FilesRow
	ex      exportInfo
	c       checked
	ch      TagChange
	rename  *Rename
	holder  int64 // a rename refused for a name another file has: that file (0 = none)
	reasons []string
}

// UpdateFiles checks each row of a combined CSV against the register with the rules of both a retention and a tags
// update, and of a rename for a changed name cell, and records the ones that pass in one transaction (docs/design.md,
// Tags, "The combined CSV" and "Renaming a file by its name cell"). A row refused by any half is refused whole, with
// each reason. If any row clears a retention, removes a tag, names a new policy or renames a file, Confirm is given
// all of them, once, before anything is written; unless it returns true nothing is recorded (Declined). A dry run asks
// nothing and writes nothing. today is the local date (YYYY-MM-DD); detail names the source for the history.
func (s *Store) UpdateFiles(ctx context.Context, rows []FilesRow, now time.Time, today, detail string,
	opt FilesUpdateOptions) (FilesUpdateResult, error) {
	var res FilesUpdateResult
	roots, err := s.Roots(ctx)
	if err != nil {
		return res, err
	}
	settings := map[int64]Settings{}
	for _, r := range roots {
		settings[r.ID] = r.Settings
	}
	own, err := s.OwnPaths(ctx) // a file renamed to one of these would be lost: no scan records them
	if err != nil {
		return res, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	q := newStmtCache(tx)
	exports := map[int64]exportInfo{}
	policies := policyCache{}
	spelled := map[string]tags.Tag{}
	var checks []*filesRowCheck
	targets := map[string][]*filesRowCheck{} // root and new path -> the rows renaming to it
	for _, r := range rows {
		ex, found := exports[r.ExportID]
		if !found {
			if ex, err = readExport(ctx, tx, r.ExportID); err != nil {
				return res, err
			}
			exports[r.ExportID] = ex
		}
		rc := &filesRowCheck{r: r, ex: ex}
		checks = append(checks, rc)
		if rc.c, err = checkRow(ctx, tx, r.RetentionRow, ex, policies); err != nil {
			return res, err
		}
		rc.reasons = append(rc.reasons, rc.c.refusal)
		if !ex.found || ex.kind != ExportFiles {
			continue // checkRow says why, once
		}
		tr := TagRow{Line: r.Line, ExportID: r.ExportID, FileID: r.FileID, Tags: r.Tags}
		var why string
		if rc.ch, why, err = checkTagRow(ctx, q, tr, ex, spelled); err != nil {
			return res, err
		}
		if why == "" && len(rc.ch.Added) > 0 {
			if why, err = untaggable(ctx, q, r.FileID); err != nil {
				return res, err
			}
		}
		rc.reasons = append(rc.reasons, why)
		if rc.rename, rc.holder, why, err = checkRename(ctx, q, r, settings, own, opt); err != nil {
			return res, err
		}
		rc.reasons = append(rc.reasons, why)
		if rc.rename != nil {
			key := strconv.FormatInt(rc.rename.RootID, 10) + "/" + rc.rename.NewPath()
			targets[key] = append(targets[key], rc)
		}
	}
	// Two rows giving one directory the same new name: both refused, never one chosen.
	for _, rcs := range targets {
		if len(rcs) > 1 {
			for _, rc := range rcs {
				rc.reasons = append(rc.reasons, fmt.Sprintf("%d rows give a file the name %s in %s", len(rcs),
					rc.rename.Name, dirLabel(rc.rename.Dir)))
			}
		}
	}
	// A chain or a swap (a to b while b is renamed to c): the row renaming b is refused too, never ordered.
	renaming := map[int64]*filesRowCheck{}
	for _, rc := range checks {
		if rc.rename != nil {
			renaming[rc.rename.FileID] = rc
		}
	}
	for _, rc := range checks {
		if other := renaming[rc.holder]; rc.holder != 0 && other != nil {
			other.reasons = append(other.reasons, "another row renames a file to its name; rename one at a time")
		}
	}
	// Last, the disk: still the file the register knows, for a row nothing else refused.
	for _, rc := range checks {
		if rc.rename != nil && joinReasons(rc.reasons...) == "" && opt.CheckDisk != nil {
			rc.reasons = append(rc.reasons, opt.CheckDisk(*rc.rename))
		}
	}

	var sets, clears []retentionChange
	var tagChanges []TagChange
	for _, rc := range checks {
		r, c, ch := rc.r, rc.c, rc.ch
		if reasons := joinReasons(rc.reasons...); reasons != "" {
			p := c.path
			if p == "" {
				p = ch.Path
			}
			if p == "" && rc.ex.found { // refused for its name: the other halves did not look the file up
				if f, ok, err := renameCandidate(ctx, q, r.FileID); err != nil {
					return res, err
				} else if ok {
					p = f.AbsPath()
				}
			}
			res.Refusals = append(res.Refusals, Refusal{Line: r.Line, FileID: r.FileID, Path: p, Reason: reasons})
			continue
		}
		tagsChange := len(ch.Added)+len(ch.Removed) > 0
		if c.unchanged && !tagsChange && rc.rename == nil {
			res.Unchanged++
			continue
		}
		switch {
		case c.unchanged:
		case c.clear:
			clears = append(clears, retentionChange{r: r.RetentionRow, c: c, afterScan: rc.ex.afterScan})
			res.Pending.Clears = append(res.Pending.Clears, Clear{FileID: r.FileID, Path: c.path,
				Retention: c.storedRetention, KeepUntil: c.storedKeepUntil})
		default:
			sets = append(sets, retentionChange{r: r.RetentionRow, c: c, afterScan: rc.ex.afterScan})
			if c.keepUntil != "" && c.keepUntil != Forever && c.keepUntil <= today {
				res.NowPast = append(res.NowPast, Past{Path: c.path, Retention: c.retention, KeepUntil: c.keepUntil})
			}
			if c.policy && c.keepUntil == "" {
				res.NoValue++
			}
		}
		if tagsChange {
			tagChanges = append(tagChanges, ch)
			if len(ch.Removed) > 0 {
				res.Pending.Removals = append(res.Pending.Removals, ch)
			}
		}
		if rc.rename != nil {
			res.Pending.Renames = append(res.Pending.Renames, *rc.rename)
		}
	}
	newRows := map[string]int{}
	for _, a := range sets {
		if a.c.newPolicy {
			newRows[a.c.retention]++
		}
	}
	for name, n := range newRows {
		res.Pending.NewPolicies = append(res.Pending.NewPolicies, NewPolicy{Name: name, Rows: n})
	}
	slices.SortFunc(res.Pending.NewPolicies, func(a, b NewPolicy) int { return strings.Compare(a.Name, b.Name) })
	// The symlinks each rename or move would break, read in this transaction (the question is asked inside it).
	if len(res.Pending.Renames) > 0 {
		targets := make([]string, len(res.Pending.Renames))
		for i, rn := range res.Pending.Renames {
			targets[i] = rn.AbsPath()
		}
		links, err := symlinksTo(ctx, q, targets)
		if err != nil {
			return res, err
		}
		for i := range res.Pending.Renames {
			res.Pending.Renames[i].Symlinks = links[targets[i]]
		}
	}
	res.Set, res.Cleared, res.TagsChanged = len(sets), len(clears), tagChanges
	if opt.DryRun {
		return res, nil
	}
	if !res.Pending.Empty() && (opt.Confirm == nil || !opt.Confirm(res.Pending)) {
		return FilesUpdateResult{Pending: res.Pending, Declined: true, Refusals: res.Refusals}, nil
	}
	if len(sets)+len(clears)+len(tagChanges) == 0 {
		return res, nil // renames only: the caller makes them
	}
	created, err := createPolicies(ctx, tx, res.Pending.NewPolicies, now)
	if err != nil {
		return res, err
	}
	res.PoliciesCreated = len(created)
	if apply := append(sets, clears...); len(apply) > 0 {
		if err := applyRetentions(ctx, tx, apply, created, now, detail); err != nil {
			return res, err
		}
	}
	if err := applyTagChanges(ctx, q, tagChanges, now); err != nil {
		return res, err
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("record update: %w", err)
	}
	return res, nil
}

// checkRename applies the register's rules to a row whose name or directory cell changed (docs/design.md, Tags,
// "Renaming a file by its name cell" and "Moving a file by its directory cell"): the rename or move it asks for, or why
// it is refused; neither for cells left as exported. opt.CheckPath is the new path's rules (package dispose's), for
// the root's settings. holder is the file that has the new path already, when that is why it is refused.
func checkRename(ctx context.Context, q querier, r FilesRow, settings map[int64]Settings, own []string,
	opt FilesUpdateOptions) (rn *Rename, holder int64, why string, err error) {
	if !r.HasName && !(r.HasDirectory && opt.Moves) {
		return nil, 0, "", nil
	}
	var cell, exportedRel, rootPath sql.NullString
	var pathID sql.NullInt64
	switch err := q.QueryRowContext(ctx, `SELECT er.name, er.path_id, ep.path, r.path FROM export_rows er
		LEFT JOIN paths ep ON ep.id = er.path_id LEFT JOIN roots r ON r.id = ep.root_id
		WHERE er.export_id = ? AND er.file_id = ?`, r.ExportID, r.FileID).Scan(&cell, &pathID, &exportedRel,
		&rootPath); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, 0, "", nil // not in the export: the other halves say so
	case err != nil:
		return nil, 0, "", fmt.Errorf("read export #%d: %w", r.ExportID, err)
	}
	nameChanged := r.HasName && !(cell.Valid && r.NameCell == cell.String)
	// The directory cell as the export wrote it, worked out again from the path it showed (sheet.FileCells).
	dirChanged := false
	if r.HasDirectory && opt.Moves && exportedRel.Valid {
		dirChanged = r.DirectoryCell != sheet.Cell(filepath.Join(rootPath.String,
			filepath.FromSlash(relDir(exportedRel.String))))
	}
	// An export that did not record the path it showed (made before moves): its directory cell is compared with the
	// file's directory now, so a cell left alone is no change.
	unrecorded := r.HasDirectory && opt.Moves && !exportedRel.Valid
	if !nameChanged && !dirChanged && !unrecorded {
		return nil, 0, "", nil
	}
	c, present, err := renameCandidate(ctx, q, r.FileID)
	if err != nil {
		return nil, 0, "", err
	}
	if unrecorded && present {
		dirChanged = r.DirectoryCell != sheet.Cell(filepath.Join(c.RootPath, filepath.FromSlash(relDir(c.Path))))
		if !nameChanged && !dirChanged {
			return nil, 0, "", nil
		}
	}
	if !present {
		return nil, 0, "no longer present, so it cannot be renamed or moved", nil
	}
	old := path.Base(c.Path)
	rn = &Rename{Candidate: c, Dir: relDir(c.Path), Name: old}
	if nameChanged {
		switch {
		case !cell.Valid && (r.Name == old || r.NameCell == sheet.Cell(old)):
			// An export from before schema 13 wrote the cell as any display cell; left as it was, it is no change.
		case !cell.Valid:
			return nil, 0, fmt.Sprintf("export #%d was made before renaming by the name cell; export again to "+
				"rename it", r.ExportID), nil
		case strings.Contains(r.Name, "/"):
			return nil, 0, "a new name cannot hold /: to move the file, change its directory cell", nil
		default:
			rn.Name = r.Name // the same as old if only the formula guard changed
		}
	}
	if dirChanged {
		if !exportedRel.Valid {
			return nil, 0, fmt.Sprintf("export #%d does not record the directory it showed; export again to move "+
				"it", r.ExportID), nil
		}
		dir, why := dirInRoot(c.RootPath, r.DirectoryCell)
		if why != "" {
			return nil, 0, why, nil
		}
		rn.Dir = dir
	}
	if rn.NewPath() == c.Path {
		return nil, 0, "", nil
	}
	if !pathID.Valid || pathID.Int64 != c.PathID {
		return nil, 0, fmt.Sprintf("moved or renamed since export #%d; export again to rename or move it",
			r.ExportID), nil
	}
	if opt.CheckPath != nil {
		st := settings[c.RootID]
		if why := opt.CheckPath(c.Path, rn.NewPath(), st.IncludeHidden, st.Ignore); why != "" {
			return nil, 0, why, nil
		}
	}
	if rn.Moves() && c.Kind == "symlink" && !filepath.IsAbs(c.LinkTarget) {
		return nil, 0, fmt.Sprintf("a symlink to %s, a relative target, would point elsewhere from another "+
			"directory; move it by hand", c.LinkTarget), nil
	}
	newAbs := filepath.Join(c.RootPath, filepath.FromSlash(rn.NewPath()))
	for _, o := range own {
		switch {
		case newAbs == o:
			return nil, 0, fmt.Sprintf("%s is the name of one of filo's own files there (its database or a tag "+
				"index's pages), which no scan records", rn.Name), nil
		case strings.HasPrefix(newAbs, o+string(filepath.Separator)):
			return nil, 0, fmt.Sprintf("%s is inside one of filo's own directories (a tag index's pages or the "+
				"register's snapshots), which no scan records", dirLabel(rn.Dir)), nil
		}
	}
	var other int64
	switch err := q.QueryRowContext(ctx, `SELECT file_id FROM paths WHERE root_id = ? AND path = ?
		AND valid_to_scan IS NULL`, c.RootID, rn.NewPath()).Scan(&other); {
	case err == nil && other != c.FileID:
		return nil, other, fmt.Sprintf("%s is another file's name in %s; rename or move one at a time", rn.Name,
			dirLabel(rn.Dir)), nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return nil, 0, "", fmt.Errorf("read paths: %w", err)
	}
	return rn, 0, "", nil
}

// dirLabel is a directory relative to a root, for a message: "the top of the root" for the root itself.
func dirLabel(dir string) string {
	if dir == "" {
		return "the top of the root"
	}
	return dir + "/"
}

// dirInRoot reads a directory cell (docs/design.md, Tags, "Moving a file by its directory cell"): the full path of a
// directory inside the root at root, or the root itself; a doubled or trailing / is tidied, and nothing else is
// guessed at. It returns the directory relative to the root ('/'-separated, "" for the root), or why not.
func dirInRoot(root, cell string) (string, string) {
	whole := "give the full path of a directory inside the root " + root
	if strings.HasPrefix(cell, "~") || !filepath.IsAbs(cell) {
		return "", "the directory " + strconvQuote(cell) + " is not a full path: " + whole
	}
	parts := strings.Split(filepath.ToSlash(cell), "/")
	kept := parts[:0]
	for _, p := range parts {
		switch p {
		case "":
			continue // a doubled or trailing /, or the leading one
		case ".", "..":
			return "", "the directory " + strconvQuote(cell) + " holds " + p + " as a part; " + whole
		}
		kept = append(kept, p)
	}
	abs := "/" + strings.Join(kept, "/")
	rootSlash := filepath.ToSlash(root)
	switch {
	case abs == rootSlash:
		return "", ""
	case strings.HasPrefix(abs, strings.TrimSuffix(rootSlash, "/")+"/"):
		return strings.TrimPrefix(abs, strings.TrimSuffix(rootSlash, "/")+"/"), ""
	}
	return "", "the directory " + strconvQuote(cell) + " is outside the file's root: a move stays inside it; " + whole
}

// strconvQuote quotes a cell for a message.
func strconvQuote(s string) string { return strconv.Quote(s) }

// untaggable says why a file cannot be given tags: a symlink, which filo tag refuses too; "" if it can.
func untaggable(ctx context.Context, q querier, fileID int64) (string, error) {
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM files WHERE id = ?`, fileID).Scan(&kind); err != nil {
		return "", fmt.Errorf("read file %d: %w", fileID, err)
	}
	if kind == "symlink" {
		return "a symlink cannot have tags: give them to the file it points to", nil
	}
	return "", nil
}

// joinReasons joins a row's refusals from its two halves, each once (a file not in the export is refused by both);
// "" if neither refused it.
func joinReasons(reasons ...string) string {
	var out []string
	for _, r := range reasons {
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return strings.Join(out, "; ")
}

// ExportKinds returns the kind of each of the given exports that is in the register, so filo update can tell a
// combined CSV that lost a column from a CSV that is not filo's.
func (s *Store) ExportKinds(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	err := inBatches(ids, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT id, kind FROM exports WHERE id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var kind string
			if err := rows.Scan(&id, &kind); err != nil {
				return err
			}
			out[id] = kind
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read exports: %w", err)
	}
	return out, nil
}

// conflictCell names a file's keys in conflict for the CSV: "date", "retention", and "tags" once for any tag.
func conflictCell(keys []string) string {
	var parts []string
	for _, k := range keys {
		w := k
		if strings.HasPrefix(k, "tag:") {
			w = "tags"
		}
		if !slices.Contains(parts, w) {
			parts = append(parts, w)
		}
	}
	return strings.Join(parts, " ")
}
