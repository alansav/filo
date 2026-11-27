package walk

import (
	"fmt"
	"strings"
)

// isHidden reports whether an entry counts as hidden. Dot-prefixed names on every OS for now; on Windows the hidden
// file attribute should also count, which needs the entry's attributes and belongs here when Windows is supported.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// Skips reports whether a walk with these settings skips an entry with this name: noise, an ignored name, or a hidden
// one unless includeHidden.
func Skips(name string, includeHidden bool, ignore []string) bool {
	if noise[name] || (!includeHidden && isHidden(name)) {
		return true
	}
	for _, n := range ignore {
		if n == name {
			return true
		}
	}
	return false
}

// CheckIgnore reports whether name can be an ignored name: a plain file or directory name.
func CheckIgnore(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("ignore %q: must be a plain file or directory name", name)
	}
	return nil
}

// SkipsPath reports whether a walk of a root with these settings never records the file at rel ('/'-separated,
// relative to the root): a name on the way that Skips, or a directory filo delete left behind (DeletingPrefix).
func SkipsPath(rel string, includeHidden bool, ignore []string) bool {
	names := strings.Split(rel, "/")
	for i, name := range names {
		if Skips(name, includeHidden, ignore) {
			return true
		}
		if i < len(names)-1 && strings.HasPrefix(name, DeletingPrefix) {
			return true
		}
	}
	return false
}
