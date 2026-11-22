package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/tags"
)

// Tags (docs/design.md, Tags): listing and renaming them, slice 3.

func mustSearch(t *testing.T, s string) tags.Search {
	t.Helper()
	q, err := tags.ParseRemoval(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// tagLines is the tag summary as "tag N" lines.
func (e *reviewEnv) tagLines() (string, TagSummary) {
	e.t.Helper()
	sum, err := e.s.TagSummary(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, c := range sum.Tags {
		out = append(out, c.Tag.String()+" "+strings.Repeat("|", int(c.Files)))
	}
	return strings.Join(out, ", "), sum
}

func TestTagSummary(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc"), f("gone", "hg"), f("del", "hd"),
		f("plain", "hp")}})
	k := e.known()
	tag := func(rel string, ts ...string) {
		var add []tags.Tag
		for _, s := range ts {
			add = append(add, mustTag(t, s))
		}
		if _, err := e.s.TagFiles(e.ctx, []int64{k[rel].FileID}, add, tagNow); err != nil {
			t.Fatal(err)
		}
	}
	tag("a", "type:bill", "sender:British Gas")
	tag("b", "Type:Bill", "urgent")
	tag("gone", "project:x")
	tag("del", "project:old", "urgent")
	e.apply(Changes{Close: []int64{k["gone"].PathID}})
	if _, err := e.s.exec(e.ctx, `UPDATE files SET deleted_utc = 'x' WHERE id = ?`, k["del"].FileID); err != nil {
		t.Fatal(err)
	}
	got, sum := e.tagLines()
	if want := "urgent |, project:x |, sender:British Gas |, type:bill ||"; got != want {
		t.Errorf("tags = %q, want %q", got, want)
	}
	if sum.Tagged != 3 || sum.Untagged != 2 { // a, b, gone (missing); c and plain
		t.Errorf("tagged %d, untagged %d", sum.Tagged, sum.Untagged)
	}
}

func TestRenameTag(t *testing.T) {
	tests := []struct {
		name      string
		old, to   string
		want      string
		files     int
		merged    int
		err       error
		otherwise string // the error's text, when not one of ours
	}{
		{"a typo in the value", "sender:british gaz", "sender:British Gas", "sender:British Gas |||, type:bill ||",
			1, 0, nil, ""},
		{"into a tag the files have, merging", "type:bill", "sender:British Gas",
			"sender:British Gas ||, sender:british gaz |", 2, 2, nil, ""},
		{"only the case", "type:bill", "type:Bill", "sender:British Gas ||, sender:british gaz |, type:Bill ||",
			2, 0, nil, ""},
		{"a key", "type:", "kind:", "kind:bill ||, sender:British Gas ||, sender:british gaz |", 2, 0, nil, ""},
		{"a key into one in use, merging", "sender:", "type:",
			"type:bill ||, type:British Gas ||, type:british gaz |", 3, 0, nil, ""},
		{"a bare tag to a keyed one", "type:bill", "bill", "bill ||, sender:British Gas ||, sender:british gaz |",
			2, 0, nil, ""},
		{"nothing has it", "type:invoice", "type:bill", "", 0, 0, ErrNoSuchTag, ""},
		{"no key has it", "colour:", "color:", "", 0, 0, ErrNoSuchTag, ""},
		{"the same tag", "type:bill", "type:bill", "sender:British Gas ||, sender:british gaz |, type:bill ||",
			0, 0, nil, ""},
		{"a key and a tag", "type:", "kind:bill", "", 0, 0, nil, "give both as key:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newReviewEnv(t)
			e.apply(Changes{New: []NewFile{f("a", "ha"), f("b", "hb"), f("c", "hc")}})
			k := e.known()
			for rel, ts := range map[string][]string{"a": {"sender:British Gas", "type:bill"},
				"b": {"sender:British Gas", "type:bill"}, "c": {"sender:british gaz"}} {
				var add []tags.Tag
				for _, s := range ts {
					add = append(add, mustTag(t, s))
				}
				if _, err := e.s.TagFiles(e.ctx, []int64{k[rel].FileID}, add, tagNow); err != nil {
					t.Fatal(err)
				}
			}
			res, err := e.s.RenameTag(e.ctx, mustSearch(t, tt.old), mustSearch(t, tt.to), tagNow)
			switch {
			case tt.err != nil || tt.otherwise != "":
				if (tt.err != nil && !errors.Is(err, tt.err)) || (tt.otherwise != "" &&
					(err == nil || !strings.Contains(err.Error(), tt.otherwise))) {
					t.Fatalf("RenameTag error = %v, want %v %q", err, tt.err, tt.otherwise)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if res.Files != tt.files || res.Merged != tt.merged {
				t.Errorf("Renamed = %+v, want %d files, %d merged", res, tt.files, tt.merged)
			}
			if got, _ := e.tagLines(); got != tt.want {
				t.Errorf("tags = %q, want %q", got, tt.want)
			}
		})
	}
}
