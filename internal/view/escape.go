package view

import (
	"fmt"
	"unicode/utf8"
)

// Escape returns s as it can safely be shown on a terminal. File names may hold any byte but '/' and NUL, and they
// come from anywhere (a downloaded attachment, a file synced from a phone, an altered register). Printed as they are,
// control characters and escape sequences could move the cursor, erase lines (so the list a person confirms before a
// deletion is not what is deleted), set the window title or write the clipboard; a newline could add invented lines
// to that list; and Unicode bidi controls could make "photo\u202egpj.exe" read as "photoexe.jpg". Escape shows all
// of those as visible escapes (\x1b, \n, \u202e), invalid UTF-8 as \xNN, a tab as \t, and a backslash as \\ so an
// escape cannot be confused with a name that holds the same text.
// Every newline is escaped, a final one too: a name that ends in one must not end the line it is printed on.
func Escape(s string) string {
	p := []byte(s)
	out := make([]byte, 0, len(p))
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
	return string(out)
}

// isBidi reports the Unicode bidirectional formatting characters, which reorder the text around them.
func isBidi(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
