package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// safeText is how everything filo prints reaches the terminal. File names may hold any byte but '/' and NUL, and
// they come from anywhere (a downloaded attachment, a file synced from a phone, an altered register). Printed as
// they are, control characters and escape sequences could move the cursor, erase lines (so the list a person
// confirms before a deletion is not what is deleted), set the window title or write the clipboard; a newline could
// add invented lines to that list; and Unicode bidi controls could make "photo\u202egpj.exe" read as "photoexe.jpg".
// safeText shows all of those as visible escapes (\x1b, \n, \u202e), and a backslash as \\ so an escape cannot be
// confused with a name that holds the same text.
//
// Filo writes each line of output with one call, so a newline passes only as the last byte of a write: one inside a
// write can only have come from data. Text of filo's own that spans lines goes through writeLines. Only the
// progress line's own control codes bypass safeText.
type safeText struct{ w io.Writer }

func (s safeText) Write(p []byte) (int, error) {
	if _, err := s.w.Write(visible(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// visible returns p with control characters, newlines (but a final one), tabs, backslashes, invalid UTF-8 and bidi
// controls escaped.
func visible(p []byte) []byte {
	end := ""
	if n := len(p); n > 0 && p[n-1] == '\n' {
		p, end = p[:n-1], "\n"
	}
	out := make([]byte, 0, len(p)+len(end))
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		switch {
		case r == utf8.RuneError && size <= 1:
			out = fmt.Appendf(out, `\x%02x`, p[0])
		case r == '\\':
			out = append(out, `\\`...)
		case r == '\n':
			out = append(out, `\n`...)
		case r == '\t':
			out = append(out, `\t`...)
		case r < 0x20 || r == 0x7f:
			out = fmt.Appendf(out, `\x%02x`, r)
		case (r >= 0x80 && r < 0xa0) || isBidi(r):
			out = fmt.Appendf(out, `\u%04x`, r)
		default:
			out = append(out, p[:size]...)
		}
		p = p[size:]
	}
	return append(out, end...)
}

// writeLines writes text of filo's own that spans several lines (help, multi-line messages) one line per write, so its
// newlines pass. Never use it for anything that holds a file name.
func writeLines(w io.Writer, text string) {
	for line := range strings.Lines(text) {
		io.WriteString(w, line)
	}
}

// isBidi reports the Unicode bidirectional formatting characters, which reorder the text around them.
func isBidi(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
