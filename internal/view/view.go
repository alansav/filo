// Package view is filo's terminal output: one Renderer and the components every command is built from (a heading,
// sections, counts, file lists, duplicate groups, problems, prompts, select-mode items, the next step), so files,
// groups, problems and prompts look the same everywhere (docs/design.md, Terminal output).
//
// In a terminal the output is rich (colour and symbols, unless NO_COLOR is set) and fits the terminal's width;
// piped, it is plain: the same words in the same order, with ASCII markers and one line per entry. Every text a
// component is given is escaped (see Escape), and each line is written with one call.
package view

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// DefaultWidth is the width used in a terminal whose width cannot be read.
const DefaultWidth = 80

// Options say where output goes and how it can look.
type Options struct {
	Out      io.Writer // where lines go, unescaped: the Renderer escapes every text itself
	Terminal bool      // Out is a terminal: overviews are cut, and Width applies
	Color    bool      // colour and symbols (a terminal, NO_COLOR unset)
	Width    int       // the terminal's width in columns; 0 = no width (one line per entry)
	Home     string    // the home directory, shown as ~
}

// Renderer writes the components. A Heading names where the output is about; paths in that root are then shown
// relative to it.
type Renderer struct {
	o    Options
	root string // the one root the current heading names, "" if several or none
	err  error  // the first write error: once one write fails, nothing more is written
}

// Err returns the first error writing the output met (a hung-up terminal, a full disk, a closed pipe), or nil. A
// command checks it before doing anything on the strength of what it showed: filo delete before each question and
// each deletion, so nothing is done that the person could not see. (Every command's exit code also reflects it, from
// the CLI's own record of stdout's first error.)
func (r *Renderer) Err() error { return r.err }

// out writes text in one call, keeping the first error and writing nothing after it.
func (r *Renderer) out(text string) {
	if r.err != nil {
		return
	}
	if _, err := io.WriteString(r.o.Out, text); err != nil {
		r.err = fmt.Errorf("write output: %w", err)
	}
}

// New returns a Renderer. A terminal with no width gets DefaultWidth; a non-terminal gets no width and no colour.
func New(o Options) *Renderer {
	if !o.Terminal {
		o.Color, o.Width = false, 0
	} else if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	return &Renderer{o: o}
}

// Terminal reports whether the output is a terminal.
func (r *Renderer) Terminal() bool { return r.o.Terminal }

// Overview is how many entries an overview list shows before "… N more": 10 in a terminal, all (0) when piped.
func (r *Renderer) Overview() int {
	if r.o.Terminal {
		return 10
	}
	return 0
}

// Mark is the marker in front of a line: what kind of news it is.
type Mark int

const (
	Done      Mark = iota // ✓ done
	Refused               // ✗ refused or failed
	Attention             // ! needs attention
	Neutral               // · for information
	Kept                  // ◆ kept on purpose (contents marked separate)
)

// marks: rich symbol, its colour, and the plain marker.
var marks = map[Mark]struct {
	symbol, color, plain string
}{
	Done:      {"✓", green, "[ok]"},
	Refused:   {"✗", red, "[refused]"},
	Attention: {"!", yellow, "[!]"},
	Neutral:   {"·", dim, "-"},
	Kept:      {"◆", magenta, "[kept]"},
}

// ANSI styles.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	magenta = "\x1b[35m"
)

// span is a piece of a line with its style ("" = plain), its text escaped once, when it is made, with the columns
// it takes. A pad span is filo's own alignment, never data: only pad spans are dropped from the end of a line.
type span struct {
	out, style string
	cols       int
	pad        bool
}

func escaped(out, style string) span { return span{out: out, style: style, cols: columns(out)} }
func plain(s string) span            { return escaped(Escape(s), "") }
func styled(s, st string) span       { return escaped(Escape(s), st) }
func spaces(n int) span              { n = max(n, 0); return span{out: strings.Repeat(" ", n), cols: n, pad: true} }

// name is a file name or path: escaped, and with a trailing space shown as \x20, so "report " and "report" never
// read the same (a backslash in a name is shown as \\, so \x20 cannot be confused with one).
func name(s string) span { return styledName(s, "") }

// styledName is a name in a style.
func styledName(s, style string) span {
	out := Escape(s)
	trimmed := strings.TrimRight(out, " ")
	return escaped(trimmed+strings.Repeat(`\x20`, len(out)-len(trimmed)), style)
}

// relName shows a path relative to a root: one that starts with ~ (a directory named ~ inside the root) gets ./ in
// front, so it never reads as a path in the home directory.
func relName(rel string) string {
	if strings.HasPrefix(rel, "~") {
		return "./" + rel
	}
	return rel
}
func (r *Renderer) mark(m Mark) span {
	mk := marks[m]
	return styled(r.pick(mk.symbol, mk.plain), mk.color)
}

