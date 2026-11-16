package store

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/rules"
)

// Directory rules (docs/design.md, Lifespan, Directory rules): the register and store, slice 1.

// ruleToday is the local date the rule tests work out statuses on.
const ruleToday = "2026-11-16"

// TestMigrateFromV9 is scenario G1: the upgrade creates the rules table and the files' columns, and nothing else
// changes.
func TestMigrateFromV9(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropRules, `UPDATE meta SET value = '9' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO policies (id, name, value, added_utc, changed_utc) VALUES (1, 'bills', '7y', 'x', 'x')`,
		`INSERT INTO files (id, kind, size, mtime_ns, hash, first_seen_scan, file_date, policy_id, keep_until)
			VALUES (1, 'file', 3, 1, 'a', 1, '2020-01-01', 1, '2027-01-01')`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) || SchemaVersion != 10 {
		t.Fatalf("schema_version = %s after upgrade, want 10", v)
	}
	var policy, keep string
	var rule, dated *string
	if err := s.db.QueryRow(`SELECT policy_id, keep_until, rule_id, rule_dated_utc FROM files WHERE id = 1`).
		Scan(&policy, &keep, &rule, &dated); err != nil || policy != "1" || keep != "2027-01-01" || rule != nil ||
		dated != nil {
		t.Errorf("file after upgrade: policy %s, %s, rule %v, dated %v (%v)", policy, keep, rule, dated, err)
	}
	counts, err := s.RowCounts(t.Context())
	if err != nil || counts["rules"] != 0 || counts["policies"] != 1 {
		t.Errorf("row counts after upgrade: %v, %v", counts, err)
	}
	if notes := s.UpgradeNotes(); len(notes) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", notes)
	}
}

// ruleList returns the rules as "dir pattern policy" lines, "." for the root, in the order Rules gives them.
func (e *reviewEnv) ruleList(rootID int64, dir string) []string {
	e.t.Helper()
	rs, err := e.s.Rules(e.ctx, rootID, dir)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		d := r.Dir
		if d == "" {
			d = "."
		}
		out = append(out, d+" "+r.Pattern+" "+r.Policy)
	}
	return out
}

func (e *reviewEnv) addRule(dir, pattern, policy string) Rule {
	e.t.Helper()
	r, _, _, err := e.s.AddRule(e.ctx, e.root.ID, dir, pattern, policy, ruleToday, policyNow)
	if err != nil {
		e.t.Fatalf("add rule %s %s %s: %v", dir, pattern, policy, err)
	}
	return r
}

