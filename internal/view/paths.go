package view

import (
	"strings"
	"unicode"
)

// PathLines writes absolute paths for a pipe, one per line, escaped as all output is (a newline in a name shows as
// \n, a backslash as \\), and returns how many were changed by it: those will not match the file on disk, so the
// command says so (docs/design.md, Finding files). A trailing space stays a space: the line is for a tool, which needs
// the name's bytes wherever escaping allows.
func (r *Renderer) PathLines(paths []string) (escaped int) {
	for _, p := range paths {
		e := Escape(p)
		if e != p {
			escaped++
		}
		r.out(e + "\n")
	}
	return escaped
}

// RawPaths writes paths as they are, byte for byte, each followed by a NUL, for xargs -0 or tar --null: filo find -0,
// the one output not escaped (docs/design.md, Commands and scheduling, "Output cannot act on the terminal"). The
// command refuses it when stdout is a terminal, so it is never called for one; it writes nothing in one either, in
// case it is.
func (r *Renderer) RawPaths(paths []string) {
	if r.o.Terminal {
		return
	}
	for _, p := range paths {
		r.out(p + "\x00")
	}
}

// Completions writes bash completion's answer (docs/design.md, Bash completion): how bash should treat the
// candidates ("files" or "words"), then one per line, as they are, for the script to read. Like RawPaths it is not
// escaped, so it is never written to a terminal (bash always reads it from a pipe), and a candidate holding a
// control character, which could only come from a name and would break the lines, is left out.
func (r *Renderer) Completions(mode string, candidates []string) {
	if r.o.Terminal {
		return
	}
	r.out(mode + "\n")
	for _, c := range candidates {
		if strings.ContainsFunc(c, unicode.IsControl) {
			continue
		}
		r.out(c + "\n")
	}
}