// pick returns rich in colour, plain otherwise: the symbols that have an ASCII form.
func (r *Renderer) pick(rich, plain string) string {
	if r.o.Color {
		return rich
	}
	return plain
}

// sep joins facts: " · " in colour, " . " plain.
func (r *Renderer) sep() string { return r.pick(" · ", " . ") }

// Join joins facts with the separator the headings use (" · ", or " . " plain), for a line that lists several:
// "hidden files skipped · ignoring build".
func (r *Renderer) Join(facts ...string) string { return strings.Join(facts, r.sep()) }

// write writes one line of spans, escaping each text, with one call.
// Trailing alignment is left out; a space that ends a file name is kept, since it is part of the name.
func (r *Renderer) write(spans ...span) {
	for len(spans) > 0 && spans[len(spans)-1].pad {
		spans = spans[:len(spans)-1]
	}
	r.out(r.render(spans) + "\n")
}

// render joins spans (already escaped) into text, each in its style when there is colour.
func (r *Renderer) render(spans []span) string {
	var b strings.Builder
	for _, s := range spans {
		if s.style != "" && r.o.Color && s.out != "" {
			b.WriteString(s.style)
			b.WriteString(s.out)
			b.WriteString(reset)
			continue
		}
		b.WriteString(s.out)
	}
	return b.String()
}

// width is the number of terminal columns spans take.
func width(spans ...span) int {
	n := 0
	for _, s := range spans {
		n += s.cols
	}
	return n
}

// Tilde shows an absolute path with the home directory as ~.
func (r *Renderer) Tilde(abs string) string {
	if r.o.Home == "" {
		return abs
	}
	h := filepath.Clean(r.o.Home)
	if h == string(filepath.Separator) || h == "." {
		return abs
	}
	if abs == h {
		return "~"
	}
	if rest, ok := strings.CutPrefix(abs, h+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return abs
}

// Full is a file's full path, with ~ for home: what the last confirmation before a deletion shows.
func (r *Renderer) Full(root, rel string) string {
	if root == "" {
		return r.Tilde(rel) // a path in no root, already whole
	}
	return r.Tilde(join(root, rel))
}

// Path names a file for reading: relative to the heading's root if it is in it, else in full.
// It is the one place this choice is made: lists and duplicate groups go through it.
func (r *Renderer) Path(root, rel string) string {
	if root != "" && root == r.root {
		return relName(rel)
	}
	return r.Full(root, rel)
}

func join(root, rel string) string {
	if rel == "" {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// shorten shows filo's own message text with the heading's root left out of the paths in it, as the paths in the
// columns are: "/home/u/Documents/b/doc is kept until …" becomes "b/doc is kept until …". The root is left out only
// where a path starts (at the start of the text, or after a space or an opening bracket), so a longer path that holds
// it ("/mnt/data/x.pdf" with the root /data) stays whole. It still rewrites text: a file name holding " /data/" is
// shortened too (docs/design.md, Terminal output, Long lists).
func (r *Renderer) shorten(text string) string {
	if r.root == "" || r.root == string(filepath.Separator) {
		return text // a root of / would strip every separator
	}
	prefix := withSep(r.root)
	var b strings.Builder
	for {
		i := strings.Index(text, prefix)
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:i])
		if i == 0 || strings.ContainsRune(" \t([{\"'", rune(text[i-1])) {
			text = text[i+len(prefix):] // a path starts here: leave the root out
		} else {
			b.WriteString(prefix) // inside a longer path: keep it whole
			text = text[i+len(prefix):]
		}
	}
}

// SplitRoot splits an absolute path into the root it is in (the longest of roots that contains it) and the path
// relative to it ('/' separators), for showing it relative to its root. A path in no root comes back whole, with no
// root.
func SplitRoot(roots []string, abs string) (root, rel string) {
	for _, r := range roots {
		if len(r) <= len(root) {
			continue
		}
		if abs == r {
			root, rel = r, "."
			continue
		}
		if rest, ok := strings.CutPrefix(abs, withSep(r)); ok {
			root, rel = r, filepath.ToSlash(rest)
		}
	}
	if root == "" {
		return "", abs
	}
	return root, rel
}

// withSep is a root with a separator after it, for matching the paths in it: "/" stays "/", not "//".
func withSep(root string) string {
	if strings.HasSuffix(root, string(filepath.Separator)) {
		return root
	}
	return root + string(filepath.Separator)
}

// Blank writes an empty line.
func (r *Renderer) Blank() { r.out("\n") }
