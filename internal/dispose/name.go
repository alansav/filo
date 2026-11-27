package dispose

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alansav/filo/internal/walk"
)

// maxName is the longest name, in bytes, most filesystems take (NAME_MAX on Linux).
const maxName = 255

// CheckName says why a file named old cannot be renamed to name in its own directory, in a root with these scan
// settings (docs/design.md, Tags, "Renaming a file by its name cell"); "" if it can. The new name must be one plain
// name that the next scan sees as the same file in the same place: no "/", not "." or "..", no control character, not
// longer than the filesystem takes, neither hiding nor unhiding the file, not Filo's own, and not one the root's scan
// skips. old must be valid UTF-8: one that is not was exported with U+FFFD, so its real name cannot be written back.
func CheckName(old, name string, includeHidden bool, ignore []string) string {
	switch {
	case !utf8.ValidString(old):
		return "its name is not valid UTF-8, so the CSV cannot show it as it is; rename it by hand"
	case !utf8.ValidString(name):
		return "the new name is not valid UTF-8"
	case name == "", name == ".", name == "..":
		return fmt.Sprintf("%q is not a file name", name)
	case strings.Contains(name, "/"):
		return "a new name cannot hold /: a rename stays in the file's directory (moving is not a rename)"
	case strings.ContainsFunc(name, unicode.IsControl):
		return "the new name holds a control character (a tab, a line break or the like)"
	case len(name) > maxName:
		return fmt.Sprintf("the new name is %d bytes long; the most a file name can be is %d", len(name), maxName)
	case strings.HasPrefix(name, walk.DeletingPrefix):
		return "names starting " + walk.DeletingPrefix + " are filo's own"
	case hidden(name) && !hidden(old):
		return "a rename never hides a file (a name starting with . is hidden); rename it by hand if you mean to"
	case !hidden(name) && hidden(old):
		return "a rename never unhides a file (a name starting with . is hidden); rename it by hand if you mean to"
	case walk.Skips(name, includeHidden, ignore):
		return "the root's scan skips that name (an ignored name), so the next scan would lose the file"
	}
	return ""
}

// hidden is the scan's rule for a hidden name (see package walk).
func hidden(name string) bool { return strings.HasPrefix(name, ".") }
