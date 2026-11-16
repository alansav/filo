// Package rules works out which directory rule names a policy for a file (docs/design.md, Lifespan, Directory
// rules). A rule is a directory and a pattern on the file's name; for a file, the rules of its own directory are
// tried first, then those of each parent up to the root, and the first directory with a rule that matches decides.
// Within a directory the pattern with the most letters wins (wildcards are not counted), and two matching patterns
// with as many letters are a tie: reported, never guessed. Nothing here reads the disk or the register.
package rules

import (
	"cmp"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// Any is the pattern that matches every name: a rule given no pattern has it.
const Any = "*"

// ErrPattern means a pattern that cannot be a rule's.
var ErrPattern = errors.New("a pattern is a file name with * and ?, without /")

// maxPattern is the longest pattern, in bytes: the longest file name most filesystems allow.
const maxPattern = 255

// Pattern checks a typed pattern and returns it as stored: "" is Any, and each run of wildcards in one form, its ?
// first and then one * if it has any (`**` is `*`, `*?` and `?*` are `?*`): they match the same names, and as two
// patterns of one directory with as many letters they would always tie. The rest is kept as typed; the case is
// ignored when matching and comparing.
func Pattern(p string) (string, error) {
	if p == "" {
		return Any, nil
	}
	if strings.ContainsAny(p, "/\x00") || !utf8.ValidString(p) || len(p) > maxPattern {
		return "", ErrPattern
	}
	var b strings.Builder
	q, star := 0, false // the run of wildcards being read: how many ?, and whether it has a *
	flush := func() {
		b.WriteString(strings.Repeat("?", q))
		if star {
			b.WriteByte('*')
		}
		q, star = 0, false
	}
	for _, r := range p {
		switch r {
		case '?':
			q++
		case '*':
			star = true
		default:
			flush()
			b.WriteRune(r)
		}
	}
	flush()
	return b.String(), nil
}

// Key is a pattern as compared: in lower case, so `*statement*` and `*Statement*` are the same pattern.
func Key(p string) string { return strings.ToLower(p) }

// Letters counts a pattern's characters that are not wildcards: the longer pattern is tried first.
func Letters(p string) int {
	n := 0
	for _, r := range p {
		if r != '*' && r != '?' {
			n++
		}
	}
	return n
}

// Match reports whether name matches pattern, in any case: * is any run of characters, ? one character, and every
// other character only itself (a [ in a name is a [, not a class).
func Match(pattern, name string) bool {
	return match([]rune(Key(pattern)), []rune(strings.ToLower(name)))
}

// match is Match on a pattern and a name already in lower case, as runes.
func match(p, s []rune) bool {
	pi, si := 0, 0
	star, mark := -1, 0 // the last * seen, and where in s it started matching
	for si < len(s) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case star >= 0:
			pi, mark = star+1, mark+1
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// Rule is a rule as matching needs it.
type Rule struct {
	ID      int64
	Dir     string // relative to its root, '/'-separated; "" for the root itself
	Pattern string // as stored (Pattern)
	Policy  string
}

// Compare orders rules as they are listed: by directory, a parent before the directories inside it, then within a
// directory in the order they are tried: most letters first, then by pattern.
func Compare(a, b Rule) int {
	if c := slices.Compare(dirParts(a.Dir), dirParts(b.Dir)); c != 0 {
		return c
	}
	if c := cmp.Compare(Letters(b.Pattern), Letters(a.Pattern)); c != 0 {
		return c
	}
	return cmp.Compare(Key(a.Pattern), Key(b.Pattern))
}

func dirParts(dir string) []string {
	if dir == "" {
		return nil
	}
	return strings.Split(dir, "/")
}

// Result is what the rules decide for one file.
type Result struct {
	Rule Rule   // the rule that decides, when Ties is empty and Found
	Ties []Rule // two or more matching rules of one directory with as many letters: nothing is decided
	// Found is true when a directory's rules matched the file: Rule decides, or Ties says why nothing does.
	Found bool
}

// Set is the rules of one root, indexed by directory with their patterns prepared, to resolve many files against.
type Set struct {
	byDir map[string][]prepared
}

// prepared is a rule with its pattern in lower case, as runes, and its letter count, worked out once.
type prepared struct {
	rule    Rule
	pattern []rune
	letters int
}

// NewSet indexes the rules of one root.
func NewSet(rs []Rule) Set {
	s := Set{byDir: map[string][]prepared{}}
	for _, r := range rs {
		s.byDir[r.Dir] = append(s.byDir[r.Dir], prepared{rule: r, pattern: []rune(Key(r.Pattern)),
			letters: Letters(r.Pattern)})
	}
	return s
}

// Resolve works out the rule for the file at rel ('/'-separated, relative to the root) from the rules of its root.
// To resolve many files, build a Set once and use its Resolve.
func Resolve(rs []Rule, rel string) Result { return NewSet(rs).Resolve(rel) }

// Resolve works out the rule for the file at rel ('/'-separated, relative to the root).
func (s Set) Resolve(rel string) Result {
	name := []rune(strings.ToLower(path.Base(rel)))
	for dir := parent(rel); ; dir = parent(dir) {
		if res := resolveIn(s.byDir[dir], name); res.Found {
			return res
		}
		if dir == "" {
			return Result{}
		}
	}
}

// resolveIn matches name, in lower case, against one directory's rules.
func resolveIn(ps []prepared, name []rune) Result {
	best := -1
	var matched []Rule
	for _, p := range ps {
		if !match(p.pattern, name) {
			continue
		}
		r := p.rule
		switch n := p.letters; {
		case n > best:
			best, matched = n, []Rule{r}
		case n == best:
			matched = append(matched, r)
		}
	}
	switch len(matched) {
	case 0:
		return Result{}
	case 1:
		return Result{Rule: matched[0], Found: true}
	}
	slices.SortFunc(matched, Compare)
	return Result{Ties: matched, Found: true}
}

// parent is the directory holding rel, "" for the root; the parent of "" is "".
func parent(rel string) string {
	// Not strings.CutLast: with no / it returns rel itself, and Resolve would never reach the root.
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return ""
	}
	return rel[:i]
}
