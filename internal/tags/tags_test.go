package tags

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want Tag
		err  error
	}{
		{"type:bill", Tag{"type", "bill"}, nil},
		{"Sender: British  Gas ", Tag{"sender", "British Gas"}, nil},
		{"  sender:British\tGas", Tag{"sender", "British Gas"}, nil},
		{"project:trip: Paris 2027", Tag{"project", "trip: Paris 2027"}, nil},
		{"10:30 call", Tag{"", "10:30 call"}, nil},
		{"re: invoice", Tag{"re", "invoice"}, nil},
		{"urgent", Tag{"", "urgent"}, nil},
		{"urgent?", Tag{"", "urgent?"}, nil},
		{"Q1 [draft]", Tag{"", "Q1 [draft]"}, nil},
		{"a-b_2:x", Tag{"a-b_2", "x"}, nil},
		{"Société:Générale", Tag{"société", "Générale"}, nil},
		{"my key:x", Tag{"", "my key:x"}, nil},       // a space: not a key
		{"2026:report", Tag{"", "2026:report"}, nil}, // no letter: not a key
		{":x", Tag{"", ":x"}, nil},                   // nothing before the colon
		{"a.b:x", Tag{"", "a.b:x"}, nil},             // a dot: not a key
		{"sender:", Tag{}, ErrNoValue},
		{"sender:   ", Tag{}, ErrNoValue},
		{"", Tag{}, ErrEmpty},
		{"   ", Tag{}, ErrEmpty},
		{"type:bill;receipt", Tag{}, ErrSeparator},
		{"*gas*", Tag{}, ErrWildcard},
		{"sender:*", Tag{}, ErrWildcard},
		{"a\x00b", Tag{}, ErrControl},
		{"a\x1bb", Tag{}, ErrControl},
		{"a\xffb", Tag{}, ErrNotUTF8},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("Parse(%q) error = %v, want %v", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
		// What String writes back is read as the same tag.
		if again, err := Parse(got.String()); err != nil || again != got {
			t.Errorf("Parse(%q).String() = %q reads back as %+v, %v", tt.in, got.String(), again, err)
		}
	}
}

func TestParseSearch(t *testing.T) {
	tests := []struct {
		in   string
		want Search
		err  error
	}{
		{"sender:", Search{Key: "sender", AnyValue: true}, nil},
		{"Sender:", Search{Key: "sender", AnyValue: true}, nil},
		{"sender:*gas*", Search{Key: "sender", Value: "*gas*"}, nil},
		{"sender:British  Gas", Search{Key: "sender", Value: "British Gas"}, nil},
		{"urgent", Search{Value: "urgent"}, nil},
		{"*", Search{Value: "*"}, nil},
		{"", Search{}, ErrEmpty},
		{"a;b", Search{}, ErrSeparator},
	}
	for _, tt := range tests {
		got, err := ParseSearch(tt.in)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("ParseSearch(%q) error = %v, want %v", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseSearch(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
	if _, err := ParseRemoval("sender:*"); !errors.Is(err, ErrNoWildcard) {
		t.Errorf("ParseRemoval(sender:*) error = %v, want ErrNoWildcard", err)
	}
	if q, err := ParseRemoval("sender:"); err != nil || !q.AnyValue {
		t.Errorf("ParseRemoval(sender:) = %+v, %v", q, err)
	}
}

func TestMatches(t *testing.T) {
	tests := []struct {
		search string
		tag    Tag
		want   bool
	}{
		{"sender:british gas", Tag{"sender", "British Gas"}, true},
		{"sender:BRITISH GAS", Tag{"sender", "British Gas"}, true},
		{"sender:", Tag{"sender", "British Gas"}, true},
		{"sender:*gas*", Tag{"sender", "British Gas"}, true},
		{"sender:*gas", Tag{"sender", "British Gas Ltd"}, false},
		{"sender:gas", Tag{"sender", "British Gas"}, false},
		{"type:", Tag{"sender", "British Gas"}, false},
		{"British Gas", Tag{"sender", "British Gas"}, false}, // a bare search finds bare tags only
		{"urgent", Tag{"", "Urgent"}, true},
		{"urg*", Tag{"", "urgent"}, true},
		{"urgent?", Tag{"", "urgent"}, false}, // ? is an ordinary character
		{"q1 [draft]", Tag{"", "Q1 [draft]"}, true},
		{"*", Tag{"", "anything"}, true},
		{"*", Tag{"type", "bill"}, false},
		{"type:straße", Tag{"type", "STRASSE"}, false}, // simple folding: ß is not ss
		{"x:k", Tag{"x", "K"}, true},                   // the Kelvin sign folds with k
	}
	for _, tt := range tests {
		q, err := ParseSearch(tt.search)
		if err != nil {
			t.Fatalf("ParseSearch(%q): %v", tt.search, err)
		}
		if got := q.Matches(tt.tag); got != tt.want {
			t.Errorf("%q matches %+v = %v, want %v", tt.search, tt.tag, got, tt.want)
		}
	}
}

func TestFoldAndSame(t *testing.T) {
	if Fold("British Gas") != Fold("bRITISH gAS") {
		t.Error("Fold differs by case")
	}
	if Fold("Gas") == Fold("Gaz") {
		t.Error("Fold made different values equal")
	}
	if !(Tag{"sender", "British Gas"}).Same(Tag{"sender", "british gas"}) {
		t.Error("Same ignores case of the value")
	}
	if (Tag{"sender", "x"}).Same(Tag{"", "x"}) {
		t.Error("a bare tag is not the same as a keyed one")
	}
}
