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
		err      bool
	}{
		{"", Any, false},
		{"*", "*", false},
		{"**", "*", false},
		{"*Daily**Statement***", "*Daily*Statement*", false},
		{"Statement [2024].pdf", "Statement [2024].pdf", false},
		{" spaced name ", " spaced name ", false},
		{"bank/*", "", true},
		{"a\x00b", "", true},
		{"\xff", "", true},
		{strings.Repeat("a", 256), "", true},
	}
	for _, tc := range tests {
		got, err := Pattern(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("Pattern(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.err)
		}
		if err != nil && !errors.Is(err, ErrPattern) {
			t.Errorf("Pattern(%q): error %v is not ErrPattern", tc.in, err)
		}
	}
}

func TestLetters(t *testing.T) {
	tests := []struct {
		p    string
		want int
	}{
		{"*", 0}, {"?", 0}, {"a", 1}, {"*2024*", 4}, {"*bank*", 4}, {"*DailyStatement*", 14}, {"*a*b*c*", 3},
		{"abc*", 3}, {"Statement ?.pdf", 14}, {"*é*", 1},
	}
	for _, tc := range tests {
		if got := Letters(tc.p); got != tc.want {
			t.Errorf("Letters(%q) = %d, want %d", tc.p, got, tc.want)
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
		{"two patterns with as many letters: a tie", "bank/bank-2024.pdf", 0, []int64{3, 4}},
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

func TestCompare(t *testing.T) {
	rs := []Rule{
		{Dir: "bank-x", Pattern: "*"},
		{Dir: "bank/2024", Pattern: "*"},
		{Dir: "bank", Pattern: "*"},
		{Dir: "bank", Pattern: "*bank*"},
		{Dir: "bank", Pattern: "*2024*"},
		{Dir: "bank", Pattern: "a"},
		{Dir: "bank", Pattern: "*DailyStatement*"},
		{Dir: "", Pattern: "*.tmp"},
	}
	slices.SortFunc(rs, Compare)
	var got []string
	for _, r := range rs {
		got = append(got, r.Dir+" "+r.Pattern)
	}
	// A parent before the directories inside it, then the order rules are tried: most letters first, a pattern a
	// (1 letter) above * (0).
	want := []string{" *.tmp", "bank *DailyStatement*", "bank *2024*", "bank *bank*", "bank a", "bank *",
		"bank/2024 *", "bank-x *"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted:\n%q\nwant\n%q", got, want)
	}
}
