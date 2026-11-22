package find

import (
	"slices"
	"testing"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"*gas*", "2026-10-03 British Gas Bill.pdf", true},
		{"*gas*", "notes.txt", false},
		{"*.PDF", "bill.pdf", true},
		{"*.PDF", "Bill.Pdf", true},
		{"*.pdf", "bill.pdf.bak", false},
		{"bill.pdf", "bill.pdf", true},
		{"bill.pdf", "BILL.PDF", true},
		{"bill.pdf", "old bill.pdf", false},
		{"bill", "bill.pdf", false},
		{"Q1 [draft]*", "Q1 [draft] budget.xlsx", true},
		{"report?.pdf", "report?.pdf", true},
		{"report?.pdf", "report1.pdf", false},
		{"[ab].txt", "a.txt", false},
		{"*", "anything", true},
		{"*", "", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"*a*a", "banana", true},
		{"**x", "x", true},
		{"ÉTÉ*", "été 2026.jpg", true},
		{"straße*", "STRASSE.txt", false},      // simple folding: ß is not "ss"
		{"*k", "the Kelvin sign \u212a", true}, // U+212A KELVIN SIGN folds with k
		{"*é", "caf\xff", false},
		{"caf\xfe", "caf\xff", false}, // two invalid bytes are not the same character
		{"caf\xff", "caf\xff", true},
		{"*\ufffd", "x\xff", false}, // nor is an invalid byte U+FFFD
		{"*\ufffd", "x\ufffd", true},
	} {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestSelect(t *testing.T) {
	f := func(root int64, rel string, ino int64, links int) store.FoundFile {
		return store.FoundFile{RootID: root, Root: "/r", Rel: rel, Inode: ino, Size: 5, MtimeNS: 9, Links: links}
	}
	stale := f(1, "g/stale.pdf", 10, 0) // a stale inode: another file's size, so not one of a/bill.pdf's links
	stale.Size = 6
	fs := []store.FoundFile{
		f(1, "a/bill.pdf", 10, 1),
		f(1, "a/other.txt", 0, 0),
		f(1, "b/copy of bill.pdf", 10, 1), // a hard link of a/bill.pdf
		f(1, "c/link.txt", 20, 1),         // a hard link whose other path does not match *bill*
		f(1, "d/bill-link.pdf", 20, 1),
		f(2, "x/bill.pdf", 10, 0), // the same inode in another root: another file
		f(1, "e/unknown.pdf", 0, 0),
		f(1, "f/unknown.pdf", 0, 0), // no inode: never merged
		stale,
		f(1, "h/outside link.pdf", 30, 1), // its link is outside the scope: listed, with the count
	}
	type row struct {
		rel   string
		links int
	}
	for _, tc := range []struct {
		pattern string
		want    []row
	}{
		{"", []row{{"a/bill.pdf", 1}, {"a/other.txt", 0}, {"c/link.txt", 1}, {"x/bill.pdf", 0},
			{"e/unknown.pdf", 0}, {"f/unknown.pdf", 0}, {"g/stale.pdf", 0}, {"h/outside link.pdf", 1}}},
		{"*bill*", []row{{"a/bill.pdf", 1}, {"d/bill-link.pdf", 1}, {"x/bill.pdf", 0}}},
		{"copy*", []row{{"b/copy of bill.pdf", 1}}},
		{"*.pdf", []row{{"a/bill.pdf", 1}, {"d/bill-link.pdf", 1}, {"x/bill.pdf", 0}, {"e/unknown.pdf", 0},
			{"f/unknown.pdf", 0}, {"g/stale.pdf", 0}, {"h/outside link.pdf", 1}}},
		{"a", nil}, // the base name only, never the directory
	} {
		var got []row
		for _, s := range Select(fs, Query{Name: tc.pattern}, nil) {
			got = append(got, row{s.Rel, s.Links})
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Select(%q) = %v\nwant %v", tc.pattern, got, tc.want)
		}
	}
}

// TestSelectTags: --tag and --no-tag, each repeatable, with --name: every condition must hold.
func TestSelectTags(t *testing.T) {
	parse := func(s string) tags.Tag {
		tg, err := tags.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	search := func(s string) tags.Search {
		q, err := tags.ParseSearch(s)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	fs := []store.FoundFile{
		{FileID: 1, RootID: 1, Rel: "a/gas bill.pdf"},
		{FileID: 2, RootID: 1, Rel: "a/water bill.pdf"},
		{FileID: 3, RootID: 1, Rel: "b/notes.txt"},
		{FileID: 4, RootID: 1, Rel: "b/untagged.txt"},
		{FileID: 5, RootID: 1, Rel: "c/link one.pdf", Inode: 7, Links: 1}, // hard links, tagged through one
		{FileID: 6, RootID: 1, Rel: "d/link two.pdf", Inode: 7, Links: 1},
	}
	tagsOf := map[int64][]tags.Tag{
		1: {parse("sender:British Gas"), parse("type:bill")},
		2: {parse("sender:Thames Water"), parse("type:bill"), parse("urgent")},
		3: {parse("project:house")},
		6: {parse("project:house"), parse("urgent")},
	}
	for _, tc := range []struct {
		name          string
		pattern       string
		tagged, lacks []string
		want          []string
	}{
		{"a tag", "", []string{"type:bill"}, nil, []string{"a/gas bill.pdf", "a/water bill.pdf"}},
		{"a value in any case", "", []string{"sender:BRITISH GAS"}, nil, []string{"a/gas bill.pdf"}},
		{"a key", "", []string{"project:"}, nil, []string{"b/notes.txt", "c/link one.pdf"}},
		{"a wildcard", "", []string{"sender:*water*"}, nil, []string{"a/water bill.pdf"}},
		{"two tags: both", "", []string{"type:bill", "urgent"}, nil, []string{"a/water bill.pdf"}},
		{"no such key: the tagging to-do list", "", nil, []string{"sender:"}, []string{"b/notes.txt", "b/untagged.txt",
			"c/link one.pdf"}},
		{"a file tagged through one link is tagged", "", nil, []string{"project:"}, []string{"a/gas bill.pdf",
			"a/water bill.pdf", "b/untagged.txt"}},
		{"hard links listed at the first whose name matches", "*two*", []string{"urgent"}, nil,
			[]string{"d/link two.pdf"}},
		{"a tag and not another", "", []string{"type:bill"}, []string{"urgent"}, []string{"a/gas bill.pdf"}},
		{"with a name", "water*", []string{"type:"}, nil, []string{"a/water bill.pdf"}},
		{"a bare search finds bare tags only", "", []string{"bill"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Query{Name: tc.pattern}
			for _, s := range tc.tagged {
				q.Tagged = append(q.Tagged, search(s))
			}
			for _, s := range tc.lacks {
				q.NotTagged = append(q.NotTagged, search(s))
			}
			var got []string
			for _, f := range Select(fs, q, tagsOf) {
				got = append(got, f.Rel)
				if f.Inode == 0 && len(f.Tags) != len(tagsOf[f.FileID]) {
					t.Errorf("%s: %d tags, want %d", f.Rel, len(f.Tags), len(tagsOf[f.FileID]))
				}
				if f.Inode != 0 && len(f.Tags) != 2 { // the links' tags together
					t.Errorf("%s: tags %v, want the links' two", f.Rel, f.Tags)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
