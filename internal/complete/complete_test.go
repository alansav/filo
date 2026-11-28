package complete

import (
	"reflect"
	"testing"
)

// TestParse: the line split as the shell splits it, and the part of the argument before bash's word, which bash
// splits at ':' and '=' and which starts after a quote still open.
func TestParse(t *testing.T) {
	for _, tc := range []struct {
		line, word string
		want       Line
	}{
		{"filo ", "", Line{Args: []string{"filo"}, OK: true}},
		{"filo sh", "sh", Line{Args: []string{"filo"}, Current: "sh", OK: true}},
		{"filo  tag  a.txt sen", "sen", Line{Args: []string{"filo", "tag", "a.txt"}, Current: "sen", OK: true}},
		// bash's word starts after the colon: the key comes before it.
		{"filo tag a sender:Bri", "Bri", Line{Args: []string{"filo", "tag", "a"}, Current: "sender:Bri",
			Before: "sender:", OK: true}},
		// Backslashes and quotes, closed and open.
		{`filo show My\ Doc`, `My\ Doc`, Line{Args: []string{"filo", "show"}, Current: "My Doc", OK: true}},
		{`filo tag 'a b' "sender:British G`, "sender:British G", Line{Args: []string{"filo", "tag", "a b"},
			Current: "sender:British G", Quote: '"', OK: true}},
		{`filo tag x 'it''s`, "s", Line{Args: []string{"filo", "tag", "x"}, Current: "its", Before: "it",
			Quote: '\'', OK: true}}, // 'it' closes, then 's opens a quote: bash's word starts after it
		{`filo tag x "a\"b`, `a\"b`, Line{Args: []string{"filo", "tag", "x"}, Current: `a"b`, Quote: '"',
			OK: true}},
		{`filo find --name=bi`, "bi", Line{Args: []string{"filo", "find"}, Current: "--name=bi", Before: "--name=",
			OK: true}},
		// A word bash gives that is not the end of the line: nothing to offer.
		{"filo sh", "zz", Line{Args: []string{"filo"}, Current: "sh"}},
	} {
		if got := Parse(tc.line, tc.word); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q, %q) =\n%+v\nwant\n%+v", tc.line, tc.word, got, tc.want)
		}
	}
}

// TestInsert: a candidate quoted for where it goes, the part bash does not replace left off, a finished word closed
// with its quote and a space, and a candidate that does not fit refused.
func TestInsert(t *testing.T) {
	for _, tc := range []struct {
		line, word, candidate string
		done                  bool
		want                  string
		ok                    bool
	}{
		{"filo tag a sen", "sen", "sender:", false, "sender:", true},
		{"filo tag a sender:bri", "bri", "sender:British Gas", true, `British\ Gas `, true},
		{`filo tag a "sender:bri`, "sender:bri", "sender:British Gas", true, `sender:British Gas" `, true},
		{`filo tag a 'sender:bri`, "sender:bri", "sender:Bob's", true, `sender:Bob'\''s' `, true},
		{`filo tag a "x`, "x", `x"$y`, true, `x\"\$y" `, true},
		{"filo tag a ur", "ur", "urgent", true, "urgent ", true},
		{"filo show ", "", "~/notes (old)/", false, `~/notes\ \(old\)/`, true},
		{"filo show ", "", "~x", true, `\~x `, true},
		{"filo show a/", "a/", "a/#1 & co", true, `a/#1\ \&\ co `, true},
		{"filo tag a sender:bri", "bri", "type:bill", true, "", false},
		{"filo tag a SENDER:", "", "sender:British Gas", true, `British\ Gas `, true},
		{`filo tag a "note:w`, "note:w", "note:wow!now", true, "", false},
		{"filo tag a note:w", "w", "note:wow!now", true, `wow\!now `, true},
	} {
		l := Parse(tc.line, tc.word)
		got, ok := l.Insert(tc.candidate, tc.done)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Insert(%q after %q) = %q, %v; want %q, %v", tc.candidate, tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

// TestRest: for bash to quote itself, the candidate after the part bash does not replace, unquoted.
func TestRest(t *testing.T) {
	l := Parse(`filo show 'My Do`, "My Do")
	if got, ok := l.Rest("My Documents/"); !ok || got != "My Documents/" {
		t.Errorf("Rest = %q, %v", got, ok)
	}
	l = Parse("filo show a:b", "b")
	if got, ok := l.Rest("a:bc.txt"); !ok || got != "bc.txt" {
		t.Errorf("Rest after a colon = %q, %v", got, ok)
	}
	if _, ok := l.Rest("x.txt"); ok {
		t.Error("Rest of a candidate that does not fit")
	}
}

func TestTypable(t *testing.T) {
	for s, want := range map[string]bool{"a b.txt": true, "café": true, "a\nb": false, "a\x1b[2J": false, "\t": false} {
		if Typable(s) != want {
			t.Errorf("Typable(%q) = %v", s, !want)
		}
	}
}
