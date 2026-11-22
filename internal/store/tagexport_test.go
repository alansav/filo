package store

import (
	"strings"
	"testing"

	"github.com/alansav/filo/internal/tags"
)

// Tags (docs/design.md, Tags, "The tags CSV"): the register side, slice 5.

func (e *reviewEnv) tagsExport() (int64, map[string]TagItem) {
	e.t.Helper()
	items, err := e.s.TagItems(e.ctx, 0, "")
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.s.RecordTagsExport(e.ctx, "/x/tags.csv", tagNow, items, func(int64) error { return nil })
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]TagItem{}
	for _, it := range items {
		m[it.Rel] = it
	}
	return id, m
}

func cell(t *testing.T, s string) []tags.Tag {
	t.Helper()
	var out []tags.Tag
	for _, p := range strings.Split(s, ";") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, mustTag(t, p))
	}
	return out
}

func TestTagItemsAndExport(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("gone", "hg"),
		{Path: "link", Kind: "symlink", LinkTarget: "a"}}})
	k := e.known()
	if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill"),
		mustTag(t, "urgent")}, tagNow); err != nil {
		t.Fatal(err)
	}
	e.apply(Changes{Close: []int64{k["gone"].PathID}})
	id, items := e.tagsExport()
	if len(items) != 2 || JoinTags(items["a"].Tags) != "urgent; type:bill" || len(items["b"].Tags) != 0 {
		t.Errorf("items = %+v", items)
	}
	var kind, cellA, cellB string
	if err := e.s.db.QueryRow(`SELECT kind FROM exports WHERE id = ?`, id).Scan(&kind); err != nil || kind != ExportTags {
		t.Errorf("export kind %q, %v", kind, err)
	}
	if err := e.s.db.QueryRow(`SELECT (SELECT tags FROM export_rows WHERE export_id = ?1 AND file_id = ?2),
		(SELECT tags FROM export_rows WHERE export_id = ?1 AND file_id = ?3)`, id, k["a"].FileID, k["b"].FileID).
		Scan(&cellA, &cellB); err != nil || cellA != "urgent; type:bill" || cellB != "" {
		t.Errorf("export rows %q, %q, %v", cellA, cellB, err)
	}
}

