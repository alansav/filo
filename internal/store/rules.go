package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/alansav/filo/internal/rules"
)

// Directory rules (docs/design.md, Lifespan, Directory rules): a policy for the files under a directory of a root,
// or only those whose name matches a pattern. Which rule decides for a file is package rules' work; this file keeps
// the rules themselves.

var (
	// ErrRuleExists means the directory has a rule with that pattern already (in any case).
	ErrRuleExists = errors.New("the directory has a rule with that pattern already")
	// ErrNoRule means the directory has no rule with that pattern.
	ErrNoRule = errors.New("no rule with that pattern there (filo rules lists them)")
)

// Rule is a directory rule, with the present files it has dated.
type Rule struct {
	ID          int64
	RootID      int64
	RootPath    string
	Dir         string // relative to the root, '/'-separated; "" for the root itself
	Pattern     string // as typed; rules.Any for none
	Policy      string // the policy's name
	PolicyValue string // the policy's value; "" = none yet
	Dated       int64  // present files this rule dated
	AddedUTC    string
}

// Matcher is the rule as package rules matches it.
func (r Rule) Matcher() rules.Rule {
	return rules.Rule{ID: r.ID, Dir: r.Dir, Pattern: r.Pattern, Policy: r.Policy}
}

const rulesQuery = `
	SELECT ru.id, ru.root_id, ro.path, ru.dir, ru.pattern, pol.name, coalesce(pol.value, ''), ru.added_utc,
	       (SELECT count(*) FROM files f WHERE f.rule_id = ru.id AND f.deleted_utc IS NULL
	          AND EXISTS (SELECT 1 FROM paths p WHERE p.file_id = f.id AND p.valid_to_scan IS NULL))
	FROM rules ru
	JOIN roots ro ON ro.id = ru.root_id
	JOIN policies pol ON pol.id = ru.policy_id`

func scanRule(rows *sql.Rows) (Rule, error) {
	var r Rule
	err := rows.Scan(&r.ID, &r.RootID, &r.RootPath, &r.Dir, &r.Pattern, &r.Policy, &r.PolicyValue, &r.AddedUTC, &r.Dated)
	return r, err
}

// readRules reads the rules at dir and below it in a root (rootID 0: every rule), through q, ordered by root path,
// then as they are listed and tried (rules.Compare).
func readRules(ctx context.Context, q querier, rootID int64, dir string) ([]Rule, error) {
	cond, args := "", []any(nil)
	if rootID != 0 {
		under, uargs := atOrUnder("ru.dir", dir)
		cond, args = ` WHERE ru.root_id = ? AND `+under, append([]any{rootID}, uargs...)
	}
	rs, err := queryAll(ctx, q, scanRule, rulesQuery+cond, args...)
	if err != nil {
		return nil, fmt.Errorf("read rules: %w", err)
	}
	slices.SortFunc(rs, func(a, b Rule) int {
		if c := cmp.Compare(a.RootPath, b.RootPath); c != 0 {
			return c
		}
		return rules.Compare(a.Matcher(), b.Matcher())
	})
	return rs, nil
}

// Rules returns the rules at dir and below it in a root, or every rule with rootID 0, ordered by root, then by
// directory (a parent before the directories inside it), then in the order they are tried.
func (s *Store) Rules(ctx context.Context, rootID int64, dir string) ([]Rule, error) {
	return readRules(ctx, s.db, rootID, dir)
}

// rulePolicy returns the ID of the named policy in tx, creating it with no value if there is none (as a CSV naming a
// new policy does), and whether it did.
func rulePolicy(ctx context.Context, tx *sql.Tx, name string, now time.Time) (int64, bool, error) {
	n, err := PolicyName(name)
	if err != nil {
		return 0, false, err
	}
	p, err := (policyCache{}).get(ctx, tx, n)
	if err != nil {
		return 0, false, err
	}
	if p.id != 0 {
		return p.id, false, nil
	}
	id, err := addPolicy(ctx, tx, n, "", now)
	return id, true, err
}

// findRule returns the rule with exactly pattern (in any case, never matched as a glob) at dir in a root.
func findRule(ctx context.Context, q querier, rootID int64, dir, pattern string) (Rule, error) {
	p, err := rules.Pattern(pattern)
	if err != nil {
		return Rule{}, err
	}
	rs, err := queryAll(ctx, q, scanRule, rulesQuery+` WHERE ru.root_id = ? AND ru.dir = ? AND ru.pattern_key = ?`,
		rootID, dir, rules.Key(p))
	if err != nil {
		return Rule{}, fmt.Errorf("read rule: %w", err)
	}
	if len(rs) == 0 {
		return Rule{}, fmt.Errorf("%s at %s: %w", p, dirName(dir), ErrNoRule)
	}
	return rs[0], nil
}

