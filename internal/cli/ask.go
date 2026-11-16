package cli

import (
	"context"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// canPrompt reports whether a question can be asked: both the question (stdout) and the answer (stdin) are in a
// terminal. With stdout in a file, the question could not be seen (docs/design.md, Terminal output).
func (e *env) canPrompt() bool { return realTerminal(e.stdin) && e.view.Terminal() }

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

// askYesNo asks a yes/no question on pr, Enter being no. relist, if not nil, offers l too: the list above the question
// was cut, and l shows it whole before asking again (docs/design.md, Terminal output: everything a yes acts on can be
// seen first).
func (e *env) askYesNo(ctx context.Context, pr *prompter, question string, relist func()) (bool, error) {
	choices := []view.Choice{{Key: "y", Word: "yes"}, {Key: "n", Word: "no"}}
	if relist != nil {
		choices = yesNoList
	}
	for {
		key, err := e.askChoice(ctx, pr, question, choices, "n")
		if err != nil {
			return false, err
		}
		e.view.Blank()
		if key != "l" {
			return key == "y", nil
		}
		relist()
	}
}

// confirmChange asks question before a change, unless yes (-y) says to go ahead. Without a terminal nothing is done,
// and one line says so; declined, one line says nothing changed. relist is as for askYesNo.
func (e *env) confirmChange(ctx context.Context, question string, yes bool, relist func()) (bool, error) {
	r := e.view
	if yes {
		return true, nil
	}
	r.Blank()
	if !e.canPrompt() {
		r.Next("Nothing changed: there is no terminal to ask in. Run it in a terminal, or pass -y.")
		return false, nil
	}
	ok, err := e.askOne(ctx, question, relist)
	if err != nil {
		return false, interrupted(err)
	}
	if !ok {
		r.Next("Nothing changed.")
	}
	return ok, nil
}

// askOne asks one yes/no question at the terminal (the caller has checked canPrompt), Enter being no; relist is as
// for askYesNo.
func (e *env) askOne(ctx context.Context, question string, relist func()) (bool, error) {
	pr := newPrompter(e.stdin)
	pr.end, pr.check = e.view.Blank, e.outputErr
	defer pr.close()
	return e.askYesNo(ctx, pr, question, relist)
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
