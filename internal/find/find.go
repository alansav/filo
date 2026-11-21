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
// matches the whole name exactly. ? and [ are ordinary characters.
func Match(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
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

// sameFolded reports whether a and b are the same character ignoring case: equal under Unicode simple folding.
func sameFolded(a, b rune) bool {
	if a == b {
		return true
	}
	if a == utf8.RuneError || b == utf8.RuneError {
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
// links, in the same root), which are not listed.
type File struct {
	store.FoundFile
	Links int
}

// Select returns the files among fs (one row per path, as store.FindFiles gives them, in its order) whose base name
// matches pattern ("" matches every name), in the same order. Paths with the same inode in the same root are one
// file: it is listed once, at its first path that matches, if any does, and its other paths are counted as Links.
func Select(fs []store.FoundFile, pattern string) []File {
	type key struct{ root, ino int64 }
	paths := map[key]int{}
	for _, f := range fs {
		if f.Inode != 0 {
			paths[key{f.RootID, f.Inode}]++
		}
	}
	listed := map[key]bool{}
	var out []File
	for _, f := range fs {
		if pattern != "" && !Match(pattern, path.Base(f.Rel)) {
			continue
		}
		file := File{FoundFile: f}
		if f.Inode != 0 {
			k := key{f.RootID, f.Inode}
			if listed[k] {
				continue
			}
			listed[k] = true
			file.Links = paths[k] - 1
		}
		out = append(out, file)
	}
	return out
}
