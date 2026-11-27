package store

import (
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// The combined CSV (docs/design.md, Tags, "The combined CSV"): the register side of the export.

func (e *reviewEnv) filesExport(today string, now time.Time) (int64, map[string]FilesItem) {
	e.t.Helper()
	items, err := e.s.FilesItems(e.ctx, today, Filter{})
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.s.RecordFilesExport(e.ctx, "/x/files.csv", now, items, func(int64) error { return nil })
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]FilesItem{}
	for _, it := range items {
		m[it.Path] = it
	}
	return id, m
}

func TestFilesItemsAndExport(t *testing.T) {
	const today = "2026-11-27"
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("gone", "hg"),
		{Path: "link", Kind: "symlink", LinkTarget: "a"}}})
	k := e.known()
	if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill"),
		mustTag(t, "urgent")}, tagNow); err != nil {
		t.Fatal(err)
	}
	ex0, _ := e.export(today, now.Add(-time.Hour))
	if _, err := e.s.UpdateRetention(e.ctx, []RetentionRow{{Line: 2, ExportID: ex0, FileID: k["a"].FileID,
		FileDate: "2020-05-01", HasFileDate: true, Retention: "10y"}}, now.Add(-time.Hour), today, "r.csv", false,
		nil); err != nil {
		t.Fatal(err)
	}
	e.apply(Changes{Close: []int64{k["gone"].PathID}})

	id, items := e.filesExport(today, now)
	if len(items) != 3 {
		t.Fatalf("items = %+v, want a, b and link (every present file, symlinks too)", items)
	}
	if a := items["a"]; JoinTags(a.Tags) != "urgent; type:bill" || a.Retention != "10y" || a.KeepUntil != "2030-05-01" {
		t.Errorf("a = %+v, want its tags and its retention", a)
	}
	if len(items["b"].Tags) != 0 || len(items["link"].Tags) != 0 {
		t.Errorf("b, link = %+v, %+v, want no tags", items["b"], items["link"])
	}
	var kind string
	if err := e.s.db.QueryRow(`SELECT kind FROM exports WHERE id = ?`, id).Scan(&kind); err != nil ||
		kind != ExportFiles {
		t.Errorf("export kind %q, %v, want files", kind, err)
	}
	if v := rawQueryString(t, e.db, `SELECT hash || ' ' || file_date || ' ' || retention || ' ' || keep_until ||
		' ' || tags FROM export_rows WHERE export_id = `+itoa(id)+` AND file_id = `+itoa(k["a"].FileID)); v != "ha 2020-05-01 10y 2030-05-01 urgent; type:bill" {
		t.Errorf("export row of a = %q, want both halves as exported", v)
	}
	if v := rawQueryString(t, e.db, `SELECT coalesce(retention, 'NULL') || ' ' || tags FROM export_rows
		WHERE export_id = `+itoa(id)+` AND file_id = `+itoa(k["b"].FileID)); v != "NULL " {
		t.Errorf("export row of b = %q, want no retention and an empty tags cell", v)
	}
}
