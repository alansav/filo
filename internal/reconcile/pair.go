package reconcile

import (
	"cmp"
	"slices"
)

// side is one candidate for pairing. ID is the caller's index; the pairing never looks at it.
type side struct {
	ID   int
	Hash string
	Size int64
	Name string // base name
}

// pair matches gone files (olds) with new files (news) that have the same content. It never guesses: for each hash,
// exactly one candidate on each side pairs; otherwise only base names that occur exactly once on both sides pair.
// Empty files and files without a hash never pair. It returns (old ID, new ID) pairs.
func pair(olds, news []side) [][2]int {
	byHash := func(xs []side) map[string][]side {
		m := map[string][]side{}
		for _, x := range xs {
			if x.Hash != "" && x.Size > 0 {
				m[x.Hash] = append(m[x.Hash], x)
			}
		}
		return m
	}
	oldBy, newBy := byHash(olds), byHash(news)

	var out [][2]int
	for h, os := range oldBy {
		ns := newBy[h]
		if len(ns) == 0 {
			continue
		}
		if len(os) == 1 && len(ns) == 1 {
			out = append(out, [2]int{os[0].ID, ns[0].ID})
			continue
		}
		out = append(out, uniqueNames(os, ns)...)
	}
	sortPairs(out)
	return out
}

// possibleMoves matches leftovers by base name alone (any content), only where the name is unique on both sides.
// These are reported, never applied.
func possibleMoves(olds, news []side) [][2]int {
	return uniqueNames(olds, news)
}

func uniqueNames(olds, news []side) [][2]int {
	count := func(xs []side) map[string][]int {
		m := map[string][]int{}
		for _, x := range xs {
			m[x.Name] = append(m[x.Name], x.ID)
		}
		return m
	}
	oc, nc := count(olds), count(news)
	var out [][2]int
	for name, o := range oc {
		if n := nc[name]; len(o) == 1 && len(n) == 1 {
			out = append(out, [2]int{o[0], n[0]})
		}
	}
	sortPairs(out)
	return out
}

// sortPairs orders pairs by new ID so results (and event order) do not depend on map iteration.
func sortPairs(ps [][2]int) {
	slices.SortFunc(ps, func(a, b [2]int) int { return cmp.Or(cmp.Compare(a[1], b[1]), cmp.Compare(a[0], b[0])) })
}
