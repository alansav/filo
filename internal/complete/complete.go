// Package complete holds the text side of bash completion (docs/design.md, Bash completion): it splits the command
// line bash hands over into arguments as the shell will, works out what of the argument being completed comes before
// the word bash replaces, quotes candidates for where they are inserted, and reads the help's usage lines. It reads
// neither the disk nor the register; package cli decides what to offer.
package complete

import (
	"strings"
	"unicode"
)

// Line is the command line up to the cursor, split as the shell will split it.
type Line struct {
	Args    []string // the arguments before the one being completed, as the command will get them (unquoted)
	Current string   // the argument being completed, as far as it is typed (unquoted)
	// Before is the part of Current before the word bash replaces (bash splits words at ':', '=' and more, the
	// shell does not); Quote is the quote open where that word starts: 0, '\'' or '"'.
	Before string
	Quote  byte
	OK     bool // false if the word bash gave is not the end of the line: nothing should be offered
}

// Parse splits line, the command line from its start to the cursor, into arguments as the shell does (blanks
// separate them; single and double quotes and backslashes quote), and places word, the text bash will replace
// (its $2), at the end of the argument being completed.
func Parse(line, word string) Line {
	var l Line
	var raw string
	l.Args, l.Current, raw, _ = split(line)
	if !strings.HasSuffix(raw, word) {
		return l
	}
	head := raw[:len(raw)-len(word)]
	_, l.Before, _, l.Quote = split(head)
	l.OK = true
	return l
}

// split reads line as the shell does: the arguments before the last, the last as it will be passed (unquoted) and
// as typed, and the quote open at the end of the line (0, a single or a double quote).
func split(line string) (args []string, last, raw string, open byte) {
	var r, cur strings.Builder
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case escaped:
			escaped = false
			if c != '\n' { // a backslash and a newline join lines
				cur.WriteByte(c)
			}
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`\n", line[i+1]) >= 0:
				escaped = true
			default:
				cur.WriteByte(c)
			}
		case c == ' ' || c == '\t' || c == '\n':
			if r.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
				r.Reset()
			}
			continue
		case c == '\\':
			escaped = true
		case c == '\'' || c == '"':
			quote = c
		default:
			cur.WriteByte(c)
		}
		r.WriteByte(c)
	}
	return args, cur.String(), r.String(), quote
}

// Insert is what replaces bash's word for candidate, a whole argument that starts with l.Current (as typed or in
// another case), when Filo quotes it itself: the rest of it after l.Before, quoted for l.Quote, and, if done (a word
// finished, not a key or a directory to go on from), closed and followed by a space. ok is false for a candidate
// that does not start with l.Before (in any case).
func (l Line) Insert(candidate string, done bool) (string, bool) {
	if len(candidate) < len(l.Before) || !strings.EqualFold(candidate[:len(l.Before)], l.Before) {
		return "", false
	}
	rest := candidate[len(l.Before):]
	var b strings.Builder
	switch l.Quote {
	case '\'':
		b.WriteString(strings.ReplaceAll(rest, "'", `'\''`))
	case '"':
		for _, r := range rest {
			if strings.ContainsRune("\"\\$`", r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	default:
		for i, r := range rest {
			atStart := i == 0 && l.Before == ""
			// A leading ~/ stays as it is, so the shell expands it to the home directory; any other ~ or # at the
			// start would be read by the shell, so it is escaped.
			home := atStart && r == '~' && strings.HasPrefix(rest, "~/")
			if strings.ContainsRune(" \t\\'\"`$&;|<>()*?[]{}!", r) || (atStart && !home && (r == '~' || r == '#')) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	if done {
		if l.Quote != 0 {
			b.WriteByte(l.Quote)
		}
		b.WriteByte(' ')
	}
	return b.String(), true
}

// Rest is what replaces bash's word for candidate when bash quotes it itself (a file name, in bash's filenames
// mode): the rest of it after l.Before, unquoted. ok is false for a candidate that does not start with l.Before.
func (l Line) Rest(candidate string) (string, bool) {
	if !strings.HasPrefix(candidate, l.Before) {
		return "", false
	}
	return candidate[len(l.Before):], true
}

// Typable reports whether s can be offered at all: no control character (bash could not take it from a line of its
// own, nor a person type it).
func Typable(s string) bool { return !strings.ContainsFunc(s, unicode.IsControl) }
