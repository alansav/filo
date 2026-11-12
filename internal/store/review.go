package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Status is where a present file stands in its lifespan. A file has one status: the first that applies, in the
// order of Statuses.
type Status string

const (
	// StatusUnreadable: flagged unreadable and not decided on since the scan that flagged it, or flagged and past its
	// keep_until (its content cannot be checked).
	StatusUnreadable Status = "unreadable"
	// StatusPossibleMove: the new side of a possible move recorded after its last decision.
	StatusPossibleMove Status = "possible_move"
	// StatusUndated: it has no keep_until.
	StatusUndated Status = "undated"
	// StatusChanged: past its keep_until, but its content is not the content the date was set on (or the date was
	// set without one), so it needs a new decision.
	StatusChanged Status = "changed"
	// StatusExpired: past its keep_until and unchanged: a deletion candidate. Never deleted automatically.
	StatusExpired Status = "expired"
	// StatusKept: kept until a date after today.
	StatusKept Status = "kept"
	// StatusForever: kept indefinitely. Never a deletion candidate, however old it is or however often it changes.
	StatusForever Status = "forever"
)

// Statuses lists every status in priority order.
var Statuses = []Status{StatusUnreadable, StatusPossibleMove, StatusUndated, StatusForever, StatusChanged,
	StatusExpired, StatusKept}

// NeedsDecision reports whether a file with this status needs a keep_until decision.
func (s Status) NeedsDecision() bool {
	return s == StatusUnreadable || s == StatusPossibleMove || s == StatusUndated || s == StatusChanged
}

// FileItem is one present file with its lifespan.
type FileItem struct {
	FileID      int64
	RootPath    string // absolute
	Path        string // relative to the root
	Kind        string
	Size        int64
	MtimeNS     int64
	Hash        string // "" = none
	FileDate    string // YYYY-MM-DD the retention counts from, "" = none
	Retention   string // as given: a period (7y), Forever or YYYY-MM-DD; "" = none
	KeepUntil   string // worked out from the two: YYYY-MM-DD, Forever, or "" = none
	ReviewedUTC string // when the current retention was set; "" = never
	Status      Status

	UnreadableSince    int64  // the scan that flagged it, 0 = not flagged
	UnreadableSinceUTC string // when that scan finished
	MoveFrom           string // possible move: the absolute path it may have come from
	MoveDetail         string // possible move: "same name" or "same name, different content"
}

// AbsPath is the item's absolute path.
func (f FileItem) AbsPath() string { return absPath(f.RootPath, f.Path) }

// Filter limits a listing to one root, and optionally to a directory inside it.
type Filter struct {
	RootID int64  // 0 = every root
	Scope  string // relative to the root, '/' separators; "" = the whole root
}

// scopeSQL returns a condition on alias p (paths) for the filter, and its arguments.
func (f Filter) scopeSQL() (string, []any) {
	if f.RootID == 0 {
		return "", nil
	}
	if f.Scope == "" {
		return " AND p.root_id = ?", []any{f.RootID}
	}
	// '0' sorts right after '/', so this range is every path below the scope (byte-wise, as BINARY compares).
	return " AND p.root_id = ? AND (p.path = ? OR (p.path >= ? AND p.path < ?))",
		[]any{f.RootID, f.Scope, f.Scope + "/", f.Scope + "0"}
}

// fileRow is a present file with everything classify needs.
type fileRow struct {
	item           FileItem
	reviewedAfter  int64 // reviewed_after_scan, 0 = none
	reviewedHash   sql.NullString
	possibleMoveIn int64 // the latest scan that recorded it as the new side of a possible move, 0 = none
}

// classify returns the file's status. today is YYYY-MM-DD.
func classify(r fileRow, today string) Status {
	it := r.item
	if it.UnreadableSince != 0 && r.reviewedAfter < it.UnreadableSince {
		return StatusUnreadable
	}
	if r.possibleMoveIn != 0 && r.reviewedAfter < r.possibleMoveIn {
		return StatusPossibleMove
	}
	if it.KeepUntil == "" {
		return StatusUndated
	}
	if it.KeepUntil == Forever {
		return StatusForever
	}
	if it.KeepUntil > today {
		return StatusKept
	}
	// Past its date. A file still flagged unreadable has a stale hash, so its content cannot be checked.
	if it.UnreadableSince != 0 {
		return StatusUnreadable
	}
	// The date covers the content it was set on. A regular file whose hash differs, or that had none, needs a new
	// decision rather than becoming a candidate.
	if it.Kind == "file" && (it.Hash == "" || !r.reviewedHash.Valid || r.reviewedHash.String != it.Hash) {
		return StatusChanged
	}
	return StatusExpired
}

