// Package find selects the files filo find lists (docs/design.md, Finding files): by base name, with * the only
// wildcard and case ignored, and with a file's hard links listed once.
package find

import (
	"path"
	"unicode"
	"unicode/utf8"

	"github.com/alansav/filo/internal/store"
)

// Match reports whether name matches pattern: * matches any run of characters (none included), every other
// character only itself, ignoring case (Unicode simple case folding, so "BILL" matches "bill"). A pattern with no *
// matches the whole name exactly. ? and [ are ordinary characters. A byte that is not valid UTF-8 matches only the
// same byte.
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

// sameFolded reports whether a and b are the same character ignoring case: equal under Unicode simple folding. An
// invalid byte (negative, see chars) is only itself.
func sameFolded(a, b rune) bool {
	if a == b {
		return true
	}
	if a < 0 || b < 0 {
		return false
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// File is one file filo find lists: at the first of its paths that matched, with how many other paths it has (hard
// links in the same root, store.FoundFile's Links), which are not listed.
type File struct {
	store.FoundFile
}

// Select returns the files among fs (one row per path, as store.FindFiles gives them, in its order) whose base name
// matches pattern ("" matches every name), in the same order. Paths that are one file, hard links (the same inode,
// size and modified time in the same root, as store.FindFiles counts them), are listed once, at the first that
// matches, if any does. A stale inode reused by a file of another size or time is another file, listed too.
func Select(fs []store.FoundFile, pattern string) []File {
	type key struct{ root, ino, size, mtime int64 }
	listed := map[key]bool{}
	var out []File
	for _, f := range fs {
		if pattern != "" && !Match(pattern, path.Base(f.Rel)) {
			continue
		}
		if f.Inode != 0 && f.Links > 0 {
			k := key{f.RootID, f.Inode, f.Size, f.MtimeNS}
			if listed[k] {
				continue
			}
			listed[k] = true
		}
		out = append(out, File{FoundFile: f})
	}
	return out
}
