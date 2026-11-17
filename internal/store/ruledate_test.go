package store

import (
	"slices"
	"strconv"
	"strings"
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
	// The files first, then *2024* written straight into the register, so it has set no policy yet (a scan would).
	e.apply(Changes{New: []NewFile{f("bank/bank-2024.pdf", "h1"), f("bank/bank.pdf", "h2")}})
	if _, err := e.s.db.Exec(`INSERT INTO policies (name, added_utc, changed_utc) VALUES ('tax', 'x', 'x');
		INSERT INTO rules (root_id, dir, pattern, pattern_key, letters, policy_id, added_utc)
			VALUES (1, 'bank', '*2024*', '*2024*', 4, (SELECT id FROM policies WHERE name = 'tax'), 'x')`); err != nil {
		t.Fatal(err)
	}
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
	ties, err := e.s.RuleTies(e.ctx, e.root.ID, ruleToday, policyNow)
	if err != nil || len(ties) != 1 || ties[0].Path != "bank/bank-2024.pdf" {
		t.Errorf("RuleTies = %+v, %v; want bank-2024.pdf", ties, err)
	}
	if v := rawQueryString(t, e.db, `SELECT count(*) FROM reviews`); v != "1" {
		t.Errorf("RuleTies left %s history rows, want the 1 from adding *bank*", v)
	}
}

// TestRuleDatingUnreadDirs: a file under a directory the latest scan covering it could not read is not dated by a
// rule (scenario F2); once a later scan covers that directory and reads it, it is.
func TestRuleDatingUnreadDirs(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("bank/locked/a.pdf", "h1"), f("bank/open.pdf", "h2"), f("bank/b.pdf", "h3")}})
	e.apply(Changes{Events: []Event{{Kind: "error", Path: "bank/locked", Detail: "open: permission denied"},
		{Kind: "error", Path: "bank/b.pdf", Detail: "hash: read: input/output error"}}})
	// A later scan of another directory does not cover bank/locked.
	if _, err := e.s.ApplyScan(e.ctx, ScanRecord{RootID: e.root.ID, Scope: "notes", Started: policyNow},
		Changes{}); err != nil {
		t.Fatal(err)
	}
	_, _, run, err := e.s.PreviewAddRule(e.ctx, e.root.ID, "bank", "", "bills", ruleToday, policyNow)
	if err != nil {
		t.Fatal(err)
	}
	var dated []string
	for _, d := range run.Dated {
		dated = append(dated, d.Path)
	}
	if !slices.Equal(dated, []string{"bank/b.pdf", "bank/open.pdf"}) {
		t.Errorf("dated %q, want all but bank/locked/a.pdf (a file that could not be hashed is not a directory)", dated)
	}
	// A scan of bank reads it now.
	if _, err := e.s.ApplyScan(e.ctx, ScanRecord{RootID: e.root.ID, Scope: "bank", Started: policyNow},
		Changes{}); err != nil {
		t.Fatal(err)
	}
	if _, _, run, err = e.s.PreviewAddRule(e.ctx, e.root.ID, "bank", "", "bills", ruleToday, policyNow); err != nil ||
		len(run.Dated) != 3 {
		t.Errorf("after bank was read again: dated %d, %v; want 3", len(run.Dated), err)
	}
	// The whole root unreadable in its latest scan: nothing is dated.
	e.apply(Changes{Events: []Event{{Kind: "error", Path: "", Detail: "open: permission denied"}}})
	if _, _, run, err = e.s.PreviewAddRule(e.ctx, e.root.ID, "bank", "", "bills", ruleToday, policyNow); err != nil ||
		len(run.Dated) != 0 {
		t.Errorf("the root unread: dated %d, %v; want none", len(run.Dated), err)
	}
}