// Files returns every present file (regular files and symlinks) under the filter with its status, ordered by root
// and path. today is the local date, YYYY-MM-DD.
func (s *Store) Files(ctx context.Context, today string, f Filter) ([]FileItem, error) {
	cond, args := f.scopeSQL()
	// A possible move event names the old file; the new file is the one whose path opened at e.path in that scan.
	rows, err := s.db.QueryContext(ctx, `
		WITH pm AS (
			SELECT np.file_id AS file_id, max(e.id) AS event_id
			FROM events e
			JOIN scans sc ON sc.id = e.scan_id
			JOIN paths np ON np.root_id = sc.root_id AND np.path = e.path AND np.valid_from_scan = e.scan_id
			WHERE e.kind = 'possible_move'
			GROUP BY np.file_id)
		SELECT f.id, r.path, p.path, f.kind, f.size, f.mtime_ns, coalesce(f.hash, ''),
		       coalesce(f.file_date, ''), coalesce(f.retention, ''), coalesce(f.keep_until, ''),
		       coalesce(f.reviewed_utc, ''), coalesce(f.reviewed_after_scan, 0),
		       f.reviewed_hash, coalesce(f.unreadable_since_scan, 0), coalesce(us.finished_utc, us.started_utc, ''),
		       coalesce(pe.scan_id, 0), coalesce(pr.path, ''), coalesce(pe.other_path, ''), coalesce(pe.detail, '')
		FROM paths p
		JOIN files f ON f.id = p.file_id
		JOIN roots r ON r.id = p.root_id
		LEFT JOIN scans us ON us.id = f.unreadable_since_scan
		LEFT JOIN pm ON pm.file_id = f.id
		LEFT JOIN events pe ON pe.id = pm.event_id
		LEFT JOIN scans ps ON ps.id = pe.scan_id
		LEFT JOIN roots pr ON pr.id = ps.root_id
		WHERE p.valid_to_scan IS NULL`+cond+`
		ORDER BY r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()
	var out []FileItem
	for rows.Next() {
		var r fileRow
		it := &r.item
		var moveRoot, moveRel string
		if err := rows.Scan(&it.FileID, &it.RootPath, &it.Path, &it.Kind, &it.Size, &it.MtimeNS, &it.Hash,
			&it.FileDate, &it.Retention, &it.KeepUntil, &it.ReviewedUTC, &r.reviewedAfter, &r.reviewedHash, &it.UnreadableSince,
			&it.UnreadableSinceUTC, &r.possibleMoveIn, &moveRoot, &moveRel, &it.MoveDetail); err != nil {
			return nil, fmt.Errorf("list files: %w", err)
		}
		if moveRel != "" {
			it.MoveFrom = absPath(moveRoot, moveRel)
		}
		it.Status = classify(r, today)
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	return out, nil
}

// latestScan returns the latest finished scan, 0 if there is none.
func latestScan(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM scans WHERE finished_utc IS NOT NULL`).
		Scan(&id); err != nil {
		return 0, fmt.Errorf("latest scan: %w", err)
	}
	return id, nil
}

