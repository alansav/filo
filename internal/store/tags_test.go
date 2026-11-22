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
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) || SchemaVersion != 11 {
		t.Fatalf("schema_version = %s after upgrade, want 11", v)
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
	if len(res.Changed) != 2 || len(res.Respelled) != 0 {
		t.Errorf("first tagging: %+v", res)
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

	want := []string{
		"add tag sender:British Gas " + strconv.FormatInt(a, 10), "add tag type:bill " + strconv.FormatInt(a, 10),
		"add tag sender:British Gas " + strconv.FormatInt(b, 10), "add tag type:bill " + strconv.FormatInt(b, 10),
		"add tag sender:British Gas " + strconv.FormatInt(c, 10), "add tag urgent " + strconv.FormatInt(c, 10),
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
