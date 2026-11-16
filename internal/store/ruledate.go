package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
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

// dateByRules dates, in tx, the datable files at scope and below in a root ("" for all of it) by the rules, and
// returns what it did. With only, it dates just the files the rules in only decide, and reports just the ties those
// rules are in: what adding, moving or copying them does. afterScan stamps the dates (the scan they were set after,
// as for a CSV); command names the run in the history ("rules add", "scan").
//
// A file is decided once, over every present path it has (hard links are one file with several paths): if its
// paths are decided by different rules, it is a tie between them, never dated by one of them. A file at or under a
// directory the latest scan of it could not read is left alone: what is there now is not known.
func dateByRules(ctx context.Context, tx *sql.Tx, rootID int64, scope string, only map[int64]bool, afterScan int64,
	today, command string, now time.Time) (RuleRun, error) {
	var run RuleRun
	rs, err := readRules(ctx, tx, rootID, "")
	if err != nil || len(rs) == 0 {
		return run, err
	}
	sets := ruleSets{tx: tx, byRoot: map[int64]rules.Set{}, byID: map[int64]Rule{}}
	sets.add(rootID, rs)
	items, err := files(ctx, tx, today, Filter{RootID: rootID, Scope: scope})
	if err != nil {
		return run, err
	}
	blocked, err := unreadDirs(ctx, tx, rootID)
	if err != nil {
		return run, err
	}
	others, err := otherPaths(ctx, tx)
	if err != nil {
		return run, err
	}
	seen := map[int64]bool{}
	for _, it := range items {
		if !datable(it) || seen[it.FileID] || slices.ContainsFunc(blocked, func(d string) bool {
			return isAtOrUnder(it.Path, d)
		}) {
			continue
		}
		seen[it.FileID] = true
		paths := others[it.FileID]
		if len(paths) == 0 {
			paths = []rootPath{{rootID, it.Path}}
		}
		decided, ties, err := sets.resolve(ctx, paths)
		if err != nil {
			return run, err
		}
		if len(decided) > 1 { // its paths say different things: a tie between them
			ties = append(ties, decided...)
			decided = nil
		}
		switch {
		case len(ties) > 0:
			tie := RuleTie{FileItem: it, Rules: uniqueRules(ties)}
			in := only == nil
			for _, r := range tie.Rules {
				in = in || only[r.ID]
			}
			if in {
				run.Ties = append(run.Ties, tie)
			}
		case len(decided) == 1 && (only == nil || only[decided[0].ID]):
			r := decided[0]
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

// rootPath is a present path of a file.
type rootPath struct {
	rootID int64
	path   string
}

// otherPaths returns, for each file with more than one present path (hard links), all of them.
func otherPaths(ctx context.Context, tx *sql.Tx) (map[int64][]rootPath, error) {
	type row struct {
		fileID int64
		p      rootPath
	}
	rows, err := queryAll(ctx, tx, func(rows *sql.Rows) (row, error) {
		var r row
		err := rows.Scan(&r.fileID, &r.p.rootID, &r.p.path)
		return r, err
	}, `SELECT file_id, root_id, path FROM paths WHERE valid_to_scan IS NULL AND file_id IN (
		SELECT file_id FROM paths WHERE valid_to_scan IS NULL GROUP BY file_id HAVING count(*) > 1)
		ORDER BY root_id, path`)
	if err != nil {
		return nil, fmt.Errorf("read hard links: %w", err)
	}
	out := map[int64][]rootPath{}
	for _, r := range rows {
		out[r.fileID] = append(out[r.fileID], r.p)
	}
	return out, nil
}

// ruleSets is the rules of each root, read once each.
type ruleSets struct {
	tx     *sql.Tx
	byRoot map[int64]rules.Set
	byID   map[int64]Rule
}

func (s ruleSets) add(rootID int64, rs []Rule) {
	ms := make([]rules.Rule, len(rs))
	for i, r := range rs {
		s.byID[r.ID], ms[i] = r, r.Matcher()
	}
	s.byRoot[rootID] = rules.NewSet(ms)
}

// resolve works out the rules deciding a file's paths: the distinct rules that decide one, and the rules of any tie.
func (s ruleSets) resolve(ctx context.Context, paths []rootPath) (decided, ties []Rule, err error) {
	for _, p := range paths {
		set, ok := s.byRoot[p.rootID]
		if !ok {
			rs, err := readRules(ctx, s.tx, p.rootID, "")
			if err != nil {
				return nil, nil, err
			}
			s.add(p.rootID, rs)
			set = s.byRoot[p.rootID]
		}
		res := set.Resolve(p.path)
		switch {
		case !res.Found:
		case len(res.Ties) > 0:
			for _, m := range res.Ties {
				ties = append(ties, s.byID[m.ID])
			}
		default:
			decided = uniqueRules(append(decided, s.byID[res.Rule.ID]))
		}
	}
	return decided, ties, nil
}

// uniqueRules drops repeated rules, keeping the first of each.
func uniqueRules(rs []Rule) []Rule {
	seen := map[int64]bool{}
	out := rs[:0:0]
	for _, r := range rs {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out
}

// unreadDirs returns the paths in a root (relative, "" for all of it) that the latest scan covering them could not
// read: a walk problem recorded as an error event with no file (not a file that could not be hashed, nor one that
// vanished), in a scan with no later scan whose scope covers it. Their files' state now is not known.
func unreadDirs(ctx context.Context, tx *sql.Tx, rootID int64) ([]string, error) {
	type scan struct {
		id    int64
		scope string
	}
	scans, err := queryAll(ctx, tx, func(rows *sql.Rows) (scan, error) {
		var s scan
		err := rows.Scan(&s.id, &s.scope)
		return s, err
	}, `SELECT id, scope FROM scans WHERE root_id = ? AND kind = 'scan' AND finished_utc IS NOT NULL ORDER BY id DESC`,
		rootID)
	if err != nil {
		return nil, fmt.Errorf("read scans: %w", err)
	}
	type problem struct {
		scan int64
		path string
	}
	problems, err := queryAll(ctx, tx, func(rows *sql.Rows) (problem, error) {
		var p problem
		err := rows.Scan(&p.scan, &p.path)
		return p, err
	}, `SELECT e.scan_id, coalesce(e.path, '') FROM events e JOIN scans s ON s.id = e.scan_id
		WHERE s.root_id = ? AND e.kind = 'error' AND e.file_id IS NULL AND coalesce(e.detail, '') NOT LIKE 'hash: %'
		AND coalesce(e.detail, '') != 'vanished during the scan'`, rootID)
	if err != nil {
		return nil, fmt.Errorf("read scan problems: %w", err)
	}
	byScan := map[int64][]string{}
	for _, p := range problems {
		if p.path == "." {
			p.path = ""
		}
		byScan[p.scan] = append(byScan[p.scan], p.path)
	}
	var covered, out []string // the scopes of the later scans; the paths still unread
	for _, sc := range scans {
		for _, p := range byScan[sc.id] {
			if !slices.ContainsFunc(covered, func(c string) bool { return isAtOrUnder(p, c) }) {
				out = append(out, p)
			}
		}
		covered = append(covered, sc.scope)
	}
	return out, nil
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
