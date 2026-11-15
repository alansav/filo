//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package view

import "os"

// CanCheck is false where Size cannot tell a terminal apart (Windows, among others): there, whether a question can
// be answered is decided by the caller's own check, and the output is plain.
const CanCheck = false

// Size reports no width and no terminal: the output is plain, with no colour codes an older console would show as
// text.
func Size(*os.File) (cols int, terminal bool) { return 0, false }