// TestScanAppliesRules covers scenarios B1, C2 and F2 in a scan: every scan sets the policy of the files in its scope
// with no retention, stamped with that scan; ties and directories it could not read are left alone.
func TestScanAppliesRules(t *testing.T) {
	e := newReviewEnv(t)
	e.addRule("bank", "", "bills")
	e.addRule("bank", "*2024*", "tax")
	e.addRule("bank", "*bank*", "banking")
	scan := func(scope string, ch Changes) (int64, RuleRun) {
		t.Helper()
		id, run, err := e.s.ApplyScanRules(e.ctx, ScanRecord{RootID: e.root.ID, Scope: scope, Started: policyNow}, ch)
		if err != nil {
			t.Fatal(err)
		}
		return id, run
	}
	id, run := scan("", Changes{
		New:    []NewFile{f("bank/a.pdf", "h1"), f("bank/bank-2024.pdf", "h2"), f("bank/locked/b.pdf", "h3"), f("notes/c", "h4")},
		Events: []Event{{Kind: "error", Path: "bank/locked", Detail: "open: permission denied"}},
	})
	if len(run.Dated) != 1 || run.Dated[0].Path != "bank/a.pdf" || run.Dated[0].Policy != "bills" {
		t.Errorf("dated %+v, want bank/a.pdf on bills", run.Dated)
	}
	if len(run.Ties) != 1 || run.Ties[0].Path != "bank/bank-2024.pdf" {
		t.Errorf("ties %+v, want bank/bank-2024.pdf", run.Ties)
	}
	if v := rawQueryString(t, e.db, `SELECT reviewed_after_scan FROM files WHERE rule_id IS NOT NULL`); v !=
		strconv.FormatInt(id, 10) {
		t.Errorf("set after scan %s, want this scan, %d", v, id)
	}
	if v := e.ruleColumns("bank/locked/b.pdf"); v != "- - - rule - never hash -" {
		t.Errorf("a file under a directory this scan could not read: %q, want untouched", v)
	}
	// The next scan reads bank/locked: its file is given a policy, though it is not new; the tie stays a tie.
	_, run = scan("bank", Changes{})
	if len(run.Dated) != 1 || run.Dated[0].Path != "bank/locked/b.pdf" || len(run.Ties) != 1 {
		t.Errorf("second scan: dated %+v, ties %+v; want bank/locked/b.pdf and the same tie", run.Dated, run.Ties)
	}
	// A scan of another directory sets no policy outside its scope.
	e.addRule("notes", "", "bills")
	if _, err := e.s.db.Exec(`UPDATE files SET rule_dated_utc = NULL, rule_id = NULL, policy_id = NULL WHERE id = ?`,
		e.knownAll("notes/c")); err != nil {
		t.Fatal(err)
	}
	if _, run = scan("bank", Changes{}); len(run.Dated) != 0 {
		t.Errorf("a scan of bank set a policy on %+v", run.Dated)
	}
}

// TestRetentionByHandLeavesTheRule: a retention set in the CSV takes a file off the rule that set its policy (it no
// longer counts as set by it), and no rule sets its policy again (scenario B5).
func TestRetentionByHandLeavesTheRule(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("bank/a.pdf", "h1"), f("bank/b.pdf", "h2")}})
	r := e.addRule("bank", "", "bills")
	if r.Dated != 2 {
		t.Fatalf("rule set a policy on %d files, want 2", r.Dated)
	}
	exportID, _ := e.export(ruleToday, policyNow)
	if res, err := e.s.UpdateRetention(e.ctx, []RetentionRow{{Line: 2, ExportID: exportID,
		FileID: e.knownAll("bank/a.pdf"), Retention: "2032-01-01"}}, policyNow, ruleToday, "r.csv", false,
		nil); err != nil || res.Set != 1 {
		t.Fatalf("update: %+v, %v", res, err)
	}
	if v := e.ruleColumns("bank/a.pdf"); v != "2032-01-01 - 2032-01-01 rule - dated hash h1" {
		t.Errorf("bank/a.pdf after the CSV: %q, want its own date, no rule, still marked", v)
	}
	if rs, _ := e.s.Rules(e.ctx, e.root.ID, ""); len(rs) != 1 || rs[0].Dated != 1 {
		t.Errorf("rules = %+v, want the rule counting 1 file", rs)
	}
	// The step a scan points to, a file_date filled in under the same policy, keeps the file on its rule.
	exportID, _ = e.export(ruleToday, policyNow)
	if res, err := e.s.UpdateRetention(e.ctx, []RetentionRow{{Line: 2, ExportID: exportID,
		FileID: e.knownAll("bank/b.pdf"), FileDate: "2020-01-01", HasFileDate: true, Retention: "bills"}}, policyNow, ruleToday, "r.csv",
		false, nil); err != nil || res.Set != 1 {
		t.Fatalf("update with a file_date: %+v, %v", res, err)
	}
	if v := e.ruleColumns("bank/b.pdf"); v != "- bills - rule "+strconv.FormatInt(r.ID, 10)+" dated hash h2" {
		t.Errorf("bank/b.pdf after its file_date was filled in: %q, want still on the rule", v)
	}
	e.apply(Changes{}) // a scan applies the rules: bank/a.pdf has a retention, so they leave it alone
	if v := e.ruleColumns("bank/a.pdf"); !strings.HasPrefix(v, "2032-01-01 - ") {
		t.Errorf("bank/a.pdf after a scan: %q, want its own date still", v)
	}
}
