package store

import (
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
