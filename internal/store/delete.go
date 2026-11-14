package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event kinds a delete run records, besides 'changed' and 'error'.
const (
	EventDeleted = "deleted"
	// EventSameAsDeleted is a scan's hint that a new file has the content of a file filo deleted (restored from a
	// backup, say). The rows stay separate: the new file needs a retention of its own.
	EventSameAsDeleted = "same_as_deleted"
)

// Candidate is a deletion candidate (a present file whose status is expired) with what the check at the moment of
// deleting needs.
type Candidate struct {
	FileItem
	RootID       int64
	PathID       int64
	CtimeNS      int64  // 0 = not known
	Ino          uint64 // 0 = not known
	LinkTarget   string // symlinks
	ReviewedHash string // the content its retention was set on (for a duplicate copy: the content it was marked on)
	DuplicateOf  string // a duplicate copy being deleted: the copy kept, absolute; "" otherwise
}

// Candidates returns the deletion candidates under the filter, ordered by root and path. today is the local date.
func (s *Store) Candidates(ctx context.Context, today string, f Filter) ([]Candidate, error) {
	items, err := s.Files(ctx, today, f)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, it := range items {
		if it.Status != StatusExpired {
			continue
		}
		c := Candidate{FileItem: it}
		var ino int64
		err := s.db.QueryRowContext(ctx, `
			SELECT p.id, p.root_id, f.ctime_ns, coalesce(f.inode, 0), coalesce(f.link_target, ''),
			       coalesce(f.reviewed_hash, '')
			FROM paths p JOIN files f ON f.id = p.file_id
			WHERE p.file_id = ? AND p.path = ? AND p.valid_to_scan IS NULL`, it.FileID, it.Path).
			Scan(&c.PathID, &c.RootID, &c.CtimeNS, &ino, &c.LinkTarget, &c.ReviewedHash)
		if err != nil {
			return nil, fmt.Errorf("read candidate %s: %w", it.AbsPath(), err)
		}
		c.Ino = uint64(ino)
		out = append(out, c)
	}
	return out, nil
}

// DeleteRun records what one run of filo delete did. Each outcome is written in its own transaction as soon as it
// happens, so an interrupted run leaves an accurate record. In each root it touches, the run is recorded as a scan of
// kind 'delete', which the deleted files' paths are closed at and the run's events belong to. Finish closes those.
type DeleteRun struct {
	s       *Store
	started time.Time
	roots   map[int64]*runRoot
}

type runRoot struct {
	scanID                  int64
	checked, changed, error int
}

// BeginDeleteRun starts recording a delete run. Nothing is written until the first outcome.
func (s *Store) BeginDeleteRun(started time.Time) *DeleteRun {
	return &DeleteRun{s: s, started: started, roots: map[int64]*runRoot{}}
}

