package store

import (
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// Renaming by the name cell (docs/design.md, Tags, "Renaming a file by its name cell"): the register side.

func TestRenameRun(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("d/Britsh Gas bill.pdf", "ha"), f("d/other.pdf", "hb")}})
	k := e.known()
	a := k["d/Britsh Gas bill.pdf"]
	if _, err := e.s.TagFiles(e.ctx, []int64{a.FileID}, []tags.Tag{mustTag(t, "type:bill")}, tagNow); err != nil {
		t.Fatal(err)
	}
	c, ok, err := renameCandidate(e.ctx, e.s.db, a.FileID)
	if err != nil || !ok || c.Path != "d/Britsh Gas bill.pdf" || c.PathID != a.PathID || c.RootID != e.root.ID {
		t.Fatalf("candidate %+v, %v, %v", c, ok, err)
	}
	if _, ok, err := renameCandidate(e.ctx, e.s.db, 9999); ok || err != nil {
		t.Errorf("a file not in the register: %v, %v", ok, err)
	}

	started := time.Now()
	run := e.s.BeginRenameRun(started)
	if err := run.Renamed(e.ctx, c, "British Gas bill.pdf", started); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(e.ctx, started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	k = e.known()
	if _, old := k["d/Britsh Gas bill.pdf"]; old || k["d/British Gas bill.pdf"].FileID != a.FileID {
		t.Fatalf("known after the rename = %+v, want the same file at its new path", k)
	}
	if e.tagList(a.FileID) != "type:bill" {
		t.Errorf("tags after the rename = %q, want them kept", e.tagList(a.FileID))
	}
	if v := rawQueryString(t, e.db, `SELECT e.kind || '|' || e.path || '|' || e.other_path || '|' || e.detail || '|' ||
		s.kind || '|' || s.moved_count || '|' || (s.finished_utc IS NOT NULL) FROM events e JOIN scans s ON s.id = e.scan_id
		WHERE e.file_id = `+itoa(a.FileID)); v != "moved|d/British Gas bill.pdf|d/Britsh Gas bill.pdf|renamed by filo update|rename|1|1" {
		t.Errorf("event = %q", v)
	}
	// The root's last scan is still its real one: a rename run is not a scan of the disk.
	if v := rawQueryString(t, e.db, `SELECT count(*) FROM scans WHERE kind = 'scan'`); v != "1" {
		t.Errorf("scans of kind scan = %s, want 1", v)
	}
	// Renaming the same path again: it is no longer current, so nothing is recorded.
	if err := run.Renamed(e.ctx, c, "again.pdf", started); err == nil {
		t.Error("a second rename of a closed path was recorded")
	}
}

func TestFilesExportRecordsPath(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha")}})
	id, _ := e.filesExport("2026-11-27", tagNow)
	k := e.known()
	if v := rawQueryString(t, e.db, `SELECT path_id FROM export_rows WHERE export_id = `+itoa(id)); v != itoa(k["a"].PathID) {
		t.Errorf("exported path_id = %s, want %d", v, k["a"].PathID)
	}
}

