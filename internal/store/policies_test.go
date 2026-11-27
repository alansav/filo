package store

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Named retention policies (docs/design.md, Lifespan, Named retention policies): the register and store, slice 1.

var policyNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// onPolicy puts the file at path on the policy, with a file_date, as the CSV will (slice 2): its own retention
// cleared, its content the one decided on.
func (e *reviewEnv) onPolicy(path, policy, fileDate string) {
	e.t.Helper()
	k := e.known()[path]
	if _, err := e.s.db.Exec(`UPDATE files SET policy_id = (SELECT id FROM policies WHERE name = ?), retention = NULL,
		file_date = ?, reviewed_hash = hash WHERE id = ?`, policy, nullString(fileDate), k.FileID); err != nil {
		e.t.Fatal(err)
	}
}

// workOut works the keep_until of a policy's files out again, as a change of the policy does.
func (e *reviewEnv) workOut(name string) {
	e.t.Helper()
	p, err := e.s.Policy(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.s.SetPolicy(e.ctx, name, p.Value, policyNow); err != nil {
		e.t.Fatal(err)
	}
}

func (e *reviewEnv) files(today string) map[string]FileItem {
	e.t.Helper()
	q, err := e.s.Files(e.ctx, today, Filter{})
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]FileItem{}
	for _, it := range q {
		m[it.Path] = it
	}
	return m
}

