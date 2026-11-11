package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Known is one current path of a root, with its file.
type Known struct {
	PathID     int64
	FileID     int64
	Path       string // relative to the root, '/'-separated
	Kind       string // "file" | "symlink"
	Size       int64
	MtimeNS    int64
	Hash       string // "" = none
	Ino        uint64 // 0 = not known
	LinkTarget string
	LinkBroken bool
}

// Known returns every current path of a root.
func (s *Store) Known(ctx context.Context, rootID int64) ([]Known, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.file_id, p.path, f.kind, f.size, f.mtime_ns,
		       coalesce(f.hash, ''), coalesce(f.inode, 0), coalesce(f.link_target, ''), f.link_broken
		FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.root_id = ? AND p.valid_to_scan IS NULL`, rootID)
	if err != nil {
		return nil, fmt.Errorf("load known files: %w", err)
	}
	defer rows.Close()
	var out []Known
	for rows.Next() {
		var k Known
		var ino int64
		if err := rows.Scan(&k.PathID, &k.FileID, &k.Path, &k.Kind, &k.Size, &k.MtimeNS,
			&k.Hash, &ino, &k.LinkTarget, &k.LinkBroken); err != nil {
			return nil, fmt.Errorf("load known files: %w", err)
		}
		k.Ino = uint64(ino)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Gone is a file with no current path anywhere, as of before this scan.
type Gone struct {
	FileID       int64
	Hash         string
	Size         int64
	LastPath     string // its most recent path
	LastRootID   int64
	MissingSince int64 // the scan that closed that path
}

// MissingByHash returns the non-empty regular files with one of the given hashes that have no current path in any
// root, each with its most recent path.
func (s *Store) MissingByHash(ctx context.Context, hashes []string) ([]Gone, error) {
	var out []Gone
	const batch = 500
	for start := 0; start < len(hashes); start += batch {
		part := hashes[start:min(start+batch, len(hashes))]
		args := make([]any, len(part))
		for i, h := range part {
			args[i] = h
		}
		q := `
			SELECT f.id, f.hash, f.size, p.path, p.root_id, p.valid_to_scan
			FROM files f JOIN paths p ON p.file_id = f.id
			WHERE f.hash IN (?` + strings.Repeat(",?", len(part)-1) + `)
			  AND f.kind = 'file' AND f.size > 0
			  AND NOT EXISTS (SELECT 1 FROM paths c WHERE c.file_id = f.id AND c.valid_to_scan IS NULL)
			  AND p.valid_to_scan = (SELECT max(valid_to_scan) FROM paths l WHERE l.file_id = f.id)`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("find missing files: %w", err)
		}
		for rows.Next() {
			var g Gone
			if err := rows.Scan(&g.FileID, &g.Hash, &g.Size, &g.LastPath, &g.LastRootID, &g.MissingSince); err != nil {
				rows.Close()
				return nil, fmt.Errorf("find missing files: %w", err)
			}
			out = append(out, g)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("find missing files: %w", err)
		}
	}
	return out, nil
}

// ScanRecord describes the scan being applied.
type ScanRecord struct {
	RootID        int64
	Scope         string
	IncludeHidden bool
	Ignores       []string
	Started       time.Time
	FilesSeen     int
	NewCount      int
	ChangedCount  int
	MovedCount    int
	MissingCount  int
	ErrorCount    int
}

// FileUpdate sets every stored property of an existing file.
type FileUpdate struct {
	FileID     int64
	Size       int64
	MtimeNS    int64
	Hash       string // "" = none
	Ino        uint64 // 0 = not known
	LinkTarget string
	LinkBroken bool
}

// OpenPath gives an existing file a new current path in the scan's root.
type OpenPath struct {
	FileID int64
	Path   string
}

// NewFile is a file first seen in this scan, at Path in the scan's root.
type NewFile struct {
	Path       string
	Kind       string
	Size       int64
	MtimeNS    int64
	Hash       string // "" = none
	Ino        uint64 // 0 = not known
	LinkTarget string
	LinkBroken bool
}

// Event is one reported change. FileID 0 means no file.
type Event struct {
	Kind      string
	FileID    int64
	Path      string
	OtherPath string
	Detail    string
}

// Changes is everything one scan writes.
type Changes struct {
	Close   []int64 // path ids to close: missing files, moved-from paths, paths whose kind changed
	Updates []FileUpdate
	Open    []OpenPath
	New     []NewFile
	Events  []Event
}

// ApplyScan records a scan and its changes in one transaction. Paths are closed before any are opened, so a path
// freed by one move can be taken by another in the same scan. On any error nothing is written.
func (s *Store) ApplyScan(ctx context.Context, rec ScanRecord, ch Changes) (int64, error) {
	ignores, err := json.Marshal(rec.Ignores)
	if err != nil {
		return 0, err
	}
	if rec.Ignores == nil {
		ignores = []byte("[]")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `INSERT INTO scans (started_utc, root_id, scope, include_hidden, ignores)
		VALUES (?, ?, ?, ?, ?)`, utc(rec.Started), rec.RootID, rec.Scope, rec.IncludeHidden, string(ignores))
	if err != nil {
		return 0, fmt.Errorf("record scan: %w", err)
	}
	scanID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if err := eachStmt(ctx, tx, `UPDATE paths SET valid_to_scan = ? WHERE id = ? AND valid_to_scan IS NULL`,
		len(ch.Close), func(st *sql.Stmt, i int) error {
			r, err := st.ExecContext(ctx, scanID, ch.Close[i])
			if err != nil {
				return err
			}
			if n, err := r.RowsAffected(); err != nil || n != 1 {
				return fmt.Errorf("path %d was not current (affected %d, %v)", ch.Close[i], n, err)
			}
			return nil
		}); err != nil {
		return 0, fmt.Errorf("close paths: %w", err)
	}

	if err := eachStmt(ctx, tx, `UPDATE files SET size = ?, mtime_ns = ?, hash = ?, inode = ?, link_target = ?,
		link_broken = ? WHERE id = ?`, len(ch.Updates), func(st *sql.Stmt, i int) error {
		u := ch.Updates[i]
		_, err := st.ExecContext(ctx, u.Size, u.MtimeNS, nullString(u.Hash), nullIno(u.Ino), nullString(u.LinkTarget),
			u.LinkBroken, u.FileID)
		return err
	}); err != nil {
		return 0, fmt.Errorf("update files: %w", err)
	}

	openPath := `INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (?, ?, ?, ?)`
	if err := eachStmt(ctx, tx, openPath, len(ch.Open), func(st *sql.Stmt, i int) error {
		_, err := st.ExecContext(ctx, ch.Open[i].FileID, rec.RootID, ch.Open[i].Path, scanID)
		return err
	}); err != nil {
		return 0, fmt.Errorf("open paths: %w", err)
	}

	if len(ch.New) > 0 {
		insFile, err := tx.PrepareContext(ctx, `INSERT INTO files
			(kind, size, mtime_ns, hash, inode, link_target, link_broken, first_seen_scan) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return 0, err
		}
		defer insFile.Close()
		insPath, err := tx.PrepareContext(ctx, openPath)
		if err != nil {
			return 0, err
		}
		defer insPath.Close()
		for _, n := range ch.New {
			r, err := insFile.ExecContext(ctx, n.Kind, n.Size, n.MtimeNS, nullString(n.Hash), nullIno(n.Ino),
				nullString(n.LinkTarget), n.LinkBroken, scanID)
			if err != nil {
				return 0, fmt.Errorf("insert file %s: %w", n.Path, err)
			}
			id, err := r.LastInsertId()
			if err != nil {
				return 0, err
			}
			if _, err := insPath.ExecContext(ctx, id, rec.RootID, n.Path, scanID); err != nil {
				return 0, fmt.Errorf("insert path %s: %w", n.Path, err)
			}
		}
	}

	if err := eachStmt(ctx, tx, `INSERT INTO events (scan_id, kind, file_id, path, other_path, detail)
		VALUES (?, ?, ?, ?, ?, ?)`, len(ch.Events), func(st *sql.Stmt, i int) error {
		e := ch.Events[i]
		var fileID any
		if e.FileID != 0 {
			fileID = e.FileID
		}
		_, err := st.ExecContext(ctx, scanID, e.Kind, fileID, nullString(e.Path), nullString(e.OtherPath), nullString(e.Detail))
		return err
	}); err != nil {
		return 0, fmt.Errorf("record events: %w", err)
	}

	finished := time.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE scans SET finished_utc = ?, files_seen = ?, new_count = ?, changed_count = ?,
		moved_count = ?, missing_count = ?, error_count = ?, elapsed_ms = ? WHERE id = ?`,
		utc(finished), rec.FilesSeen, rec.NewCount, rec.ChangedCount, rec.MovedCount, rec.MissingCount, rec.ErrorCount,
		finished.Sub(rec.Started).Milliseconds(), scanID); err != nil {
		return 0, fmt.Errorf("finish scan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit scan: %w", err)
	}
	return scanID, nil
}

// eachStmt prepares q once and runs fn for each of n items.
func eachStmt(ctx context.Context, tx *sql.Tx, q string, n int, fn func(*sql.Stmt, int) error) error {
	if n == 0 {
		return nil
	}
	st, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer st.Close()
	for i := range n {
		if err := fn(st, i); err != nil {
			return err
		}
	}
	return nil
}

// nullIno stores an unknown inode (0) as NULL. SQLite integers are signed 64-bit; inode numbers fit in practice, and
// the bit pattern round-trips either way.
func nullIno(ino uint64) any {
	if ino == 0 {
		return nil
	}
	return int64(ino)
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