// dirName is a directory as an error names it: "the root" for "".
func dirName(dir string) string {
	if dir == "" {
		return "the root"
	}
	return dir
}

// insertRule adds a rule in tx and returns its ID; ErrRuleExists if the directory has the pattern in any case.
func insertRule(ctx context.Context, tx *sql.Tx, rootID int64, dir, pattern string, policyID int64,
	now time.Time) (int64, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE root_id = ? AND dir = ? AND pattern_key = ?`,
		rootID, dir, rules.Key(pattern)).Scan(&n); err != nil {
		return 0, fmt.Errorf("add rule: %w", err)
	}
	if n > 0 {
		return 0, fmt.Errorf("%s at %s: %w", pattern, dirName(dir), ErrRuleExists)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO rules (root_id, dir, pattern, pattern_key, letters, policy_id,
		added_utc) VALUES (?, ?, ?, ?, ?, ?, ?)`, rootID, dir, pattern, rules.Key(pattern), rules.Letters(pattern),
		policyID, utc(now))
	if err != nil {
		return 0, fmt.Errorf("add rule: %w", err)
	}
	return res.LastInsertId()
}

// AddRule adds a rule naming policy for the files at dir and below in a root whose name matches pattern ("" for
// every name), and dates the files it decides now, in one transaction (the question before it is the CLI's, from
// PreviewAddRule). A policy that does not exist is created with no value; created says so. ErrRuleExists if the
// directory has that pattern already. today is the local date, YYYY-MM-DD.
func (s *Store) AddRule(ctx context.Context, rootID int64, dir, pattern, policy, today string,
	now time.Time) (Rule, bool, RuleRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	defer tx.Rollback()
	r, created, run, err := addRule(ctx, tx, rootID, dir, pattern, policy, today, now)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	return r, created, run, s.commit(tx)
}

// PreviewAddRule works out what AddRule would do, and changes nothing.
func (s *Store) PreviewAddRule(ctx context.Context, rootID int64, dir, pattern, policy, today string,
	now time.Time) (Rule, bool, RuleRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	defer tx.Rollback() // never committed: the same work as AddRule, undone
	return addRule(ctx, tx, rootID, dir, pattern, policy, today, now)
}

func addRule(ctx context.Context, tx *sql.Tx, rootID int64, dir, pattern, policy, today string,
	now time.Time) (Rule, bool, RuleRun, error) {
	p, err := rules.Pattern(pattern)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	policyID, created, err := rulePolicy(ctx, tx, policy, now)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	id, err := insertRule(ctx, tx, rootID, dir, p, policyID, now)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	afterScan, err := latestScan(ctx, tx)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	run, err := dateByRules(ctx, tx, rootID, map[int64]bool{id: true}, afterScan, today, "rules add", now)
	if err != nil {
		return Rule{}, false, RuleRun{}, err
	}
	r, err := findRule(ctx, tx, rootID, dir, p)
	return r, created, run, err
}

// SetRule changes the policy of the rule with exactly pattern at dir in a root (ErrNoRule if there is none). Files it
// dated keep their policy: the change is for the files it dates from now on. A policy that does not exist is created
// with no value. It returns the rule as it was, with the files it has dated.
func (s *Store) SetRule(ctx context.Context, rootID int64, dir, pattern, policy string,
	now time.Time) (old Rule, created bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, false, err
	}
	defer tx.Rollback()
	if old, err = findRule(ctx, tx, rootID, dir, pattern); err != nil {
		return Rule{}, false, err
	}
	policyID, created, err := rulePolicy(ctx, tx, policy, now)
	if err != nil {
		return Rule{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE rules SET policy_id = ? WHERE id = ?`, policyID, old.ID); err != nil {
		return Rule{}, false, fmt.Errorf("set rule: %w", err)
	}
	return old, created, s.commit(tx)
}

// RemoveRule removes the rule with exactly pattern at dir in a root (ErrNoRule if there is none) and returns it.
// Files it dated keep their policy, and stay dated by a rule, so no rule dates them again.
func (s *Store) RemoveRule(ctx context.Context, rootID int64, dir, pattern string) (Rule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()
	r, err := findRule(ctx, tx, rootID, dir, pattern)
	if err != nil {
		return Rule{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE files SET rule_id = NULL WHERE rule_id = ?`, r.ID); err != nil {
		return Rule{}, fmt.Errorf("remove rule: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, r.ID); err != nil {
		return Rule{}, fmt.Errorf("remove rule: %w", err)
	}
	return r, s.commit(tx)
}

// rulesUsing counts the rules naming a policy, in q.
func rulesUsing(ctx context.Context, q querier, policyID int64) (int64, error) {
	var n int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM rules WHERE policy_id = ?`, policyID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count rules: %w", err)
	}
	return n, nil
}
