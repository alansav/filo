package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is where a present file stands in its retention. A file has one status: the first that applies, in the
// order of Statuses.
type Status string

const (
	// StatusUnreadable: flagged unreadable and not decided on since the scan that flagged it, or flagged and past its
	// keep_until (its content cannot be checked).
	StatusUnreadable Status = "unreadable"
	// StatusPossibleMove: the new side of a possible move recorded after its last decision.
	StatusPossibleMove Status = "possible_move"
	// StatusNeedsFileDate: its policy is a period, and it has no file_date to count from (docs/design.md, Named
	// retention policies). Never a deletion candidate.
	StatusNeedsFileDate Status = "needs_file_date"
	// StatusUndated: it has no keep_until (a policy with no value included).
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
var Statuses = []Status{StatusUnreadable, StatusPossibleMove, StatusNeedsFileDate, StatusUndated, StatusForever,
	StatusChanged, StatusExpired, StatusKept}

// NeedsDecision reports whether a file with this status needs a keep_until decision.
func (s Status) NeedsDecision() bool {
	return s == StatusUnreadable || s == StatusPossibleMove || s == StatusNeedsFileDate || s == StatusUndated ||
		s == StatusChanged
}

// FileItem is one present file with its retention.
type FileItem struct {
	FileID      int64
	RootPath    string // absolute
	Path        string // relative to the root
	Kind        string
	Size        int64
	MtimeNS     int64
	Hash        string // "" = none
	FileDate    string // YYYY-MM-DD the retention counts from, "" = none
	Retention   string // as given: a period (7y), Forever or YYYY-MM-DD, or its policy's name; "" = none
	Policy      string // its retention policy's name, "" = none (then Retention is its own)
	PolicyValue string // that policy's value: a period, Forever, YYYY-MM-DD, or "" for none yet
	KeepUntil   string // worked out from the two: YYYY-MM-DD, Forever, or "" = none
	ReviewedUTC string // when the current retention was set; "" = never
	RuleDated   bool   // a directory rule set its policy once: no rule sets it again
	RuleID      int64  // the rule its policy is set by; 0 = none (or its retention since set by hand)
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
	RootID   int64  // 0 = every root
	Scope    string // relative to the root, '/' separators; "" = the whole root
	PolicyID int64  // only the files on this retention policy; 0 = any
	FileID   int64  // only this file; 0 = any
	// Ruleless: only the files a directory rule may give a policy, before their status: no retention, no policy,
	// never given one by a rule. What a scan's rules look at, so they classify only those.
	Ruleless bool
}

// scopeSQL returns a condition on alias p (paths) for the filter, and its arguments.
// A policy filter, Ruleless and a file are conditions on alias f (files).
func (f Filter) scopeSQL() (string, []any) {
	cond, args := "", []any(nil)
	if f.PolicyID != 0 {
		cond, args = " AND f.policy_id = ?", []any{f.PolicyID}
	}
	if f.Ruleless {
		cond += " AND f.retention IS NULL AND f.policy_id IS NULL AND f.rule_dated_utc IS NULL"
	}
	if f.FileID != 0 {
		cond, args = cond+" AND f.id = ?", append(args, f.FileID)
	}
	switch {
	case f.RootID == 0:
	case f.Scope == "":
		cond, args = cond+" AND p.root_id = ?", append(args, f.RootID)
	default:
		// '0' sorts right after '/', so this range is every path below the scope (byte-wise, as BINARY compares).
		cond += " AND p.root_id = ? AND (p.path = ? OR (p.path >= ? AND p.path < ?))"
		args = append(args, f.RootID, f.Scope, f.Scope+"/", f.Scope+"0")
	}
	return cond, args
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
	if it.NeedsFileDate() {
		return StatusNeedsFileDate
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
	return files(ctx, s.db, today, f)
}

// files is Files through q: the store, or a transaction that acts on what it reads.
func files(ctx context.Context, q querier, today string, f Filter) ([]FileItem, error) {
	cond, args := f.scopeSQL()
	// A possible move event names the old file; the new file is the one whose path opened at e.path in that scan.
	out, err := queryAll(ctx, q, func(rows *sql.Rows) (FileItem, error) {
		var r fileRow
		it := &r.item
		var moveRoot, moveRel string
		if err := rows.Scan(&it.FileID, &it.RootPath, &it.Path, &it.Kind, &it.Size, &it.MtimeNS, &it.Hash,
			&it.FileDate, &it.Retention, &it.Policy, &it.PolicyValue, &it.KeepUntil, &it.ReviewedUTC, &it.RuleDated, &it.RuleID, &r.reviewedAfter, &r.reviewedHash, &it.UnreadableSince,
			&it.UnreadableSinceUTC, &r.possibleMoveIn, &moveRoot, &moveRel, &it.MoveDetail); err != nil {
			return *it, err
		}
		if moveRel != "" {
			it.MoveFrom = absPath(moveRoot, moveRel)
		}
		it.Status = classify(r, today)
		return *it, nil
	}, `
		WITH pm AS (
			SELECT np.file_id AS file_id, max(e.id) AS event_id
			FROM events e
			JOIN scans sc ON sc.id = e.scan_id
			JOIN paths np ON np.root_id = sc.root_id AND np.path = e.path AND np.valid_from_scan = e.scan_id
			WHERE e.kind = 'possible_move'
			GROUP BY np.file_id)
		SELECT f.id, r.path, p.path, f.kind, f.size, f.mtime_ns, coalesce(f.hash, ''),
		       coalesce(f.file_date, ''), `+retentionColumns+`, coalesce(f.keep_until, ''),
		       coalesce(f.reviewed_utc, ''), f.rule_dated_utc IS NOT NULL, coalesce(f.rule_id, 0),
		       coalesce(f.reviewed_after_scan, 0),
		       f.reviewed_hash, coalesce(f.unreadable_since_scan, 0), coalesce(us.finished_utc, us.started_utc, ''),
		       coalesce(pe.scan_id, 0), coalesce(pr.path, ''), coalesce(pe.other_path, ''), coalesce(pe.detail, '')
		FROM paths p
		JOIN files f ON f.id = p.file_id`+policyJoin+`
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

// recordExport stores an export of the given kind, its rows (written by addRows), and calls write with its ID. It is
// committed only if write succeeds.
func (s *Store) recordExport(ctx context.Context, path string, now time.Time, kind string,
	addRows func(tx *sql.Tx, exportID int64) error, write func(exportID int64) error) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	after, err := latestScan(ctx, tx)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO exports (created_utc, path, after_scan, kind) VALUES (?, ?, ?, ?)`,
		utc(now), nullString(path), nullInt(after), kind)
	if err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := addRows(tx, id); err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	if err := write(id); err != nil {
		return 0, err
	}
	if err := s.commit(tx); err != nil {
		return 0, fmt.Errorf("record export: %w", err)
	}
	return id, nil
}

// RetentionRow is the retention half of one row of an edited combined CSV.
type RetentionRow struct {
	Line         int // in the CSV, for messages
	ExportID     int64
	FileID       int64
	FileDate     string // YYYY-MM-DD, or marked as read from the file name (MarkedDate); "" = blank
	HasFileDate  bool   // the CSV has a file_date column; without one, the file_date exported is kept
	Retention    string // a period (7y), Forever, YYYY-MM-DD or a policy name (lower case), as checked; "" = blank
	KeepUntil    string // the display cell as read, to check it was left alone
	HasKeepUntil bool   // the CSV has a keep_until column
}

// Refusal is a row that was not applied, and why.
type Refusal struct {
	Line   int
	FileID int64
	Path   string // absolute, "" if the file has no current path
	Reason string
	Group  int64 // a duplicates group refused whole: its group in the CSV, 0 otherwise
	Export int64 // and the export the group is in
}

// Clear is a row whose blank retention would clear a stored one.
type Clear struct {
	FileID    int64
	Path      string
	Retention string // the stored retention
	KeepUntil string // and the date it gave
}

// Past is a file an update left past its retention, for the summary to list (a typo such as 7d for 7y shows here).
type Past struct {
	Path      string
	Retention string
	KeepUntil string
}

// NewPolicy is a policy a CSV names that does not exist yet, and how many rows name it.
type NewPolicy struct {
	Name string
	Rows int
}

// retentionChange is a row of an update that passed its checks, to apply.
type retentionChange struct {
	r         RetentionRow
	c         checked
	afterScan sql.NullInt64 // its export's
}

// createPolicies creates the new policies an update names, with no value, and returns each one's ID by name.
func createPolicies(ctx context.Context, tx *sql.Tx, nps []NewPolicy, now time.Time) (map[string]int64, error) {
	created := map[string]int64{}
	for _, np := range nps {
		id, err := addPolicy(ctx, tx, np.Name, "", now)
		if err != nil {
			return nil, err
		}
		created[np.Name] = id
	}
	return created, nil
}

// applyRetentions records the rows' file_date and retention in one update batch (detail names its source), each with
// its review. created gives the ID of each new policy the rows name.
func applyRetentions(ctx context.Context, tx *sql.Tx, apply []retentionChange, created map[string]int64,
	now time.Time, detail string) error {
	batch, err := newBatch(ctx, tx, now, "update", detail)
	if err != nil {
		return err
	}
	for _, a := range apply {
		r, c := a.r, a.c
		if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, export_id, file_date, retention,
			keep_until, hash) VALUES (?, ?, ?, ?, ?, ?, ?)`, batch, r.FileID, r.ExportID, nullString(c.fileDate),
			nullString(c.retention), nullString(c.keepUntil), c.exportHash); err != nil {
			return fmt.Errorf("record retention: %w", err)
		}
		// A file has a policy or a retention of its own, never both: a policy name goes in policy_id (the policy
		// created above if it is new), anything else in retention, taking the file off its policy.
		// The rule that set its policy stays only while that is still its policy (a file_date filled in keeps it;
		// the old values are what CASE reads).
		own, policy := nullString(c.retention), sql.NullInt64{}
		if c.policy {
			own, policy = sql.NullString{}, sql.NullInt64{Int64: c.policyID, Valid: true}
			if c.newPolicy {
				policy.Int64 = created[c.retention]
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET file_date = ?, retention = ?, policy_id = ?,
			keep_until = ?, reviewed_utc = ?, reviewed_after_scan = ?, reviewed_hash = ?,
			rule_id = CASE WHEN policy_id IS ? THEN rule_id END WHERE id = ?`,
			nullString(c.fileDate), own, policy, nullString(c.keepUntil), utc(now), a.afterScan, c.exportHash, policy,
			r.FileID); err != nil {
			return fmt.Errorf("record retention: %w", err)
		}
	}
	return nil
}

