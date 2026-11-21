package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// SetValues is what filo set gives (docs/design.md, Showing and setting a file's details): each field whose flag is
// given, with its new value, "" to clear it. Values are as checked: a date YYYY-MM-DD; a retention as
// retention.ParseRetention returns it (a period, forever, a date, or a policy's name).
type SetValues struct {
	FileDate, Retention       string
	SetFileDate, SetRetention bool
}

// SetRow is one file filo set acts on: its values before and after.
type SetRow struct {
	FileID                       int64
	RootPath, Path               string
	OldFileDate, FileDate        string
	OldRetention, Retention      string // a policy's name, or the file's own retention; "" = none
	OldPolicyValue, PolicyValue  string // when the retention is a policy: its value, "" for none yet
	OldKeepUntil, KeepUntil      string
	RuleDir, RulePattern         string // the rule that set the old policy, which the new retention replaces
	Unchanged, Replaces, Clears  bool   // nothing to do; a stored value replaced; the retention cleared
	oldPolicy, policy, newPolicy bool   // the old and new retention are policies; the new one does not exist yet
	policyID                     int64
	hash                         sql.NullString
}

// AbsPath is the row's file in full.
func (r SetRow) AbsPath() string { return absPath(r.RootPath, r.Path) }

// SetRefusal is a file filo set will not change, and why.
type SetRefusal struct {
	RootPath, Path string
	Reason         string
}

// SetResult is what filo set did, or would do.
type SetResult struct {
	Rows     []SetRow     // every file, ordered by path; Unchanged ones included
	Refusals []SetRefusal // when any, nothing is written
	NowPast  []Past       // of the changed files: those whose keep_until is today or earlier
	// NewPolicies are the policies named that did not exist, by name: created with no value when the changes are
	// written (so their files need a retention until they have one), or that would be.
	NewPolicies []string
	Applied     bool // the changes were written
}

// Changed counts the rows that change.
func (r SetResult) Changed() int {
	n := 0
	for _, row := range r.Rows {
		if !row.Unchanged {
			n++
		}
	}
	return n
}

// SetOptions are how filo set runs: a dry run writes nothing; Confirm, given every row, is asked when some row
// replaces a stored value (nil declines, as with no terminal) and is never asked in a dry run.
type SetOptions struct {
	DryRun  bool
	Confirm func([]SetRow) bool
}

// SetFiles sets the file_date and the retention of the files, all or nothing, in one transaction, by the retention
// CSV's rules: a period needs a file_date, keep_until is worked out again, and a policy that does not exist yet is
// created with no value (without asking: the caller says so). A file not present,
// or whose values cannot stand, refuses the whole run (every refusal is returned). Each change is recorded as a
// review of a batch of command "set", on the file's current content; a retention given by hand replaces a rule's
// policy, as the CSV's does. today (YYYY-MM-DD) decides which files are left past their retention.
func (s *Store) SetFiles(ctx context.Context, ids []int64, v SetValues, now time.Time, today string,
	opt SetOptions) (SetResult, error) {
	var res SetResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	policies := policyCache{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		row, refusal, err := checkSet(ctx, tx, id, v, policies)
		if err != nil {
			return res, err
		}
		if refusal != "" {
			res.Refusals = append(res.Refusals, SetRefusal{RootPath: row.RootPath, Path: row.Path, Reason: refusal})
			continue
		}
		res.Rows = append(res.Rows, row)
		if row.newPolicy && !slices.Contains(res.NewPolicies, row.Retention) {
			res.NewPolicies = append(res.NewPolicies, row.Retention)
		}
		if !row.Unchanged && row.KeepUntil != "" && row.KeepUntil != Forever && row.KeepUntil <= today {
			res.NowPast = append(res.NowPast, Past{Path: row.AbsPath(), Retention: row.Retention,
				KeepUntil: row.KeepUntil})
		}
	}
	byPath := func(a, b SetRow) int { return strings.Compare(a.AbsPath(), b.AbsPath()) }
	slices.SortFunc(res.Rows, byPath)
	slices.SortFunc(res.Refusals, func(a, b SetRefusal) int {
		return strings.Compare(absPath(a.RootPath, a.Path), absPath(b.RootPath, b.Path))
	})
	slices.SortFunc(res.NowPast, func(a, b Past) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(res.NewPolicies)
	if len(res.Refusals) > 0 || opt.DryRun || res.Changed() == 0 {
		return res, nil
	}
	if slices.ContainsFunc(res.Rows, func(r SetRow) bool { return r.Replaces }) &&
		(opt.Confirm == nil || !opt.Confirm(res.Rows)) {
		return res, nil
	}

	after, err := latestScan(ctx, tx)
	if err != nil {
		return res, err
	}
	batch, err := newBatch(ctx, tx, now, "set", "filo set")
	if err != nil {
		return res, err
	}
	created := map[string]int64{}
	for _, name := range res.NewPolicies {
		if created[name], err = addPolicy(ctx, tx, name, "", now); err != nil {
			return res, err
		}
	}
	for _, r := range res.Rows {
		if r.Unchanged {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, file_date, retention, keep_until,
			hash) VALUES (?, ?, ?, ?, ?, ?)`, batch, r.FileID, nullString(r.FileDate), nullString(r.Retention),
			nullString(r.KeepUntil), r.hash); err != nil {
			return res, fmt.Errorf("record retention: %w", err)
		}
		// A file has a policy or a retention of its own, never both. The rule that set its policy stays only while
		// that is still its policy (a file_date set alone keeps it; the old values are what CASE reads).
		own, policy := nullString(r.Retention), sql.NullInt64{}
		if r.policy {
			own, policy = sql.NullString{}, sql.NullInt64{Int64: r.policyID, Valid: true}
			if r.newPolicy {
				policy.Int64 = created[r.Retention]
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET file_date = ?, retention = ?, policy_id = ?,
			keep_until = ?, reviewed_utc = ?, reviewed_after_scan = ?, reviewed_hash = ?,
			rule_id = CASE WHEN policy_id IS ? THEN rule_id END WHERE id = ?`,
			nullString(r.FileDate), own, policy, nullString(r.KeepUntil), utc(now), nullInt(after), r.hash, policy,
			r.FileID); err != nil {
			return res, fmt.Errorf("record retention: %w", err)
		}
	}
	if err := s.commit(tx); err != nil {
		return res, fmt.Errorf("record retention: %w", err)
	}
	res.Applied = true
	return res, nil
}

