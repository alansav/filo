package store

import (
	"strings"
	"testing"
	"time"
)

// SetFiles (docs/design.md, Showing and setting a file's details): the CSV's rules, all or nothing, a question only
// when a stored value would be replaced.
func TestSetFiles(t *testing.T) {
	e := newReviewEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("a.pdf", "ha"), f("b.pdf", "hb"), f("c.pdf", "hc"), f("gone.pdf", "hg")}})
	k := e.known()
	e.apply(Changes{Close: []int64{k["gone.pdf"].PathID}})
	if err := e.s.AddPolicy(e.ctx, "bills", "7y", now); err != nil {
		t.Fatal(err)
	}
	id := func(p string) int64 { return k[p].FileID }
	asked := 0
	yes := SetOptions{Confirm: func(SetResult) bool { asked++; return true }}
	set := func(ids []int64, v SetValues, opt SetOptions) SetResult {
		t.Helper()
		res, err := e.s.SetFiles(e.ctx, ids, v, now, "2026-11-20", opt)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// First values: no question. A file listed twice counts once.
	res := set([]int64{id("a.pdf"), id("b.pdf"), id("a.pdf")}, SetValues{FileDate: "2024-03-31", SetFileDate: true,
		Retention: "7y", SetRetention: true}, yes)
	if !res.Applied || asked != 0 || len(res.Rows) != 2 || res.Changed() != 2 || res.Rows[0].KeepUntil != "2031-03-31" ||
		res.Rows[0].Replaces {
		t.Errorf("first values: %+v (asked %d)", res, asked)
	}
	fs := e.files("2026-11-20")
	if a := fs["a.pdf"]; a.FileDate != "2024-03-31" || a.Retention != "7y" || a.KeepUntil != "2031-03-31" ||
		a.Status != StatusKept {
		t.Errorf("a.pdf = %+v", a)
	}
	sh, err := e.s.ShowFile(e.ctx, e.root.ID, "a.pdf", "2026-11-20")
	if err != nil || sh.ReviewedBy != "set" || sh.ReviewedHash != "ha" {
		t.Errorf("review: by %q on %q (%v)", sh.ReviewedBy, sh.ReviewedHash, err)
	}

	// Replacing asks; declined, nothing changes, the first-time file c.pdf included (all or nothing).
	no := SetOptions{Confirm: func(SetResult) bool { asked++; return false }}
	res = set([]int64{id("a.pdf"), id("c.pdf")}, SetValues{Retention: "bills", SetRetention: true,
		FileDate: "2024-03-31", SetFileDate: true}, no)
	if res.Applied || asked != 1 || !res.Rows[0].Replaces || res.Rows[1].Replaces {
		t.Errorf("declined: %+v (asked %d)", res, asked)
	}
	if fs := e.files("2026-11-20"); fs["c.pdf"].Retention != "" || fs["a.pdf"].Retention != "7y" {
		t.Errorf("declined but changed: %+v", fs)
	}
	// A dry run neither asks nor writes; no Confirm declines.
	if res = set([]int64{id("a.pdf")}, SetValues{Retention: "bills", SetRetention: true},
		SetOptions{DryRun: true, Confirm: no.Confirm}); res.Applied || asked != 1 {
		t.Errorf("dry run: %+v (asked %d)", res, asked)
	}
	if res = set([]int64{id("a.pdf")}, SetValues{Retention: "bills", SetRetention: true}, SetOptions{}); res.Applied {
		t.Errorf("no Confirm: applied")
	}
	// Confirmed: a.pdf goes on the policy.
	res = set([]int64{id("a.pdf")}, SetValues{Retention: "bills", SetRetention: true}, yes)
	if !res.Applied || res.Rows[0].PolicyValue != "7y" || res.Rows[0].OldRetention != "7y" {
		t.Errorf("onto a policy: %+v", res)
	}
	if a := e.files("2026-11-20")["a.pdf"]; a.Policy != "bills" || a.Retention != "bills" || a.KeepUntil != "2031-03-31" {
		t.Errorf("a.pdf on bills = %+v", a)
	}

	// Already the values: unchanged, nothing written, no question.
	asked = 0
	res = set([]int64{id("a.pdf")}, SetValues{Retention: "bills", SetRetention: true}, yes)
	if res.Applied || !res.Rows[0].Unchanged || asked != 0 {
		t.Errorf("unchanged: %+v", res)
	}

	// Refusals: every one listed, nothing written.
	res = set([]int64{id("b.pdf"), id("c.pdf"), id("gone.pdf")}, SetValues{Retention: "7y", SetRetention: true}, yes)
	var reasons []string
	for _, r := range res.Refusals {
		reasons = append(reasons, r.Path+": "+r.Reason)
	}
	if res.Applied || len(reasons) != 2 || !strings.Contains(reasons[0], "c.pdf: retention 7y needs a file_date") ||
		!strings.Contains(reasons[1], "gone.pdf: missing") {
		t.Errorf("refusals: %v", reasons)
	}
	for _, tt := range []struct {
		v    SetValues
		want string
	}{
		{SetValues{SetFileDate: true}, "--no-file-date cannot clear it"},
	} {
		res = set([]int64{id("b.pdf")}, tt.v, yes)
		if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, tt.want) {
			t.Errorf("%+v: %+v, want %q", tt.v, res.Refusals, tt.want)
		}
	}

	// A policy that does not exist yet: listed in a dry run and not created; created with no value when written,
	// its files then needing a retention.
	res = set([]int64{id("c.pdf")}, SetValues{Retention: "tax", SetRetention: true}, SetOptions{DryRun: true})
	if res.Applied || len(res.NewPolicies) != 1 || res.NewPolicies[0] != "tax" {
		t.Errorf("new policy, dry run: %+v", res)
	}
	if _, err := e.s.Policy(e.ctx, "tax"); err == nil {
		t.Errorf("a dry run created the policy")
	}
	res = set([]int64{id("c.pdf")}, SetValues{Retention: "tax", SetRetention: true}, yes)
	if !res.Applied || len(res.NewPolicies) != 1 || res.Rows[0].KeepUntil != "" {
		t.Errorf("new policy: %+v", res)
	}
	if p, err := e.s.Policy(e.ctx, "tax"); err != nil || p.Value != "" {
		t.Errorf("tax = %+v, %v; want created with no value", p, err)
	}
	if c := e.files("2026-11-20")["c.pdf"]; c.Policy != "tax" || c.Status != StatusUndated {
		t.Errorf("c.pdf on tax = %+v", c)
	}

	// A new file_date works a period out again; a past date is listed.
	res = set([]int64{id("b.pdf")}, SetValues{FileDate: "2015-01-01", SetFileDate: true}, yes)
	if !res.Applied || res.Rows[0].KeepUntil != "2022-01-01" || len(res.NowPast) != 1 {
		t.Errorf("past: %+v", res)
	}
	// Clearing a retention asks and clears.
	asked = 0
	res = set([]int64{id("b.pdf")}, SetValues{SetRetention: true}, yes)
	if !res.Applied || asked != 1 || !res.Rows[0].Clears {
		t.Errorf("clear: %+v", res)
	}
	if b := e.files("2026-11-20")["b.pdf"]; b.Retention != "" || b.KeepUntil != "" || b.FileDate != "2015-01-01" {
		t.Errorf("cleared b.pdf = %+v", b)
	}
}

