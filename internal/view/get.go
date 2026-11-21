package view

import "strings"

// Line writes one value on a line of its own, escaped: what filo get prints for one field of one file.
func (r *Renderer) Line(text string) { r.write(plain(text)) }

// Pairs writes names and values, the values lined up after the longest name: "retention   7y". Piped, a tab
// separates each name from its value instead, for cut -f2.
func (r *Renderer) Pairs(pairs [][2]string) {
	w := 0
	for _, p := range pairs {
		w = max(w, width(plain(p[0])))
	}
	for _, p := range pairs {
		if !r.o.Terminal {
			r.out(Escape(p[0]) + "\t" + Escape(p[1]) + "\n")
			continue
		}
		r.write(plain(p[0]), spaces(w-width(plain(p[0]))+3), plain(p[1]))
	}
}

// Record writes one record for a pipe: its fields joined by tabs, each escaped as everywhere, ended by a newline. With
// nul it is ended by a NUL instead and its fields are written as they are, unescaped, for xargs -0 and tar --null to
// get any file name exactly; a NUL cannot occur in a name, and only a pipe or a file gets this (at a terminal it
// is escaped, and the caller refuses -0 there), so nothing written can act on one.
func (r *Renderer) Record(fields []string, nul bool) {
	if !nul {
		esc := make([]string, len(fields))
		for i, f := range fields {
			esc[i] = Escape(f)
		}
		r.out(strings.Join(esc, "\t") + "\n")
		return
	}
	if r.o.Terminal {
		r.write(plain(strings.Join(fields, "\t"))) // never raw at a terminal: the caller refuses -0 there
		return
	}
	r.out(strings.Join(fields, "\t") + "\x00")
}
