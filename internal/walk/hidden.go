package walk

import "strings"

// isHidden reports whether an entry counts as hidden. Dot-prefixed names on every OS for now; on Windows the hidden
// file attribute should also count, which needs the entry's attributes and belongs here when Windows is supported.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}
