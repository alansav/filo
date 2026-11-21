package find

import (
	"slices"
	"testing"

	"github.com/alansav/filo/internal/store"
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
	} {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestSelect(t *testing.T) {
	f := func(root int64, rel string, ino int64) store.FoundFile {
		return store.FoundFile{RootID: root, Root: "/r", Rel: rel, Inode: ino}
	}
	fs := []store.FoundFile{
		f(1, "a/bill.pdf", 10),
		f(1, "a/other.txt", 0),
		f(1, "b/copy of bill.pdf", 10), // a hard link of a/bill.pdf
		f(1, "c/link.txt", 20),         // a hard link whose other path does not match *bill*
		f(1, "d/bill-link.pdf", 20),
		f(2, "x/bill.pdf", 10), // the same inode in another root: another file
		f(1, "e/unknown.pdf", 0),
		f(1, "f/unknown.pdf", 0), // no inode: never merged
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
			{"e/unknown.pdf", 0}, {"f/unknown.pdf", 0}}},
		{"*bill*", []row{{"a/bill.pdf", 1}, {"d/bill-link.pdf", 1}, {"x/bill.pdf", 0}}},
		{"copy*", []row{{"b/copy of bill.pdf", 1}}},
		{"*.pdf", []row{{"a/bill.pdf", 1}, {"d/bill-link.pdf", 1}, {"x/bill.pdf", 0}, {"e/unknown.pdf", 0},
			{"f/unknown.pdf", 0}}},
		{"a", nil}, // the base name only, never the directory
	} {
		var got []row
		for _, s := range Select(fs, tc.pattern) {
			got = append(got, row{s.Rel, s.Links})
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Select(%q) = %v\nwant %v", tc.pattern, got, tc.want)
		}
	}
}
