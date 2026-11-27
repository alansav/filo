// Package tags holds the spelling rules of tags (docs/design.md, Tags): how a typed tag is read as a key and a value,
// how a search is read, and how values compare, ignoring case. It knows nothing of the register.
package tags

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tag is one tag: a key and a value, or a bare value (Key ""). Key is lower case; Value is as spelled, trimmed, with
// each run of spaces one space.
type Tag struct {
	Key   string
	Value string
}

// String is the tag as typed back: key:value, or the bare value. Parse reads it back as the same tag.
func (t Tag) String() string {
	if t.Key == "" {
		return t.Value
	}
	return t.Key + ":" + t.Value
}

// Folded is the value in its folded form (Fold), which the register matches on.
func (t Tag) Folded() string { return Fold(t.Value) }

// Same reports whether two tags are one: the same key, and values equal ignoring case.
func (t Tag) Same(o Tag) bool { return t.Key == o.Key && t.Folded() == o.Folded() }

// Errors that say why a typed tag is refused.
var (
	ErrEmpty      = errors.New("give a tag")
	ErrNoValue    = errors.New("give a value")
	ErrSeparator  = errors.New(`a tag cannot hold ";", which separates tags in a CSV`)
	ErrWildcard   = errors.New(`a tag cannot hold "*", the wildcard of a search`)
	ErrControl    = errors.New("a tag cannot hold a control character")
	ErrNotUTF8    = errors.New("a tag must be text (valid UTF-8)")
	ErrNoWildcard = errors.New(`"*" is not taken here: give the tag as it is`)
)

// Parse reads a typed tag: the first colon separates the key when what is before it is a valid key (ValidKey, in any
// case: it is stored in lower case); otherwise the whole text is a bare value. The value is trimmed and each run of
// white space becomes one space. A key with no value, an empty tag, and a tag holding ";", "*" or a control character
// are refused.
func Parse(s string) (Tag, error) {
	t, err := split(s)
	if err != nil {
		return Tag{}, quoted(s, err)
	}
	if strings.ContainsRune(t.Value, '*') {
		return Tag{}, quoted(s, ErrWildcard)
	}
	if t.Value == "" {
		if t.Key != "" {
			return Tag{}, fmt.Errorf("%q: %w, such as %s:something (%s: alone is only a search)", s, ErrNoValue,
				t.Key, t.Key)
		}
		return Tag{}, ErrEmpty
	}
	return t, nil
}

// Search is a tag search (filo find --tag): a key and a value, where * in the value matches any run of characters;
// a key alone (AnyValue: every value of the key); or a bare value, which finds bare tags only.
type Search struct {
	Key      string
	Value    string // as typed, cleaned as a tag's value; "" with AnyValue
	AnyValue bool
}

// String is the search as typed back.
func (q Search) String() string {
	if q.AnyValue {
		return q.Key + ":"
	}
	return Tag{Key: q.Key, Value: q.Value}.String()
}

// ParseSearch reads a typed search, with the spelling rules of Parse: key: is every value of the key, and * in a
// value is a wildcard. An empty search, and one holding ";" or a control character, are refused.
func ParseSearch(s string) (Search, error) {
	t, err := split(s)
	if err != nil {
		return Search{}, quoted(s, err)
	}
	switch {
	case t.Value == "" && t.Key != "":
		return Search{Key: t.Key, AnyValue: true}, nil
	case t.Value == "":
		return Search{}, ErrEmpty
	}
	return Search{Key: t.Key, Value: t.Value}, nil
}

// ParseRemoval reads a tag to remove (filo untag): a tag, or key: for every value of the key. * is refused.
func ParseRemoval(s string) (Search, error) {
	q, err := ParseSearch(s)
	if err != nil {
		return Search{}, err
	}
	if strings.ContainsRune(q.Value, '*') {
		return Search{}, quoted(s, ErrNoWildcard)
	}
	return q, nil
}

// Matches reports whether a tag is one the search finds: the same key (bare with bare), and, unless AnyValue, a
// value matching ignoring case, with * matching any run of characters.
func (q Search) Matches(t Tag) bool {
	if q.Key != t.Key {
		return false
	}
	return q.AnyValue || Match(q.Value, t.Value)
}

// split reads the key and the cleaned value of s, refusing what no tag or search may hold. The value may be empty.
func split(s string) (Tag, error) {
	if !utf8.ValidString(s) {
		return Tag{}, ErrNotUTF8
	}
	text := clean(s)
	if strings.ContainsRune(text, ';') {
		return Tag{}, ErrSeparator
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return Tag{}, ErrControl
		}
	}
	if key, value, ok := strings.Cut(text, ":"); ok && ValidKey(key) {
		// strings.ToLower maps each letter to one letter (İ to i), so the key stays a valid key.
		return Tag{Key: strings.ToLower(key), Value: strings.TrimSpace(value)}, nil
	}
	return Tag{Value: text}, nil
}

// clean trims s and makes each run of white space one space.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// ValidKey reports whether k is a key, in any case: letters, digits, - and _ only, and at least one letter.
func ValidKey(k string) bool {
	letter := false
	for _, r := range k {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return letter
}

// quoted names the typed text in a refusal.
func quoted(s string, err error) error { return fmt.Errorf("%q: %w", s, err) }

// Fold is s with each character in one form for all its cases (Unicode simple case folding: the smallest character
// that folds with it), so two values are equal ignoring case exactly when their folded forms are equal.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

// foldRune is the smallest character in r's case-folding orbit.
func foldRune(r rune) rune {
	least := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < least {
			least = f
		}
	}
	return least
}

// Match reports whether name matches pattern ignoring case: * matches any run of characters (none included), every
// other character only itself in any case (Unicode simple case folding, so "BILL" matches "bill", as Fold compares).
// A pattern with no * matches the whole name exactly. ? and [ are ordinary characters. A byte that is not valid UTF-8
// matches only the same byte. It is the one matcher of filo find, for --name and --tag alike.
func Match(pattern, name string) bool {
	p, n := chars(pattern), chars(name)
	// The usual * backtracking: on a mismatch, let the last * take one more character.
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case pi < len(p) && sameFolded(p[pi], n[ni]):
			pi++
			ni++
		case star >= 0:
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// chars splits s into its characters, a byte that is not valid UTF-8 as its negative value minus one, so it is never
// taken for U+FFFD or for another invalid byte.
func chars(s string) []rune {
	out := make([]rune, 0, len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size <= 1 {
			r = -rune(s[0]) - 1
		}
		out = append(out, r)
		s = s[size:]
	}
	return out
}

// sameFolded reports whether a and b are the same character ignoring case: in one Unicode simple folding orbit, so
// exactly when foldRune gives them the same form. An invalid byte (negative, see chars) is only itself.
func sameFolded(a, b rune) bool {
	if a == b {
		return true
	}
	if a < 0 || b < 0 {
		return false
	}
	return foldRune(a) == foldRune(b)
}
