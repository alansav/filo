package store

import (
	"path"
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
	if err := run.Renamed(e.ctx, c, "d/British Gas bill.pdf", started); err != nil {
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
	if err := run.Renamed(e.ctx, c, "d/again.pdf", started); err == nil {
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
	checkName := func(_, newRel string, _ bool, _ []string) string {
		if path.Base(newRel) == "bad" {
			return "a bad name"
		}
		return ""
	}
	disk := ""
	opt := FilesUpdateOptions{DryRun: true, CheckPath: checkName, CheckDisk: func(Rename) string { return disk }}

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

	t.Run("a name of filo's own (a tag index's page) is refused", func(t *testing.T) {
		e, id, it := setup(t)
		if _, err := e.s.AddTagIndex(e.ctx, e.root.ID, "d", "bills", now); err != nil {
			t.Fatal(err)
		}
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["d/b.pdf"], "bills.html"),
			row(3, id, it["d/c.pdf"], ".bills")}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 2 || !strings.Contains(res.Refusals[0].Reason, "filo's own files") ||
			!strings.Contains(res.Refusals[1].Reason, "filo's own files") {
			t.Errorf("result %+v", res)
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
			FilesUpdateOptions{CheckPath: checkName, Confirm: func(p FilesPending) bool { asked = len(p.Renames) == 1; return false }})
		if err != nil || !asked || !res.Declined {
			t.Errorf("result %+v, %v, asked %v", res, err, asked)
		}
	})
}

func TestDirInRoot(t *testing.T) {
	const root = "/home/u/Documents"
	for _, tc := range []struct{ cell, dir, why string }{
		{"/home/u/Documents/received/2026/11", "received/2026/11", ""},
		{"/home/u/Documents//received/2026/11/", "received/2026/11", ""},
		{"/home/u/Documents", "", ""},
		{"/home/u/Documents/", "", ""},
		{"received/2026/11", "", "not a full path"},
		{"~/Documents/received", "", "not a full path"},
		{"/home/u/Documents/../x", "", "holds .. as a part"},
		{"/home/u/Documents/./x", "", "holds . as a part"},
		{"/home/u/Documentsx/a", "", "outside the file's root"},
		{"/home/u", "", "outside the file's root"},
	} {
		dir, why := dirInRoot(root, tc.cell)
		if dir != tc.dir || (tc.why == "") != (why == "") || !strings.Contains(why, tc.why) {
			t.Errorf("dirInRoot(%q) = %q, %q; want %q, %q", tc.cell, dir, why, tc.dir, tc.why)
		}
	}
}

func TestUpdateFilesMoves(t *testing.T) {
	const today = "2026-11-27"
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (*reviewEnv, int64, map[string]FilesItem) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("inbox/bill.pdf", "ha"), f("inbox/scan.pdf", "hb"), f("archive/scan.pdf", "hc"),
			{Path: "inbox/rel-link", Kind: "symlink", LinkTarget: "bill.pdf"},
			{Path: "inbox/abs-link", Kind: "symlink", LinkTarget: "/etc/hosts"}}})
		id, items := e.filesExport(today, now)
		return e, id, items
	}
	row := func(e *reviewEnv, line int, ex int64, it FilesItem, dir string) FilesRow {
		return FilesRow{RetentionRow: RetentionRow{Line: line, ExportID: ex, FileID: it.FileID}, HasName: true,
			NameCell: it.NameCell, Name: it.NameCell, HasDirectory: true, DirectoryCell: dir}
	}
	opt := FilesUpdateOptions{DryRun: true, Moves: true}

	t.Run("a directory cell edited asks for a move; one left as exported does not", func(t *testing.T) {
		e, id, it := setup(t)
		root := e.root.Path
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(e, 2, id, it["inbox/bill.pdf"], root+"/received/2026/11"),
			row(e, 3, id, it["archive/scan.pdf"], root+"/archive"),
			row(e, 4, id, it["inbox/abs-link"], root+"/"),
		}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Pending.Renames) != 2 || res.Unchanged != 1 || len(res.Refusals) != 0 {
			t.Fatalf("result %+v", res)
		}
		if r := res.Pending.Renames[0]; r.Dir != "received/2026/11" || r.Name != "bill.pdf" || !r.Moves() ||
			r.NewPath() != "received/2026/11/bill.pdf" {
			t.Errorf("move %+v", r)
		}
		if r := res.Pending.Renames[1]; r.Dir != "" || r.NewPath() != "abs-link" {
			t.Errorf("move to the top of the root %+v", r)
		}
	})

	t.Run("without Moves the directory cell is display only", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(e, 2, id, it["inbox/bill.pdf"], "/elsewhere")}, now,
			today, "f.csv", FilesUpdateOptions{DryRun: true})
		if err != nil || res.Unchanged != 1 || len(res.Pending.Renames)+len(res.Refusals) != 0 {
			t.Errorf("result %+v, %v", res, err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		e, id, it := setup(t)
		root := e.root.Path
		if _, err := e.s.AddTagIndex(e.ctx, e.root.ID, "", "bills", now); err != nil {
			t.Fatal(err)
		}
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(e, 2, id, it["inbox/bill.pdf"], "received"),                 // not a full path
			row(e, 3, id, it["inbox/scan.pdf"], root+"/archive"),            // another file's name there
			row(e, 4, id, it["inbox/rel-link"], root+"/archive"),            // a relative symlink
			row(e, 5, id, it["archive/scan.pdf"], root+"/.bills/x"),         // inside a tag index's pages
			row(e, 6, id, it["inbox/abs-link"], "/somewhere/else/entirely"), // outside the root
		}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		want := map[int]string{2: "not a full path", 3: "another file's name in archive/", 4: "relative target",
			5: ".bills/x/ is inside one of filo's own directories", 6: "outside the file's root"}
		if len(res.Refusals) != len(want) {
			t.Fatalf("refusals %+v", res.Refusals)
		}
		for _, rf := range res.Refusals {
			if !strings.Contains(rf.Reason, want[rf.Line]) {
				t.Errorf("line %d: %q, want %q", rf.Line, rf.Reason, want[rf.Line])
			}
		}
	})

	t.Run("an export that did not record its paths: a directory cell left alone is no change", func(t *testing.T) {
		e, id, it := setup(t)
		rawExec(t, e.db, `UPDATE export_rows SET path_id = NULL`)
		root := e.root.Path
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(e, 2, id, it["inbox/bill.pdf"], root+"/inbox"), // as it was
			row(e, 3, id, it["inbox/scan.pdf"], root+"/new"),   // changed: refused, export again
		}, now, today, "f.csv", opt)
		if err != nil {
			t.Fatal(err)
		}
		if res.Unchanged != 1 || len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "export again") {
			t.Errorf("result %+v", res)
		}
	})

	t.Run("two files moved to one name in a directory are both refused", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(e, 2, id, it["inbox/scan.pdf"], e.root.Path+"/new"),
			row(e, 3, id, it["archive/scan.pdf"], e.root.Path+"/new")}, now, today, "f.csv", opt)
		if err != nil || len(res.Refusals) != 2 || !strings.Contains(res.Refusals[0].Reason, "2 rows give a file the name scan.pdf in new/") {
			t.Errorf("result %+v, %v", res, err)
		}
	})
}
