package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alansav/filo/internal/tags"
)

const tagByRename = "rename"

// TagCount is one tag and how many files have it.
type TagCount struct {
	Tag   tags.Tag
	Files int64
}

// TagSummary is every tag with its count of files (files not deleted by filo: present or missing), in the order
// filo tags lists them (bare first, then by key and folded value), with how many such files have a tag and how many
// present regular files have none.
type TagSummary struct {
	Tags     []TagCount
	Tagged   int64
	Untagged int64
}

// TagSummary reads the tags in use (docs/design.md, Tags, "filo tags"). A tag only files deleted by filo have is
// not in use.
func (s *Store) TagSummary(ctx context.Context) (TagSummary, error) {
	var out TagSummary
	var err error
	out.Tags, err = queryAll(ctx, s.db, func(rows *sql.Rows) (TagCount, error) {
		var c TagCount
		return c, rows.Scan(&c.Tag.Key, &c.Tag.Value, &c.Files)
	}, `SELECT t.key, (SELECT s.value FROM tags s JOIN files sf ON sf.id = s.file_id
			WHERE s.key = t.key AND s.folded = t.folded AND sf.deleted_utc IS NULL ORDER BY s.rowid LIMIT 1),
			count(*)
		FROM tags t JOIN files f ON f.id = t.file_id WHERE f.deleted_utc IS NULL
		GROUP BY t.key, t.folded ORDER BY t.key, t.folded`)
	if err != nil {
		return out, fmt.Errorf("read tags: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(DISTINCT t.file_id) FROM tags t JOIN files f ON f.id = t.file_id
		WHERE f.deleted_utc IS NULL`).Scan(&out.Tagged); err != nil {
		return out, fmt.Errorf("count tagged files: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM files f
		WHERE f.kind = 'file' AND f.deleted_utc IS NULL
		  AND EXISTS (SELECT 1 FROM paths p WHERE p.file_id = f.id AND p.valid_to_scan IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM tags t WHERE t.file_id = f.id)`).Scan(&out.Untagged); err != nil {
		return out, fmt.Errorf("count untagged files: %w", err)
	}
	return out, nil
}

// ErrNoSuchTag means no file (not deleted by filo) has the tag, or no tag has the key.
var ErrNoSuchTag = errors.New("no file has the tag")

// Renamed is what RenameTag did: how many files it changed, and how many of them had the new tag already (the two
// merged into one).
type Renamed struct {
	Files  int
	Merged int
}

// RenameTag renames a tag across every file not deleted by filo that has it (docs/design.md, Tags, "filo tags
// rename"): old, matched ignoring case, becomes to, spelled as typed, which every file with to then has (a rename
// that changes only the case respells it). With old.AnyValue, every tag with old's key gets to's key instead, each
// value kept (spelled as the new key's tag is already, if it is). A file with both keeps one. Rows of files deleted by
// filo stay as they are. ErrNoSuchTag if no file has old.
func (s *Store) RenameTag(ctx context.Context, old, to tags.Search, now time.Time) (Renamed, error) {
	var res Renamed
	if old.AnyValue != to.AnyValue {
		return res, errors.New("give both as key: to rename a key, or both as tags")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	type row struct {
		fileID int64
		t      tags.Tag
	}
	cond, args := ` AND t.folded = ?`, []any{old.Key, tags.Fold(old.Value)}
	if old.AnyValue {
		cond, args = "", args[:1]
	}
	rows, err := queryAll(ctx, tx, func(rs *sql.Rows) (row, error) {
		var r row
		return r, rs.Scan(&r.fileID, &r.t.Key, &r.t.Value)
	}, `SELECT t.file_id, t.key, t.value FROM tags t JOIN files f ON f.id = t.file_id
		WHERE f.deleted_utc IS NULL AND t.key = ?`+cond+` ORDER BY t.key, t.folded, t.file_id`, args...)
	if err != nil {
		return res, fmt.Errorf("read tag: %w", err)
	}
	if len(rows) == 0 {
		return res, ErrNoSuchTag
	}
	// The new tag for each old one, spelled once.
	target := map[string]tags.Tag{} // by the old tag's folded value
	for _, r := range rows {
		if _, ok := target[r.t.Folded()]; ok {
			continue
		}
		if !old.AnyValue {
			target[r.t.Folded()] = tags.Tag{Key: to.Key, Value: to.Value}
			continue
		}
		t, err := spelling(ctx, tx, tags.Tag{Key: to.Key, Value: r.t.Value})
		if err != nil {
			return res, err
		}
		target[r.t.Folded()] = t
	}
	changed := map[int64]bool{}
	merged := map[int64]bool{}
	for _, r := range rows {
		nt := target[r.t.Folded()]
		if nt == r.t {
			continue // the same tag, spelled the same: nothing to do
		}
		if _, err := removeTags(ctx, tx, r.fileID, tags.Search{Key: r.t.Key, Value: r.t.Value}, tagByRename,
			now); err != nil {
			return res, err
		}
		added, err := addTag(ctx, tx, r.fileID, nt, tagByRename, now)
		if err != nil {
			return res, err
		}
		changed[r.fileID] = true
		if !added {
			merged[r.fileID] = true
		}
	}
	// Respell the new tag where it is spelled otherwise (a rename that changes the case, or a file that had it).
	if !old.AnyValue {
		nt := target[rows[0].t.Folded()]
		others, err := queryAll(ctx, tx, func(rs *sql.Rows) (row, error) {
			var r row
			return r, rs.Scan(&r.fileID, &r.t.Key, &r.t.Value)
		}, `SELECT t.file_id, t.key, t.value FROM tags t JOIN files f ON f.id = t.file_id
			WHERE f.deleted_utc IS NULL AND t.key = ? AND t.folded = ? AND t.value <> ?`, nt.Key, nt.Folded(), nt.Value)
		if err != nil {
			return res, fmt.Errorf("read tag: %w", err)
		}
		for _, r := range others {
			if _, err := removeTags(ctx, tx, r.fileID, tags.Search{Key: r.t.Key, Value: r.t.Value}, tagByRename,
				now); err != nil {
				return res, err
			}
			if _, err := addTag(ctx, tx, r.fileID, nt, tagByRename, now); err != nil {
				return res, err
			}
			changed[r.fileID] = true
		}
	}
	res.Files, res.Merged = len(changed), len(merged)
	if res.Files == 0 {
		return res, nil
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("commit rename: %w", err)
	}
	return res, nil
}
