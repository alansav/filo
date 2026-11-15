package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// canPrompt reports whether a question can be asked: both the question (stdout) and the answer (stdin) are in a
// terminal. With stdout in a file, the question could not be seen (docs/design.md, Terminal output).
func (e *env) canPrompt() bool { return canAsk(e.stdin) && e.view.Terminal() }

// askChoice asks question until the answer is one of choices (a key or its word, any case); an answer it does not
// know shows the choices and asks again. Enter, or the end of input, is def: always the safe choice.
func (e *env) askChoice(ctx context.Context, pr *prompter, question string, choices []view.Choice,
	def string) (string, error) {
	for {
		ans, ok, err := pr.ask(ctx, func() { e.view.Ask(question, choices, def) })
		if err != nil {
			return def, err
		}
		if !ok {
			return def, nil
		}
		if key, known := view.Pick(ans, choices, def); known {
			return key, nil
		}
		e.view.Again(choices)
	}
}

// yesNoList are the answers to a confirmation that covers a list cut to its first entries.
var yesNoList = []view.Choice{{Key: "y", Word: "yes"}, {Key: "n", Word: "no"}, {Key: "l", Word: "list"}}

// registeredRoots returns the roots' paths, for showing files relative to them.
func registeredRoots(ctx context.Context, s *store.Store) ([]string, error) {
	roots, err := s.Roots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.Path
	}
	return out, nil
}

// splitRoot splits an absolute path into the root it is in (the longest that contains it) and the path relative to
// it ('/' separators). A path in no root comes back whole, with no root.
func splitRoot(roots []string, abs string) (root, rel string) {
	for _, r := range roots {
		if len(r) <= len(root) {
			continue
		}
		if rest, ok := strings.CutPrefix(abs, r+string(filepath.Separator)); ok {
			root, rel = r, filepath.ToSlash(rest)
		}
	}
	if root == "" {
		return "", abs
	}
	return root, rel
}
