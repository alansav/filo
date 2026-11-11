package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Reason says why a file is in the review queue. A file has one reason: the first that applies, in this order.
type Reason string

const (
	// ReasonUnreadable: flagged unreadable and not reviewed since the scan that flagged it.
	ReasonUnreadable Reason = "unreadable"
	// ReasonChanged: its content is not the content its review decision was made on.
	ReasonChanged Reason = "changed"
	// ReasonPossibleMove: it is the new side of a possible move recorded after its last review.
	ReasonPossibleMove Reason = "possible_move"
	// ReasonDue: its review date has come.
	ReasonDue Reason = "due"
	// ReasonUndated: it has no review date and no action.
	ReasonUndated Reason = "undated"
)

// Reasons lists every reason in priority order.
var Reasons = []Reason{ReasonUnreadable, ReasonChanged, ReasonPossibleMove, ReasonDue, ReasonUndated}

// ActionDelete is the only action. In iteration 2 it is recorded, never carried out.
const ActionDelete = "delete"

// QueueItem is one queued file with what the review needs to show.
type QueueItem struct {
	FileID      int64
	RootPath    string // absolute
	Path        string // relative to the root
	Kind        string
	Size        int64
	MtimeNS     int64
	Hash        string // "" = none
	Reason      Reason
	ReviewDate  string // YYYY-MM-DD, "" = none
	Action      string
	RetainUntil string // YYYY-MM-DD, "" = none
	ReviewedUTC string // when the current decision was imported; "" = never reviewed

	UnreadableSince    int64  // the scan that flagged it, 0 = not flagged
	UnreadableSinceUTC string // when that scan finished
	MoveFrom           string // possible move: the absolute path it may have come from
	MoveDetail         string // possible move: "same name" or "same name, different content"
}

// AbsPath is the item's absolute path.
func (q QueueItem) AbsPath() string { return absPath(q.RootPath, q.Path) }

// QueueFilter limits the queue to one root, and optionally to a directory inside it.
type QueueFilter struct {
	RootID int64  // 0 = every root
	Scope  string // relative to the root, '/' separators; "" = the whole root
}