func TestUpdateFilesRenames(t *testing.T) {
	const today = "2026-11-27"
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (*reviewEnv, int64, map[string]FilesItem) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("d/Britsh bill.pdf", "ha"), f("d/b.pdf", "hb"), f("d/c.pdf", "hc"),
			f("e/c.pdf", "hd")}})
		id, items := e.filesExport(today, now)
		return e, id, items
	}
	// row renames it (a name cell edited) and changes nothing else.
	row := func(line int, ex int64, it FilesItem, name string) FilesRow {
		return FilesRow{RetentionRow: RetentionRow{Line: line, ExportID: ex, FileID: it.FileID}, HasName: true,
			NameCell: name, Name: name}
	}
	untouched := func(line int, ex int64, it FilesItem) FilesRow { return row(line, ex, it, it.NameCell) }
	checkName := func(old, name string, _ bool, _ []string) string {
		if name == "bad" {
			return "a bad name"
		}
		return ""
	}
	disk := ""
	opt := FilesUpdateOptions{DryRun: true, CheckName: checkName,
		CheckDisk: func(Candidate, string) string { return disk }}

	t.Run("a name cell edited asks for a rename; one left as exported does not", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/Britsh bill.pdf"], "British bill.pdf"),
			untouched(3, id, it["d/b.pdf"])}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Pending.Renames) != 1 || res.Unchanged != 1 || len(res.Refusals) != 0 {
			t.Fatalf("result %+v", res)
		}
		if r := res.Pending.Renames[0]; r.Name != "British bill.pdf" || r.Path != "d/Britsh bill.pdf" ||
			r.FileID != it["d/Britsh bill.pdf"].FileID || r.PathID == 0 {
			t.Errorf("rename %+v", r)
		}
	})

	t.Run("refused", func(t *testing.T) {
		e, id, it := setup(t)
		disk = "changed since the last scan"
		defer func() { disk = "" }()
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(2, id, it["d/Britsh bill.pdf"], "bad"), // the name rules
			row(3, id, it["d/b.pdf"], "c.pdf"),         // another file's name in the directory
			row(4, id, it["d/c.pdf"], "x.pdf"),         // the file line 3 wants the name of: refused too
			row(5, id, it["e/c.pdf"], "c2.pdf"),        // the disk check, for a rename nothing else refused
		}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]string{2: "a bad name", 3: "another file's name", 4: "another row renames a file to its name",
			5: "changed since the last scan"}
		if len(res.Refusals) != len(want) || len(res.Pending.Renames) != 0 {
			t.Fatalf("result %+v", res)
		}
		for _, rf := range res.Refusals {
			if !strings.Contains(rf.Reason, want[rf.Line]) {
				t.Errorf("line %d: %q, want %q", rf.Line, rf.Reason, want[rf.Line])
			}
		}
	})

	t.Run("a chain or a swap: both rows refused", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/b.pdf"], "c.pdf"),
			row(3, id, it["d/c.pdf"], "z.pdf")}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 2 || len(res.Pending.Renames) != 0 ||
			!strings.Contains(res.Refusals[1].Reason, "another row renames a file to its name") {
			t.Errorf("result %+v", res)
		}
	})

	t.Run("two rows giving one directory the same name are both refused", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/b.pdf"], "x.pdf"),
			row(3, id, it["d/c.pdf"], "x.pdf"), row(4, id, it["e/c.pdf"], "x.pdf")}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 2 || len(res.Pending.Renames) != 1 || res.Pending.Renames[0].Path != "e/c.pdf" {
			t.Errorf("result %+v, want d's two refused and e's (another directory) asked for", res)
		}
	})

	t.Run("a file moved since the export, or an export from before schema 13", func(t *testing.T) {
		e, id, it := setup(t)
		k := e.known()
		e.apply(Changes{Close: []int64{k["d/b.pdf"].PathID}, Open: []OpenPath{{FileID: k["d/b.pdf"].FileID,
			Path: "d/moved.pdf"}}})
		rawExec(t, e.db, `UPDATE export_rows SET name = NULL WHERE file_id = `+itoa(it["d/c.pdf"].FileID))
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/b.pdf"], "x.pdf"),
			row(3, id, it["d/c.pdf"], "y.pdf"), row(4, id, it["e/c.pdf"], "c.pdf")}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 2 || !strings.Contains(res.Refusals[0].Reason, "moved or renamed since export") ||
			!strings.Contains(res.Refusals[1].Reason, "made before renaming") || res.Unchanged != 1 {
			t.Errorf("result %+v", res)
		}
	})

	t.Run("a rename asks, and declined records nothing", func(t *testing.T) {
		e, id, it := setup(t)
		asked := false
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/b.pdf"], "x.pdf")}, now, today, "f.csv",
			FilesUpdateOptions{CheckName: checkName, Confirm: func(p FilesPending) bool { asked = len(p.Renames) == 1; return false }})
		if err != nil || !asked || !res.Declined {
			t.Errorf("result %+v, %v, asked %v", res, err, asked)
		}
	})
}
