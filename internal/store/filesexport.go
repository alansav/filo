package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

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
	out := make([]FilesItem, len(fs))
	for i, it := range fs {
		out[i] = FilesItem{FileItem: it, Tags: ts[it.FileID]}
	}
	return out, nil
}

// RecordFilesExport stores a combined export of items (made at now, written to path) and calls write with its ID.
// Each row keeps what both a retention and a tags export keep: the file's hash, the file_date (as filled in from the
// name, if it was), retention and keep_until it was exported with, and its tags cell, so the update can tell an
// untouched cell in either half, and its name cell, so a changed one can ask for a rename. The export is committed
// only if write succeeds.
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
}

// FilesPending is what the combined update asks about before it records anything: the retentions blank cells clear,
// the changes that remove a tag, and the policies the rows name that do not exist yet.
type FilesPending struct {
	Clears      []Clear
	Removals    []TagChange
	NewPolicies []NewPolicy
}

// Empty reports whether there is nothing to ask about.
func (p FilesPending) Empty() bool { return len(p.Clears)+len(p.Removals)+len(p.NewPolicies) == 0 }

// FilesUpdateOptions are how a combined update runs: a dry run, and its one question (nil declines, as with no
// terminal).
type FilesUpdateOptions struct {
	DryRun  bool
	Confirm func(FilesPending) bool // given what is pending, once, when there is any
}

// FilesUpdateResult is what a combined update did (or, in a dry run, would do).
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

// UpdateFiles checks each row of a combined CSV against the register with the rules of both a retention and a tags
// update, and records the ones that pass in one transaction (docs/design.md, Tags, "The combined CSV"). A row
// refused by either half is refused whole, with each reason. If any row clears a retention, removes a tag or names a
// new policy, Confirm is given all of them, once, before anything is written; unless it returns true nothing is
// recorded (Declined). A dry run asks nothing and writes nothing. today is the local date (YYYY-MM-DD); detail names
// the source for the history.
func (s *Store) UpdateFiles(ctx context.Context, rows []FilesRow, now time.Time, today, detail string,
	opt FilesUpdateOptions) (FilesUpdateResult, error) {
	var res FilesUpdateResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	q := newStmtCache(tx)
	exports := map[int64]exportInfo{}
	policies := policyCache{}
	spelled := map[string]tags.Tag{}
	var sets, clears []retentionChange
	var tagChanges []TagChange
	for _, r := range rows {
		ex, found := exports[r.ExportID]
		if !found {
			if ex, err = readExport(ctx, tx, r.ExportID); err != nil {
				return res, err
			}
			exports[r.ExportID] = ex
		}
		c, err := checkRow(ctx, tx, r.RetentionRow, ex, ExportFiles, policies)
		if err != nil {
			return res, err
		}
		tr := TagRow{Line: r.Line, ExportID: r.ExportID, FileID: r.FileID, Tags: r.Tags}
		var ch TagChange
		var why string
		if ex.found && ex.kind == ExportFiles { // otherwise checkRow says why, once
			if ch, why, err = checkTagRow(ctx, q, tr, ex, ExportFiles, spelled); err != nil {
				return res, err
			}
		}
		tagsChange := len(ch.Added)+len(ch.Removed) > 0
		if why == "" && len(ch.Added) > 0 {
			if why, err = untaggable(ctx, q, r.FileID); err != nil {
				return res, err
			}
		}
		if reasons := joinReasons(c.refusal, why); reasons != "" {
			path := c.path
			if path == "" {
				path = ch.Path
			}
			res.Refusals = append(res.Refusals, Refusal{Line: r.Line, FileID: r.FileID, Path: path, Reason: reasons})
			continue
		}
		if c.unchanged && !tagsChange {
			res.Unchanged++
			continue
		}
		switch {
		case c.unchanged:
		case c.clear:
			clears = append(clears, retentionChange{r: r.RetentionRow, c: c, afterScan: ex.afterScan})
			res.Pending.Clears = append(res.Pending.Clears, Clear{FileID: r.FileID, Path: c.path,
				Retention: c.storedRetention, KeepUntil: c.storedKeepUntil})
		default:
			sets = append(sets, retentionChange{r: r.RetentionRow, c: c, afterScan: ex.afterScan})
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
	res.Set, res.Cleared, res.TagsChanged = len(sets), len(clears), tagChanges
	if opt.DryRun {
		return res, nil
	}
	if !res.Pending.Empty() && (opt.Confirm == nil || !opt.Confirm(res.Pending)) {
		return FilesUpdateResult{Pending: res.Pending, Declined: true, Refusals: res.Refusals}, nil
	}
	if len(sets)+len(clears)+len(tagChanges) == 0 {
		return res, nil
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
// combined CSV that lost a column from a retention or tags CSV.
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