// RecordExport stores an export of items (made at now, written to path) and calls write with its ID. Each row keeps
// the file's hash and the file_date, retention and keep_until it was exported with (so file_date as filled in from
// the name, if it was). The export is committed only if write succeeds, so a
// failed write leaves no trace in the register.
func (s *Store) RecordExport(ctx context.Context, path string, now time.Time, items []FileItem,
	write func(exportID int64) error) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	after, err := latestScan(ctx, tx)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO exports (created_utc, path, after_scan) VALUES (?, ?, ?)`,
		utc(now), nullString(path), nullInt(after))
	if err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO export_rows (export_id, file_id, hash, file_date, retention, keep_until) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, id, it.FileID, nullString(it.Hash), nullString(it.FileDate),
			nullString(it.Retention), nullString(it.KeepUntil)); err != nil {
			return 0, fmt.Errorf("record export: %w", err)
		}
	}
	if err := write(id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	return id, nil
}

// LifespanRow is one row of an edited lifespan CSV.
type LifespanRow struct {
	Line         int // in the CSV, for messages
	ExportID     int64
	FileID       int64
	FileDate     string // YYYY-MM-DD, "" = blank
	HasFileDate  bool   // the CSV has a file_date column; without one, the file_date exported is kept
	Retention    string // a period (7y), Forever or YYYY-MM-DD, as checked; "" = blank
	KeepUntil    string // the display cell as read, to check it was left alone
	HasKeepUntil bool   // the CSV has a keep_until column
}

// Refusal is a row that was not applied, and why.
type Refusal struct {
	Line   int
	FileID int64
	Path   string // absolute, "" if the file has no current path
	Reason string
}

// Clear is a row whose blank retention would clear a stored one.
type Clear struct {
	FileID    int64
	Path      string
	Retention string // the stored retention
	KeepUntil string // and the date it gave
}

// UpdateResult counts what an update did (or, in a dry run, would do).
type UpdateResult struct {
	Set           int // rows whose file_date or retention changed, clears not included
	Cleared       int // retentions cleared by a blank cell
	ClearsSkipped int // blank cells that would clear a retention, not confirmed
	NowPast       int // of Set: files whose keep_until is today or earlier, so deletion candidates if unchanged
	Unchanged     int // rows as exported, or with the values already stored
	Refusals      []Refusal
}

// UpdateLifespan checks each row against the register and applies the ones that pass, in one transaction. A blank
// retention where one is stored clears it, but only if confirm (given every such row, once) returns true; confirm is
// not called in a dry run, or when there is nothing to clear. today is the local date (YYYY-MM-DD); detail names the
// source for the history.
func (s *Store) UpdateLifespan(ctx context.Context, rows []LifespanRow, now time.Time, today, detail string,
	dryRun bool, confirm func([]Clear) bool) (UpdateResult, error) {
	var res UpdateResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	type ok struct {
		r         LifespanRow
		c         checked
		afterScan sql.NullInt64
	}
	var sets, clears []ok
	var clearList []Clear
	exports := map[int64]exportInfo{}
	for _, r := range rows {
		ex, found := exports[r.ExportID]
		if !found {
			if ex, err = readExport(ctx, tx, r.ExportID); err != nil {
				return res, err
			}
			exports[r.ExportID] = ex
		}
		c, err := checkRow(ctx, tx, r, ex)
		if err != nil {
			return res, err
		}
		switch {
		case c.refusal != "":
			res.Refusals = append(res.Refusals, Refusal{Line: r.Line, FileID: r.FileID, Path: c.path, Reason: c.refusal})
		case c.unchanged:
			res.Unchanged++
		case c.clear:
			clears = append(clears, ok{r: r, c: c, afterScan: ex.afterScan})
			clearList = append(clearList, Clear{FileID: r.FileID, Path: c.path, Retention: c.storedRetention,
				KeepUntil: c.storedKeepUntil})
		default:
			sets = append(sets, ok{r: r, c: c, afterScan: ex.afterScan})
			if c.keepUntil != "" && c.keepUntil != Forever && c.keepUntil <= today {
				res.NowPast++
			}
		}
	}
	res.Set = len(sets)
	if dryRun {
		res.Cleared = len(clears)
		return res, nil
	}
	if len(clears) > 0 && (confirm == nil || !confirm(clearList)) {
		res.ClearsSkipped = len(clears)
		clears = nil
	}
	res.Cleared = len(clears)
	apply := append(sets, clears...)
	if len(apply) == 0 {
		return res, nil
	}

	batch, err := newBatch(ctx, tx, now, "update", detail)
	if err != nil {
		return res, err
	}
	for _, a := range apply {
		r, c := a.r, a.c
		if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, export_id, file_date, retention,
			keep_until, hash) VALUES (?, ?, ?, ?, ?, ?, ?)`, batch, r.FileID, r.ExportID, nullString(c.fileDate),
			nullString(c.retention), nullString(c.keepUntil), c.exportHash); err != nil {
			return res, fmt.Errorf("record retention: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET file_date = ?, retention = ?, keep_until = ?,
			reviewed_utc = ?, reviewed_after_scan = ?, reviewed_hash = ? WHERE id = ?`, nullString(c.fileDate),
			nullString(c.retention), nullString(c.keepUntil), utc(now), a.afterScan, c.exportHash,
			r.FileID); err != nil {
			return res, fmt.Errorf("record retention: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("record retention: %w", err)
	}
	return res, nil
}

type exportInfo struct {
	found     bool
	created   time.Time
	afterScan sql.NullInt64
}

func readExport(ctx context.Context, tx *sql.Tx, id int64) (exportInfo, error) {
	var ex exportInfo
	var created string
	err := tx.QueryRowContext(ctx, `SELECT created_utc, after_scan FROM exports WHERE id = ?`, id).
		Scan(&created, &ex.afterScan)
	if errors.Is(err, sql.ErrNoRows) {
		return ex, nil
	}
	if err != nil {
		return ex, fmt.Errorf("read export %d: %w", id, err)
	}
	if ex.created, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return ex, fmt.Errorf("read export %d: %w", id, err)
	}
	ex.found = true
	return ex, nil
}

type checked struct {
	path       string
	refusal    string // "" = passes
	unchanged  bool   // as exported, or already the stored values: nothing to do
	clear      bool   // a blank retention where one is stored
	exportHash sql.NullString

	fileDate, retention, keepUntil   string // the values to store
	storedRetention, storedKeepUntil string
}

// checkRow applies the update's rules to one row (design section 2, Lifespan CSV, second shape).
func checkRow(ctx context.Context, tx *sql.Tx, r LifespanRow, ex exportInfo) (checked, error) {
	var c checked
	if !ex.found {
		c.refusal = fmt.Sprintf("export %d is not in this register", r.ExportID)
		return c, nil
	}
	var expDate, expRetention, expKeep sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT hash, file_date, retention, keep_until FROM export_rows
		WHERE export_id = ? AND file_id = ?`, r.ExportID, r.FileID).Scan(&c.exportHash, &expDate, &expRetention, &expKeep)
	if errors.Is(err, sql.ErrNoRows) {
		c.refusal = fmt.Sprintf("file %d is not in export %d (rows cannot be added)", r.FileID, r.ExportID)
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read export %d: %w", r.ExportID, err)
	}
	var root, rel string
	err = tx.QueryRowContext(ctx, `SELECT r.path, p.path FROM paths p JOIN roots r ON r.id = p.root_id
		WHERE p.file_id = ? AND p.valid_to_scan IS NULL`, r.FileID).Scan(&root, &rel)
	present := err == nil
	switch {
	case present:
		c.path = absPath(root, rel)
	case !errors.Is(err, sql.ErrNoRows):
		return c, fmt.Errorf("read file %d: %w", r.FileID, err)
	}
	// keep_until is worked out by filo; a changed cell means a date was typed in the wrong place.
	if r.HasKeepUntil && r.KeepUntil != expKeep.String {
		c.refusal = fmt.Sprintf("keep_until was changed to %q, but filo works it out; change retention instead",
			r.KeepUntil)
		return c, nil
	}
	c.fileDate, c.retention = r.FileDate, r.Retention
	if !r.HasFileDate {
		c.fileDate = expDate.String
	}
	// Cells left as exported are not a decision, whatever happened since.
	if c.fileDate == expDate.String && c.retention == expRetention.String {
		c.unchanged = true
		return c, nil
	}

	var hash sql.NullString
	var storedDate string
	if err := tx.QueryRowContext(ctx, `SELECT hash, coalesce(file_date, ''), coalesce(retention, ''),
		coalesce(keep_until, '') FROM files WHERE id = ?`, r.FileID).
		Scan(&hash, &storedDate, &c.storedRetention, &c.storedKeepUntil); err != nil {
		return c, fmt.Errorf("read file %d: %w", r.FileID, err)
	}
	if !present {
		c.refusal = "no longer present; it is not in the register's current files"
		return c, nil
	}

	// The latest update of this file: from this export (edited again: allowed) or a later one (refused, so an old CSV
	// never overwrites a newer decision).
	var lastExport sql.NullInt64
	var lastCreated string
	err = tx.QueryRowContext(ctx, `SELECT rv.export_id, b.created_utc FROM reviews rv
		JOIN review_batches b ON b.id = rv.batch_id
		WHERE rv.file_id = ? ORDER BY rv.id DESC LIMIT 1`, r.FileID).Scan(&lastExport, &lastCreated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("read history of file %d: %w", r.FileID, err)
	}
	if err == nil && !(lastExport.Valid && lastExport.Int64 == r.ExportID) {
		if t, perr := time.Parse(time.RFC3339Nano, lastCreated); perr == nil && t.After(ex.created) {
			c.refusal = fmt.Sprintf("its retention was changed after export %d; export again", r.ExportID)
			return c, nil
		}
	}

	if c.fileDate == storedDate && c.retention == c.storedRetention {
		c.unchanged = true
		return c, nil
	}
	if hash != c.exportHash {
		c.refusal = fmt.Sprintf("changed since export %d; export again to set its retention", r.ExportID)
		return c, nil
	}
	if c.keepUntil, err = KeepUntil(c.fileDate, c.retention); err != nil {
		c.refusal = err.Error()
		return c, nil
	}
	c.clear = c.retention == "" && c.storedRetention != ""
	return c, nil
}

func newBatch(ctx context.Context, tx *sql.Tx, now time.Time, command, detail string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO review_batches (created_utc, command, detail) VALUES (?, ?, ?)`,
		utc(now), command, nullString(detail))
	if err != nil {
		return 0, fmt.Errorf("record update: %w", err)
	}
	return res.LastInsertId()
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v != 0} }
