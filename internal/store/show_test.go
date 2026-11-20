package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// ShowFile (docs/design.md, Showing and setting a file's details): a file found by a path it has now or had last,
// with its whole record.
func TestShowFile(t *testing.T) {
	e := newReviewEnv(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	first := e.apply(Changes{New: []NewFile{f("inbox/gas.pdf", "hg"), f("backup/gas.pdf", "hg"), f("notes.txt", "hn"),
		f("old.txt", "ho"), f("reused.txt", "h1"), f("gone.txt", "hd")}})
	k := e.known()
	// Scan 2: gas.pdf moves to bills/, old.txt goes missing, reused.txt goes and a new file takes its path.
	second := e.apply(Changes{
		Close: []int64{k["inbox/gas.pdf"].PathID, k["old.txt"].PathID, k["reused.txt"].PathID},
		Open:  []OpenPath{{FileID: k["inbox/gas.pdf"].FileID, Path: "bills/gas.pdf"}},
		New:   []NewFile{f("reused.txt", "h2")},
	})
	gas := k["inbox/gas.pdf"].FileID
	if _, err := e.s.SetRetention(e.ctx, gas, "7y", now); err == nil {
		t.Fatal("a period with no file_date was accepted")
	}
	if _, err := e.s.db.Exec(`UPDATE files SET file_date = '2022-01-31' WHERE id = ?`, gas); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetRetention(e.ctx, gas, "7y", now); err != nil {
		t.Fatal(err)
	}
	// gone.txt is deleted by filo: its path closed, deleted_utc set.
	if _, err := e.s.db.Exec(`UPDATE files SET deleted_utc = '2026-11-19T10:00:00Z' WHERE id = ?`,
		k["gone.txt"].FileID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.db.Exec(`UPDATE paths SET valid_to_scan = ? WHERE id = ?`, second,
		k["gone.txt"].PathID); err != nil {
		t.Fatal(err)
	}

	show := func(rel string) Shown {
		t.Helper()
		sh, err := e.s.ShowFile(e.ctx, e.root.ID, rel, "2026-11-20")
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return sh
	}

	sh := show("bills/gas.pdf")
	if !sh.Present || sh.Status != StatusKept || sh.Retention != "7y" || sh.FileDate != "2022-01-31" ||
		sh.KeepUntil != "2029-01-31" || sh.ReviewedBy != "delete" || sh.ReviewedHash != "hg" {
		t.Errorf("bills/gas.pdf = %+v", sh)
	}
	if sh.FirstSeenScan != first || sh.FirstSeenUTC == "" || sh.LastScanUTC == "" {
		t.Errorf("first seen %d %q, last scan %q", sh.FirstSeenScan, sh.FirstSeenUTC, sh.LastScanUTC)
	}
	if len(sh.Paths) != 2 || sh.Paths[0].Path != "bills/gas.pdf" || sh.Paths[0].ToScan != 0 ||
		sh.Paths[1].Path != "inbox/gas.pdf" || sh.Paths[1].FromScan != first || sh.Paths[1].ToScan != second ||
		sh.Paths[1].ToUTC == "" {
		t.Errorf("paths = %+v, want the current one first, then inbox/ from scan %d to %d", sh.Paths, first, second)
	}
	if len(sh.Copies) != 1 || sh.Copies[0].Path != "backup/gas.pdf" || !sh.CopiesInGroups || sh.Always ||
		sh.RecordSet != 0 {
		t.Errorf("copies = %+v (group %v)", sh.Copies, sh.CopiesInGroups)
	}
	if sh.AbsPath() != filepath.Join(e.root.Path, "bills", "gas.pdf") || sh.RootID != e.root.ID {
		t.Errorf("abs path %s, root %d", sh.AbsPath(), sh.RootID)
	}

	// The old path finds the same file while no other file took it.
	if old := show("inbox/gas.pdf"); old.FileID != gas || !old.Present {
		t.Errorf("inbox/gas.pdf = file %d (present %v), want %d", old.FileID, old.Present, gas)
	}

	if m := show("old.txt"); m.Present || m.Status != "" || m.GoneScan != second || m.GoneUTC == "" ||
		m.DeletedUTC != "" || m.LastScanUTC != "" {
		t.Errorf("missing old.txt = %+v", m)
	}
	if d := show("gone.txt"); d.Present || d.DeletedUTC != "2026-11-19T10:00:00Z" || d.GoneScan != second {
		t.Errorf("deleted gone.txt = %+v", d)
	}
	// A path another file took since: the one there now.
	if r := show("reused.txt"); !r.Present || r.Hash != "h2" || len(r.Paths) != 1 {
		t.Errorf("reused.txt = %+v, want the new file", r)
	}
	if _, err := e.s.ShowFile(e.ctx, e.root.ID, "never.txt", "2026-11-20"); !errors.Is(err, ErrNotInRegister) {
		t.Errorf("never.txt: %v, want ErrNotInRegister", err)
	}

	// Duplicate decisions: always, then a record set, then an exclusion.
	if err := e.s.AddAlways(e.ctx, "hg", now); err != nil {
		t.Fatal(err)
	}
	if sh := show("bills/gas.pdf"); !sh.Always || sh.CopiesInGroups || len(sh.Copies) != 1 {
		t.Errorf("always: %+v", sh)
	}
	if err := e.s.RemoveAlways(e.ctx, "hg"); err != nil {
		t.Fatal(err)
	}
	k = e.known()
	if err := e.s.RecordSet(e.ctx, []RecordCopy{{FileID: gas, Hash: "hg"},
		{FileID: k["backup/gas.pdf"].FileID, Hash: "hg"}}, now); err != nil {
		t.Fatal(err)
	}
	if sh := show("bills/gas.pdf"); sh.RecordSet == 0 || !sh.OneRecordSet || sh.CopiesInGroups {
		t.Errorf("records: %+v", sh)
	}
	if _, err := e.s.Exclude(e.ctx, e.root.ID, "bills", now); err != nil {
		t.Fatal(err)
	}
	if sh := show("bills/gas.pdf"); !sh.Excluded || len(sh.Copies) != 1 {
		t.Errorf("excluded: %+v", sh)
	}
}

// A rule's policy shows the rule's directory and pattern.
func TestShowFileRule(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("bills/a.pdf", "ha")}})
	if _, _, _, err := e.s.AddRule(e.ctx, e.root.ID, "bills", "*.pdf", "bills", "2026-11-20", policyNow); err != nil {
		t.Fatal(err)
	}
	sh, err := e.s.ShowFile(e.ctx, e.root.ID, "bills/a.pdf", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	if sh.Policy != "bills" || sh.RuleID == 0 || sh.RuleDir != "bills" || sh.RulePattern != "*.pdf" ||
		sh.ReviewedBy != "rules add" {
		t.Errorf("rule: %+v", sh)
	}
}
