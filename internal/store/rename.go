package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"
)

// ScanRename is the kind of scan an update's renames are recorded under in a root (docs/design.md, Tags, "Renaming
// a file by its name cell").
const ScanRename = "rename"

// renamedDetail is a rename's moved event detail.
const renamedDetail = "renamed by filo update"

// RenameRun records the renames one run of filo update makes. Each is written in its own transaction straight after
// the file is renamed, so a run cut off part-way leaves an accurate record. Each root it renames in gets one scan of
// kind rename, created on its first rename; Finish finishes them.
type RenameRun struct {
	s       *Store
	started time.Time
	scans   map[int64]int64 // root -> the run's scan in it
	renamed map[int64]int   // root -> renames recorded
}

// BeginRenameRun starts recording an update's renames. Nothing is written until the first.
func (s *Store) BeginRenameRun(started time.Time) *RenameRun {
	return &RenameRun{s: s, started: started, scans: map[int64]int64{}, renamed: map[int64]int{}}
}

// Renamed records that c was renamed to name in its own directory: its path is closed at the run's scan and the new
// one opened on the same file row, so its retention, tags and history stay with it; a moved event says it was renamed.
func (r *RenameRun) Renamed(ctx context.Context, c Candidate, name string, now time.Time) error {
	scanID, ok := r.scans[c.RootID]
	if !ok {
		res, err := r.s.exec(ctx, `INSERT INTO scans (started_utc, root_id, kind) VALUES (?, ?, ?)`, utc(r.started),
			c.RootID, ScanRename)
		if err != nil {
			return fmt.Errorf("record rename run: %w", err)
		}
		if scanID, err = res.LastInsertId(); err != nil {
			return err
		}
		r.scans[c.RootID] = scanID
	}
	newPath := path.Join(path.Dir(c.Path), name)
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE paths SET valid_to_scan = ? WHERE id = ? AND valid_to_scan IS NULL`,
		scanID, c.PathID)
	if err != nil {
		return fmt.Errorf("record the rename of %s: %w", c.AbsPath(), err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("record the rename of %s: path %d was not current (affected %d, %v)", c.AbsPath(),
			c.PathID, n, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (?, ?, ?, ?)`,
		c.FileID, c.RootID, newPath, scanID); err != nil {
		return fmt.Errorf("record the rename of %s: %w", c.AbsPath(), err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events (scan_id, kind, file_id, path, other_path, detail)
		VALUES (?, 'moved', ?, ?, ?, ?)`, scanID, c.FileID, newPath, c.Path, renamedDetail); err != nil {
		return fmt.Errorf("record the rename of %s: %w", c.AbsPath(), err)
	}
	if err := r.s.commit(tx); err != nil {
		return fmt.Errorf("record the rename of %s: %w", c.AbsPath(), err)
	}
	r.renamed[c.RootID]++
	return nil
}

// Finish finishes the run's scans, counting the files each renamed as seen and moved.
func (r *RenameRun) Finish(ctx context.Context, finished time.Time) error {
	var errs []error
	for root, id := range r.scans {
		n := r.renamed[root]
		if _, err := r.s.exec(ctx, `UPDATE scans SET finished_utc = ?, files_seen = ?, new_count = 0,
			changed_count = 0, moved_count = ?, missing_count = 0, error_count = 0, elapsed_ms = ? WHERE id = ?`,
			utc(finished), n, n, finished.Sub(r.started).Milliseconds(), id); err != nil {
			errs = append(errs, fmt.Errorf("finish rename run: %w", err))
		}
	}
	return errors.Join(errs...)
}

// renameCandidate is the file a row asks to rename, as the register knows it at its current path, for the check
// before the question and the rename itself.
func renameCandidate(ctx context.Context, q querier, fileID int64) (Candidate, bool, error) {
	var c Candidate
	var ino int64
	err := q.QueryRowContext(ctx, `SELECT f.id, p.id, p.root_id, r.path, p.path, f.kind, f.size, f.mtime_ns,
		coalesce(f.inode, 0), coalesce(f.link_target, '')
		FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL JOIN roots r ON r.id = p.root_id
		WHERE f.id = ? AND f.deleted_utc IS NULL`, fileID).Scan(&c.FileID, &c.PathID, &c.RootID, &c.RootPath, &c.Path,
		&c.Kind, &c.Size, &c.MtimeNS, &ino, &c.LinkTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("read file %d: %w", fileID, err)
	}
	c.Ino = uint64(ino)
	return c, true, nil
}
