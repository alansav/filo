package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Events returns a scan's events in the order they were recorded.
func (s *Store) Events(ctx context.Context, scanID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, coalesce(file_id, 0), coalesce(path, ''), coalesce(other_path, ''),
		coalesce(detail, '') FROM events WHERE scan_id = ? ORDER BY id`, scanID)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Kind, &e.FileID, &e.Path, &e.OtherPath, &e.Detail); err != nil {
			return nil, fmt.Errorf("read events: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Interval is one location in a file's history. To is 0 while it is the current path.
type Interval struct {
	RootID int64
	Path   string
	From   int64
	To     int64
}

// History returns a file's path intervals, oldest first.
func (s *Store) History(ctx context.Context, fileID int64) ([]Interval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT root_id, path, valid_from_scan, coalesce(valid_to_scan, 0)
		FROM paths WHERE file_id = ? ORDER BY valid_from_scan, id`, fileID)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer rows.Close()
	var out []Interval
	for rows.Next() {
		var iv Interval
		if err := rows.Scan(&iv.RootID, &iv.Path, &iv.From, &iv.To); err != nil {
			return nil, fmt.Errorf("read history: %w", err)
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// ScanInfo is a recorded scan's summary row.
type ScanInfo struct {
	ID, RootID                                              int64
	Scope                                                   string
	FilesSeen, New, Changed, Moved, Missing, Errors, MsTook int64
	Finished                                                bool
	StartedUTC, FinishedUTC                                 string
}

// Scan returns one recorded scan, or false if there is none with that id.
func (s *Store) Scan(ctx context.Context, id int64) (ScanInfo, bool, error) {
	var si ScanInfo
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, root_id, scope, coalesce(files_seen, 0), coalesce(new_count, 0),
		coalesce(changed_count, 0), coalesce(moved_count, 0), coalesce(missing_count, 0), coalesce(error_count, 0),
		coalesce(elapsed_ms, 0), finished_utc, started_utc FROM scans WHERE id = ?`, id).Scan(&si.ID, &si.RootID, &si.Scope,
		&si.FilesSeen, &si.New, &si.Changed, &si.Moved, &si.Missing, &si.Errors, &si.MsTook, &finished, &si.StartedUTC)
	if err == sql.ErrNoRows {
		return ScanInfo{}, false, nil
	}
	if err != nil {
		return ScanInfo{}, false, fmt.Errorf("read scan %d: %w", id, err)
	}
	si.Finished = finished.Valid
	si.FinishedUTC = finished.String
	return si, true, nil
}

// RowCounts returns the number of rows in each register table.
func (s *Store) RowCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range []string{"roots", "scans", "files", "paths", "events"} {
		var n int64
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}
