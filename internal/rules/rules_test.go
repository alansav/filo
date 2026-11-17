package rules

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPattern(t *testing.T) {
	tests := []struct {
		in, want string
		err      string // the refusal, "" for none
	}{
		{"", Any, ""},
		{"*", "*", ""},
		{"**", "*", ""},
		{"*Daily**Statement***", "*Daily*Statement*", ""},
		{"*?", "?*", ""},
		{"?*?", "??*", ""},
		{"*?*a?**", "?*a?*", ""},
		{"a?b", "a?b", ""},
		{"Statement [2024].pdf", "Statement [2024].pdf", ""},
		{" spaced name ", " spaced name ", ""},
		{"notes..bak", "notes..bak", ""}, // a name, not a path: .. is two dots
		{"..", "..", ""},                 // matches no file, but is not refused
		{strings.Repeat("a", 255), strings.Repeat("a", 255), ""},
		{"bank/*", "", "a pattern is a file name, without / or a backslash"},
		{`bank\*`, "", "a pattern is a file name, without / or a backslash"},
		{`\*`, "", "a pattern is a file name, without / or a backslash"},
		{"a\x00b", "", "a pattern cannot contain a NUL character"},
		{"\xff", "", "a pattern must be valid UTF-8"},
		{"C:\\x\xff", "", "a pattern is a file name, without / or a backslash"}, // the separator first
		{strings.Repeat("a", 256), "", "a pattern is at most 255 bytes"},
		{strings.Repeat("é", 128), "", "a pattern is at most 255 bytes"}, // 256 bytes, 128 characters
	}
	for _, tc := range tests {
		got, err := Pattern(tc.in)
		var gotErr string
		if err != nil {
			gotErr = err.Error()
		}
		if gotErr != tc.err || got != tc.want {
			t.Errorf("Pattern(%q) = %q, %q; want %q, %q", tc.in, got, gotErr, tc.want, tc.err)
		}
		if err != nil && !errors.Is(err, ErrPattern) {
			t.Errorf("Pattern(%q): error %v is not ErrPattern", tc.in, err)
		}
	}
}

