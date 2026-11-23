package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// Tag index pages (docs/design.md, Tag index pages): an index declared on a directory of a root, and the files Filo
// wrote for it. Writing the pages is package tagindex's work; this file keeps the indexes and what was written.

var (
	// ErrIndexExists means the directory has an index of that name already.
	ErrIndexExists = errors.New("the directory has an index of that name already (filo indexes lists them)")
	// ErrIndexName means an index name Filo does not take.
	ErrIndexName = errors.New("an index name holds letters, digits, - and _, and starts with a letter or digit")
	// ErrNoIndex means no index with that ID.
	ErrNoIndex = errors.New("no such index (filo indexes lists them)")
)

// TagIndex is a tag index: its directory in a root, its name, and what was last written for it.
type TagIndex struct {
	ID         int64
	RootID     int64
	RootPath   string
	Dir        string // relative to the root, '/'-separated; "" for the root itself
	Name       string // NAME.html and .NAME/
	AddedUTC   string
	DataHash   string // the hash of the data last written in full; "" = never
	WrittenUTC string // when its pages were last brought up to date; "" = never
	Problem    string // why the last writing failed; "" = it did not
	// Files is every file and directory Filo wrote for it, by its path relative to Dir ('/'-separated): a file's
	// SHA-256 as written, "" for a directory.
	Files map[string]string
}

// Page is the index's main page, relative to its directory: NAME.html.
func (i TagIndex) Page() string { return i.Name + ".html" }

// PagesDir is the directory of its other pages, relative to its directory: .NAME.
func (i TagIndex) PagesDir() string { return "." + i.Name }

// Rel is a path relative to the index's directory made relative to its root.
func (i TagIndex) Rel(p string) string {
	if i.Dir == "" {
		return p
	}
	return path.Join(i.Dir, p)
}

// DirAbs is the index's directory, absolute.
func (i TagIndex) DirAbs() string { return absPath(i.RootPath, i.Dir) }

// Abs is a path relative to the index's directory, absolute.
func (i TagIndex) Abs(p string) string { return filepath.Join(i.DirAbs(), filepath.FromSlash(p)) }

// ValidIndexName reports whether Filo takes name for an index: letters, digits, - and _ (ASCII), starting with a
// letter or digit, so NAME.html and .NAME are plain names in every file system and URL.
func ValidIndexName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && (i == 0 || r != '-' && r != '_') {
			return false
		}
	}
	return true
}

// AddTagIndex records an index on dir (relative to the root, "" for the root itself) named name. A directory has
// each name once, in any case, since NAME.html and NAME.HTML are one file on some file systems.
func (s *Store) AddTagIndex(ctx context.Context, rootID int64, dir, name string, now time.Time) (TagIndex, error) {
	if err := checkRuleDir(dir); err != nil {
		return TagIndex{}, err
	}
	if !ValidIndexName(name) {
		return TagIndex{}, fmt.Errorf("%q: %w", name, ErrIndexName)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TagIndex{}, err
	}
	defer tx.Rollback()
	if err := checkRuleRoot(ctx, tx, rootID); err != nil {
		return TagIndex{}, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tag_indexes WHERE root_id = ? AND dir = ? AND lower(name) = ?`,
		rootID, dir, strings.ToLower(name)).Scan(&n); err != nil {
		return TagIndex{}, fmt.Errorf("read indexes: %w", err)
	}
	if n > 0 {
		return TagIndex{}, ErrIndexExists
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO tag_indexes (root_id, dir, name, added_utc) VALUES (?, ?, ?, ?)`,
		rootID, dir, name, utc(now))
	if err != nil {
		return TagIndex{}, fmt.Errorf("add index: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return TagIndex{}, err
	}
	if err := s.commit(tx); err != nil {
		return TagIndex{}, fmt.Errorf("add index: %w", err)
	}
	return s.TagIndex(ctx, id)
}

const tagIndexQuery = `
	SELECT ti.id, ti.root_id, r.path, ti.dir, ti.name, ti.added_utc, coalesce(ti.data_hash, ''),
	       coalesce(ti.written_utc, ''), coalesce(ti.problem, '')
	FROM tag_indexes ti JOIN roots r ON r.id = ti.root_id`

// TagIndexes returns every index, by root path, then directory and name, each with the files written for it.
func (s *Store) TagIndexes(ctx context.Context) ([]TagIndex, error) {
	return s.readTagIndexes(ctx, ``)
}

// TagIndex returns one index, with the files written for it.
func (s *Store) TagIndex(ctx context.Context, id int64) (TagIndex, error) {
	out, err := s.readTagIndexes(ctx, ` WHERE ti.id = ?`, id)
	if err != nil {
		return TagIndex{}, err
	}
	if len(out) == 0 {
		return TagIndex{}, ErrNoIndex
	}
	return out[0], nil
}

func (s *Store) readTagIndexes(ctx context.Context, cond string, args ...any) ([]TagIndex, error) {
	out, err := queryAll(ctx, s.db, func(rs *sql.Rows) (TagIndex, error) {
		var i TagIndex
		return i, rs.Scan(&i.ID, &i.RootID, &i.RootPath, &i.Dir, &i.Name, &i.AddedUTC, &i.DataHash, &i.WrittenUTC,
			&i.Problem)
	}, tagIndexQuery+cond+` ORDER BY r.path, ti.dir, ti.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	for k := range out {
		out[k].Files = map[string]string{}
	}
	pos := map[int64]int{}
	for k, i := range out {
		pos[i.ID] = k
	}
	type written struct {
		id         int64
		path, hash string
	}
	files, err := queryAll(ctx, s.db, func(rs *sql.Rows) (written, error) {
		var w written
		return w, rs.Scan(&w.id, &w.path, &w.hash)
	}, `SELECT index_id, path, coalesce(hash, '') FROM tag_index_files`)
	if err != nil {
		return nil, fmt.Errorf("read index files: %w", err)
	}
	for _, w := range files {
		if k, ok := pos[w.id]; ok {
			out[k].Files[w.path] = w.hash
		}
	}
	return out, nil
}

// IndexWrite is what writing an index's pages did: every file and directory of the index Filo now knows it wrote
// (the whole set, replacing the one recorded), and either the hash of the data written in full or why it failed.
type IndexWrite struct {
	Files    map[string]string // path relative to the index's directory: the file's SHA-256, "" for a directory
	DataHash string            // set when every page was written; "" when the writing failed
	Problem  string            // why it failed; "" when it did not
}

// RecordTagIndex records what writing an index's pages did, at now. A full write records its data hash and time and
// clears the problem; a failed one records the problem and clears the data hash, so the next run writes again, and
// keeps the time of the last full write. Either way the files are the ones written, so Filo knows its own.
func (s *Store) RecordTagIndex(ctx context.Context, id int64, w IndexWrite, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var res sql.Result
	if w.Problem == "" {
		res, err = tx.ExecContext(ctx, `UPDATE tag_indexes SET data_hash = ?, written_utc = ?, problem = NULL WHERE id = ?`,
			w.DataHash, utc(now), id)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE tag_indexes SET data_hash = NULL, problem = ? WHERE id = ?`, w.Problem, id)
	}
	if err != nil {
		return fmt.Errorf("record index: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNoIndex
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tag_index_files WHERE index_id = ?`, id); err != nil {
		return fmt.Errorf("record index files: %w", err)
	}
	for p, h := range w.Files {
		var hash any
		if h != "" {
			hash = h
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tag_index_files (index_id, path, hash) VALUES (?, ?, ?)`, id, p,
			hash); err != nil {
			return fmt.Errorf("record index files: %w", err)
		}
	}
	if err := s.commit(tx); err != nil {
		return fmt.Errorf("record index: %w", err)
	}
	return nil
}

