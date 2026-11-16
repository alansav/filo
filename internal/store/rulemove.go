package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Moving and copying directory rules (docs/design.md, Lifespan, Directory rules, filo rules move and copy).

var (
	// ErrNoRulesToMove means a move or copy from a directory with no rules at or below it.
	ErrNoRulesToMove = errors.New("no rules at or below it")
	// ErrRulesThere means a move or copy to a directory that has rules at or below it: they are never merged.
	ErrRulesThere = errors.New("it has rules already: remove them first, or add the rules by hand")
	// ErrRuleInside means a move or copy of a directory's rules to itself or a directory inside it.
	ErrRuleInside = errors.New("it is the directory moved from, or inside it")
)

// RulesThereError is ErrRulesThere, with the rules found at or below the directory moved or copied to.
type RulesThereError struct{ Rules []Rule }

func (e RulesThereError) Error() string {
	n := int64(len(e.Rules))
	return fmt.Sprintf("%d %s: %s", n, plural(n, "rule", "rules"), ErrRulesThere)
}

func (e RulesThereError) Unwrap() error { return ErrRulesThere }

// RuleMove is a move or copy of the rules at a directory and below it to the same places under another.
type RuleMove struct {
	FromRoot int64
	FromDir  string
	ToRoot   int64
	ToDir    string
	Copy     bool // keep the rules at FromDir too
}

// newDir is where a rule at dir (at or below m.FromDir) goes.
func (m RuleMove) newDir(dir string) string {
	rest := dir
	if m.FromDir != "" {
		rest = strings.TrimPrefix(strings.TrimPrefix(dir, m.FromDir), "/")
	}
	switch {
	case rest == "":
		return m.ToDir
	case m.ToDir == "":
		return rest
	}
	return m.ToDir + "/" + rest
}

// checkMove refuses a move of a directory's rules to itself or inside it, from a directory with no rules, or to one
// with rules at or below it, and returns the rules to move.
func checkMove(ctx context.Context, q querier, m RuleMove) ([]Rule, error) {
	if m.FromRoot == m.ToRoot && isAtOrUnder(m.ToDir, m.FromDir) {
		return nil, ErrRuleInside
	}
	from, err := readRules(ctx, q, m.FromRoot, m.FromDir)
	if err != nil {
		return nil, err
	}
	if len(from) == 0 {
		return nil, ErrNoRulesToMove
	}
	there, err := readRules(ctx, q, m.ToRoot, m.ToDir)
	if err != nil {
		return nil, err
	}
	if len(there) > 0 {
		return nil, RulesThereError{Rules: there}
	}
	return from, nil
}

// isAtOrUnder reports whether rel is dir or below it ("" is the whole root): the Go side of atOrUnder.
func isAtOrUnder(rel, dir string) bool {
	return dir == "" || rel == dir || strings.HasPrefix(rel, dir+"/")
}

// MoveRules moves (or, with m.Copy, copies) the rules at m.FromDir and below it to the same places under m.ToDir,
// in one transaction, and returns the rules as they are now at their new places. Refused, with nothing changed, if
// m.ToDir is m.FromDir or inside it (ErrRuleInside), if there are no rules to move (ErrNoRulesToMove), or if m.ToDir
// or a directory below it has rules (RulesThereError: they are never merged). Files dated by a moved rule keep their
// policy and stay dated by it; a copy has dated none yet.
func (s *Store) MoveRules(ctx context.Context, m RuleMove, now time.Time) ([]Rule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	from, err := checkMove(ctx, tx, m)
	if err != nil {
		return nil, err
	}
	for _, r := range from {
		dir := m.newDir(r.Dir)
		if m.Copy {
			if _, err := tx.ExecContext(ctx, `INSERT INTO rules (root_id, dir, pattern, pattern_key, letters, policy_id,
				added_utc) SELECT ?, ?, pattern, pattern_key, letters, policy_id, ? FROM rules WHERE id = ?`,
				m.ToRoot, dir, utc(now), r.ID); err != nil {
				return nil, fmt.Errorf("copy rules: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE rules SET root_id = ?, dir = ? WHERE id = ?`, m.ToRoot, dir,
			r.ID); err != nil {
			return nil, fmt.Errorf("move rules: %w", err)
		}
	}
	moved, err := readRules(ctx, tx, m.ToRoot, m.ToDir)
	if err != nil {
		return nil, err
	}
	return moved, s.commit(tx)
}
