package store

import (
	"slices"
	"strconv"
	"testing"
)

// ruleColumns returns a file's columns as one line, for the dating tests.
func (e *reviewEnv) ruleColumns(path string) string {
	e.t.Helper()
	return rawQueryString(e.t, e.db, `SELECT coalesce(retention, '-') || ' ' || coalesce((SELECT name FROM policies
		WHERE id = policy_id), '-') || ' ' || coalesce(keep_until, '-') || ' rule ' || coalesce(rule_id, '-') || ' ' ||
		CASE WHEN rule_dated_utc IS NULL THEN 'never' ELSE 'dated' END || ' hash ' || coalesce(reviewed_hash, '-')
		FROM files WHERE id = `+strconv.FormatInt(e.knownAll(path), 10))
}

// TestRuleDating covers scenarios A1, B2-B5 in the store: which files adding a rule dates, what it records, and that
// the preview changes nothing.
func TestRuleDating(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("bank/new.pdf", "h1"), f("bank/nodate.pdf", "h2"), f("bank/own.pdf", "h3"),
		f("bank/novalue.pdf", "h4"), f("bank/unread.pdf", "h5"), f("bank/cleared.pdf", "h6"), f("old/moved.pdf", "h7"),
		f("other/x.pdf", "h8"), f("bank/sub/deep.pdf", "h9")}})
	k := e.known()
	scan2 := e.apply(Changes{
		Close:      []int64{k["old/moved.pdf"].PathID},
		New:        []NewFile{f("bank/moved.pdf", "h7b")},
		Unreadable: []int64{k["bank/unread.pdf"].FileID},
		Events: []Event{{Kind: "possible_move", FileID: k["old/moved.pdf"].FileID, Path: "bank/moved.pdf",
			OtherPath: "old/moved.pdf", Detail: "same name, different content"}},
	})
	if err := e.s.AddPolicy(e.ctx, "empty", "", policyNow); err != nil {
		t.Fatal(err)
	}
	if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	for _, st := range []struct {
		q    string
		path string
	}{
		{`UPDATE files SET file_date = '2020-03-01' WHERE id = ?`, "bank/new.pdf"},
		{`UPDATE files SET retention = '2030-01-01', keep_until = '2030-01-01' WHERE id = ?`, "bank/own.pdf"},
		{`UPDATE files SET policy_id = (SELECT id FROM policies WHERE name = 'empty') WHERE id = ?`, "bank/novalue.pdf"},
		{`UPDATE files SET rule_dated_utc = 'x' WHERE id = ?`, "bank/cleared.pdf"},
	} {
		if _, err := e.s.db.Exec(st.q, k[st.path].FileID); err != nil {
			t.Fatal(err)
		}
	}
	before := e.ruleColumns("bank/own.pdf")

	// A1: the preview lists what the rule would date, and changes nothing.
	r, created, run, err := e.s.PreviewAddRule(e.ctx, e.root.ID, "bank", "*.pdf", "bills", ruleToday, policyNow)
	if err != nil || created || r.Pattern != "*.pdf" {
		t.Fatalf("preview = %+v, created %v, %v", r, created, err)
	}
	var got []string
	for _, d := range run.Dated {
		got = append(got, d.Path+" "+d.KeepUntil+" "+string(d.Status))
	}
	want := []string{"bank/new.pdf 2027-03-01 kept", "bank/nodate.pdf  needs_file_date",
		"bank/sub/deep.pdf  needs_file_date"}
	if !slices.Equal(got, want) || len(run.Ties) != 0 {
		t.Errorf("preview dates:\n%q\nwant\n%q (ties %v)", got, want, run.Ties)
	}
	if rs := e.ruleList(0, ""); len(rs) != 0 {
		t.Errorf("the preview added a rule: %q", rs)
	}
	if v := e.ruleColumns("bank/new.pdf"); v != "- - - rule - never hash -" {
		t.Errorf("the preview dated a file: %q", v)
	}
	if _, _, _, err := e.s.PreviewAddRule(e.ctx, e.root.ID, "bank", "*.pdf", "newpolicy", ruleToday,
		policyNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Policy(e.ctx, "newpolicy"); err == nil {
		t.Error("the preview created a policy")
	}

	// Adding it dates the same files.
	r, _, run, err = e.s.AddRule(e.ctx, e.root.ID, "bank", "*.pdf", "bills", ruleToday, policyNow)
	if err != nil || len(run.Dated) != 3 || r.Dated != 3 {
		t.Fatalf("add = %+v (%d dated), %v", r, len(run.Dated), err)
	}
	id := strconv.FormatInt(r.ID, 10)
	for path, want := range map[string]string{
		"bank/new.pdf":      "- bills 2027-03-01 rule " + id + " dated hash h1",
		"bank/nodate.pdf":   "- bills - rule " + id + " dated hash h2", // B4: needs a file_date
		"bank/sub/deep.pdf": "- bills - rule " + id + " dated hash h9",
		"bank/own.pdf":      before,                          // B2: a retention of its own
		"bank/novalue.pdf":  "- empty - rule - never hash -", // B2: a policy with no value is a choice
		"bank/unread.pdf":   "- - - rule - never hash -",     // B3: unreadable
		"bank/moved.pdf":    "- - - rule - never hash -",     // B3: the new side of a possible move
		"bank/cleared.pdf":  "- - - rule - dated hash -",     // B5: a rule dated it before
		"other/x.pdf":       "- - - rule - never hash -",     // not under the rule
	} {
		if v := e.ruleColumns(path); v != want {
			t.Errorf("%s: %q, want %q", path, v, want)
		}
	}
	st := e.statuses(ruleToday)
	if st["bank/new.pdf"] != StatusKept || st["bank/nodate.pdf"] != StatusNeedsFileDate {
		t.Errorf("statuses after: %v", st)
	}
	if v := rawQueryString(t, e.db, `SELECT group_concat(b.command || ':' || r.retention || ':' || coalesce(r.hash, ''),
		' ') FROM reviews r JOIN review_batches b ON b.id = r.batch_id ORDER BY r.id`); v !=
		"rules add:bills:h1 rules add:bills:h2 rules add:bills:h9" {
		t.Errorf("history = %q", v)
	}
	if v := rawQueryString(t, e.db, `SELECT DISTINCT reviewed_after_scan FROM files WHERE rule_id IS NOT NULL`); v !=
		strconv.FormatInt(scan2, 10) {
		t.Errorf("dated after scan %s, want the latest, %d", v, scan2)
	}

	// The files dated are not dated again by a rule added later, nearer or longer; nor is a file the person cleared.
	if _, err := e.s.db.Exec(`UPDATE files SET policy_id = NULL, keep_until = NULL WHERE id = ?`,
		k["bank/nodate.pdf"].FileID); err != nil {
		t.Fatal(err)
	}
	if _, _, run, err := e.s.AddRule(e.ctx, e.root.ID, "bank", "*date*", "empty", ruleToday, policyNow); err != nil ||
		len(run.Dated) != 0 {
		t.Errorf("a second rule dated %+v, %v; want none", run.Dated, err)
	}
}

// TestRuleTies covers scenario C2 in the store: a file two rules of one directory match equally is not dated, and the
// rule just added reports it.
func TestRuleTies(t *testing.T) {
	e := newReviewEnv(t)
	e.addRule("bank", "*2024*", "tax")
	e.apply(Changes{New: []NewFile{f("bank/bank-2024.pdf", "h1"), f("bank/bank.pdf", "h2")}})
	r, _, run, err := e.s.AddRule(e.ctx, e.root.ID, "bank", "*bank*", "banking", ruleToday, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Dated) != 1 || run.Dated[0].Path != "bank/bank.pdf" {
		t.Errorf("dated %+v, want bank/bank.pdf only", run.Dated)
	}
	if len(run.Ties) != 1 || run.Ties[0].Path != "bank/bank-2024.pdf" || len(run.Ties[0].Rules) != 2 ||
		run.Ties[0].Rules[0].Pattern != "*2024*" || run.Ties[0].Rules[1].ID != r.ID {
		t.Errorf("ties = %+v, want bank-2024.pdf between *2024* and *bank*", run.Ties)
	}
	if v := e.ruleColumns("bank/bank-2024.pdf"); v != "- - - rule - never hash -" {
		t.Errorf("the tied file: %q, want undated", v)
	}
}