// RemoveTagIndex removes an index and the record of its files from the register. Removing the files themselves is
// package tagindex's work, done first.
func (s *Store) RemoveTagIndex(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tag_index_files WHERE index_id = ?`, id); err != nil {
		return fmt.Errorf("remove index: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tag_indexes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove index: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return ErrNoIndex
	}
	if err := s.commit(tx); err != nil {
		return fmt.Errorf("remove index: %w", err)
	}
	return nil
}

// indexPaths returns the absolute paths of every index's main page and pages directory, which no scan records.
func (s *Store) indexPaths(ctx context.Context) ([]string, error) {
	type ix struct{ root, dir, name string }
	rows, err := queryAll(ctx, s.db, func(rs *sql.Rows) (ix, error) {
		var i ix
		return i, rs.Scan(&i.root, &i.dir, &i.name)
	}, `SELECT r.path, ti.dir, ti.name FROM tag_indexes ti JOIN roots r ON r.id = ti.root_id`)
	if err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	var out []string
	for _, i := range rows {
		t := TagIndex{RootPath: i.root, Dir: i.dir, Name: i.name}
		out = append(out, t.Abs(t.Page()), t.Abs(t.PagesDir()))
	}
	return out, nil
}

// IndexFile is one regular file present under an index's directory, as its pages list it.
type IndexFile struct {
	Rel      string // relative to the root, '/'-separated
	MtimeNS  int64
	FileDate string // YYYY-MM-DD; "" = none
	Tags     []tags.Tag
}

// IndexFiles returns the regular files present at dir and below in a root ("" for all of it), each path a file of
// its own (a hard link too), with its file_date and tags, ordered by path (docs/design.md, Tag index pages, "The
// files"). Missing files, files deleted by filo and symlinks are not among them.
func (s *Store) IndexFiles(ctx context.Context, rootID int64, dir string) ([]IndexFile, error) {
	fs, err := s.FindFiles(ctx, rootID, dir)
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
	dates := map[int64]string{}
	err = inBatches(ids, func(in string, args []any) error {
		rows, err := s.db.QueryContext(ctx, `SELECT id, file_date FROM files WHERE file_date IS NOT NULL AND id IN (`+in+`)`,
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var d string
			if err := rows.Scan(&id, &d); err != nil {
				return err
			}
			dates[id] = d
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read file dates: %w", err)
	}
	out := make([]IndexFile, len(fs))
	for i, f := range fs {
		out[i] = IndexFile{Rel: f.Rel, MtimeNS: f.MtimeNS, FileDate: dates[f.FileID], Tags: ts[f.FileID]}
	}
	return out, nil
}