// scopeSQL returns a condition on alias p (paths) for the filter, and its arguments.
func (f QueueFilter) scopeSQL() (string, []any) {
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

// queueRow is a present file with everything classify needs.
type queueRow struct {
	item           QueueItem
	reviewedAfter  int64 // reviewed_after_scan, 0 = none
	reviewedHash   sql.NullString
	possibleMoveIn int64 // the latest scan that recorded it as the new side of a possible move, 0 = none
}

// classify returns why the file is queued, or "" if it is not. today is YYYY-MM-DD.
func classify(r queueRow, today string) Reason {
	it := r.item
	if it.UnreadableSince != 0 && r.reviewedAfter < it.UnreadableSince {
		return ReasonUnreadable
	}
	// A decision covers the content it was made on. A file reviewed without a hash (never read) or whose hash has
	// changed since comes back. While it is flagged unreadable its stored hash is stale, and the rule above governs.
	if it.ReviewedUTC != "" && it.Kind == "file" && it.UnreadableSince == 0 &&
		(it.Hash == "" || !r.reviewedHash.Valid || r.reviewedHash.String != it.Hash) {
		return ReasonChanged
	}
	if r.possibleMoveIn != 0 && r.reviewedAfter < r.possibleMoveIn {
		return ReasonPossibleMove
	}
	if it.Action != "" {
		return "" // decided; waits for disposal (a later iteration)
	}
	if it.ReviewDate == "" {
		return ReasonUndated
	}
	if it.ReviewDate <= today {
		return ReasonDue
	}
	return ""
}

// Queue returns the files due for review under the filter, ordered by root and path. today is the local date,
// YYYY-MM-DD.
func (s *Store) Queue(ctx context.Context, today string, f QueueFilter) ([]QueueItem, error) {
	return queue(ctx, s.db, today, f)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queue(ctx context.Context, db querier, today string, f QueueFilter) ([]QueueItem, error) {
	cond, args := f.scopeSQL()
	// A possible move event names the old file; the new file is the one whose path opened at e.path in that scan.
	rows, err := db.QueryContext(ctx, `
		WITH pm AS (
			SELECT np.file_id AS file_id, max(e.id) AS event_id
			FROM events e
			JOIN scans sc ON sc.id = e.scan_id
			JOIN paths np ON np.root_id = sc.root_id AND np.path = e.path AND np.valid_from_scan = e.scan_id
			WHERE e.kind = 'possible_move'
			GROUP BY np.file_id)
		SELECT f.id, r.path, p.path, f.kind, f.size, f.mtime_ns, coalesce(f.hash, ''),
		       coalesce(f.review_date, ''), coalesce(f.action, ''), coalesce(f.retain_until, ''),
		       coalesce(f.reviewed_utc, ''), coalesce(f.reviewed_after_scan, 0), f.reviewed_hash,
		       coalesce(f.unreadable_since_scan, 0), coalesce(us.finished_utc, us.started_utc, ''),
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
		return nil, fmt.Errorf("review queue: %w", err)
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var r queueRow
		it := &r.item
		var moveRoot, moveRel string
		if err := rows.Scan(&it.FileID, &it.RootPath, &it.Path, &it.Kind, &it.Size, &it.MtimeNS, &it.Hash,
			&it.ReviewDate, &it.Action, &it.RetainUntil, &it.ReviewedUTC, &r.reviewedAfter, &r.reviewedHash,
			&it.UnreadableSince, &it.UnreadableSinceUTC, &r.possibleMoveIn, &moveRoot, &moveRel,
			&it.MoveDetail); err != nil {
			return nil, fmt.Errorf("review queue: %w", err)
		}
		if moveRel != "" {
			it.MoveFrom = absPath(moveRoot, moveRel)
		}
		if it.Reason = classify(r, today); it.Reason != "" {
			out = append(out, *it)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review queue: %w", err)
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

// RecordExport stores an export of items (made at now, written to path; "" = standard output) and calls write with
// its ID. The export is committed only if write succeeds, so a failed write leaves no trace in the register.
func (s *Store) RecordExport(ctx context.Context, path string, now time.Time, items []QueueItem,
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
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO export_rows (export_id, file_id, reason, hash) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	defer stmt.Close()
	for _, it := range items {
		if _, err := stmt.ExecContext(ctx, id, it.FileID, string(it.Reason), nullString(it.Hash)); err != nil {
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

// Decision is one reviewed row of an imported CSV, with its dates already resolved.
type Decision struct {
	Line        int // in the CSV, for messages
	ExportID    int64
	FileID      int64
	ReviewDate  string // YYYY-MM-DD, "" = not given
	Action      string // ActionDelete or ""
	RetainUntil string // YYYY-MM-DD, "" = keep the current floor
}

// Refusal is a decision that was not applied, and why.
type Refusal struct {
	Line   int
	FileID int64
	Path   string // absolute, "" if the file has no current path
	Reason string
}

// ImportResult counts what an import did (or, in a dry run, would do).
type ImportResult struct {
	Dates     int // new review dates
	Deletes   int // marked for deletion
	Already   int // already imported from the same export
	Untouched int // only retain_until given, and it is the value already recorded
	Refusals  []Refusal
}

// ApplyDecisions checks each decision against the register and applies the ones that pass, in one transaction.
// today is the local date (YYYY-MM-DD); detail names the source for the history. A dry run checks everything and
// writes nothing.
func (s *Store) ApplyDecisions(ctx context.Context, ds []Decision, now time.Time, today, detail string,
	dryRun bool) (ImportResult, error) {
	var res ImportResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	type ok struct {
		d         Decision
		hash      sql.NullString
		afterScan sql.NullInt64
	}
	var apply []ok
	exports := map[int64]exportInfo{}
	for _, d := range ds {
		ex, found := exports[d.ExportID]
		if !found {
			ex, err = readExport(ctx, tx, d.ExportID)
			if err != nil {
				return res, err
			}
			exports[d.ExportID] = ex
		}
		c, err := checkDecision(ctx, tx, d, ex, today)
		if err != nil {
			return res, err
		}
		switch {
		case c.refusal != "":
			res.Refusals = append(res.Refusals, Refusal{Line: d.Line, FileID: d.FileID, Path: c.path, Reason: c.refusal})
		case c.already:
			res.Already++
		case c.untouched:
			res.Untouched++
		default:
			apply = append(apply, ok{d: d, hash: c.exportHash, afterScan: ex.afterScan})
			if d.Action == ActionDelete {
				res.Deletes++
			} else {
				res.Dates++
			}
		}
	}
	if dryRun || len(apply) == 0 {
		return res, nil
	}

	batch, err := newBatch(ctx, tx, now, "import", detail)
	if err != nil {
		return res, err
	}
	for _, a := range apply {
		d := a.d
		if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, export_id, review_date, action,
			retain_until, hash) VALUES (?, ?, ?, ?, ?, ?, ?)`, batch, d.FileID, d.ExportID, nullString(d.ReviewDate),
			nullString(d.Action), nullString(d.RetainUntil), a.hash); err != nil {
			return res, fmt.Errorf("record review: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET review_date = ?, action = ?,
			retain_until = coalesce(?, retain_until), reviewed_utc = ?, reviewed_after_scan = ?, reviewed_hash = ?
			WHERE id = ?`, nullString(d.ReviewDate), nullString(d.Action), nullString(d.RetainUntil), utc(now),
			a.afterScan, a.hash, d.FileID); err != nil {
			return res, fmt.Errorf("record review: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("record reviews: %w", err)
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
	already    bool   // imported from this export before: nothing to do
	untouched  bool   // a row with only the retain_until the export filled in
	exportHash sql.NullString
}

// checkDecision applies the import's rules to one decision (design section 2, Review).
func checkDecision(ctx context.Context, tx *sql.Tx, d Decision, ex exportInfo, today string) (checked, error) {
	var c checked
	if !ex.found {
		c.refusal = fmt.Sprintf("export %d is not in this register", d.ExportID)
		return c, nil
	}
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT reason, hash FROM export_rows WHERE export_id = ? AND file_id = ?`,
		d.ExportID, d.FileID).Scan(&reason, &c.exportHash)
	if errors.Is(err, sql.ErrNoRows) {
		c.refusal = fmt.Sprintf("file %d is not in export %d", d.FileID, d.ExportID)
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read export %d: %w", d.ExportID, err)
	}

	var retain string
	var hash sql.NullString
	var unreadable int64
	if err := tx.QueryRowContext(ctx, `SELECT hash, coalesce(unreadable_since_scan, 0), coalesce(retain_until, '')
		FROM files WHERE id = ?`, d.FileID).Scan(&hash, &unreadable, &retain); err != nil {
		return c, fmt.Errorf("read file %d: %w", d.FileID, err)
	}
	var root, rel string
	err = tx.QueryRowContext(ctx, `SELECT r.path, p.path FROM paths p JOIN roots r ON r.id = p.root_id
		WHERE p.file_id = ? AND p.valid_to_scan IS NULL`, d.FileID).Scan(&root, &rel)
	switch {
	case err == nil:
		c.path = absPath(root, rel)
	case !errors.Is(err, sql.ErrNoRows):
		return c, fmt.Errorf("read file %d: %w", d.FileID, err)
	}

	// A row with only retain_until: the export fills that column in, so an unchanged value is an untouched row. A
	// changed floor on its own is not a review outcome.
	if d.ReviewDate == "" && d.Action == "" {
		if d.RetainUntil == retain {
			c.untouched = true
		} else {
			c.refusal = "retain_until needs a review date or delete with it"
		}
		return c, nil
	}

	// The latest review that came from an import: an earlier import of this export, or a later review.
	var lastExport sql.NullInt64
	var lastCreated string
	err = tx.QueryRowContext(ctx, `SELECT rv.export_id, b.created_utc FROM reviews rv
		JOIN review_batches b ON b.id = rv.batch_id
		WHERE rv.file_id = ? AND b.command = 'import' ORDER BY rv.id DESC LIMIT 1`, d.FileID).
		Scan(&lastExport, &lastCreated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("read reviews of file %d: %w", d.FileID, err)
	}
	if err == nil {
		if lastExport.Valid && lastExport.Int64 == d.ExportID {
			c.already = true
			return c, nil
		}
		if t, perr := time.Parse(time.RFC3339Nano, lastCreated); perr == nil && t.After(ex.created) {
			c.refusal = fmt.Sprintf("reviewed again since export %d; export the queue again", d.ExportID)
			return c, nil
		}
	}

	if hash != c.exportHash {
		c.refusal = fmt.Sprintf("changed since export %d; it stays in the queue", d.ExportID)
		return c, nil
	}
	if d.Action == ActionDelete {
		floor := retain
		if d.RetainUntil != "" {
			floor = d.RetainUntil
		}
		switch {
		case unreadable != 0:
			c.refusal = "flagged unreadable: its content cannot be checked, so only a review date is accepted"
		case Reason(reason) == ReasonPossibleMove:
			c.refusal = "a possible move: only a review date is accepted"
		case floor > today:
			c.refusal = fmt.Sprintf("retained until %s: delete is not allowed before then", floor)
		}
	}
	return c, nil
}

func newBatch(ctx context.Context, tx *sql.Tx, now time.Time, command, detail string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO review_batches (created_utc, command, detail) VALUES (?, ?, ?)`,
		utc(now), command, nullString(detail))
	if err != nil {
		return 0, fmt.Errorf("record review batch: %w", err)
	}
	return res.LastInsertId()
}

// SetReviewDate gives every present file under the filter that has no review date and no action the review date
// date (YYYY-MM-DD), and returns how many files that is. It never overwrites a decision, and it does not count as a
// review: an unreadable file or a possible move stays in the queue. A dry run only counts.
func (s *Store) SetReviewDate(ctx context.Context, f QueueFilter, date string, now time.Time, detail string,
	dryRun bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	cond, args := f.scopeSQL()
	sel := `SELECT f.id FROM paths p JOIN files f ON f.id = p.file_id
		WHERE p.valid_to_scan IS NULL AND f.review_date IS NULL AND f.action IS NULL` + cond
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (`+sel+`)`, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("set review dates: %w", err)
	}
	if dryRun || n == 0 {
		return n, nil
	}
	batch, err := newBatch(ctx, tx, now, "set-date", detail)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, review_date)
		SELECT ?, id, ? FROM (`+sel+`)`, append([]any{batch, date}, args...)...); err != nil {
		return 0, fmt.Errorf("set review dates: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET review_date = ?
		WHERE id IN (SELECT file_id FROM reviews WHERE batch_id = ?)`, date, batch); err != nil {
		return 0, fmt.Errorf("set review dates: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("set review dates: %w", err)
	}
	return n, nil
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v != 0} }
