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
// one unless includeHidden. (bin and obj beside a project file are skipped too, but that depends on their siblings.)
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