// root returns the run's record in a root, creating its scan row on first use.
func (r *DeleteRun) root(ctx context.Context, rootID int64) (*runRoot, error) {
	if rr, ok := r.roots[rootID]; ok {
		return rr, nil
	}
	res, err := r.s.exec(ctx, `INSERT INTO scans (started_utc, root_id, kind) VALUES (?, ?, 'delete')`,
		utc(r.started), rootID)
	if err != nil {
		return nil, fmt.Errorf("record delete run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	rr := &runRoot{scanID: id}
	r.roots[rootID] = rr
	return rr, nil
}

// record runs fn in one transaction with the run's scan in c's root.
func (r *DeleteRun) record(ctx context.Context, c Candidate, fn func(tx *sql.Tx, scanID int64) error) (*runRoot, error) {
	rr, err := r.root(ctx, c.RootID)
	if err != nil {
		return nil, err
	}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := fn(tx, rr.scanID); err != nil {
		return nil, err
	}
	if err := r.s.commit(tx); err != nil {
		return nil, err
	}
	rr.checked++
	return rr, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, scanID int64, kind string, fileID int64, path, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events (scan_id, kind, file_id, path, detail) VALUES (?, ?, ?, ?, ?)`,
		scanID, kind, fileID, nullString(path), nullString(detail))
	return err
}

// Deleted records that c was deleted at now: the file is marked deleted, its path closed and a deleted event
// recorded. The row stays, with its last path, hash, size and retention: it is the history of what filo deleted.
func (r *DeleteRun) Deleted(ctx context.Context, c Candidate, now time.Time) error {
	_, err := r.record(ctx, c, func(tx *sql.Tx, scanID int64) error {
		if _, err := tx.ExecContext(ctx, `UPDATE files SET deleted_utc = ? WHERE id = ? AND deleted_utc IS NULL`,
			utc(now), c.FileID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE paths SET valid_to_scan = ? WHERE id = ? AND valid_to_scan IS NULL`,
			scanID, c.PathID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("path %d was not current (affected %d, %v)", c.PathID, n, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_marks WHERE file_id = ?`, c.FileID); err != nil {
			return err
		}
		detail := "retention " + c.Retention
		if c.KeepUntil != c.Retention {
			detail += ", kept until " + c.KeepUntil
		}
		if c.DuplicateOf != "" {
			detail = "duplicate; the copy kept is " + c.DuplicateOf
		}
		return insertEvent(ctx, tx, scanID, EventDeleted, c.FileID, c.Path, detail)
	})
	if err != nil {
		return fmt.Errorf("record the deletion of %s: %w", c.AbsPath(), err)
	}
	return nil
}

// Changed records that c's content was found changed when it was re-read: its new properties are stored as a scan
// would store them, so it needs a new retention.
func (r *DeleteRun) Changed(ctx context.Context, c Candidate, u FileUpdate) error {
	rr, err := r.record(ctx, c, func(tx *sql.Tx, scanID int64) error {
		if _, err := tx.ExecContext(ctx, `UPDATE files SET size = ?, mtime_ns = ?, ctime_ns = ?, hash = ?, inode = ?,
			unreadable_since_scan = NULL WHERE id = ?`, u.Size, u.MtimeNS, u.CtimeNS, nullString(u.Hash),
			nullIno(u.Ino), c.FileID); err != nil {
			return err
		}
		return insertEvent(ctx, tx, scanID, "changed", c.FileID, c.Path, "content, found by filo delete")
	})
	if err != nil {
		return fmt.Errorf("record the change of %s: %w", c.AbsPath(), err)
	}
	rr.changed++
	return nil
}

// Unreadable records that c could not be read: it is flagged unreadable as a scan would flag it, so it is not a
// candidate again until a scan reads it.
func (r *DeleteRun) Unreadable(ctx context.Context, c Candidate, why string) error {
	rr, err := r.record(ctx, c, func(tx *sql.Tx, scanID int64) error {
		if _, err := tx.ExecContext(ctx, `UPDATE files SET unreadable_since_scan = coalesce(unreadable_since_scan, ?)
			WHERE id = ?`, scanID, c.FileID); err != nil {
			return err
		}
		return insertEvent(ctx, tx, scanID, "error", c.FileID, c.Path, why)
	})
	if err != nil {
		return fmt.Errorf("record that %s is unreadable: %w", c.AbsPath(), err)
	}
	rr.error++
	return nil
}

// Finish marks the run's scans finished. Call it however the run ended.
func (r *DeleteRun) Finish(ctx context.Context, finished time.Time) error {
	var errs []error
	for _, rr := range r.roots {
		if _, err := r.s.exec(ctx, `UPDATE scans SET finished_utc = ?, files_seen = ?, new_count = 0,
			changed_count = ?, moved_count = 0, missing_count = 0, error_count = ?, elapsed_ms = ? WHERE id = ?`,
			utc(finished), rr.checked, rr.changed, rr.error, finished.Sub(r.started).Milliseconds(),
			rr.scanID); err != nil {
			errs = append(errs, fmt.Errorf("finish delete run: %w", err))
		}
	}
	return errors.Join(errs...)
}

// SetRetention sets one present file's retention, from filo delete's prompt. It keeps the file's file_date and
// works keep_until out again; the retention covers the file's current content, which for a candidate is the content
// its last retention was set on. It returns the new keep_until.
func (s *Store) SetRetention(ctx context.Context, fileID int64, retention string, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var fileDate string
	var hash sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT coalesce(f.file_date, ''), f.hash FROM files f
		WHERE f.id = ? AND f.deleted_utc IS NULL
		  AND EXISTS (SELECT 1 FROM paths p WHERE p.file_id = f.id AND p.valid_to_scan IS NULL)`, fileID).
		Scan(&fileDate, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("file %d is no longer present", fileID)
	}
	if err != nil {
		return "", fmt.Errorf("read file %d: %w", fileID, err)
	}
	keep, err := KeepUntil(fileDate, retention)
	if err != nil {
		return "", err
	}
	after, err := latestScan(ctx, tx)
	if err != nil {
		return "", err
	}
	batch, err := newBatch(ctx, tx, now, "delete", "filo delete")
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, file_date, retention, keep_until, hash)
		VALUES (?, ?, ?, ?, ?, ?)`, batch, fileID, nullString(fileDate), nullString(retention), nullString(keep),
		hash); err != nil {
		return "", fmt.Errorf("record retention: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET retention = ?, keep_until = ?, reviewed_utc = ?,
		reviewed_after_scan = ?, reviewed_hash = ? WHERE id = ?`, nullString(retention), nullString(keep), utc(now),
		nullInt(after), hash, fileID); err != nil {
		return "", fmt.Errorf("record retention: %w", err)
	}
	if err := s.commit(tx); err != nil {
		return "", fmt.Errorf("record retention: %w", err)
	}
	return keep, nil
}

