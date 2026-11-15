//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package view

import "os"

// Size reports no terminal where the window size cannot be read this way (Windows, among others): the output is
// then plain, with no colour codes an older console would show as text.
func Size(*os.File) (cols int, terminal bool) { return 0, false }
