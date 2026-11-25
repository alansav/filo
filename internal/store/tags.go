package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// Commands that change tags, as tag_changes records them.
const (
	tagByTag   = "tag"
	tagByUntag = "untag"
	tagByScan  = "scan" // a new file took its copies' tags
)

// EventCopyTags is a scan's note that a new file took the tags of copies whose tags differed (docs/design.md, Tags,
// "A copy keeps the tags"): its detail names the tags it took.
const EventCopyTags = "copy_tags"

// FileTags returns the tags of each of the given files that has any, in the order filo tags lists them: bare tags
// first, then by key and by folded value.
func (s *Store) FileTags(ctx context.Context, ids []int64) (map[int64][]tags.Tag, error) {
	out := map[int64][]tags.Tag{}
	err := inBatches(ids, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT file_id, key, value FROM tags WHERE file_id IN (`+in+`)
			ORDER BY file_id, key, folded`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var t tags.Tag
			if err := rows.Scan(&id, &t.Key, &t.Value); err != nil {
				return err
			}
			out[id] = append(out[id], t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	return out, nil
}

// Copy is a file present with a content: one of its current paths, and its tags.
type Copy struct {
	FileID   int64
	RootID   int64
	RootPath string
	Path     string // relative to the root; the first current path, should it have several (hard links)
	Tags     []tags.Tag
}

// AbsPath is the copy's absolute path.
func (c Copy) AbsPath() string { return absPath(c.RootPath, c.Path) }

// Copies returns, for each of the given hashes, the regular files with that content that are present (in any root),
// not empty and not deleted by filo, each once with its tags: the copies a new file with that content takes its tags
// from (docs/design.md, Tags, "A copy keeps the tags"). Files with no tag are among them.
func (s *Store) Copies(ctx context.Context, hashes []string) (map[string][]Copy, error) {
	var all []Copy
	var hashOf []string
	err := inBatches(hashes, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `
			SELECT f.id, f.hash, p.root_id, r.path, min(p.path) FROM files f
			JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL JOIN roots r ON r.id = p.root_id
			WHERE f.hash IN (`+in+`) AND f.kind = 'file' AND f.size > 0 AND f.deleted_utc IS NULL
			GROUP BY f.id ORDER BY f.id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Copy
			var h string
			if err := rows.Scan(&c.FileID, &h, &c.RootID, &c.RootPath, &c.Path); err != nil {
				return err
			}
			all, hashOf = append(all, c), append(hashOf, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("find copies: %w", err)
	}
	ids := make([]int64, len(all))
	for i, c := range all {
		ids[i] = c.FileID
	}
	ts, err := s.FileTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[string][]Copy{}
	for i, c := range all {
		c.Tags = ts[c.FileID]
		out[hashOf[i]] = append(out[hashOf[i]], c)
	}
	return out, nil
}

// Respelled is a tag typed in another case than the one it has already: the tag is given its first spelling.
type Respelled struct {
	Typed, Spelled tags.Tag
}

// TagResult is what TagFiles or UntagFiles did.
type TagResult struct {
	Changed   map[int64]bool // the files that gained or lost a tag
	Respelled []Respelled    // tags given the spelling they have already, not the one typed (TagFiles only)
}

// TagFiles gives each file every tag in add that it lacks, all in one transaction, each with the spelling the tag has
// already on a file not deleted by filo, or the one typed if it is new (docs/design.md, Tags, "Spelling"). A tag the
// file has already, in any case, is left alone; a tag given twice is one. The files are files filo has not deleted;
// the caller checks.
func (s *Store) TagFiles(ctx context.Context, ids []int64, add []tags.Tag, now time.Time) (TagResult, error) {
	res := TagResult{Changed: map[int64]bool{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	if err := checkNotDeleted(ctx, tx, ids); err != nil {
		return res, err
	}
	var spelled []tags.Tag
	for _, t := range add {
		if i := slices.IndexFunc(spelled, t.Same); i >= 0 {
			if spelled[i].Value != t.Value { // typed twice, in two spellings: the first is kept
				res.Respelled = append(res.Respelled, Respelled{Typed: t, Spelled: spelled[i]})
			}
			continue
		}
		sp, err := spelling(ctx, tx, t)
		if err != nil {
			return res, err
		}
		if sp.Value != t.Value {
			res.Respelled = append(res.Respelled, Respelled{Typed: t, Spelled: sp})
		}
		spelled = append(spelled, sp)
	}
	for _, id := range ids {
		for _, t := range spelled {
			added, err := addTag(ctx, tx, id, t, tagByTag, now)
			if err != nil {
				return res, err
			}
			if added {
				res.Changed[id] = true
			}
		}
	}
	if len(res.Changed) == 0 {
		return res, nil // nothing written: no snapshot
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("commit tags: %w", err)
	}
	return res, nil
}

// UntagFiles removes from each file every tag that one of remove names: a tag in any case, or every value of a key
// (AnyValue). Wildcards are not taken: the caller reads removals with tags.ParseRemoval.
func (s *Store) UntagFiles(ctx context.Context, ids []int64, remove []tags.Search, now time.Time) (TagResult, error) {
	res := TagResult{Changed: map[int64]bool{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	if err := checkNotDeleted(ctx, tx, ids); err != nil {
		return res, err
	}
	for _, id := range ids {
		for _, q := range remove {
			n, err := removeTags(ctx, tx, id, q, tagByUntag, now)
			if err != nil {
				return res, err
			}
			if n > 0 {
				res.Changed[id] = true
			}
		}
	}
	if len(res.Changed) == 0 {
		return res, nil
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("commit tags: %w", err)
	}
	return res, nil
}

// spelling is t with the spelling its tag has already on a file filo has not deleted, or t as it is if no such file
// has it. Should two spellings be stored (a tag added while its only other file was deleted), the earliest wins.
func spelling(ctx context.Context, q querier, t tags.Tag) (tags.Tag, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT t.value FROM tags t JOIN files f ON f.id = t.file_id
		WHERE t.key = ? AND t.folded = ? AND f.deleted_utc IS NULL ORDER BY t.rowid LIMIT 1`, t.Key, t.Folded()).
		Scan(&value)
	switch {
	case err == sql.ErrNoRows:
		return t, nil
	case err != nil:
		return t, fmt.Errorf("read tag %s: %w", t, err)
	}
	return tags.Tag{Key: t.Key, Value: value}, nil
}

// addTag gives a file a tag, spelled as t is, unless it has it already in any case, and records the change. It
// reports whether the tag was added.
func addTag(ctx context.Context, tx execer, fileID int64, t tags.Tag, command string, now time.Time) (bool, error) {
	r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags (file_id, key, value, folded, added_utc)
		VALUES (?, ?, ?, ?, ?)`, fileID, t.Key, t.Value, t.Folded(), utc(now))
	if err != nil {
		return false, fmt.Errorf("tag file %d with %s: %w", fileID, t, err)
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	return true, recordTagChange(ctx, tx, "add", fileID, t, command, now)
}

// removeTags removes from a file the tags q names (q.Value matched ignoring case, never as a pattern) and records
// each change. It returns how many it removed.
func removeTags(ctx context.Context, tx execer, fileID int64, q tags.Search, command string, now time.Time) (int,
	error) {
	cond, args := ` AND folded = ?`, []any{fileID, q.Key, tags.Fold(q.Value)}
	if q.AnyValue {
		cond, args = "", args[:2]
	}
	gone, err := queryAll(ctx, tx, func(rows *sql.Rows) (tags.Tag, error) {
		var t tags.Tag
		return t, rows.Scan(&t.Key, &t.Value)
	}, `DELETE FROM tags WHERE file_id = ? AND key = ?`+cond+` RETURNING key, value`, args...)
	if err != nil {
		return 0, fmt.Errorf("untag file %d: %w", fileID, err)
	}
	for _, t := range gone {
		if err := recordTagChange(ctx, tx, "remove", fileID, t, command, now); err != nil {
			return 0, err
		}
	}
	return len(gone), nil
}

// recordTagChange adds a row to the tags' history.
func recordTagChange(ctx context.Context, tx execer, kind string, fileID int64, t tags.Tag, command string,
	now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO tag_changes (kind, file_id, key, value, command, changed_utc)
		VALUES (?, ?, ?, ?, ?, ?)`, kind, fileID, t.Key, t.Value, command, utc(now)); err != nil {
		return fmt.Errorf("record tag change: %w", err)
	}
	return nil
}

// checkNotDeleted refuses ids that name no file, or a file filo deleted, whose tags no listing shows.
func checkNotDeleted(ctx context.Context, tx *sql.Tx, ids []int64) error {
	return inBatches(ids, func(in string, args []any) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM files WHERE id IN (`+in+`) AND deleted_utc IS NULL`,
			args...).Scan(&n); err != nil {
			return fmt.Errorf("check files: %w", err)
		}
		if n != len(uniq(args)) {
			return fmt.Errorf("tag files: a file is not in the register or was deleted by filo")
		}
		return nil
	})
}

// uniq is args with each value once.
func uniq(args []any) map[any]bool {
	m := make(map[any]bool, len(args))
	for _, a := range args {
		m[a] = true
	}
	return m
}
