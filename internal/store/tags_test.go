package store

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// Tags (docs/design.md, Tags): the register, slice 1.

var tagNow = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

func mustTag(t *testing.T, s string) tags.Tag {
	t.Helper()
	tg, err := tags.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func mustRemoval(t *testing.T, s string) tags.Search {
	t.Helper()
	q, err := tags.ParseRemoval(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// tagList is a file's tags as typed back, joined with "; ".
func (e *reviewEnv) tagList(id int64) string {
	e.t.Helper()
	m, err := e.s.FileTags(e.ctx, []int64{id})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, t := range m[id] {
		out = append(out, t.String())
	}
	return strings.Join(out, "; ")
}

// tagHistory is tag_changes as "kind command tag file" lines, in order.
func (e *reviewEnv) tagHistory() []string {
	e.t.Helper()
	rows, err := e.s.db.Query(`SELECT kind, command, key, value, file_id FROM tag_changes ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, cmd string
		var tg tags.Tag
		var id int64
		if err := rows.Scan(&kind, &cmd, &tg.Key, &tg.Value, &id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, kind+" "+cmd+" "+tg.String()+" "+strconv.FormatInt(id, 10))
	}
	return out
}

func TestMigrateFromV10(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropTags, `UPDATE meta SET value = '10' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	counts, err := s.RowCounts(t.Context())
	if err != nil || counts["tags"] != 0 || counts["tag_changes"] != 0 {
		t.Errorf("row counts after upgrade: %v, %v", counts, err)
	}
	var tagsCol int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('export_rows') WHERE name = 'tags'`).
		Scan(&tagsCol); err != nil || tagsCol != 1 {
		t.Errorf("export_rows.tags after upgrade: %d, %v", tagsCol, err)
	}
	if notes := s.UpgradeNotes(); len(notes) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", notes)
	}
}

func TestTagFiles(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc")}})
	k := e.known()
	a, b, c := k["a"].FileID, k["b"].FileID, k["c"].FileID

	res, err := e.s.TagFiles(e.ctx, []int64{a, b},
		[]tags.Tag{mustTag(t, "sender:British Gas"), mustTag(t, "type:bill"), mustTag(t, "TYPE:Bill")}, tagNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 2 || len(res.Respelled) != 1 || res.Respelled[0].Spelled.Value != "bill" {
		t.Errorf("first tagging: %+v", res) // TYPE:Bill is type:bill, typed first
	}
	if got := e.tagList(a); got != "sender:British Gas; type:bill" {
		t.Errorf("a's tags = %q", got)
	}

	t.Run("the first spelling is the tag's", func(t *testing.T) {
		res, err := e.s.TagFiles(e.ctx, []int64{c}, []tags.Tag{mustTag(t, "sender:british gas"), mustTag(t, "urgent")},
			tagNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.tagList(c); got != "urgent; sender:British Gas" {
			t.Errorf("c's tags = %q", got)
		}
		if len(res.Respelled) != 1 || res.Respelled[0].Typed.Value != "british gas" ||
			res.Respelled[0].Spelled.Value != "British Gas" {
			t.Errorf("Respelled = %+v", res.Respelled)
		}
	})

	t.Run("a tag the file has in any case is left alone, and nothing is written", func(t *testing.T) {
		e.s.changed = false
		res, err := e.s.TagFiles(e.ctx, []int64{a}, []tags.Tag{mustTag(t, "Type:BILL")}, tagNow)
		if err != nil || len(res.Changed) != 0 {
			t.Errorf("tagging again: %+v, %v", res, err)
		}
		if e.s.changed {
			t.Error("a tagging that changed nothing was noted as a change")
		}
		if got := e.tagList(a); got != "sender:British Gas; type:bill" {
			t.Errorf("a's tags = %q", got)
		}
	})

	t.Run("a tag typed twice in two spellings is one, the first", func(t *testing.T) {
		res, err := e.s.TagFiles(e.ctx, []int64{c}, []tags.Tag{mustTag(t, "kind:Gas"), mustTag(t, "kind:GAS")}, tagNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.tagList(c); got != "urgent; kind:Gas; sender:British Gas" {
			t.Errorf("c's tags = %q", got)
		}
		if len(res.Respelled) != 1 || res.Respelled[0].Typed.Value != "GAS" || res.Respelled[0].Spelled.Value != "Gas" {
			t.Errorf("Respelled = %+v", res.Respelled)
		}
		if _, err := e.s.UntagFiles(e.ctx, []int64{c}, []tags.Search{mustRemoval(t, "kind:")}, tagNow); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a deleted file's spelling is not the tag's", func(t *testing.T) {
		if _, err := e.s.TagFiles(e.ctx, []int64{a}, []tags.Tag{mustTag(t, "project:Old")}, tagNow); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.exec(e.ctx, `UPDATE files SET deleted_utc = '2026-11-22T00:00:00Z' WHERE id = ?`, a); err != nil {
			t.Fatal(err)
		}
		res, err := e.s.TagFiles(e.ctx, []int64{b}, []tags.Tag{mustTag(t, "project:OLD")}, tagNow)
		if err != nil || len(res.Respelled) != 0 {
			t.Errorf("tagging after a delete: %+v, %v", res, err)
		}
		if got := e.tagList(b); got != "project:OLD; sender:British Gas; type:bill" {
			t.Errorf("b's tags = %q", got)
		}
	})

	t.Run("a file deleted by filo, or not in the register, is refused", func(t *testing.T) {
		for _, ids := range [][]int64{{b, a}, {b, 999}} {
			if _, err := e.s.TagFiles(e.ctx, ids, []tags.Tag{mustTag(t, "x")}, tagNow); err == nil {
				t.Errorf("TagFiles(%v) succeeded", ids)
			}
			if _, err := e.s.UntagFiles(e.ctx, ids, []tags.Search{mustRemoval(t, "x")}, tagNow); err == nil {
				t.Errorf("UntagFiles(%v) succeeded", ids)
			}
		}
		if got := e.tagList(b); got != "project:OLD; sender:British Gas; type:bill" {
			t.Errorf("b's tags = %q after a refusal", got)
		}
	})

	want := []string{
		"add tag sender:British Gas " + strconv.FormatInt(a, 10), "add tag type:bill " + strconv.FormatInt(a, 10),
		"add tag sender:British Gas " + strconv.FormatInt(b, 10), "add tag type:bill " + strconv.FormatInt(b, 10),
		"add tag sender:British Gas " + strconv.FormatInt(c, 10), "add tag urgent " + strconv.FormatInt(c, 10),
		"add tag kind:Gas " + strconv.FormatInt(c, 10), "remove untag kind:Gas " + strconv.FormatInt(c, 10),
		"add tag project:Old " + strconv.FormatInt(a, 10), "add tag project:OLD " + strconv.FormatInt(b, 10),
	}
	if got := e.tagHistory(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("history:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestUntagFiles(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb")}})
	k := e.known()
	a, b := k["a"].FileID, k["b"].FileID
	if _, err := e.s.TagFiles(e.ctx, []int64{a, b}, []tags.Tag{mustTag(t, "project:house sale"),
		mustTag(t, "project:trip"), mustTag(t, "type:bill"), mustTag(t, "urgent")}, tagNow); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		ids     []int64
		remove  string
		changed int
		a, b    string
	}{
		{"a tag in any case", []int64{a}, "TYPE:Bill", 1,
			"urgent; project:house sale; project:trip", "urgent; project:house sale; project:trip; type:bill"},
		{"a tag the file lacks", []int64{a}, "type:bill", 0,
			"urgent; project:house sale; project:trip", "urgent; project:house sale; project:trip; type:bill"},
		{"every value of a key", []int64{a, b}, "project:", 2, "urgent", "urgent; type:bill"},
		{"a bare tag is not a key's value", []int64{b}, "bill", 0, "urgent", "urgent; type:bill"},
		{"a bare tag", []int64{a, b}, "Urgent", 2, "", "type:bill"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := e.s.UntagFiles(e.ctx, tt.ids, []tags.Search{mustRemoval(t, tt.remove)}, tagNow)
			if err != nil || len(res.Changed) != tt.changed {
				t.Errorf("UntagFiles(%s) = %+v, %v; want %d changed", tt.remove, res, err, tt.changed)
			}
			if got := e.tagList(a); got != tt.a {
				t.Errorf("a's tags = %q, want %q", got, tt.a)
			}
			if got := e.tagList(b); got != tt.b {
				t.Errorf("b's tags = %q, want %q", got, tt.b)
			}
		})
	}
	var removed int
	for _, h := range e.tagHistory() {
		if strings.HasPrefix(h, "remove untag ") {
			removed++
		}
	}
	if removed != 7 {
		t.Errorf("%d removals recorded, want 7:\n%s", removed, strings.Join(e.tagHistory(), "\n"))
	}
}

// TestCopies: the files present with a content, each once with its tags; empty, missing and deleted files are not
// copies. A new file given tags by a scan has them, recorded as the scan's.
func TestCopies(t *testing.T) {
	e := newReviewEnv(t)
	empty := f("empty", "he")
	empty.Size = 0
	e.apply(Changes{New: []NewFile{f("a", "h1"), f("b", "h1"), f("gone", "h1"), f("del", "h1"), f("other", "h2"),
		empty}})
	k := e.known()
	if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID, k["gone"].FileID, k["del"].FileID},
		[]tags.Tag{mustTag(t, "type:bill")}, tagNow); err != nil {
		t.Fatal(err)
	}
	e.apply(Changes{Close: []int64{k["gone"].PathID}})
	if _, err := e.s.exec(e.ctx, `UPDATE files SET deleted_utc = 'x' WHERE id = ?`, k["del"].FileID); err != nil {
		t.Fatal(err)
	}
	got, err := e.s.Copies(e.ctx, []string{"h1", "he", "h3"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range got["h1"] {
		var ts []string
		for _, tg := range c.Tags {
			ts = append(ts, tg.String())
		}
		lines = append(lines, c.Path+" ["+strings.Join(ts, "; ")+"]")
	}
	if strings.Join(lines, ", ") != "a [type:bill], b []" || len(got) != 1 {
		t.Errorf("Copies = %v (%d contents)", lines, len(got))
	}
	if c := got["h1"][0]; c.AbsPath() != e.root.Path+"/a" {
		t.Errorf("AbsPath = %q", c.AbsPath())
	}

	n := f("c", "h1")
	n.Tags = []tags.Tag{mustTag(t, "type:bill"), mustTag(t, "urgent")}
	e.apply(Changes{New: []NewFile{n}})
	if got := e.tagList(e.known()["c"].FileID); got != "urgent; type:bill" {
		t.Errorf("new file's tags = %q", got)
	}
	h := e.tagHistory()
	if len(h) != 5 || !strings.HasPrefix(h[3], "add scan type:bill ") || !strings.HasPrefix(h[4], "add scan urgent ") {
		t.Errorf("history = %q", h)
	}
}

// TestFoundAndShownTags: filo find's rows carry their file's id, so its tags can be read, and filo show's record has
// the file's tags.
func TestFoundAndShownTags(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb")}})
	k := e.known()
	if _, err := e.s.TagFiles(e.ctx, []int64{k["a"].FileID}, []tags.Tag{mustTag(t, "type:bill")}, tagNow); err != nil {
		t.Fatal(err)
	}
	found, err := e.s.FindFiles(e.ctx, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.FileID != k[f.Rel].FileID {
			t.Errorf("%s: FileID %d, want %d", f.Rel, f.FileID, k[f.Rel].FileID)
		}
	}
	for rel, want := range map[string]string{"a": "type:bill", "b": ""} {
		sh, err := e.s.ShowFile(e.ctx, e.root.ID, rel, "2026-11-22")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tg := range sh.Tags {
			got = append(got, tg.String())
		}
		if strings.Join(got, "; ") != want {
			t.Errorf("%s: Tags %v, want %q", rel, got, want)
		}
	}
}
