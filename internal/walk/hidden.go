package walk

import (
	"fmt"
	"os"
	"path/filepath"
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

// SkipsPath reports whether a walk of root with these settings never records the file at rel ('/'-separated,
// relative to root): a name on the way that Skips, a directory filo delete left behind (DeletingPrefix), or bin or
// obj in a directory that holds a project file. A directory that cannot be listed counts as holding none.
func SkipsPath(root, rel string, includeHidden bool, ignore []string) bool {
	names := strings.Split(rel, "/")
	dir := root
	for i, name := range names {
		if Skips(name, includeHidden, ignore) {
			return true
		}
		last := i == len(names)-1
		if !last && strings.HasPrefix(name, DeletingPrefix) {
			return true
		}
		if !last && (name == "bin" || name == "obj") && holdsProject(dir) {
			return true
		}
		dir = filepath.Join(dir, name)
	}
	return false
}

// holdsProject reports whether the directory holds a project file (.csproj, .fsproj), whose bin and obj a walk skips.
func holdsProject(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range entries {
		if de.Type().IsRegular() && isProjectFile(de.Name()) {
			return true
		}
	}
	return false
}
