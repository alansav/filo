package cli

import (
	"io"
	"strings"

	"github.com/alansav/filo/internal/view"
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
	if _, err := s.w.Write(view.Visible(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// writeLines writes text of filo's own that spans several lines (help, multi-line messages) one line per write, so its
// newlines pass. Never use it for anything that holds a file name.
func writeLines(w io.Writer, text string) {
	for line := range strings.Lines(text) {
		io.WriteString(w, line)
	}
}
