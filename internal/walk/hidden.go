package walk

import "strings"

// isHidden reports whether an entry counts as hidden. Dot-prefixed names on every OS for now; on Windows the hidden
// file attribute should also count, which needs the entry's attributes and belongs here when Windows is supported.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// SkippedByDefault reports whether a scan without --include-hidden or --ignore skips a directory with this name:
// hidden names and the noise directories. (bin and obj beside a project file are skipped too, but that depends on
// their siblings.)
func SkippedByDefault(name string) bool {
	return isHidden(name) || noise[name]
}
