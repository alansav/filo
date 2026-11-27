package store

import (
	"maps"
	"strings"
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
	for i := range items {
		items[i].NameCell = "cell:" + items[i].Path // as the CSV shows it; package filecsv works it out
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
	if _, err := e.update([]RetentionRow{{Line: 2, ExportID: ex0, FileID: k["a"].FileID,
		FileDate: "2020-05-01", HasFileDate: true, Retention: "10y"}}, now.Add(-time.Hour), today, false,
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
	if v := rawQueryString(t, e.db, `SELECT name FROM export_rows WHERE export_id = `+itoa(id)+` AND file_id = `+
		itoa(k["a"].FileID)); v != "cell:a" {
		t.Errorf("exported name cell of a = %q, want the cell as shown", v)
	}
}

func TestMigrateFromV12(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropExportNames, `UPDATE meta SET value = '12' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != "13" {
		t.Fatalf("schema_version = %s after upgrade, want 13", v)
	}
	if _, err := s.db.Exec(`SELECT name FROM export_rows`); err != nil {
		t.Errorf("export_rows.name after the upgrade: %v", err)
	}
}

func TestUpdateFiles(t *testing.T) {
	const today = "2026-11-27"
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	// setup: a has tags and a retention, b and c have neither, link is a symlink; then a combined export.
	setup := func(t *testing.T) (*reviewEnv, int64, map[string]FilesItem) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc"),
			{Path: "link", Kind: "symlink", LinkTarget: "a"}}})
		k := e.known()
		if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill")},
			tagNow); err != nil {
			t.Fatal(err)
		}
		ex0, _ := e.export(today, now.Add(-time.Hour))
		if _, err := e.update([]RetentionRow{{Line: 2, ExportID: ex0, FileID: k["a"].FileID,
			FileDate: "2020-05-01", HasFileDate: true, Retention: "10y"}}, now.Add(-time.Hour), today,
			false, nil); err != nil {
			t.Fatal(err)
		}
		id, items := e.filesExport(today, now)
		return e, id, items
	}
	// row is a row of the combined CSV with a file_date column and no keep_until column.
	row := func(line int, ex int64, it FilesItem, date, ret, cellText string) FilesRow {
		return FilesRow{RetentionRow: RetentionRow{Line: line, ExportID: ex, FileID: it.FileID, FileDate: date,
			HasFileDate: true, Retention: ret}, Tags: cell(t, cellText)}
	}
	retentionOf := func(e *reviewEnv, id int64) string {
		return rawQueryString(t, e.db, `SELECT coalesce(retention, '') FROM files WHERE id = `+itoa(id))
	}

	t.Run("both halves of a row apply, with no question when nothing is cleared or removed", func(t *testing.T) {
		e, id, it := setup(t)
		asked := false
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(2, id, it["a"], "2020-05-01", "10y", "type:bill"), // untouched
			row(3, id, it["b"], "", "2030-01-01", "urgent"),       // both halves
			row(4, id, it["c"], "", "", "sender:British Gas"),     // tags only
			row(5, id, it["link"], "", "forever", ""),             // retention only, a symlink
		}, now, today, "f.csv", FilesUpdateOptions{Confirm: func(FilesPending) bool { asked = true; return true }})
		if err != nil {
			t.Fatal(err)
		}
		if asked || res.Declined || res.Set != 2 || len(res.TagsChanged) != 2 || res.Unchanged != 1 ||
			len(res.Refusals) != 0 {
			t.Errorf("result %+v, asked %v", res, asked)
		}
		if retentionOf(e, it["b"].FileID) != "2030-01-01" || e.tagList(it["b"].FileID) != "urgent" ||
			e.tagList(it["c"].FileID) != "sender:British Gas" || retentionOf(e, it["link"].FileID) != "forever" {
			t.Errorf("b %q %q, c %q, link %q", retentionOf(e, it["b"].FileID), e.tagList(it["b"].FileID),
				e.tagList(it["c"].FileID), retentionOf(e, it["link"].FileID))
		}
	})

	t.Run("a row refused by either half is refused whole", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{
			row(2, id, it["b"], "", "7y", "urgent"),           // a period with no file_date
			row(3, id, it["link"], "", "forever", "urgent"),   // tags on a symlink
			row(4, id, it["c"], "", "2030-01-01", "urgent"),   // fine
			row(5, id+1, it["c"], "", "2030-01-01", "urgent"), // no such export: one reason, not two
		}, now, today, "f.csv", FilesUpdateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Refusals) != 3 || res.Set != 1 || len(res.TagsChanged) != 1 {
			t.Fatalf("result %+v", res)
		}
		if !strings.Contains(res.Refusals[0].Reason, "file_date") || !strings.Contains(res.Refusals[1].Reason,
			"symlink") || strings.Count(res.Refusals[2].Reason, "not in this register") != 1 {
			t.Errorf("refusals %+v", res.Refusals)
		}
		if e.tagList(it["b"].FileID) != "" || retentionOf(e, it["link"].FileID) != "" {
			t.Errorf("a refused row's other half was applied: b %q, link %q", e.tagList(it["b"].FileID),
				retentionOf(e, it["link"].FileID))
		}
	})

	t.Run("a row from another kind of export is refused", func(t *testing.T) {
		e, _, it := setup(t)
		ex := e.oldExport("tags")
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, ex, it["b"], "", "", "urgent")}, now, today, "f.csv",
			FilesUpdateOptions{})
		if err != nil || len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "is a tags export, not a combined export") {
			t.Errorf("result %+v, %v", res, err)
		}
	})

	clearing := func(id int64, it map[string]FilesItem) []FilesRow {
		return []FilesRow{
			row(2, id, it["a"], "2020-05-01", "", ""),       // clears the retention, removes type:bill
			row(3, id, it["b"], "", "tax", ""),              // a new policy
			row(4, id, it["c"], "", "2030-01-01", "urgent"), // needs no question, but waits for it
		}
	}
	for _, tc := range []struct {
		name    string
		confirm func(FilesPending) bool
		want    bool // recorded
	}{
		{"declined: nothing is recorded, not even the rows that needed no question",
			func(FilesPending) bool { return false }, false},
		{"no one to ask: nothing is recorded", nil, false},
		{"confirmed: every row is recorded", func(FilesPending) bool { return true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, id, it := setup(t)
			var pending FilesPending
			confirm := tc.confirm
			if confirm != nil {
				confirm = func(p FilesPending) bool { pending = p; return tc.confirm(p) }
			}
			before, err := e.s.RowCounts(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.s.UpdateFiles(e.ctx, clearing(id, it), now, today, "f.csv", FilesUpdateOptions{Confirm: confirm})
			if err != nil {
				t.Fatal(err)
			}
			if p := res.Pending; len(p.Clears) != 1 || len(p.Removals) != 1 || len(p.NewPolicies) != 1 ||
				p.NewPolicies[0].Name != "tax" {
				t.Errorf("pending %+v", p)
			}
			if tc.confirm != nil && len(pending.Clears)+len(pending.Removals)+len(pending.NewPolicies) != 3 {
				t.Errorf("asked about %+v", pending)
			}
			after, err := e.s.RowCounts(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if res.Declined == tc.want || maps.Equal(before, after) == tc.want {
				t.Errorf("declined %v, wrote %v, want recorded %v", res.Declined, !maps.Equal(before, after), tc.want)
			}
			if !tc.want {
				return
			}
			if res.Cleared != 1 || res.Set != 2 || res.PoliciesCreated != 1 || res.NoValue != 1 ||
				len(res.TagsChanged) != 2 {
				t.Errorf("result %+v", res)
			}
			if retentionOf(e, it["a"].FileID) != "" || e.tagList(it["a"].FileID) != "" ||
				e.tagList(it["c"].FileID) != "urgent" {
				t.Errorf("a %q %q, c %q", retentionOf(e, it["a"].FileID), e.tagList(it["a"].FileID),
					e.tagList(it["c"].FileID))
			}
		})
	}

	t.Run("a dry run asks nothing, writes nothing, and says what is pending", func(t *testing.T) {
		e, id, it := setup(t)
		before, err := e.s.RowCounts(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.s.UpdateFiles(e.ctx, clearing(id, it), now, today, "f.csv", FilesUpdateOptions{DryRun: true,
			Confirm: func(FilesPending) bool { t.Error("asked in a dry run"); return true }})
		if err != nil {
			t.Fatal(err)
		}
		after, err := e.s.RowCounts(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(before, after) || res.Pending.Empty() || res.Cleared != 1 || res.Set != 2 {
			t.Errorf("result %+v, wrote %v", res, !maps.Equal(before, after))
		}
	})

	t.Run("a period past today is listed as now past its retention", func(t *testing.T) {
		e, id, it := setup(t)
		res, err := e.s.UpdateFiles(e.ctx, []FilesRow{row(2, id, it["b"], "2015-01-01", "7y", "")}, now, today,
			"f.csv", FilesUpdateOptions{})
		if err != nil || len(res.NowPast) != 1 || res.NowPast[0].KeepUntil != "2022-01-01" {
			t.Errorf("result %+v, %v", res, err)
		}
	})
}

func TestExportKinds(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha")}})
	files, _ := e.filesExport("2026-11-27", tagNow)
	tagsID := e.oldExport("tags")
	got, err := e.s.ExportKinds(e.ctx, []int64{files, tagsID, 999})
	if err != nil || len(got) != 2 || got[files] != ExportFiles || got[tagsID] != "tags" {
		t.Errorf("ExportKinds = %v, %v", got, err)
	}
}
