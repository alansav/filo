package cli

import (
	"fmt"
	"os"
	"strings"
)

// checkTilde refuses a path that starts with ~. The shell expands ~ only when it is unquoted, so "~/Documents"
// arrives as a relative path and would name a folder called ~ in the current directory. Filo does not expand it
// itself: a name that starts with ~ is legal, and guessing would sometimes be wrong. what names the argument
// ("PATH", "--db", "FILO_DB") for the message.
func checkTilde(what, p string) error {
	if !strings.HasPrefix(p, "~") {
		return nil
	}
	instead := "the full path"
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || os.IsPathSeparator(rest[0])) {
		instead = "$HOME" + rest
	}
	return fmt.Errorf("%s %s starts with ~, which the shell expands only when unquoted; write %s instead, or ./%s for a name that starts with ~",
		what, p, instead, p)
}

// checkTildes runs checkTilde on each path.
func checkTildes(what string, paths ...string) error {
	for _, p := range paths {
		if err := checkTilde(what, p); err != nil {
			return err
		}
	}
	return nil
}