func TestUpdateTags(t *testing.T) {
	setup := func(t *testing.T) (*reviewEnv, int64, map[string]Known) {
		e := newReviewEnv(t)
		e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc")}})
		k := e.known()
		if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill"),
			mustTag(t, "sender:British Gas")}, tagNow); err != nil {
			t.Fatal(err)
		}
		id, _ := e.tagsExport()
		return e, id, k
	}

	t.Run("cells replace the tags; an untouched cell or one in another case changes nothing", func(t *testing.T) {
		e, id, k := setup(t)
		var asked []TagChange
		res, err := e.s.UpdateTags(e.ctx, []TagRow{
			{Line: 2, ExportID: id, FileID: k["a"].FileID, Tags: cell(t, "SENDER:British Gas")},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "sender:british gas; urgent; urgent")},
			{Line: 4, ExportID: id, FileID: k["c"].FileID, Tags: nil},
		}, tagNow, TagUpdateOptions{ConfirmRemovals: func(cs []TagChange) bool { asked = cs; return true }})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Changed) != 2 || res.Unchanged != 1 || res.Removing != 1 || len(asked) != 1 ||
			asked[0].Removed[0].String() != "type:bill" {
			t.Errorf("result %+v, asked %+v", res, asked)
		}
		if got := e.tagList(k["a"].FileID); got != "sender:British Gas" {
			t.Errorf("a's tags = %q", got)
		}
		if got := e.tagList(k["b"].FileID); got != "urgent; sender:British Gas" { // the spelling the tag has
			t.Errorf("b's tags = %q", got)
		}
		var cmds int
		if err := e.s.db.QueryRow(`SELECT count(*) FROM tag_changes WHERE command = 'update'`).Scan(&cmds); err != nil ||
			cmds != 3 {
			t.Errorf("%d changes recorded by update, %v", cmds, err)
		}
	})

	t.Run("a CSV applied twice changes nothing the second time", func(t *testing.T) {
		e, id, k := setup(t)
		rows := []TagRow{{Line: 2, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "urgent")}}
		if _, err := e.s.UpdateTags(e.ctx, rows, tagNow, TagUpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		res, err := e.s.UpdateTags(e.ctx, rows, tagNow, TagUpdateOptions{})
		if err != nil || res.Unchanged != 1 || len(res.Refusals)+len(res.Changed) != 0 {
			t.Errorf("again: %+v, %v", res, err)
		}
	})

	t.Run("removals not confirmed are left alone; additions still apply", func(t *testing.T) {
		e, id, k := setup(t)
		res, err := e.s.UpdateTags(e.ctx, []TagRow{
			{Line: 2, ExportID: id, FileID: k["a"].FileID, Tags: cell(t, "type:bill; urgent")},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "urgent")},
		}, tagNow, TagUpdateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Changed) != 1 || res.Skipped != 1 {
			t.Errorf("result %+v", res)
		}
		if got := e.tagList(k["a"].FileID); got != "sender:British Gas; type:bill" {
			t.Errorf("a's tags = %q", got)
		}
		if got := e.tagList(k["b"].FileID); got != "urgent" {
			t.Errorf("b's tags = %q", got)
		}
	})

	t.Run("a dry run writes nothing, and shows the spelling the run would give", func(t *testing.T) {
		e, id, k := setup(t)
		e.s.changed = false
		res, err := e.s.UpdateTags(e.ctx, []TagRow{{Line: 2, ExportID: id, FileID: k["a"].FileID},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "TYPE:BILL")}}, tagNow,
			TagUpdateOptions{DryRun: true, ConfirmRemovals: func([]TagChange) bool { t.Error("asked"); return true }})
		if err != nil || len(res.Changed) != 2 || res.Removing != 1 || e.s.changed {
			t.Errorf("dry run: %+v, %v, changed %v", res, err, e.s.changed)
		}
		if got := JoinTags(res.Changed[1].Added); got != "type:bill" {
			t.Errorf("dry run adds %q, want type:bill", got)
		}
		if got := e.tagList(k["a"].FileID); got != "sender:British Gas; type:bill" {
			t.Errorf("a's tags = %q", got)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e, id, k := setup(t)
		if _, err := e.s.TagFiles(e.ctx, []int64{k["b"].FileID}, []tags.Tag{mustTag(t, "later")}, tagNow); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.exec(e.ctx, `UPDATE files SET deleted_utc = 'x' WHERE id = ?`, k["c"].FileID); err != nil {
			t.Fatal(err)
		}
		retID, err := e.s.RecordExport(e.ctx, "/x/r.csv", tagNow, nil, func(int64) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.s.UpdateTags(e.ctx, []TagRow{
			{Line: 2, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "x")},
			{Line: 3, ExportID: id, FileID: k["c"].FileID, Tags: cell(t, "x")},
			{Line: 7, ExportID: id, FileID: k["b"].FileID}, // left as exported: unchanged, though b was tagged since
			{Line: 8, ExportID: id, FileID: k["c"].FileID}, // and though c was deleted since
			{Line: 4, ExportID: 99, FileID: k["a"].FileID, Tags: cell(t, "x")},
			{Line: 5, ExportID: retID, FileID: k["a"].FileID, Tags: cell(t, "x")},
			{Line: 6, ExportID: id, FileID: 999, Tags: cell(t, "x")},
		}, tagNow, TagUpdateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			"its tags were changed after export 1 (now later); export again",
			"deleted by filo since the export",
			"export 99 is not in this register",
			"export 2 is a retention export, not a tags export",
			"file 999 is not in export 1 (rows cannot be added)",
		}
		var got []string
		for _, rf := range res.Refusals {
			got = append(got, rf.Reason)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") || len(res.Changed) != 0 || res.Unchanged != 2 {
			t.Errorf("refusals:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}
