package store

import (
	"slices"
	"testing"
	"time"
)

// ShowDir (docs/design.md, Showing and setting a file's details): every file at a directory and below, present or
// missing, the deleted ones counted, the duplicate state of each, and the rules in the order tried.
func TestShowDir(t *testing.T) {
	e := newReviewEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("bills/a.pdf", "ha"), f("backup/a.pdf", "ha"), f("bills/b.pdf", "hb"),
		f("bills/2023/c.pdf", "hc"), f("bills/gone.pdf", "hg"), f("bills/del.pdf", "hd"), f("bills/r1", "hr"),
		f("bills/r2", "hr"), f("billsx/d.pdf", "hx"), f("bills/re.pdf", "h1")}})
	k := e.known()
	second := e.apply(Changes{Close: []int64{k["bills/gone.pdf"].PathID, k["bills/re.pdf"].PathID},
		New: []NewFile{f("bills/re.pdf", "h2")}})
	if _, err := e.s.db.Exec(`UPDATE files SET deleted_utc = '2026-11-19T10:00:00Z' WHERE id = ?`,
		k["bills/del.pdf"].FileID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`UPDATE paths SET valid_to_scan = ? WHERE id = ?`, second,
		k["bills/del.pdf"].PathID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecordSet(e.ctx, []RecordCopy{{FileID: k["bills/r1"].FileID, Hash: "hr"},
		{FileID: k["bills/r2"].FileID, Hash: "hr"}}, now); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ dir, pattern, policy string }{{"", "*", "docs"}, {"bills", "*", "bills"},
		{"bills", "*Council*", "council"}, {"bills/2023", "*", "old"}, {"billsx", "*", "x"}} {
		if _, _, _, err := e.s.AddRule(e.ctx, e.root.ID, r.dir, r.pattern, r.policy, "2026-11-20", now); err != nil {
			t.Fatal(err)
		}
	}

	d, err := e.s.ShowDir(e.ctx, e.root.ID, "bills", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	got := map[string]DirEntry{}
	for _, f := range d.Files {
		paths = append(paths, f.Path)
		got[f.Path] = f
	}
	if want := []string{"bills/2023/c.pdf", "bills/a.pdf", "bills/b.pdf", "bills/gone.pdf", "bills/r1", "bills/r2",
		"bills/re.pdf"}; !slices.Equal(paths, want) {
		t.Errorf("files = %v, want %v (not billsx/, the deleted one counted apart, the reused path once)", paths, want)
	}
	if g := got["bills/gone.pdf"]; g.Present || g.GoneUTC == "" || g.Status != "" {
		t.Errorf("missing = %+v", g)
	}
	if re := got["bills/re.pdf"]; !re.Present || re.Hash != "h2" {
		t.Errorf("reused path = %+v, want the file there now", re)
	}
	if !slices.Equal(d.Deleted, []string{"bills/del.pdf"}) {
		t.Errorf("deleted = %v", d.Deleted)
	}
	if got["bills/a.pdf"].Dup != DupCopies || got["bills/b.pdf"].Dup != DupNone || got["bills/r1"].Dup != DupRecord {
		t.Errorf("dup states: a %v, b %v, r1 %v", got["bills/a.pdf"].Dup, got["bills/b.pdf"].Dup, got["bills/r1"].Dup)
	}
	var tried []string
	for _, r := range d.Rules {
		tried = append(tried, r.Dir+":"+r.Pattern)
	}
	if want := []string{"bills:*Council*", "bills:*", ":*"}; !slices.Equal(tried, want) {
		t.Errorf("rules tried = %v, want %v", tried, want)
	}
	if len(d.RulesBelow) != 1 || d.RulesBelow[0].Dir != "bills/2023" {
		t.Errorf("rules below = %+v", d.RulesBelow)
	}
	if d.LastScanUTC == "" || d.RootPath != e.root.Path {
		t.Errorf("last scan %q, root %q", d.LastScanUTC, d.RootPath)
	}

	if err := e.s.AddAlways(e.ctx, "ha", now); err != nil {
		t.Fatal(err)
	}
	d, err = e.s.ShowDir(e.ctx, e.root.ID, "bills", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		if f.Path == "bills/a.pdf" && f.Dup != DupOnPurpose {
			t.Errorf("always: %v", f.Dup)
		}
	}

	for _, tt := range []struct {
		dir  string
		want bool
	}{{"bills", true}, {"bills/2023", true}, {"bills/2023/c.pdf", false}, {"bil", false}, {"", true}} {
		if has, err := e.s.HasFilesUnder(e.ctx, e.root.ID, tt.dir); err != nil || has != tt.want {
			t.Errorf("HasFilesUnder(%q) = %v, %v; want %v", tt.dir, has, err, tt.want)
		}
	}
}
