package view

import "strings"

// Where says what a heading is about: the roots its output covers and, with one root, the directory under it a
// DIR argument narrowed it to ('/' separators, relative to the root; "" = the whole root).
type Where struct {
	Roots []string
	Under string
}

// Heading writes a command's first line: a title, its facts, and where: "Ready to delete   5 files · 820 MB ·
// ~/Documents, under video/". With one root, paths in it are shown relative to it from then on.
func (r *Renderer) Heading(title string, where Where, facts ...string) {
	r.root = ""
	roots := uniq(where.Roots)
	var place []span // the root and the directory under it, shown as names (a trailing space as \x20)
	switch len(roots) {
	case 0:
	case 1:
		r.root = roots[0]
		place = []span{name(r.Tilde(roots[0]))}
		if where.Under != "" {
			place = append(place, plain(", under "), name(where.Under), plain("/"))
		}
	default:
		facts = append(facts, Plural(int64(len(roots)), "root", "roots"))
	}
	r.titleLine(title, facts, place...)
}

// titleLine writes a bold title and its facts, then place: the heading's and the outcome's shape.
func (r *Renderer) titleLine(title string, facts []string, place ...span) {
	spans := []span{styled(title, bold)}
	if len(facts) > 0 {
		spans = append(spans, plain("   "+strings.Join(facts, r.sep())))
	}
	if len(place) > 0 {
		if len(facts) > 0 {
			spans = append(spans, plain(r.sep()))
		} else {
			spans = append(spans, plain("   "))
		}
		spans = append(spans, place...)
	}
	r.write(spans...)
}

// Outcome writes the line that says what a run did, "Done   deleted 3 files · freed 812 MB", keeping the heading's
// root for the lines after it.
func (r *Renderer) Outcome(title string, facts ...string) { r.titleLine(title, facts) }

// Section writes a section's title, with its facts at the right edge of the terminal ("Past their retention
// … 5 files · 820 MB"), or after it when there is no width.
func (r *Renderer) Section(title string, facts ...string) {
	r.rightAligned([]span{styled(title, bold)}, facts)
}

// rightAligned writes head with facts at the right edge, one column short of it (some consoles wrap as soon as the
// last column is written), or three spaces after head when there is no width or no room.
func (r *Renderer) rightAligned(head []span, facts []string) {
	if len(facts) == 0 {
		r.write(head...)
		return
	}
	tail := plain(strings.Join(facts, r.sep()))
	gap := 3
	if r.o.Width > 0 {
		gap = max(r.o.Width-1-width(append(head, tail)...), 3)
	}
	r.write(append(head, spaces(gap), tail)...)
}

// Entry writes a root (or another place) in a section, with its facts at the right edge: "  ~/Documents      13
// files · 313 KiB · 2 symlinks". The path is shown as a name, with ~ for home.
func (r *Renderer) Entry(indent int, path string, facts ...string) {
	r.rightAligned([]span{spaces(indent), styledName(r.Tilde(path), bold)}, facts)
}

// Detail writes a dimmed line of detail under an entry or a list: "    last scan 1, 2026-11-15 16:39, whole root".
func (r *Renderer) Detail(indent int, text string) { r.write(spaces(indent), styled(text, dim)) }

// DetailPath writes a dimmed line of detail that ends in a path, shown as a name with ~ for home: "    on the
// filesystem mounted at /media/archive".
func (r *Renderer) DetailPath(indent int, text, path string) {
	r.write(spaces(indent), styled(text, dim), styledName(r.Tilde(path), dim))
}

// CountName is Count with a path in the middle of the text, shown as a name (a trailing space as \x20): "  ! none in
// ~/x, though the register has changed".
func (r *Renderer) CountName(m Mark, before, path, after string) {
	r.write(spaces(2), r.mark(m), plain(" "+before), name(r.Tilde(path)), plain(after))
}

// Count writes one line of a summary under a marker: "  ✓ 41 files given a retention".
func (r *Renderer) Count(m Mark, text string) { r.write(spaces(2), r.mark(m), plain(" "+text)) }

// Note writes a line that stands on its own under a marker: "! 1 mark no longer counts: …".
func (r *Renderer) Note(m Mark, text string) { r.write(r.mark(m), plain(" "+text)) }

// Next writes the closing line: what happened and what to do next.
func (r *Renderer) Next(text string) { r.write(plain(text)) }

func uniq(ss []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// helpWidth is the width help is wrapped to where there is none (piped): a file of help reads well.
const helpWidth = 80

// wrapWidth is the width filo's own prose is wrapped to: the terminal's, one column short of it, or helpWidth.
func (r *Renderer) wrapWidth() int {
	if r.o.Width > 0 {
		return r.o.Width - 1
	}
	return helpWidth
}

// wrap splits text into lines of at most width columns, at spaces; a word longer than width gets a line of its own.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case columns(line)+1+columns(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Paragraph writes filo's own prose (help), indented, wrapped at spaces to the terminal's width, or to 80 columns
// when there is none: the text is stored unwrapped.
func (r *Renderer) Paragraph(indent int, text string) {
	for _, line := range wrap(text, max(r.wrapWidth()-indent, 20)) {
		r.write(spaces(indent), plain(line))
	}
}

// Term is one entry of a two-column list in help: a command or a flag, and what it does.
type Term struct{ Name, Text string }

// Terms writes a two-column list, names indented by indent and texts starting at column col, each text wrapped
// under its own column. A name too long for its column puts its text on the next line. A terminal too narrow for two
// columns (under 20 left for the text) gets each text on the lines under its name, indented a little.
func (r *Renderer) Terms(indent, col int, terms []Term) {
	width := r.wrapWidth() - col
	if width < 20 {
		col = indent + 4
		width = max(r.wrapWidth()-col, 10)
		for _, t := range terms {
			r.write(spaces(indent), styled(t.Name, bold))
			for _, l := range wrap(t.Text, width) {
				r.write(spaces(col), plain(l))
			}
		}
		return
	}
	for _, t := range terms {
		lines := wrap(t.Text, width)
		if columns(t.Name)+indent+2 > col || len(lines) == 0 {
			r.write(spaces(indent), styled(t.Name, bold))
		} else {
			r.write(spaces(indent), styled(t.Name, bold), spaces(col-indent-columns(t.Name)), plain(lines[0]))
			lines = lines[1:]
		}
		for _, l := range lines {
			r.write(spaces(col), plain(l))
		}
	}
}
