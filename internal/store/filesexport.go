package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// ExportFiles is the combined CSV's export kind (exports.kind): every column a person edits, file_date, retention
// and tags, in one CSV (docs/design.md, Tags, "The combined CSV").
const ExportFiles = "files"

// FilesItem is one present file for the combined CSV: its retention and its tags. A symlink has no tags.
type FilesItem struct {
	FileItem
	Tags []tags.Tag
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
// untouched cell in either half. The export is committed only if write succeeds.
func (s *Store) RecordFilesExport(ctx context.Context, path string, now time.Time, items []FilesItem,
	write func(exportID int64) error) (int64, error) {
	return s.recordExport(ctx, path, now, ExportFiles, func(tx *sql.Tx, id int64) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO export_rows (export_id, file_id, hash, file_date, retention,
			keep_until, tags) VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, it := range items {
			if _, err := stmt.ExecContext(ctx, id, it.FileID, nullString(it.Hash), nullString(it.FileDate),
				nullString(it.Retention), nullString(it.KeepUntil), JoinTags(it.Tags)); err != nil {
				return err
			}
		}
		return nil
	}, write)
}
