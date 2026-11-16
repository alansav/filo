package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alansav/filo/internal/rules"
)

// Dating files by directory rules (docs/design.md, Lifespan, Directory rules, "Which files a rule dates"): a rule
// gives its policy to a present file with no retention at all, once. A file that is unreadable or the new side of a
// possible move waits until that is resolved: its status says so, and only an undated file is dated.

// RuleDated is a file a rule dated (or would date), as it is after: on the rule's policy.
type RuleDated struct {
	FileItem
	Rule Rule
}

// RuleTie is a file that two or more rules of one directory match equally: no rule dates it.
type RuleTie struct {
	FileItem
	Rules []Rule
}

// RuleRun is what the rules did (or would do) in one root.
type RuleRun struct {
	Dated []RuleDated // in path order
	Ties  []RuleTie   // in path order
}

// datable reports whether a rule may date the file: present with no retention at all (not even a policy with no
// value), never dated by a rule before, and undated by its status (not unreadable, not the new side of a possible
// move, both of which outrank undated).
func datable(it FileItem) bool {
	return it.Status == StatusUndated && it.Retention == "" && it.Policy == "" && !it.RuleDated
}

// dateByRules dates, in tx, the datable files of a root by its rules, and returns what it did. With only, it dates
// just the files the rules in only decide, and reports just the ties those rules are in: what adding, moving or
// copying them does. afterScan stamps the dates (the scan they were set after, as for a CSV); command names the run
// in the history ("rules add", "scan").
func dateByRules(ctx context.Context, tx *sql.Tx, rootID int64, only map[int64]bool, afterScan int64, today,
	command string, now time.Time) (RuleRun, error) {
	var run RuleRun
	rs, err := readRules(ctx, tx, rootID, "")
	if err != nil || len(rs) == 0 {
		return run, err
	}
	byID := map[int64]Rule{}
	ms := make([]rules.Rule, len(rs))
	for i, r := range rs {
		byID[r.ID], ms[i] = r, r.Matcher()
	}
	items, err := files(ctx, tx, today, Filter{RootID: rootID})
	if err != nil {
		return run, err
	}
	for _, it := range items {
		if !datable(it) {
			continue
		}
		res := rules.Resolve(ms, it.Path)
		switch {
		case !res.Found:
		case len(res.Ties) > 0:
			tie := RuleTie{FileItem: it}
			in := only == nil
			for _, m := range res.Ties {
				tie.Rules = append(tie.Rules, byID[m.ID])
				in = in || only[m.ID]
			}
			if in {
				run.Ties = append(run.Ties, tie)
			}
		case only == nil || only[res.Rule.ID]:
			r := byID[res.Rule.ID]
			it.Retention, it.Policy, it.PolicyValue = r.Policy, r.Policy, r.PolicyValue
			it.KeepUntil, _ = KeepUntil(it.FileDate, r.PolicyValue) // a period with no file_date has none
			it.Status = classify(fileRow{item: it, reviewedAfter: afterScan,
				reviewedHash: sql.NullString{String: it.Hash, Valid: it.Hash != ""}}, today)
			it.ReviewedUTC, it.RuleDated = utc(now), true
			run.Dated = append(run.Dated, RuleDated{FileItem: it, Rule: r})
		}
	}
	if len(run.Dated) == 0 {
		return run, nil
	}
	batch, err := newBatch(ctx, tx, now, command, "")
	if err != nil {
		return run, err
	}
	for _, d := range run.Dated {
		if err := dateByRule(ctx, tx, d, batch, afterScan, now); err != nil {
			return run, err
		}
	}
	return run, nil
}

// dateByRule records one file dated by a rule, as filo update records a retention: the policy, the keep_until worked
// out from it, the content it was set on, the scan it was set after, and a history row.
func dateByRule(ctx context.Context, tx *sql.Tx, d RuleDated, batch, afterScan int64, now time.Time) error {
	hash := sql.NullString{String: d.Hash, Valid: d.Hash != ""}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reviews (batch_id, file_id, file_date, retention, keep_until, hash)
		VALUES (?, ?, ?, ?, ?, ?)`, batch, d.FileID, nullString(d.FileDate), d.Policy, nullString(d.KeepUntil),
		hash); err != nil {
		return fmt.Errorf("date %s by a rule: %w", d.Path, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET retention = NULL, policy_id = (SELECT policy_id FROM rules
		WHERE id = ?), keep_until = ?, reviewed_utc = ?, reviewed_after_scan = ?, reviewed_hash = ?, rule_id = ?,
		rule_dated_utc = ? WHERE id = ?`, d.Rule.ID, nullString(d.KeepUntil), utc(now), nullInt(afterScan), hash,
		d.Rule.ID, utc(now), d.FileID); err != nil {
		return fmt.Errorf("date %s by a rule: %w", d.Path, err)
	}
	return nil
}