func TestPolicyName(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"bills", "bills", true},
		{" Bills ", "bills", true},
		{"end-of-the-year", "end-of-the-year", true},
		{"tax_2024", "tax_2024", true},
		{"forever", "", false},
		{"FOREVER", "", false},
		{"7y", "", false},
		{"2030-12-31", "", false},
		{"bills!", "", false},
		{"my bills", "", false},
		{"-bills", "", false},
		{"", "", false},
		{"\u212Aills", "", false}, // the Kelvin sign, which Unicode lowers to k
		{"bïlls", "", false},
	}
	for _, tc := range tests {
		got, err := PolicyName(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("PolicyName(%q) = %q, %v; want %q, ok %v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

// TestAddPolicy: A4, and the values a policy takes.
func TestAddPolicy(t *testing.T) {
	e := newReviewEnv(t)
	for _, tc := range []struct{ name, value string }{{"bills", "7y"}, {"payslips", Forever},
		{"project-x", "2030-12-31"}, {"bils", ""}} {
		if err := e.s.AddPolicy(e.ctx, tc.name, tc.value, policyNow); err != nil {
			t.Fatalf("AddPolicy(%s, %q): %v", tc.name, tc.value, err)
		}
	}
	if err := e.s.AddPolicy(e.ctx, "Bills", "10y", policyNow); !errors.Is(err, ErrPolicyExists) {
		t.Errorf("adding Bills again: %v, want ErrPolicyExists", err)
	}
	for _, name := range []string{"forever", "7y"} {
		if err := e.s.AddPolicy(e.ctx, name, "", policyNow); !errors.Is(err, ErrPolicyName) {
			t.Errorf("adding %q: %v, want ErrPolicyName", name, err)
		}
	}
	for _, value := range []string{"7x", "2030-02-30", "bills"} {
		if err := e.s.AddPolicy(e.ctx, "other", value, policyNow); err == nil {
			t.Errorf("adding a policy with value %q: no error", value)
		}
	}
	ps, err := e.s.Policies(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Name+"="+p.Value)
	}
	if want := []string{"bills=7y", "bils=", "payslips=forever", "project-x=2030-12-31"}; !slices.Equal(got, want) {
		t.Errorf("policies = %q, want %q", got, want)
	}
}

// TestPolicyKeepUntil: B3, E2's status and F1. A file on a policy has the policy's name as its retention and its
// keep_until worked out from the policy; a period with no file_date is "needs a file_date"; a date policy makes its
// files candidates on that date.
func TestPolicyKeepUntil(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc"), f("d", "hd"), f("e", "he")}})
	for _, p := range [][2]string{{"bills", "7y"}, {"project-x", "2026-12-31"}, {"payslips", Forever}, {"bils", ""}} {
		if err := e.s.AddPolicy(e.ctx, p[0], p[1], policyNow); err != nil {
			t.Fatal(err)
		}
	}
	e.onPolicy("a", "bills", "2019-03-14")
	e.onPolicy("b", "bills", "")
	e.onPolicy("c", "project-x", "")
	e.onPolicy("d", "payslips", "")
	e.onPolicy("e", "bils", "2020-01-01")
	for _, name := range []string{"bills", "project-x", "payslips", "bils"} {
		e.workOut(name)
	}

	got := e.files("2026-12-30")
	want := map[string]struct {
		keep   string
		status Status
	}{
		"a": {"2026-03-14", StatusExpired},
		"b": {"", StatusNeedsFileDate},
		"c": {"2026-12-31", StatusKept},
		"d": {Forever, StatusForever},
		"e": {"", StatusUndated},
	}
	for path, w := range want {
		if it := got[path]; it.KeepUntil != w.keep || it.Status != w.status {
			t.Errorf("%s: keep_until %q, status %q; want %q, %q", path, it.KeepUntil, it.Status, w.keep, w.status)
		}
	}
	if a := got["a"]; a.Retention != "bills" || a.Policy != "bills" || a.PolicyValue != "7y" {
		t.Errorf("a: retention %q, policy %q (%q); want the policy's name, bills (7y)", a.Retention, a.Policy,
			a.PolicyValue)
	}
	if !StatusNeedsFileDate.NeedsDecision() {
		t.Error("needs a file_date does not need a decision")
	}
	// F1: on its date, a file on a date policy is a candidate, like a date typed per file.
	if st := e.statuses("2026-12-31")["c"]; st != StatusExpired {
		t.Errorf("c on 2026-12-31: %q, want expired", st)
	}
	cands, err := e.s.Candidates(e.ctx, "2026-12-31", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range cands {
		paths = append(paths, c.Path)
	}
	if !slices.Equal(paths, []string{"a", "c"}) {
		t.Errorf("candidates on 2026-12-31 = %v, want a and c", paths)
	}

	ps, err := e.s.Policies(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string][2]int64{}
	for _, p := range ps {
		counts[p.Name] = [2]int64{p.Files, p.NoFileDate}
	}
	if counts["bills"] != [2]int64{2, 1} || counts["project-x"] != [2]int64{1, 0} {
		t.Errorf("policy counts (files, without a file_date) = %v", counts)
	}
}

// TestSetPolicy: C1-C4 in the store. The preview lists the files a change puts past their date (and only those
// newly past) and the files it leaves needing a file_date; setting it works every file out again and records the
// change.
func TestSetPolicy(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("new", "h1"), f("old", "h2"), f("undated", "h3")}})
	if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	e.onPolicy("new", "bills", "2022-01-01") // 7y: 2029-01-01
	e.onPolicy("old", "bills", "2015-06-01") // 7y: 2022-06-01, past already
	e.onPolicy("undated", "bills", "")       // needs a file_date
	e.workOut("bills")
	const today = "2026-11-16"

	preview := func(value string) PolicyPreview {
		t.Helper()
		p, err := e.s.PreviewPolicy(e.ctx, "bills", value, today)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	paths := func(fs []PolicyFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	if p := preview("10y"); len(p.Past) != 0 || len(p.NoFileDate) != 0 || p.Files != 3 {
		t.Errorf("lengthen: %+v, want 3 files, none newly past, none newly needing a file_date", p)
	}
	if p := preview("5y"); len(p.Past) != 0 {
		t.Errorf("shorten, none newly past: past %v", paths(p.Past))
	}
	if p := preview("3y"); !slices.Equal(paths(p.Past), []string{"new"}) || p.Past[0].KeepUntil != "2025-01-01" ||
		p.Past[0].OldKeepUntil != "2029-01-01" || p.Past[0].Status != "" {
		t.Errorf("shorten, one newly past: %+v", p.Past)
	}
	if p := preview(Forever); len(p.Past) != 0 || len(p.NoFileDate) != 0 {
		t.Errorf("to forever: %+v", p)
	}

	// From forever back to a period, the file without a file_date newly needs one.
	if _, err := e.s.SetPolicy(e.ctx, "bills", Forever, policyNow); err != nil {
		t.Fatal(err)
	}
	if p := preview("7y"); !slices.Equal(paths(p.NoFileDate), []string{"undated"}) {
		t.Errorf("forever to 7y: needing a file_date %v, want undated", paths(p.NoFileDate))
	}
	n, err := e.s.SetPolicy(e.ctx, "bills", "10y", policyNow)
	if err != nil || n != 3 {
		t.Fatalf("SetPolicy 10y: %d, %v; want 3 files", n, err)
	}
	got := e.files(today)
	if got["new"].KeepUntil != "2032-01-01" || got["old"].KeepUntil != "2025-06-01" ||
		got["undated"].Status != StatusNeedsFileDate {
		t.Errorf("after 10y: new %q, old %q, undated %q", got["new"].KeepUntil, got["old"].KeepUntil,
			got["undated"].Status)
	}
	if err := e.s.AddPolicy(e.ctx, "x", "", policyNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetPolicy(e.ctx, "nope", "1y", policyNow); !errors.Is(err, ErrNoPolicy) {
		t.Errorf("setting a policy that does not exist: %v, want ErrNoPolicy", err)
	}
	changes := e.policyChanges()
	if want := []string{"add bills  7y 0", "set bills 7y forever 3", "set bills forever 10y 3", "add x   0"}; !slices.Equal(changes, want) {
		t.Errorf("policy_changes = %q, want %q", changes, want)
	}
}

func (e *reviewEnv) policyChanges() []string {
	e.t.Helper()
	rows, err := e.s.db.Query(`SELECT kind, policy, coalesce(old_value, ''), coalesce(new_value, ''), files
		FROM policy_changes ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, policy, old, new string
		var files int64
		if err := rows.Scan(&kind, &policy, &old, &new, &files); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, kind+" "+policy+" "+old+" "+new+" "+strconv.FormatInt(files, 10))
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// TestMoveAndRemovePolicy: D1 and D2 in the store. Moving into a new name renames; into an existing policy merges,
// working the files out under its value. Removing a policy in use is refused.
func TestMoveAndRemovePolicy(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb")}})
	for _, p := range [][2]string{{"bils", ""}, {"bills", "7y"}} {
		if err := e.s.AddPolicy(e.ctx, p[0], p[1], policyNow); err != nil {
			t.Fatal(err)
		}
	}
	e.onPolicy("a", "bils", "2020-01-01")
	e.onPolicy("b", "bills", "2021-01-01")
	e.workOut("bils")
	e.workOut("bills")

	if err := e.s.RemovePolicy(e.ctx, "bils", policyNow); !errors.Is(err, ErrPolicyInUse) {
		t.Errorf("removing bils in use: %v, want ErrPolicyInUse", err)
	}
	// A rename: bils to bilz, a new name.
	if n, err := e.s.MovePolicy(e.ctx, "bils", "bilz", policyNow); err != nil || n != 1 {
		t.Fatalf("rename: %d, %v", n, err)
	}
	if got := e.files("2026-11-16")["a"]; got.Policy != "bilz" || got.Status != StatusUndated {
		t.Errorf("a after the rename: %+v", got)
	}
	// A merge into bills: a takes 7y from its file_date.
	p, err := e.s.PreviewMove(e.ctx, "bilz", "bills", "2026-11-16")
	if err != nil || p.Files != 1 {
		t.Fatalf("preview of the merge: %+v, %v", p, err)
	}
	if n, err := e.s.MovePolicy(e.ctx, "bilz", "bills", policyNow); err != nil || n != 1 {
		t.Fatalf("merge: %d, %v", n, err)
	}
	if got := e.files("2026-11-16")["a"]; got.Policy != "bills" || got.KeepUntil != "2027-01-01" {
		t.Errorf("a after the merge: %+v", got)
	}
	if err := e.s.RemovePolicy(e.ctx, "bilz", policyNow); err != nil {
		t.Errorf("removing bilz, now unused: %v", err)
	}
	if _, err := e.s.Policy(e.ctx, "bilz"); !errors.Is(err, ErrNoPolicy) {
		t.Errorf("bilz after removing it: %v, want ErrNoPolicy", err)
	}
	if _, err := e.s.MovePolicy(e.ctx, "bills", "forever", policyNow); !errors.Is(err, ErrPolicyName) {
		t.Errorf("moving into forever: %v, want ErrPolicyName", err)
	}
	if want := []string{"add bils   0", "add bills  7y 0", "rename bils  bilz 1", "move bilz  bills 1",
		"remove bilz   0"}; !slices.Equal(e.policyChanges(), want) {
		t.Errorf("policy_changes = %q, want %q", e.policyChanges(), want)
	}
}

// TestSetRetentionLeavesThePolicy: B4 in the store: a retention of its own (filo delete's prompt) takes the file off
// its policy.
func TestSetRetentionLeavesThePolicy(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha")}})
	if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	e.onPolicy("a", "bills", "2019-03-14")
	e.workOut("bills")
	if _, err := e.s.SetRetention(e.ctx, e.known()["a"].FileID, "2030-01-01", policyNow); err != nil {
		t.Fatal(err)
	}
	if got := e.files("2026-11-16")["a"]; got.Policy != "" || got.Retention != "2030-01-01" {
		t.Errorf("a after a retention of its own: policy %q, retention %q", got.Policy, got.Retention)
	}
}

// TestMigrateFromV8: G1. The upgrade creates the policies tables and the files column, and changes nothing else.
func TestMigrateFromV8(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropPolicies, `UPDATE meta SET value = '8' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO files (id, kind, size, mtime_ns, hash, first_seen_scan, file_date, retention, keep_until)
			VALUES (1, 'file', 3, 1, 'a', 1, '2020-01-01', '7y', '2027-01-01')`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	var retention, keep string
	var policy *int64
	if err := s.db.QueryRow(`SELECT retention, keep_until, policy_id FROM files WHERE id = 1`).
		Scan(&retention, &keep, &policy); err != nil || retention != "7y" || keep != "2027-01-01" || policy != nil {
		t.Errorf("file after upgrade: %q, %q, policy %v (%v)", retention, keep, policy, err)
	}
	counts, err := s.RowCounts(t.Context())
	if err != nil || counts["policies"] != 0 || counts["policy_changes"] != 0 {
		t.Errorf("row counts after upgrade: %v, %v", counts, err)
	}
	if notes := s.UpgradeNotes(); len(notes) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", notes)
	}
}

// TestPolicyReviewFindings: the cases found in the code review of slice 1 (2026-11-16).
func TestPolicyReviewFindings(t *testing.T) {
	t.Run("counts are of present files; the history counts the dates changed", func(t *testing.T) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha"), f("gone", "hg"), f("c", "hc")}})
		if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
			t.Fatal(err)
		}
		e.onPolicy("a", "bills", "2020-01-01")
		e.onPolicy("gone", "bills", "")
		e.onPolicy("c", "bills", "2021-01-01")
		e.workOut("bills")
		e.apply(Changes{Close: []int64{e.known()["gone"].PathID}}) // missing now
		if _, err := e.s.SetRetention(e.ctx, e.known()["c"].FileID, "2030-01-01", policyNow); err != nil {
			t.Fatal(err)
		}
		// One present file (a); gone, missing and without a file_date, keeps no keep_until either way.
		if n, err := e.s.SetPolicy(e.ctx, "bills", "10y", policyNow); err != nil || n != 1 {
			t.Errorf("SetPolicy: %d, %v; want 1 present file", n, err)
		}
		if n, err := e.s.MovePolicy(e.ctx, "bills", "invoices", policyNow); err != nil || n != 1 {
			t.Errorf("rename: %d, %v; want 1", n, err)
		}
		if err := e.s.AddPolicy(e.ctx, "tax", "5y", policyNow); err != nil {
			t.Fatal(err)
		}
		if n, err := e.s.MovePolicy(e.ctx, "invoices", "tax", policyNow); err != nil || n != 1 {
			t.Errorf("merge: %d, %v; want 1, as the rename counts", n, err)
		}
		want := []string{"add bills  7y 0", "set bills 7y 10y 1", "rename bills  invoices 1", "add tax  5y 0",
			"move invoices  tax 1"}
		if got := e.policyChanges(); !slices.Equal(got, want) {
			t.Errorf("policy_changes = %q, want %q", got, want)
		}

		// Removing tax once a has a retention of its own: gone, missing with no file_date, gets no retention
		// rather than 5y with nothing to count from.
		if _, err := e.s.SetRetention(e.ctx, e.known()["a"].FileID, "2031-01-01", policyNow); err != nil {
			t.Fatal(err)
		}
		if err := e.s.RemovePolicy(e.ctx, "tax", policyNow); err != nil {
			t.Fatal(err)
		}
		var retention, policy *string
		if err := e.s.db.QueryRow(`SELECT retention, policy_id FROM files WHERE id = ?`,
			e.knownAll("gone")).Scan(&retention, &policy); err != nil || retention != nil || policy != nil {
			t.Errorf("gone after the removal: retention %v, policy %v (%v); want neither", retention, policy, err)
		}
	})

	t.Run("a retention of its own through the CSV takes the file off its policy", func(t *testing.T) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha")}})
		if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
			t.Fatal(err)
		}
		e.onPolicy("a", "bills", "2019-03-14")
		e.workOut("bills")
		ex, _ := e.export("2026-11-16", policyNow)
		res, err := e.update([]RetentionRow{{Line: 2, ExportID: ex, FileID: e.known()["a"].FileID,
			FileDate: "2019-03-14", HasFileDate: true, Retention: "2030-01-01"}}, policyNow, "2026-11-16",
			false, nil)
		if err != nil || len(res.Refusals) != 0 || res.Set != 1 {
			t.Fatalf("update: %+v, %v", res, err)
		}
		if got := e.files("2026-11-16")["a"]; got.Policy != "" || got.Retention != "2030-01-01" ||
			got.KeepUntil != "2030-01-01" {
			t.Errorf("a: policy %q, retention %q, until %q; want its own 2030-01-01", got.Policy, got.Retention,
				got.KeepUntil)
		}
	})
}

// knownAll returns the file ID of path, present or not.
func (e *reviewEnv) knownAll(path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.s.db.QueryRow(`SELECT file_id FROM paths WHERE path = ? ORDER BY id DESC LIMIT 1`, path).
		Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// TestUpdatePolicies: policy names in the retention column (slice 2): A1, A2, B1, B2, B4, B5 and E3 in the store.
func TestUpdatePolicies(t *testing.T) {
	const today = "2026-11-16"
	setup := func(t *testing.T) (*reviewEnv, int64) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc"), f("d", "hd")}})
		if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
			t.Fatal(err)
		}
		ex, _ := e.export(today, policyNow)
		return e, ex
	}
	row := func(e *reviewEnv, ex int64, path, date, ret string) RetentionRow {
		return RetentionRow{Line: 2, ExportID: ex, FileID: e.known()[path].FileID, FileDate: date, HasFileDate: true,
			Retention: ret}
	}
	update := func(e *reviewEnv, rows []RetentionRow, confirm func(FilesPending) bool) FilesUpdateResult {
		e.t.Helper()
		res, err := e.update(rows, policyNow, today, false, confirm)
		if err != nil {
			e.t.Fatal(err)
		}
		return res
	}
	yes := func(FilesPending) bool { return true }

	t.Run("B1, B2: an existing policy; a period with no file_date refuses the row", func(t *testing.T) {
		e, ex := setup(t)
		res := update(e, []RetentionRow{row(e, ex, "a", "2019-03-14", "bills"), row(e, ex, "b", "", "bills")}, nil)
		if res.Set != 1 || len(res.Refusals) != 1 || res.Refusals[0].Reason != "retention bills (7y) needs a file_date" {
			t.Fatalf("result = %+v", res)
		}
		a := e.files(today)["a"]
		if a.Policy != "bills" || a.KeepUntil != "2026-03-14" || a.Retention != "bills" {
			t.Errorf("a = %+v", a)
		}
		var own *string
		if err := e.s.db.QueryRow(`SELECT retention FROM files WHERE id = ?`, a.FileID).Scan(&own); err != nil ||
			own != nil {
			t.Errorf("a's own retention = %v (%v), want none while it is on a policy", own, err)
		}
	})

	t.Run("A1, A2: new policies are created only once confirmed", func(t *testing.T) {
		e, ex := setup(t)
		rows := []RetentionRow{row(e, ex, "a", "", "bils"), row(e, ex, "b", "", "Payslip"),
			row(e, ex, "c", "", "bils"), row(e, ex, "d", "2020-01-01", "bills")}
		rows[1].Retention = "payslip" // as the CSV reader gives it
		var asked []NewPolicy
		res := update(e, rows, func(p FilesPending) bool { asked = p.NewPolicies; return false })
		if want := []NewPolicy{{"bils", 2}, {"payslip", 1}}; !slices.Equal(asked, want) {
			t.Errorf("asked about %v, want %v", asked, want)
		}
		if !res.Declined || res.Set != 0 || res.PoliciesCreated != 0 {
			t.Errorf("declined: %+v, want nothing recorded", res)
		}
		if _, err := e.s.Policy(e.ctx, "bils"); !errors.Is(err, ErrNoPolicy) {
			t.Errorf("bils after declining: %v", err)
		}
		if d := e.files(today)["d"]; d.Policy != "" {
			t.Errorf("d after declining: %+v, want not on bills", d)
		}
		// No terminal: confirm is nil, the same as declining.
		if res := update(e, rows[:1], nil); !res.Declined {
			t.Errorf("no terminal: %+v", res)
		}
		res = update(e, rows[:3], yes)
		if res.PoliciesCreated != 2 || res.Set != 3 {
			t.Errorf("confirmed: %+v", res)
		}
		got := e.files(today)
		if got["a"].Policy != "bils" || got["a"].Status != StatusUndated || got["b"].Policy != "payslip" {
			t.Errorf("after creating: a %+v, b %+v", got["a"], got["b"])
		}
		if p, err := e.s.Policy(e.ctx, "bils"); err != nil || p.Value != "" || p.Files != 2 {
			t.Errorf("bils = %+v, %v; want no value, 2 files", p, err)
		}
	})

	t.Run("a dry run lists the new policies and creates nothing", func(t *testing.T) {
		e, ex := setup(t)
		res, err := e.update([]RetentionRow{row(e, ex, "a", "", "bils")}, policyNow, today, true, yes)
		if err != nil || !slices.Equal(res.Pending.NewPolicies, []NewPolicy{{"bils", 1}}) || res.Set != 1 {
			t.Fatalf("dry run: %+v, %v", res, err)
		}
		if _, err := e.s.Policy(e.ctx, "bils"); !errors.Is(err, ErrNoPolicy) {
			t.Errorf("bils after a dry run: %v", err)
		}
	})

	t.Run("B4, B5, E3: to a retention of its own and back; untouched rows; clearing the file_date", func(t *testing.T) {
		e, ex := setup(t)
		update(e, []RetentionRow{row(e, ex, "a", "2019-03-14", "bills")}, nil)
		ex2, _ := e.export(today, policyNow.Add(time.Minute))
		// B5: the export shows the policy's name; the row untouched is unchanged, not refused.
		res := update(e, []RetentionRow{row(e, ex2, "a", "2019-03-14", "bills")}, nil)
		if res.Unchanged != 1 || len(res.Refusals) != 0 {
			t.Errorf("untouched policy row: %+v", res)
		}
		// E3: clearing the file_date of a file on a period policy is refused.
		res = update(e, []RetentionRow{row(e, ex2, "a", "", "bills")}, nil)
		if len(res.Refusals) != 1 || res.Refusals[0].Reason != "retention bills (7y) needs a file_date" {
			t.Errorf("clearing file_date: %+v", res)
		}
		// B4: a retention of its own, then back to the policy.
		update(e, []RetentionRow{row(e, ex2, "a", "2019-03-14", "2030-01-01")}, nil)
		if a := e.files(today)["a"]; a.Policy != "" || a.Retention != "2030-01-01" {
			t.Errorf("own retention: %+v", a)
		}
		ex3, _ := e.export(today, policyNow.Add(2*time.Minute))
		update(e, []RetentionRow{row(e, ex3, "a", "2019-03-14", "bills")}, nil)
		if a := e.files(today)["a"]; a.Policy != "bills" || a.KeepUntil != "2026-03-14" {
			t.Errorf("back on bills: %+v", a)
		}
		// A blank retention clears the policy, once confirmed.
		res, err := e.update([]RetentionRow{row(e, ex3, "a", "2019-03-14", "")}, policyNow, today, false,
			func(p FilesPending) bool { return len(p.Clears) == 1 && p.Clears[0].Retention == "bills" })
		if err != nil || res.Cleared != 1 {
			t.Fatalf("clear: %+v, %v", res, err)
		}
		if a := e.files(today)["a"]; a.Policy != "" || a.Retention != "" || a.Status != StatusUndated {
			t.Errorf("cleared: %+v", a)
		}
	})
}

// TestSetRetentionPolicy: filo delete's prompt takes a policy name too: an existing policy puts the file on it; an
// unknown one is refused (that prompt cannot ask to create it); a period policy needs a file_date.
func TestSetRetentionPolicy(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb")}})
	if err := e.s.AddPolicy(e.ctx, "bills", "7y", policyNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`UPDATE files SET file_date = '2019-03-14' WHERE id = ?`, e.known()["a"].FileID); err != nil {
		t.Fatal(err)
	}
	keep, err := e.s.SetRetention(e.ctx, e.known()["a"].FileID, "bills", policyNow)
	if err != nil || keep != "2026-03-14" {
		t.Fatalf("SetRetention bills: %q, %v", keep, err)
	}
	if a := e.files("2026-11-16")["a"]; a.Policy != "bills" || a.Retention != "bills" {
		t.Errorf("a = %+v, want on bills", a)
	}
	if _, err := e.s.SetRetention(e.ctx, e.known()["a"].FileID, "bils", policyNow); !errors.Is(err, ErrNoPolicy) {
		t.Errorf("an unknown policy: %v, want ErrNoPolicy", err)
	}
	if _, err := e.s.SetRetention(e.ctx, e.known()["b"].FileID, "bills", policyNow); err == nil ||
		err.Error() != "retention bills (7y) needs a file_date" {
		t.Errorf("no file_date: %v", err)
	}
	if _, err := KeepUntil("2020-01-01", "bills"); err == nil {
		t.Error("KeepUntil of a policy name: no error")
	}
}

// TestOldCSVAfterMove: a CSV exported before a move or rename never puts a file back on the old name: a row edited
// since is refused, as for any retention changed after its export (code review of slice 4, 2026-11-16).
func TestOldCSVAfterMove(t *testing.T) {
	for _, rename := range []bool{false, true} {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha")}})
		for _, p := range []string{"bils", "bills"} {
			if err := e.s.AddPolicy(e.ctx, p, "7y", policyNow); err != nil {
				t.Fatal(err)
			}
		}
		e.onPolicy("a", "bils", "2020-01-01")
		e.workOut("bils")
		ex, _ := e.export("2026-11-16", policyNow)
		target := "bills"
		if rename {
			if err := e.s.RemovePolicy(e.ctx, "bills", policyNow); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.s.MovePolicy(e.ctx, "bils", target, policyNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		res, err := e.update([]RetentionRow{{Line: 2, ExportID: ex,
			FileID: e.known()["a"].FileID, FileDate: "2021-01-01", HasFileDate: true, Retention: "bils"}},
			policyNow.Add(2*time.Minute), "2026-11-16", false, func(FilesPending) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 1 || res.Refusals[0].Reason != "its retention was changed after export #"+
			strconv.FormatInt(ex, 10)+"; export again" || res.PoliciesCreated != 0 {
			t.Errorf("rename %v: %+v, want the row refused and no policy created", rename, res)
		}
		if a := e.files("2026-11-16")["a"]; a.Policy != "bills" {
			t.Errorf("rename %v: a is on %q, want bills", rename, a.Policy)
		}
	}
}
