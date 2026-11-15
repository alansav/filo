package view

// Copy is one copy in a duplicate group: where it is, a note after its path (its retention), and the symlinks
// that point at it and would break if it were deleted (absolute).
type Copy struct {
	Root, Rel string
	Note      string
	Symlinks  []string
}

// DupGroup is a duplicate group as shown before deleting: its name and size, the copies kept and the copies to
// delete.
type DupGroup struct {
	Name   string // the content's file name
	Each   string // its size, such as "8 MB"
	Keep   []Copy
	Delete []Copy
	NoKeep string // shown in place of the keep lines when no copy is kept
}

// Dup writes a duplicate group: "contract.pdf · 8 MB each", then a keep and a delete line per copy. Paths are
// relative to the heading's root when every copy is in it, and in full otherwise.
func (r *Renderer) Dup(g DupGroup) {
	r.write(spaces(4), plain(g.Name+r.sep()+g.Each+" each"))
	inRoot := r.root != ""
	for _, c := range append(append([]Copy{}, g.Keep...), g.Delete...) {
		inRoot = inRoot && c.Root == r.root
	}
	path := func(c Copy) string {
		if inRoot {
			return relName(c.Rel)
		}
		return r.Full(c.Root, c.Rel)
	}
	if len(g.Keep) == 0 && g.NoKeep != "" {
		r.write(spaces(6), styled("keep  ", green), spaces(3), styled(g.NoKeep, yellow))
	}
	line := func(word, color string, c Copy) {
		spans := []span{spaces(6), styled(word, color), spaces(9 - len(word)), name(path(c))}
		if c.Note != "" {
			spans = append(spans, spaces(3), styled(c.Note, dim))
		}
		r.write(spans...)
		for _, l := range c.Symlinks {
			r.write(spaces(15), plain(r.arrow()+" the symlink "), name(r.Tilde(l)), plain(" points here and would break"))
		}
	}
	for _, c := range g.Keep {
		line("keep", green, c)
	}
	for _, c := range g.Delete {
		line("delete", red, c)
	}
}

// Dups writes duplicate groups, at most max of them (0 = all), then "… N more groups".
func (r *Renderer) Dups(groups []DupGroup, max int) {
	shown := groups
	if max > 0 && len(groups) > max {
		shown = groups[:max]
	}
	for _, g := range shown {
		r.Dup(g)
	}
	if more := len(groups) - len(shown); more > 0 {
		r.more(more, "group", "groups")
	}
}

// arrow is "↳" in colour, "->" plain.
func (r *Renderer) arrow() string { return r.pick("↳", "->") }
