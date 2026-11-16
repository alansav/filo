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

// Pattern checks a typed pattern and returns it as stored: "" is Any, and a run of * is one * (they match the same
// names, and as two patterns of one directory with no letters they would always tie). The rest is kept as typed; the
// case is ignored when matching and comparing.
func Pattern(p string) (string, error) {
	if p == "" {
		return Any, nil
	}
	if strings.ContainsAny(p, "/\x00") || !utf8.ValidString(p) || len(p) > maxPattern {
		return "", ErrPattern
	}
	for strings.Contains(p, "**") {
		p = strings.ReplaceAll(p, "**", "*")
	}
	return p, nil
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
	p, s := []rune(Key(pattern)), []rune(strings.ToLower(name))
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

// Resolve works out the rule for the file at rel ('/'-separated, relative to the root) from the rules of its root.
func Resolve(rs []Rule, rel string) Result {
	byDir := map[string][]Rule{}
	for _, r := range rs {
		byDir[r.Dir] = append(byDir[r.Dir], r)
	}
	name := path.Base(rel)
	for dir := parent(rel); ; dir = parent(dir) {
		if res := resolveIn(byDir[dir], name); res.Found {
			return res
		}
		if dir == "" {
			return Result{}
		}
	}
}

// resolveIn matches name against one directory's rules.
func resolveIn(rs []Rule, name string) Result {
	best := -1
	var matched []Rule
	for _, r := range rs {
		if !Match(r.Pattern, name) {
			continue
		}
		switch n := Letters(r.Pattern); {
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