// A retention by hand replaces a rule's policy: the row says which rule, and the file is no longer the rule's.
func TestSetReplacesARulesPolicy(t *testing.T) {
	e := newReviewEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("bills/a.pdf", "ha")}})
	if _, _, _, err := e.s.AddRule(e.ctx, e.root.ID, "bills", "*.pdf", "bills", "2026-11-20", now); err != nil {
		t.Fatal(err)
	}
	id := e.known()["bills/a.pdf"].FileID
	// A file_date alone keeps the rule's policy.
	res, err := e.s.SetFiles(e.ctx, []int64{id}, SetValues{FileDate: "2024-01-01", SetFileDate: true}, now,
		"2026-11-20", SetOptions{})
	if err != nil || !res.Applied || res.Rows[0].RulePath() != "" {
		t.Fatalf("file_date alone: %+v, %v", res, err)
	}
	if it := e.files("2026-11-20")["bills/a.pdf"]; it.RuleID == 0 {
		t.Errorf("the rule was dropped by a file_date")
	}
	res, err = e.s.SetFiles(e.ctx, []int64{id}, SetValues{Retention: "forever", SetRetention: true}, now, "2026-11-20",
		SetOptions{Confirm: func(SetResult) bool { return true }})
	if err != nil || !res.Applied || res.Rows[0].RulePath() != "bills/*.pdf" {
		t.Fatalf("by hand: %+v, %v", res, err)
	}
	if it := e.files("2026-11-20")["bills/a.pdf"]; it.RuleID != 0 || it.Policy != "" || it.Retention != Forever {
		t.Errorf("after: %+v", it)
	}
}

