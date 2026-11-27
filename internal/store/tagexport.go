package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/tags"
)

const tagByUpdate = "update"

// TagSeparator separates the tags in a tags cell of the combined CSV, and in the cell export_rows keeps.
const TagSeparator = "; "

// JoinTags is a tags cell: the tags as typed back, in the order given, separated by TagSeparator.
func JoinTags(ts []tags.Tag) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.String()
	}
	return strings.Join(names, TagSeparator)
}

// TagItem is one present regular file with its tags, for a tag index.
type TagItem struct {
	FoundFile
	Tags []tags.Tag
}

// TagItems returns the regular files present at scope and below in a root (0 for every root), each with its tags,
// ordered by root and then path (docs/design.md, Tag index pages).
func (s *Store) TagItems(ctx context.Context, rootID int64, scope string) ([]TagItem, error) {
	fs, err := s.FindFiles(ctx, rootID, scope)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(fs))
	for i, f := range fs {
		ids[i] = f.FileID
	}
	ts, err := s.FileTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]TagItem, len(fs))
	for i, f := range fs {
		out[i] = TagItem{FoundFile: f, Tags: ts[f.FileID]}
	}
	return out, nil
}

// TagRow is the tags half of one row of an edited combined CSV: the tags its cell holds, each once.
type TagRow struct {
	Line     int // in the CSV, for messages
	ExportID int64
	FileID   int64
	Tags     []tags.Tag
}

// TagChange is what an update does to one file's tags.
type TagChange struct {
	FileID         int64
	Path           string // absolute; "" if the file has no current path (missing)
	Added, Removed []tags.Tag
}

// applyTagChanges makes the changes, each tag added with the spelling the register has for it then (written back
// into changes).
func applyTagChanges(ctx context.Context, q *stmtCache, changes []TagChange, now time.Time) error {
	for i, ch := range changes {
		for _, t := range ch.Removed {
			if _, err := removeTags(ctx, q, ch.FileID, tags.Search{Key: t.Key, Value: t.Value}, tagByUpdate,
				now); err != nil {
				return err
			}
		}
		for j, t := range ch.Added {
			sp, err := spelling(ctx, q, t) // again: an earlier row may have removed the tag's only other spelling
			if err != nil {
				return err
			}
			if _, err := addTag(ctx, q, ch.FileID, sp, tagByUpdate, now); err != nil {
				return err
			}
			changes[i].Added[j] = sp
		}
	}
	return nil
}

// checkTagRow applies the update's rules to the tags half of one row of a combined CSV: what it changes, or why it
// is refused.
func checkTagRow(ctx context.Context, tx querier, r TagRow, ex exportInfo, spelled map[string]tags.Tag) (TagChange,
	string, error) {
	ch := TagChange{FileID: r.FileID}
	if !ex.found {
		return ch, fmt.Sprintf("export #%d is not in this register", r.ExportID), nil
	}
	if ex.kind != ExportFiles {
		return ch, fmt.Sprintf("export #%d is a %s export, not a %s export", r.ExportID, KindName(ex.kind),
			KindName(ExportFiles)), nil
	}
	var exported sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT tags FROM export_rows WHERE export_id = ? AND file_id = ?`, r.ExportID,
		r.FileID).Scan(&exported)
	if errors.Is(err, sql.ErrNoRows) {
		return ch, fmt.Sprintf("file %d is not in export #%d (rows cannot be added)", r.FileID, r.ExportID), nil
	}
	if err != nil {
		return ch, "", fmt.Errorf("read export #%d: %w", r.ExportID, err)
	}
	// A cell left as exported is not a decision, whatever happened to the file since (as for a retention).
	if sameTags(splitCell(exported.String), r.Tags) {
		return ch, "", nil
	}
	var deleted sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT deleted_utc FROM files WHERE id = ?`, r.FileID).Scan(&deleted); err != nil {
		return ch, "", fmt.Errorf("read file %d: %w", r.FileID, err)
	}
	var root, rel string
	switch err := tx.QueryRowContext(ctx, `SELECT r.path, p.path FROM paths p JOIN roots r ON r.id = p.root_id
		WHERE p.file_id = ? AND p.valid_to_scan IS NULL`, r.FileID).Scan(&root, &rel); {
	case err == nil:
		ch.Path = absPath(root, rel)
	case !errors.Is(err, sql.ErrNoRows):
		return ch, "", fmt.Errorf("read file %d: %w", r.FileID, err)
	}
	if deleted.Valid {
		return ch, "deleted by filo since the export", nil
	}
	have, err := fileTagsIn(ctx, tx, r.FileID)
	if err != nil {
		return ch, "", err
	}
	if sameTags(have, r.Tags) {
		return ch, "", nil // the tags the file has: unchanged, whatever happened since the export (a CSV applied twice)
	}
	if JoinTags(have) != exported.String {
		return ch, fmt.Sprintf("its tags were changed after export #%d (now %s); export again", r.ExportID,
			cellOrNone(have)), nil
	}
	for _, t := range r.Tags {
		if !slices.ContainsFunc(have, t.Same) {
			sk := t.Key + "\x00" + t.Value // as written: a spelling not yet in the register is its own
			sp, ok := spelled[sk]
			if !ok {
				var err error
				if sp, err = spelling(ctx, tx, t); err != nil { // as the run gives it, so a dry run shows the same
					return ch, "", err
				}
				spelled[sk] = sp
			}
			ch.Added = append(ch.Added, sp)
		}
	}
	for _, t := range have {
		if !slices.ContainsFunc(r.Tags, t.Same) {
			ch.Removed = append(ch.Removed, t)
		}
	}
	return ch, "", nil
}

// splitCell reads a tags cell as export_rows keeps it (JoinTags of tags that were stored, so each parses).
func splitCell(cell string) []tags.Tag {
	var out []tags.Tag
	for _, part := range strings.Split(cell, ";") {
		if t, err := tags.Parse(part); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// sameTags reports whether a and b hold the same tags, in any case and order (each holds a tag once).
func sameTags(a, b []tags.Tag) bool {
	if len(a) != len(b) {
		return false
	}
	for _, t := range a {
		if !slices.ContainsFunc(b, t.Same) {
			return false
		}
	}
	return true
}

// fileTagsIn is one file's tags, read in tx, in the order FileTags gives them.
func fileTagsIn(ctx context.Context, tx querier, id int64) ([]tags.Tag, error) {
	out, err := queryAll(ctx, tx, func(rows *sql.Rows) (tags.Tag, error) {
		var t tags.Tag
		return t, rows.Scan(&t.Key, &t.Value)
	}, `SELECT key, value FROM tags WHERE file_id = ? ORDER BY key, folded`, id)
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	return out, nil
}

// cellOrNone is a tags cell for a message: the tags, or "no tags".
func cellOrNone(ts []tags.Tag) string {
	if len(ts) == 0 {
		return "no tags"
	}
	return JoinTags(ts)
}