func TestLength(t *testing.T) {
	tests := []struct {
		p    string
		want int
	}{
		{"*", 0}, {"?", 1}, {"?*", 1}, {"??*", 2}, {"a", 1}, {"*2024*", 4}, {"*bank*", 4}, {"*DailyStatement*", 14},
		{"*a*b*c*", 3}, {"abc*", 3}, {"Statement ?.pdf", 15}, {"IMG_????*", 8}, {"*é*", 1},
	}
	for _, tc := range tests {
		if got := Length(tc.p); got != tc.want {
			t.Errorf("Length(%q) = %d, want %d", tc.p, got, tc.want)
		}
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything.pdf", true},
		{"*", "", true},
		{"*Statement*", "QuarterlyStatement-Q1.pdf", true},
		{"*statement*", "QuarterlyStatement-Q1.pdf", true},
		{"*STATEMENT*", "quarterlystatement.pdf", true},
		{"*Statement*", "Statement", true},
		{"*Statement*", "Statment.pdf", false},
		{"Daily*", "DailyStatement.pdf", true},
		{"Daily*", "MyDailyStatement.pdf", false},
		{"*.pdf", "a.pdf", true},
		{"*.pdf", "a.pdf.txt", false},
		{"?.pdf", "a.pdf", true},
		{"?.pdf", "ab.pdf", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*a*b*c*", "xxaxxbxxcxx", true},
		{"*a*b*c*", "cba", false},
		{"[2024]*", "[2024] tax.pdf", true}, // [ is a character, not a class
		{"[2024]*", "2 tax.pdf", false},
		{"*\\*", "a\\", true}, // \ is a character, not an escape
		{"a*", "a*b", true},
		{"*é*", "CAFÉ.txt", true},
		{"photo", "photo", true},
		{"photo", "photo.jpg", false},
	}
	for _, tc := range tests {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// The rules of the design's example (docs/design.md, Lifespan, Directory rules, Matching).
var example = []Rule{
	{ID: 1, Dir: "bank", Pattern: "*DailyStatement*", Policy: "daily-statements"},
	{ID: 2, Dir: "bank", Pattern: "*Statement*", Policy: "statements"},
	{ID: 3, Dir: "bank", Pattern: "*2024*", Policy: "tax-2024"},
	{ID: 4, Dir: "bank", Pattern: "*bank*", Policy: "banking"},
	{ID: 5, Dir: "bank", Pattern: "*", Policy: "financial"},
	{ID: 6, Dir: "bank/2024", Pattern: "*", Policy: "tax-2024"},
	{ID: 7, Dir: "", Pattern: "*.tmp", Policy: "scratch"},
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name, rel string
		want      int64   // the rule that decides, 0 for none
		ties      []int64 // the tie, if any
	}{
		{"the longest match", "bank/DailyStatement-03.pdf", 1, nil},
		{"one pattern", "bank/QuarterlyStatement-Q1.pdf", 2, nil},
		{"two patterns as long: a tie", "bank/bank-2024.pdf", 0, []int64{3, 4}},
		{"* is the fallback", "bank/letter.pdf", 5, nil},
		{"a parent's rules at any depth", "bank/old/deep/letter.pdf", 5, nil},
		{"the nearest directory wins over a longer pattern above", "bank/2024/DailyStatement-01.pdf", 6, nil},
		{"the root's rule, at any depth", "notes/a.tmp", 7, nil},
		{"the root's rule is below every other directory's", "bank/x.tmp", 5, nil},
		{"no rule", "notes/a.txt", 0, nil},
		{"a file in the root", "a.tmp", 7, nil},
		{"a directory named like a pattern is not matched", "bank-2024/a.txt", 0, nil},
		{"bank-x is not inside bank", "bank-x/letter.pdf", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := Resolve(example, tc.rel)
			var ties []int64
			for _, r := range res.Ties {
				ties = append(ties, r.ID)
			}
			if res.Rule.ID != tc.want || !slices.Equal(ties, tc.ties) || res.Found != (tc.want != 0 || tc.ties != nil) {
				t.Errorf("Resolve(%q) = rule %d, ties %v, found %v; want %d, %v", tc.rel, res.Rule.ID, ties, res.Found,
					tc.want, tc.ties)
			}
		})
	}
}

// A ? counts as the one character it matches: ?* is tried before *, and a? before *a*.
func TestResolveQuestionMark(t *testing.T) {
	rs := []Rule{
		{ID: 1, Dir: "scans", Pattern: "*"},
		{ID: 2, Dir: "scans", Pattern: "?*"},
		{ID: 3, Dir: "scans", Pattern: "*a*"},
		{ID: 4, Dir: "scans", Pattern: "a?"},
		{ID: 5, Dir: "scans", Pattern: "*b*"},
	}
	tests := []struct {
		rel  string
		want int64
		ties []int64
	}{
		{"scans/x.pdf", 2, nil},               // ?* (1) over * (0)
		{"scans/ab", 4, nil},                  // a? (2) over *a* and *b* (1 each)
		{"scans/ba.pdf", 0, []int64{3, 5, 2}}, // *a*, *b* and ?* all count 1 (sorted by pattern)
	}
	for _, tc := range tests {
		res := Resolve(rs, tc.rel)
		var ties []int64
		for _, r := range res.Ties {
			ties = append(ties, r.ID)
		}
		if res.Rule.ID != tc.want || !slices.Equal(ties, tc.ties) {
			t.Errorf("Resolve(%q) = rule %d, ties %v; want %d, %v", tc.rel, res.Rule.ID, ties, tc.want, tc.ties)
		}
	}
}

func TestCompare(t *testing.T) {
	rs := []Rule{
		{Dir: "bank-x", Pattern: "*"},
		{Dir: "bank/2024", Pattern: "*"},
		{Dir: "bank", Pattern: "*"},
		{Dir: "bank", Pattern: "*bank*"},
		{Dir: "bank", Pattern: "*2024*"},
		{Dir: "bank", Pattern: "a"},
		{Dir: "bank", Pattern: "?*"},
		{Dir: "bank", Pattern: "*DailyStatement*"},
		{Dir: "", Pattern: "*.tmp"},
	}
	slices.SortFunc(rs, Compare)
	var got []string
	for _, r := range rs {
		got = append(got, r.Dir+" "+r.Pattern)
	}
	// A parent before the directories inside it, then the order rules are tried: longest first, a pattern ?* or a
	// (1 each, then by pattern) above * (0).
	want := []string{" *.tmp", "bank *DailyStatement*", "bank *2024*", "bank *bank*", "bank ?*", "bank a", "bank *",
		"bank/2024 *", "bank-x *"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted:\n%q\nwant\n%q", got, want)
	}
}