// TestAddRule covers scenarios A2 and A3 in the store: a new policy is created with no value, a pattern the
// directory has (in any case) is refused, and rules are listed in the order they are tried.
func TestAddRule(t *testing.T) {
	e := newReviewEnv(t)
	if err := e.s.AddPolicy(e.ctx, "statements", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	r, created, _, err := e.s.AddRule(e.ctx, e.root.ID, "bank", "", "Bills", ruleToday, policyNow)
	if err != nil || !created || r.Pattern != "*" || r.Policy != "bills" || r.PolicyValue != "" || r.Dir != "bank" {
		t.Fatalf("add rule with a new policy = %+v, created %v, %v", r, created, err)
	}
	if p, err := e.s.Policy(e.ctx, "bills"); err != nil || p.Value != "" {
		t.Errorf("the policy created: %+v, %v", p, err)
	}
	if r, created, _, err = e.s.AddRule(e.ctx, e.root.ID, "bank", "*Statement*", "statements", ruleToday, policyNow); err != nil ||
		created || r.PolicyValue != "7y" {
		t.Errorf("add rule with an existing policy = %+v, created %v, %v", r, created, err)
	}
	e.addRule("bank", "*DailyStatement*", "statements")
	e.addRule("bank/2024", "*", "bills")
	e.addRule("", "*.tmp", "bills")
	e.addRule("bank-x", "*", "bills")

	for _, tc := range []struct {
		name, dir, pattern, policy string
		want                       error
	}{
		{"the same pattern in another case", "bank", "*statement*", "bills", ErrRuleExists},
		{"no pattern, when * is there", "bank", "", "bills", ErrRuleExists},
		{"** is *", "bank", "**", "bills", ErrRuleExists},
		{"a pattern with /", "bank", "a/*", "bills", rules.ErrPattern},
		{"not a policy name", "bank", "*.pdf", "bills!", ErrPolicyName},
		{"forever is not a policy", "bank", "*.pdf", "forever", ErrPolicyName},
		{"*? is ?*", "bank", "*?", "bills", nil},
		{"?* after *?", "bank", "?*", "bills", ErrRuleExists},
		{"a directory ending in /", "bank/", "*.pdf", "bills", ErrRuleDir},
		{"a directory starting with ./", "./bank", "*.pdf", "bills", ErrRuleDir},
		{"a directory with //", "bank//2024", "*.pdf", "bills", ErrRuleDir},
		{"a directory with ..", "bank/../x", "*.pdf", "bills", ErrRuleDir},
		{"an absolute directory", "/bank", "*.pdf", "bills", ErrRuleDir},
	} {
		if _, _, _, err := e.s.AddRule(e.ctx, e.root.ID, tc.dir, tc.pattern, tc.policy, ruleToday, policyNow); (err ==
			nil) != (tc.want == nil) || !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, _, _, err := e.s.AddRule(e.ctx, 999, "bank", "*.doc", "nopolicy", ruleToday, policyNow); !errors.Is(err,
		ErrRuleRoot) {
		t.Errorf("a root that is not registered: err = %v, want ErrRuleRoot", err)
	}
	if _, err := e.s.Policy(e.ctx, "nopolicy"); err == nil {
		t.Error("a rule refused for its root created its policy")
	}
	if _, err := e.s.RemoveRule(e.ctx, e.root.ID, "bank/", "*?"); !errors.Is(err, ErrRuleDir) {
		t.Errorf("remove at bank/: err = %v, want ErrRuleDir", err)
	}
	if _, err := e.s.RemoveRule(e.ctx, e.root.ID, "bank", "*?"); err != nil {
		t.Errorf("remove *? (stored as ?*): %v", err)
	}
	if _, err := e.s.Policy(e.ctx, "bills!"); err == nil {
		t.Error("a refused rule created its policy")
	}

	want := []string{". *.tmp bills", "bank *DailyStatement* statements", "bank *Statement* statements", "bank * bills",
		"bank/2024 * bills", "bank-x * bills"}
	if got := e.ruleList(0, ""); !slices.Equal(got, want) {
		t.Errorf("every rule:\n%q\nwant\n%q", got, want)
	}
	if got := e.ruleList(e.root.ID, "bank"); !slices.Equal(got, want[1:5]) {
		t.Errorf("rules at bank and below:\n%q\nwant\n%q", got, want[1:5])
	}
	if got := e.ruleList(e.root.ID, "bank/2024/q1"); len(got) != 0 {
		t.Errorf("rules below bank/2024/q1 = %q, want none (the rules above it are not listed)", got)
	}
}

// TestSetAndRemoveRule covers scenario D2 in the store: set and remove name the pattern exactly, in any case, never
// as a glob; a removed rule's files keep their policy and stay dated by a rule.
func TestSetAndRemoveRule(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("bank/a.pdf", "ha")}})
	e.addRule("bank", "*Statement*", "statements")
	r := e.addRule("bank", "", "bills")

	for _, pattern := range []string{"*Statement", "*DailyStatement*", "*Statement**x"} {
		if _, _, err := e.s.SetRule(e.ctx, e.root.ID, "bank", pattern, "other", policyNow); !errors.Is(err, ErrNoRule) {
			t.Errorf("set %s: err = %v, want ErrNoRule", pattern, err)
		}
		if _, err := e.s.RemoveRule(e.ctx, e.root.ID, "bank", pattern); !errors.Is(err, ErrNoRule) {
			t.Errorf("remove %s: err = %v, want ErrNoRule", pattern, err)
		}
	}
	if _, _, err := e.s.SetRule(e.ctx, e.root.ID, "bank/2024", "*", "other", policyNow); !errors.Is(err, ErrNoRule) {
		t.Errorf("set at a directory with no rules: err = %v, want ErrNoRule", err)
	}
	old, created, err := e.s.SetRule(e.ctx, e.root.ID, "bank", "*STATEMENT*", "letters", policyNow)
	if err != nil || !created || old.Policy != "statements" {
		t.Errorf("set = %+v, created %v, %v", old, created, err)
	}
	if got, want := e.ruleList(0, ""), []string{"bank *Statement* letters", "bank * bills"}; !slices.Equal(got, want) {
		t.Errorf("after set: %q, want %q", got, want)
	}

	// A file the * rule dated: removing the rule leaves its policy and the mark that a rule dated it.
	id := e.known()["bank/a.pdf"].FileID
	if _, err := e.s.db.Exec(`UPDATE files SET policy_id = (SELECT id FROM policies WHERE name = 'bills'), rule_id = ?,
		rule_dated_utc = 'x' WHERE id = ?`, r.ID, id); err != nil {
		t.Fatal(err)
	}
	if rs, _ := e.s.Rules(e.ctx, e.root.ID, ""); len(rs) != 2 || rs[1].Dated != 1 {
		t.Errorf("rules = %+v, want the * rule with 1 file dated", rs)
	}
	if got, err := e.s.RemoveRule(e.ctx, e.root.ID, "bank", ""); err != nil || got.ID != r.ID {
		t.Fatalf("remove * = %+v, %v", got, err)
	}
	if v := rawQueryString(t, e.db, `SELECT coalesce(rule_id, 'none') || ' ' || rule_dated_utc || ' ' ||
		policy_id FROM files WHERE id = `+strconv.FormatInt(id, 10)); v != "none x 2" {
		t.Errorf("the file after its rule was removed: %q, want no rule, still dated by one, policy bills", v)
	}
	if got, want := e.ruleList(0, ""), []string{"bank *Statement* letters"}; !slices.Equal(got, want) {
		t.Errorf("after remove: %q, want %q", got, want)
	}
}

// TestPoliciesAndRules is scenario D3: a policy a rule names is not removed, and a move of a policy takes its rules.
func TestPoliciesAndRules(t *testing.T) {
	e := newReviewEnv(t)
	e.addRule("bank", "", "bils")
	if err := e.s.RemovePolicy(e.ctx, "bils", policyNow); !errors.Is(err, ErrPolicyInUse) ||
		!strings.Contains(err.Error(), "1 rule ") {
		t.Errorf("remove a policy a rule names: %v, want ErrPolicyInUse naming 1 rule", err)
	}
	if _, err := e.s.MovePolicy(e.ctx, "bils", "bills", policyNow); err != nil { // a rename
		t.Fatal(err)
	}
	if got := e.ruleList(0, ""); !slices.Equal(got, []string{"bank * bills"}) {
		t.Errorf("after a rename: %q", got)
	}
	if err := e.s.AddPolicy(e.ctx, "financial", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.MovePolicy(e.ctx, "bills", "financial", policyNow); err != nil { // a merge
		t.Fatal(err)
	}
	if got := e.ruleList(0, ""); !slices.Equal(got, []string{"bank * financial"}) {
		t.Errorf("after a merge: %q", got)
	}
	if err := e.s.RemovePolicy(e.ctx, "bills", policyNow); err != nil {
		t.Errorf("remove the policy merged away: %v", err)
	}
}
