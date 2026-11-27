package store

import (
	"strings"
	"testing"

	"github.com/alansav/filo/internal/tags"
)

// The tags half of the combined CSV (docs/design.md, Tags, "The combined CSV"): the register side.

// oldExport records an export of a kind filo no longer writes ("retention" or "tags", as a register from before
// 2026-11-27 holds them), with no rows, and returns its ID.
func (e *reviewEnv) oldExport(kind string) int64 {
	e.t.Helper()
	res, err := e.s.exec(e.ctx, `INSERT INTO exports (created_utc, path, kind) VALUES ('2026-11-20T00:00:00Z', '/x/old.csv',
		?)`, kind)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// updateTags records the tags half of rows through the combined update, each row's retention cells left as exported
// (blank: these tests give no file a retention). confirm answers its one question (nil declines).
func (e *reviewEnv) updateTags(rows []TagRow, dry bool, confirm func(FilesPending) bool) FilesUpdateResult {
	e.t.Helper()
	frs := make([]FilesRow, len(rows))
	for i, r := range rows {
		frs[i] = FilesRow{RetentionRow: RetentionRow{Line: r.Line, ExportID: r.ExportID, FileID: r.FileID},
			Tags: r.Tags}
	}
	res, err := e.s.UpdateFiles(e.ctx, frs, tagNow, "2026-11-27", "t.csv", FilesUpdateOptions{DryRun: dry,
		Confirm: confirm})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
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

// TestTagItems: every present regular file with its tags, for a tag index: no missing file, no symlink.
func TestTagItems(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("gone", "hg"),
		{Path: "link", Kind: "symlink", LinkTarget: "a"}}})
	k := e.known()
	if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill"),
		mustTag(t, "urgent")}, tagNow); err != nil {
		t.Fatal(err)
	}
	e.apply(Changes{Close: []int64{k["gone"].PathID}})
	items, err := e.s.TagItems(e.ctx, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Rel] = JoinTags(it.Tags)
	}
	if len(got) != 2 || got["a"] != "urgent; type:bill" || got["b"] != "" {
		t.Errorf("items = %v", got)
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
		id, _ := e.filesExport("2026-11-27", tagNow)
		return e, id, k
	}

	t.Run("cells replace the tags; an untouched cell or one in another case changes nothing", func(t *testing.T) {
		e, id, k := setup(t)
		var asked []TagChange
		res := e.updateTags([]TagRow{
			{Line: 2, ExportID: id, FileID: k["a"].FileID, Tags: cell(t, "SENDER:British Gas")},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "sender:british gas; urgent; urgent")},
			{Line: 4, ExportID: id, FileID: k["c"].FileID, Tags: nil},
		}, false, func(p FilesPending) bool { asked = p.Removals; return true })
		if len(res.TagsChanged) != 2 || res.Unchanged != 1 || len(asked) != 1 ||
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
		e.updateTags(rows, false, nil)
		res := e.updateTags(rows, false, nil)
		if res.Unchanged != 1 || len(res.Refusals)+len(res.TagsChanged) != 0 {
			t.Errorf("again: %+v", res)
		}
	})

	t.Run("a removal not confirmed records nothing, not even additions", func(t *testing.T) {
		e, id, k := setup(t)
		res := e.updateTags([]TagRow{
			{Line: 2, ExportID: id, FileID: k["a"].FileID, Tags: cell(t, "type:bill; urgent")},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "urgent")},
		}, false, nil)
		if !res.Declined || len(res.Pending.Removals) != 1 {
			t.Errorf("result %+v", res)
		}
		if got := e.tagList(k["a"].FileID); got != "sender:British Gas; type:bill" {
			t.Errorf("a's tags = %q", got)
		}
		if got := e.tagList(k["b"].FileID); got != "" {
			t.Errorf("b's tags = %q", got)
		}
	})

	t.Run("a dry run writes nothing, and shows the spelling the run would give", func(t *testing.T) {
		e, id, k := setup(t)
		e.s.changed = false
		res := e.updateTags([]TagRow{{Line: 2, ExportID: id, FileID: k["a"].FileID},
			{Line: 3, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "TYPE:BILL")}}, true,
			func(FilesPending) bool { t.Error("asked"); return true })
		if len(res.TagsChanged) != 2 || len(res.Pending.Removals) != 1 || e.s.changed {
			t.Errorf("dry run: %+v, changed %v", res, e.s.changed)
		}
		if got := JoinTags(res.TagsChanged[1].Added); got != "type:bill" {
			t.Errorf("dry run adds %q, want type:bill", got)
		}
		if got := e.tagList(k["a"].FileID); got != "sender:British Gas; type:bill" {
			t.Errorf("a's tags = %q", got)
		}
	})

	t.Run("a spelling repeated over rows: a dry run shows each as written, the run gives the first", func(t *testing.T) {
		e, id, k := setup(t)
		rows := []TagRow{
			{Line: 2, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "SENDER:british gas; batch:New")},
			{Line: 3, ExportID: id, FileID: k["c"].FileID, Tags: cell(t, "sender:british gas; batch:NEW")},
		}
		res := e.updateTags(rows, true, nil)
		if len(res.TagsChanged) != 2 {
			t.Fatalf("dry run: %+v", res)
		}
		for i, want := range []string{"sender:British Gas; batch:New", "sender:British Gas; batch:NEW"} {
			if got := JoinTags(res.TagsChanged[i].Added); got != want {
				t.Errorf("dry run row %d adds %q, want %q", i, got, want)
			}
		}
		e.updateTags(rows, false, nil)
		for _, f := range []string{"b", "c"} {
			if got := e.tagList(k[f].FileID); got != "batch:New; sender:British Gas" {
				t.Errorf("%s's tags = %q", f, got)
			}
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
		tagsID, retID := e.oldExport("tags"), e.oldExport("retention")
		res := e.updateTags([]TagRow{
			{Line: 2, ExportID: id, FileID: k["b"].FileID, Tags: cell(t, "x")},
			{Line: 3, ExportID: id, FileID: k["c"].FileID, Tags: cell(t, "x")},
			{Line: 7, ExportID: id, FileID: k["b"].FileID}, // left as exported: unchanged, though b was tagged since
			{Line: 4, ExportID: 99, FileID: k["a"].FileID, Tags: cell(t, "x")},
			{Line: 5, ExportID: tagsID, FileID: k["a"].FileID, Tags: cell(t, "x")},
			{Line: 6, ExportID: retID, FileID: k["a"].FileID, Tags: cell(t, "x")},
			{Line: 8, ExportID: id, FileID: 999, Tags: cell(t, "x")},
		}, false, nil)
		want := map[int]string{
			2: "its tags were changed after export #1 (now later); export again",
			3: "deleted by filo since the export",
			4: "export #99 is not in this register",
			5: "export #2 is a tags export, not a combined export",
			6: "export #3 is a retention export, not a combined export",
			8: "file 999 is not in export #1 (rows cannot be added)",
		}
		for _, rf := range res.Refusals {
			if w, ok := want[rf.Line]; !ok || !strings.Contains(rf.Reason, w) {
				t.Errorf("line %d refused: %q, want %q", rf.Line, rf.Reason, w)
			}
			delete(want, rf.Line)
		}
		if len(want) != 0 || len(res.TagsChanged) != 0 || res.Unchanged != 1 {
			t.Errorf("not refused: %v; result %+v", want, res)
		}
	})
}