type exportInfo struct {
	found     bool
	kind      string
	created   time.Time
	afterScan sql.NullInt64
}

func readExport(ctx context.Context, tx *sql.Tx, id int64) (exportInfo, error) {
	var ex exportInfo
	var created string
	err := tx.QueryRowContext(ctx, `SELECT created_utc, after_scan, kind FROM exports WHERE id = ?`, id).
		Scan(&created, &ex.afterScan, &ex.kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ex, nil
	}
	if err != nil {
		return ex, fmt.Errorf("read export #%d: %w", id, err)
	}
	if ex.created, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return ex, fmt.Errorf("read export #%d: %w", id, err)
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
	storedRetention, storedKeepUntil string // the stored retention: its policy's name if it is on one
	policy                           bool   // retention names a policy
	policyID                         int64  // its ID, 0 for a new one
	newPolicy                        bool   // one that does not exist yet: created with no value, once confirmed
}

// checkRow applies the update's rules to the retention half of one row of a combined CSV (docs/design.md, Retention
// CSV columns).
func checkRow(ctx context.Context, tx *sql.Tx, r RetentionRow, ex exportInfo, policies policyCache) (checked, error) {
	var c checked
	if !ex.found {
		c.refusal = fmt.Sprintf("export #%d is not in this register", r.ExportID)
		return c, nil
	}
	if ex.kind != ExportFiles {
		c.refusal = fmt.Sprintf("export #%d is a %s export, not a %s export", r.ExportID, KindName(ex.kind),
			KindName(ExportFiles))
		return c, nil
	}
	var expDate, expRetention, expKeep sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT hash, file_date, retention, keep_until FROM export_rows
		WHERE export_id = ? AND file_id = ?`, r.ExportID, r.FileID).Scan(&c.exportHash, &expDate, &expRetention, &expKeep)
	if errors.Is(err, sql.ErrNoRows) {
		c.refusal = fmt.Sprintf("file %d is not in export #%d (rows cannot be added)", r.FileID, r.ExportID)
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read export #%d: %w", r.ExportID, err)
	}
	// The file's current path, for messages; looked up only once a row needs one, so untouched rows cost one query.
	present := false
	lookupPath := func() error {
		var root, rel string
		err := tx.QueryRowContext(ctx, `SELECT r.path, p.path FROM paths p JOIN roots r ON r.id = p.root_id
			WHERE p.file_id = ? AND p.valid_to_scan IS NULL`, r.FileID).Scan(&root, &rel)
		switch {
		case err == nil:
			present, c.path = true, absPath(root, rel)
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read file %d: %w", r.FileID, err)
		}
		return nil
	}
	// keep_until is worked out by filo; a changed cell means a date was typed in the wrong place.
	if r.HasKeepUntil && r.KeepUntil != expKeep.String {
		if err := lookupPath(); err != nil {
			return c, err
		}
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

	if err := lookupPath(); err != nil {
		return c, err
	}
	var hash sql.NullString
	var storedDate string
	var unused string // the policy's name and value, which the stored retention already says
	if err := tx.QueryRowContext(ctx, `SELECT f.hash, coalesce(f.file_date, ''), `+retentionColumns+`,
		coalesce(f.keep_until, '') FROM files f`+policyJoin+`WHERE f.id = ?`, r.FileID).
		Scan(&hash, &storedDate, &c.storedRetention, &unused, &unused, &c.storedKeepUntil); err != nil {
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
			c.refusal = fmt.Sprintf("its retention was changed after export #%d; export again", r.ExportID)
			return c, nil
		}
	}

	// The value the row's retention stands for: itself, or its policy's (none yet for a new policy).
	value := c.retention
	if c.policy = IsPolicyRef(c.retention); c.policy {
		p, err := policies.get(ctx, tx, c.retention)
		if err != nil {
			return c, err
		}
		c.policyID, c.newPolicy, value = p.id, p.id == 0, p.value
	}
	// A file_date read from the file name is marked (MarkedDate) and counts only once the mark is removed: left as
	// exported it is no decision about the date, so the stored one stays; edited but still marked it is refused.
	marked := IsMarkedDate(c.fileDate)
	if marked {
		if c.fileDate != expDate.String {
			c.refusal = fmt.Sprintf("file_date %s is marked as read from the file name, but it is not the one "+
				"exported: remove the ! to use it", c.fileDate)
			return c, nil
		}
		c.fileDate = storedDate
	}
	// A CSV from before dates were marked: a file_date the export suggested from the name, left as it was, is a guess
	// nobody has checked. It is stored only when the row's retention is a period, or a policy whose value is one,
	// which is when it is used; otherwise a later period could count from it without anyone having looked.
	if storedDate == "" && expDate.String != "" && c.fileDate == expDate.String && !IsPeriod(value) {
		c.fileDate = ""
	}
	if marked && c.fileDate == "" && IsPeriod(value) {
		c.refusal = markedDateRefusal(expDate.String)
		return c, nil
	}
	if c.fileDate == storedDate && c.retention == c.storedRetention {
		c.unchanged = true
		return c, nil
	}
	if hash != c.exportHash {
		c.refusal = fmt.Sprintf("changed since export #%d; export again to set its retention", r.ExportID)
		return c, nil
	}
	if c.policy && IsPeriod(value) && c.fileDate == "" {
		c.refusal = NeedsFileDateError{c.retention, value}.Error()
		return c, nil
	}
	if c.keepUntil, err = KeepUntil(c.fileDate, value); err != nil {
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

// A file_date the export read from the file name is written marked, "!2023-07-14", and counts only once the person
// removes the mark (docs/design.md, Retention CSV columns).
const dateMark = "!"

// MarkedDate marks a file_date read from a file name.
func MarkedDate(date string) string { return dateMark + date }

// IsMarkedDate reports whether a file_date is marked as read from a file name.
func IsMarkedDate(s string) bool { return strings.HasPrefix(s, dateMark) }

func markedDateRefusal(date string) string {
	return fmt.Sprintf("file_date %s was read from the file name: remove the ! to accept it", date)
}