// --duplicates (docs/design.md, Showing and setting a file's details): a decision about a content, asked when it
// replaces or undoes one already stored, all or nothing with the rest.
func TestSetDuplicates(t *testing.T) {
	e := newReviewEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	lic := func(p string) NewFile { return NewFile{Path: p, Kind: "file", Size: 3, MtimeNS: 1, Hash: "hl"} }
	e.apply(Changes{New: []NewFile{lic("a/LICENSE"), lic("b/LICENSE"), lic("c/LICENSE"),
		{Path: "terms20.pdf", Kind: "file", Size: 5, MtimeNS: 1, Hash: "ht"},
		{Path: "terms21.pdf", Kind: "file", Size: 5, MtimeNS: 1, Hash: "ht"},
		{Path: "empty", Kind: "file", Size: 0, MtimeNS: 1, Hash: "he"},
		{Path: "link", Kind: "symlink", LinkTarget: "a/LICENSE"}}})
	k := e.known()
	id := func(p string) int64 { return k[p].FileID }
	asked := 0
	yes := SetOptions{Confirm: func(SetResult) bool { asked++; return true }}
	set := func(ids []int64, want string, opt SetOptions) SetResult {
		t.Helper()
		res, err := e.s.SetFiles(e.ctx, ids, SetValues{Duplicates: want}, now, "2026-11-20", opt)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	groups := func() int {
		t.Helper()
		n, err := e.s.GroupCount(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if groups() != 2 {
		t.Fatalf("groups = %d, want 2", groups())
	}

	// always: the content, every copy listed; no question the first time.
	res := set([]int64{id("a/LICENSE")}, DupAlways, yes)
	if !res.Applied || asked != 0 || len(res.Dups) != 1 || len(res.Dups[0].Copies) != 3 || res.Dups[0].Was != DupCounted {
		t.Errorf("always: %+v (asked %d)", res, asked)
	}
	if groups() != 1 {
		t.Errorf("groups after always = %d, want 1", groups())
	}
	if res = set([]int64{id("b/LICENSE")}, DupAlways, yes); res.Applied || !res.Dups[0].Unchanged {
		t.Errorf("always again: %+v", res)
	}

	// records: two copies named; then always over records asks; counted undoes it, asking.
	res = set([]int64{id("terms20.pdf"), id("terms21.pdf")}, DupRecords, yes)
	if !res.Applied || asked != 0 || groups() != 0 {
		t.Errorf("records: %+v, groups %d", res, groups())
	}
	if res = set([]int64{id("terms21.pdf"), id("terms20.pdf")}, DupRecords, yes); res.Applied || !res.Dups[0].Unchanged {
		t.Errorf("records again: %+v", res)
	}
	no := SetOptions{Confirm: func(SetResult) bool { asked++; return false }}
	if res = set([]int64{id("terms20.pdf")}, DupAlways, no); res.Applied || asked != 1 || !res.Dups[0].Replaces ||
		res.Dups[0].Was != DupRecords {
		t.Errorf("always over records, declined: %+v", res)
	}
	if res = set([]int64{id("terms20.pdf")}, DupCounted, yes); !res.Applied || asked != 2 || groups() != 1 {
		t.Errorf("counted: %+v, groups %d", res, groups())
	}
	if res = set([]int64{id("a/LICENSE")}, DupCounted, yes); !res.Applied || groups() != 2 {
		t.Errorf("counted always: %+v, groups %d", res, groups())
	}

	// Refusals: one copy named for records, an empty file, a symlink.
	for _, tt := range []struct {
		ids  []int64
		want string
	}{
		{[]int64{id("terms20.pdf")}, "records needs two or more copies"},
		{[]int64{id("empty")}, "empty files are never duplicates"},
		{[]int64{id("link")}, "only regular files have copies"},
	} {
		res = set(tt.ids, DupRecords, yes)
		if res.Applied || len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, tt.want) {
			t.Errorf("%v: %+v, want %q", tt.ids, res.Refusals, tt.want)
		}
	}
	// With a retention in the same run: a refusal of one stops both.
	res, err := e.s.SetFiles(e.ctx, []int64{id("terms20.pdf")}, SetValues{Duplicates: DupRecords, Retention: "forever",
		SetRetention: true}, now, "2026-11-20", yes)
	if err != nil || res.Applied {
		t.Errorf("combined, refused: %+v, %v", res, err)
	}
	if it := e.files("2026-11-20")["terms20.pdf"]; it.Retention != "" {
		t.Errorf("the retention was set though --duplicates was refused: %+v", it)
	}
}