// DeletedTotals is what filo has deleted, in all and in its last run that deleted anything.
type DeletedTotals struct {
	Files, Bytes         int64
	LastRunUTC           string // when that run started; "" = none
	LastFiles, LastBytes int64
}

// Deleted returns the totals of the files filo deleted. Bytes count regular files only.
func (s *Store) Deleted(ctx context.Context) (DeletedTotals, error) {
	var d DeletedTotals
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(CASE WHEN kind = 'file' THEN size END), 0)
		FROM files WHERE deleted_utc IS NOT NULL`).Scan(&d.Files, &d.Bytes); err != nil {
		return d, fmt.Errorf("deleted files: %w", err)
	}
	if d.Files == 0 {
		return d, nil
	}
	err := s.db.QueryRowContext(ctx, `SELECT sc.started_utc FROM events e JOIN scans sc ON sc.id = e.scan_id
		WHERE e.kind = 'deleted' ORDER BY e.id DESC LIMIT 1`).Scan(&d.LastRunUTC)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, fmt.Errorf("last delete run: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(CASE WHEN f.kind = 'file' THEN f.size END), 0)
		FROM events e JOIN scans sc ON sc.id = e.scan_id JOIN files f ON f.id = e.file_id
		WHERE e.kind = 'deleted' AND sc.kind = 'delete' AND sc.started_utc = ?`, d.LastRunUTC).
		Scan(&d.LastFiles, &d.LastBytes); err != nil {
		return d, fmt.Errorf("last delete run: %w", err)
	}
	return d, nil
}

// DeletedByHash returns, for each of the given hashes held by a file filo deleted, when the latest such file was
// deleted (RFC 3339, UTC).
func (s *Store) DeletedByHash(ctx context.Context, hashes []string) (map[string]string, error) {
	out := map[string]string{}
	const batch = 500
	for start := 0; start < len(hashes); start += batch {
		part := hashes[start:min(start+batch, len(hashes))]
		args := make([]any, len(part))
		for i, h := range part {
			args[i] = h
		}
		rows, err := s.db.QueryContext(ctx, `SELECT hash, max(deleted_utc) FROM files
			WHERE deleted_utc IS NOT NULL AND kind = 'file' AND hash IN (?`+strings.Repeat(",?", len(part)-1)+`)
			GROUP BY hash`, args...)
		if err != nil {
			return nil, fmt.Errorf("find deleted files: %w", err)
		}
		for rows.Next() {
			var h, when string
			if err := rows.Scan(&h, &when); err != nil {
				rows.Close()
				return nil, fmt.Errorf("find deleted files: %w", err)
			}
			out[h] = when
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("find deleted files: %w", err)
		}
	}
	return out, nil
}
