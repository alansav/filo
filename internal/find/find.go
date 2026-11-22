// Package find selects the files filo find lists (docs/design.md, Finding files): by base name, with * the only
// wildcard and case ignored, and with a file's hard links listed once.
package find

import (
	"cmp"
	"path"
	"slices"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Match reports whether name matches pattern: * matches any run of characters (none included), every other
// character only itself, ignoring case (Unicode simple case folding, so "BILL" matches "bill"). A pattern with no *
// matches the whole name exactly. ? and [ are ordinary characters. A byte that is not valid UTF-8 matches only the
// same byte. It is tags.Match, so --name and --tag match alike.
func Match(pattern, name string) bool { return tags.Match(pattern, name) }

// File is one file filo find lists: at the first of its paths that matched, with how many other paths it has (hard
// links in the same root, store.FoundFile's Links), which are not listed, and its tags.
type File struct {
	store.FoundFile
	Tags []tags.Tag
}

// Query is what filo find looks for: every condition must hold (docs/design.md, Finding files and Tags).
type Query struct {
	Name      string        // the base name, with * the one wildcard; "" matches every name
	Tagged    []tags.Search // each must find one of the file's tags
	NotTagged []tags.Search // none may find one
}

// Matches reports whether a file with this base name and these tags is one q finds.
func (q Query) Matches(name string, ts []tags.Tag) bool {
	if q.Name != "" && !Match(q.Name, name) {
		return false
	}
	for _, s := range q.Tagged {
		if !slices.ContainsFunc(ts, s.Matches) {
			return false
		}
	}
	for _, s := range q.NotTagged {
		if slices.ContainsFunc(ts, s.Matches) {
			return false
		}
	}
	return true
}

// Select returns the files among fs (one row per path, as store.FindFiles gives them, in its order) that q finds,
// with tagsOf their tags by file id, in the same order. Paths that are one file, hard links (the same inode, size and
// modified time in the same root, as store.FindFiles counts them), are listed once, at the first whose name matches,
// if any does. The register keeps each link as a file with tags of its own, so a file's tags are those of all its
// links: tagging it through one link tags the file. A stale inode reused by a file of another size or time is another
// file, listed too.
func Select(fs []store.FoundFile, q Query, tagsOf map[int64][]tags.Tag) []File {
	type key struct{ root, ino, size, mtime int64 }
	keyOf := func(f store.FoundFile) (key, bool) {
		return key{f.RootID, f.Inode, f.Size, f.MtimeNS}, f.Inode != 0 && f.Links > 0
	}
	linked := map[key][]tags.Tag{} // the tags of every link of a file, each once
	for _, f := range fs {
		if k, ok := keyOf(f); ok {
			for _, t := range tagsOf[f.FileID] {
				if !slices.ContainsFunc(linked[k], t.Same) {
					linked[k] = append(linked[k], t)
				}
			}
		}
	}
	for _, ts := range linked {
		slices.SortFunc(ts, func(a, b tags.Tag) int {
			return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Folded(), b.Folded()))
		})
	}
	listed := map[key]bool{}
	var out []File
	for _, f := range fs {
		ts := tagsOf[f.FileID]
		k, isLinked := keyOf(f)
		if isLinked {
			ts = linked[k]
		}
		if !q.Matches(path.Base(f.Rel), ts) {
			continue
		}
		if isLinked {
			if listed[k] {
				continue
			}
			listed[k] = true
		}
		out = append(out, File{FoundFile: f, Tags: ts})
	}
	return out
}
