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

// Record writes one row for a pipe: values, then a file's path, joined by tabs and ended by a newline. The values are
// escaped as everywhere (a tab in one shows as \t, so the tabs between fields are always filo's own), and the path as
// a name (a trailing space as \x20).
func (r *Renderer) Record(values []string, path string) {
	fields := make([]string, 0, len(values)+1)
	for _, v := range values {
		fields = append(fields, Escape(v))
	}
	r.out(strings.Join(append(fields, name(path).out), "\t") + "\n")
}
