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
// longer than the filesystem takes, not Filo's own, and not one the root's scan skips. old must be valid UTF-8: one
// that is not was exported with U+FFFD, so its real name cannot be written back. Hidden names are CheckPath's: a
// hidden file is never renamed, and no file is renamed to a hidden name.
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
	case walk.Skips(name, includeHidden, ignore):
		return "the root's scan skips that name (an ignored name), so the next scan would lose the file"
	}
	return ""
}

// hidden is the scan's rule for a hidden name (see package walk).
func hidden(name string) bool { return strings.HasPrefix(name, ".") }

// CheckPath says why a file at oldRel cannot be renamed or moved to newRel within its root, with these scan
// settings (both '/'-separated, relative to the root); "" if it can (docs/design.md, Tags, "Moving a file by its
// directory cell"). A hidden file, or one under a hidden directory, is never renamed or moved ("A hidden file is
// never renamed or moved by the CSV"), and the new path has no hidden part, so no file is hidden. Then a new name
// follows CheckName's rules; each directory of the new path that is not on the old one must be a plain name as a new
// file name must be (what the file has now is not judged again); and the root's scan must record the new path
// (walk.SkipsPath). These come after the hidden parts, so a hidden new path says so, not that the scan skips it.
func CheckPath(oldRel, newRel string, includeHidden bool, ignore []string) string {
	oldParts, newParts := strings.Split(oldRel, "/"), strings.Split(newRel, "/")
	if hiddenPart(oldParts) {
		return "a hidden file, or one in a hidden directory, is never renamed or moved by filo update; rename or " +
			"move it by hand if you mean to"
	}
	if hiddenPart(newParts) {
		return "a rename or move never hides a file (a name or directory starting with . is hidden); rename or move " +
			"it by hand if you mean to"
	}
	oldName, newName := oldParts[len(oldParts)-1], newParts[len(newParts)-1]
	if oldName != newName {
		if why := CheckName(oldName, newName, includeHidden, ignore); why != "" {
			return why
		}
	}
	oldDirs, newDirs := oldParts[:len(oldParts)-1], newParts[:len(newParts)-1]
	same := 0 // the directories the two paths share, from the root down
	for same < len(oldDirs) && same < len(newDirs) && oldDirs[same] == newDirs[same] {
		same++
	}
	for _, dir := range newDirs[same:] {
		switch {
		case !utf8.ValidString(dir):
			return fmt.Sprintf("the directory %q is not valid UTF-8", dir)
		case dir == "" || dir == "." || dir == "..":
			return fmt.Sprintf("%q is not a directory name", dir)
		case strings.ContainsFunc(dir, unicode.IsControl):
			return "a directory on the new path holds a control character (a tab, a line break or the like)"
		case len(dir) > maxName:
			return fmt.Sprintf("a directory on the new path is %d bytes long; the most a name can be is %d", len(dir),
				maxName)
		case strings.HasPrefix(dir, walk.DeletingPrefix):
			return "directories starting " + walk.DeletingPrefix + " are filo's own"
		}
	}
	if walk.SkipsPath(newRel, includeHidden, ignore) {
		return "the root's scan skips a directory on the new path (an ignored name), so the next scan would lose the file"
	}
	return ""
}

// hiddenPart reports whether a part of the path, the file's name or a directory above it, is hidden.
func hiddenPart(parts []string) bool {
	for _, p := range parts {
		if hidden(p) {
			return true
		}
	}
	return false
}
