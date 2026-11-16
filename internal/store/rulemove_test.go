package store

import (
	"errors"
	"slices"
	"testing"
)

// TestMoveRules covers scenarios E1-E3 in the store.
func TestMoveRules(t *testing.T) {
	setup := func(t *testing.T) *reviewEnv {
		e := newReviewEnv(t)
		e.addRule("bank", "*Statement*", "statements")
		e.addRule("bank", "", "bills")
		e.addRule("bank/2024", "", "tax")
		e.addRule("other", "", "bills")
		return e
	}
	t.Run("E1 move after a rename, with a subdirectory", func(t *testing.T) {
		e := setup(t)
		before, _ := e.s.Rules(e.ctx, e.root.ID, "bank")
		moved, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, FromDir: "bank", ToRoot: e.root.ID,
			ToDir: "banking"}, policyNow)
		if err != nil || len(moved) != 3 {
			t.Fatalf("move = %+v, %v", moved, err)
		}
		want := []string{"banking *Statement* statements", "banking * bills", "banking/2024 * tax", "other * bills"}
		if got := e.ruleList(0, ""); !slices.Equal(got, want) {
			t.Errorf("after move: %q, want %q", got, want)
		}
		if moved[0].ID != before[0].ID {
			t.Errorf("a moved rule has a new ID (%d, was %d): its files would no longer count as dated by it",
				moved[0].ID, before[0].ID)
		}
	})
	t.Run("E2 copy keeps the rules where they were", func(t *testing.T) {
		e := setup(t)
		if _, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, FromDir: "bank/2024", ToRoot: e.root.ID,
			ToDir: "bank/2025", Copy: true}, policyNow); err != nil {
			t.Fatal(err)
		}
		want := []string{"bank *Statement* statements", "bank * bills", "bank/2024 * tax", "bank/2025 * tax",
			"other * bills"}
		if got := e.ruleList(0, ""); !slices.Equal(got, want) {
			t.Errorf("after copy: %q, want %q", got, want)
		}
	})
	t.Run("the root's rules, to a directory and back", func(t *testing.T) {
		e := newReviewEnv(t)
		e.addRule("", "*.tmp", "scratch")
		e.addRule("a", "", "bills")
		if _, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, ToRoot: e.root.ID, ToDir: "x"},
			policyNow); !errors.Is(err, ErrRuleInside) {
			t.Errorf("the root into a directory of it: err = %v, want ErrRuleInside", err)
		}
		if _, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, FromDir: "a", ToRoot: e.root.ID, ToDir: "b",
			Copy: true}, policyNow); err != nil {
			t.Fatal(err)
		}
		if got, want := e.ruleList(0, ""), []string{". *.tmp scratch", "a * bills", "b * bills"}; !slices.Equal(got,
			want) {
			t.Errorf("after copy: %q, want %q", got, want)
		}
	})
	t.Run("E3 refused, nothing changed", func(t *testing.T) {
		e := setup(t)
		for _, tc := range []struct {
			name     string
			from, to string
			want     error
		}{
			{"to a directory with rules", "bank", "other", ErrRulesThere},
			{"to itself", "bank", "bank", ErrRuleInside},
			{"inside itself", "bank", "bank/old", ErrRuleInside},
			{"from a directory with no rules at or below it", "nothing", "x", ErrNoRulesToMove},
			{"to a parent whose subdirectory has rules", "other", "", ErrRulesThere},
		} {
			_, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, FromDir: tc.from, ToRoot: e.root.ID,
				ToDir: tc.to}, policyNow)
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
			}
		}
		var there RulesThereError
		_, err := e.s.MoveRules(e.ctx, RuleMove{FromRoot: e.root.ID, FromDir: "other", ToRoot: e.root.ID,
			ToDir: "bank", Copy: true}, policyNow)
		if !errors.As(err, &there) || len(there.Rules) != 3 || there.Error() != "3 rules: "+ErrRulesThere.Error() {
			t.Errorf("copy onto bank: %v (%+v), want the 3 rules at and below it", err, there.Rules)
		}
		want := []string{"bank *Statement* statements", "bank * bills", "bank/2024 * tax", "other * bills"}
		if got := e.ruleList(0, ""); !slices.Equal(got, want) {
			t.Errorf("after the refusals: %q, want %q", got, want)
		}
	})
}
