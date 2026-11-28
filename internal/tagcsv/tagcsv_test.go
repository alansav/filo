package tagcsv

import (
	"strings"
	"testing"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// TestGuardCell: a cell a spreadsheet would read as a formula, or one that starts with an apostrophe itself, is
// written after an apostrophe, and ParseCell gives back the tags as they were.
func TestGuardCell(t *testing.T) {
	for _, tc := range []struct {
		tags []tags.Tag
		cell string
	}{
		{[]tags.Tag{{Key: "sender", Value: "British Gas"}, {Key: "type", Value: "bill"}}, "sender:British Gas; type:bill"},
		{[]tags.Tag{{Value: "-draft"}}, "'-draft"},
		{[]tags.Tag{{Value: "'-quoted"}}, "''-quoted"},
		{nil, ""},
	} {
		cell := GuardCell(store.JoinTags(tc.tags))
		if cell != tc.cell {
			t.Errorf("GuardCell(%v) = %q, want %q", tc.tags, cell, tc.cell)
		}
		back, err := ParseCell(cell)
		if err != nil || store.JoinTags(back) != store.JoinTags(tc.tags) {
			t.Errorf("ParseCell(%q) = %v, %v; want %v", cell, back, err, tc.tags)
		}
	}
}

// TestParseCell: tags separated by ";", spaces around them dropped, a tag typed twice (in any case) one, the
// spelling rules applied.
func TestParseCell(t *testing.T) {
	for _, tc := range []struct{ cell, want, err string }{
		{"Type:Bill ;  urgent;; type:bill ", "type:Bill; urgent", ""},
		{"'=x; y", "=x; y", ""},
		{"  ", "", ""},
		{"sender:", "", "give a value"},
		{"a*b", "", `a tag cannot hold "*"`},
	} {
		got, err := ParseCell(tc.cell)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("ParseCell(%q) error %v, want %q", tc.cell, err, tc.err)
		case tc.err == "" && (err != nil || store.JoinTags(got) != tc.want):
			t.Errorf("ParseCell(%q) = %q, %v; want %q", tc.cell, store.JoinTags(got), err, tc.want)
		}
	}
}