// checkSet works out one file's new values, or why they cannot be set.
func checkSet(ctx context.Context, tx *sql.Tx, id int64, v SetValues, policies policyCache) (SetRow, string, error) {
	r := SetRow{FileID: id}
	var deleted sql.NullString
	var ruleID int64
	err := tx.QueryRowContext(ctx, `
		SELECT f.hash, coalesce(f.file_date, ''), `+retentionColumns+`, coalesce(f.keep_until, ''), f.deleted_utc,
		       coalesce(f.rule_id, 0), coalesce(ru.dir, ''), coalesce(ru.pattern, ''), f.policy_id IS NOT NULL
		FROM files f`+policyJoin+` LEFT JOIN rules ru ON ru.id = f.rule_id WHERE f.id = ?`, id).
		Scan(&r.hash, &r.OldFileDate, &r.OldRetention, new(string), &r.OldPolicyValue, &r.OldKeepUntil, &deleted,
			&ruleID, &r.RuleDir, &r.RulePattern, &r.oldPolicy)
	if err != nil {
		return r, "", fmt.Errorf("read file %d: %w", id, err)
	}
	if !r.oldPolicy {
		r.OldPolicyValue = ""
	}
	// Its current path, else its last one, for the messages.
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT ro.path, p.path, p.valid_to_scan IS NULL FROM paths p
		JOIN roots ro ON ro.id = p.root_id WHERE p.file_id = ?
		ORDER BY p.valid_to_scan IS NOT NULL, coalesce(p.valid_to_scan, 0) DESC, p.id DESC LIMIT 1`, id).
		Scan(&r.RootPath, &r.Path, &current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, "", fmt.Errorf("read the path of file %d: %w", id, err)
	}
	switch {
	case deleted.Valid:
		return r, "deleted by filo: only present files are set", nil
	case !current:
		return r, "missing: only present files are set (run filo scan if it was moved)", nil
	}

	r.FileDate, r.Retention = r.OldFileDate, r.OldRetention
	if v.SetFileDate {
		r.FileDate = v.FileDate
	}
	if v.SetRetention {
		r.Retention = v.Retention
	}
	value := r.Retention
	if r.policy = IsPolicyRef(r.Retention); r.policy {
		p, err := policies.get(ctx, tx, r.Retention)
		if err != nil {
			return r, "", err
		}
		// A policy that does not exist yet is created with no value (SetResult.NewPolicies), as the CSV's is.
		r.policyID, r.PolicyValue, value, r.newPolicy = p.id, p.value, p.value, p.id == 0
	}
	if IsPeriod(value) && r.FileDate == "" {
		what := "retention " + r.Retention
		if r.policy {
			what += " (" + value + ")"
		}
		if v.SetFileDate {
			return r, what + " needs a file_date to count from: --no-file-date cannot clear it", nil
		}
		return r, what + " needs a file_date to count from: add --file-date DATE", nil
	}
	if r.KeepUntil, err = KeepUntil(r.FileDate, value); err != nil {
		return r, err.Error(), nil
	}
	r.Unchanged = r.FileDate == r.OldFileDate && r.Retention == r.OldRetention
	r.Clears = r.Retention == "" && r.OldRetention != ""
	r.Replaces = (r.OldFileDate != "" && r.FileDate != r.OldFileDate) ||
		(r.OldRetention != "" && r.Retention != r.OldRetention)
	if ruleID == 0 || !r.oldPolicy || r.Retention == r.OldRetention {
		r.RuleDir, r.RulePattern = "", "" // no rule's policy is replaced
	}
	return r, "", nil
}

// RulePath is the rule a row's old policy was set by, as "dir/pattern" ("" for none).
func (r SetRow) RulePath() string {
	if r.RulePattern == "" {
		return ""
	}
	return path.Join(r.RuleDir, r.RulePattern)
}
