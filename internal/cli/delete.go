package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// delete lists the deletion candidates, asks, and deletes the ones chosen, each after the check at the moment of
// deleting (docs/design.md, Disposal).
func (e *env) delete(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("delete")
	db := dbFlag(fl, dbBefore)
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "delete", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"delete takes at most one DIR or FILE"}
	}
	// A directory limits the retention list; anything else is a duplicates CSV to carry out (docs/design.md,
	// Duplicates), so a mistyped file name fails as a file that cannot be opened, not as a directory.
	csvPath := ""
	if len(pos) == 1 {
		if fi, err := os.Stat(pos[0]); err != nil || !fi.IsDir() {
			if csvPath, err = filepath.Abs(pos[0]); err != nil {
				return err
			}
		}
	}
	what := "DIR"
	if csvPath != "" {
		what = "FILE"
	}
	if err := checkTildes(what, pos...); err != nil {
		return err
	}
	if csvPath != "" {
		if _, err := os.Stat(csvPath); err != nil {
			return fmt.Errorf("open %s: %w", csvPath, errors.Unwrap(err))
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	if csvPath != "" {
		return e.deleteDuplicates(ctx, s, csvPath, *yes)
	}
	f, where, err := filterFor(ctx, s, pos)
	if err != nil {
		return err
	}
	cands, err := s.Candidates(ctx, today(), f)
	if err != nil {
		return err
	}
	r := e.view
	if len(cands) == 0 {
		under := ""
		if f.RootID != 0 { // a DIR was given
			under = " under " + r.Tilde(filepath.Join(where.Roots[0], filepath.FromSlash(where.Under)))
		}
		r.Next("No files" + under + " are past their retention.")
		return nil
	}
	list := buildDeleteList(f, cands)
	// Ask only if both the question can be seen (stdout) and answered (stdin): with stdout in a file, the list and the
	// question would go there while filo waited on the keyboard.
	asking := !*yes && e.canPrompt()
	// The full list unless a question follows: with -y, or with no terminal to ask in, the output is the record.
	max := 0
	if asking {
		max = r.Overview()
	}
	cut := list.draw(r, max)

	selecting := false
	var pr *prompter
	if !*yes {
		if !asking {
			r.Next("Nothing deleted: there is no terminal to ask in (filo asks only when both the question and your " +
				"answer are in a terminal). Run filo delete in a terminal, or pass -y to delete them all.")
			return nil
		}
		pr = newPrompter(e.stdin)
		pr.end, pr.check = r.Blank, e.outputErr
		defer pr.close()
		total := len(cands)
		choices := []view.Choice{{Key: "a", Word: "all"}, {Key: "s", Word: "select"}, {Key: "l", Word: "list"},
			{Key: "n", Word: "none"}}
		question := fmt.Sprintf("Delete all %s, select one by one, list them all, or none?", report.Count(int64(total)))
		yesNo := []view.Choice{{Key: "y", Word: "yes"}, {Key: "n", Word: "no"}}
	ask:
		for {
			key, err := e.askChoice(ctx, pr, question, choices, "n")
			if err != nil {
				return interrupted(err)
			}
			switch key {
			case "a":
				if !cut {
					break ask
				}
				// Everything deleted is on screen right before the last question (docs/design.md, Terminal output).
				r.Blank()
				list.draw(r, 0)
				sure, err := e.askChoice(ctx, pr, fmt.Sprintf("Delete these %s?", report.Plural(int64(total), "file",
					"files")), yesNo, "n")
				if err != nil {
					return interrupted(err)
				}
				if sure != "y" {
					r.Next("Nothing deleted.")
					return nil
				}
				break ask
			case "s":
				selecting = true
				break ask
			case "l":
				r.Blank()
				cut = list.draw(r, 0)
			default:
				r.Next("Nothing deleted.")
				return nil
			}
		}
		r.Blank()
	}

	run := s.BeginDeleteRun(now())
	d := dispose.New(run, now)
	type skipped struct {
		root, rel, reason string
		failed            bool // could not be read or deleted: a failure, not a skip
	}
	var (
		deleted, renewed, left int
		freed                  int64
		skips                  []skipped
		failed                 bool
		runErr                 error
	)
	total := len(cands)
	n := 0 // files gone through, for (n/total)

files:
	for i, c := range cands {
		n++
		if selecting {
			if n > 1 {
				r.Blank()
			}
			r.Item(n, total, "past its retention", "until "+c.KeepUntil, report.Size(c.Size))
			r.ItemPath("", c.RootPath, c.Path, "")
			act, err := e.choose(ctx, pr, choice{view.Choice{Key: "r", Word: "retention", Label: "new retention"},
				actRetention,
				func() (bool, error) { return e.askRetention(ctx, pr, s, c) }})
			if err != nil {
				runErr = err
				left += len(cands) - i
				break
			}
			switch act {
			case actRetention:
				renewed++
				continue
			case actSkip:
				left++
				r.Result(view.Neutral, "skipped")
				continue
			case actQuit:
				left += len(cands) - i
				break files
			}
		}
		var o dispose.Outcome
		err := e.outputErr() // never delete once output has failed: what is deleted must have been shown
		if err == nil {
			o, err = d.Delete(ctx, c)
		}
		if o.Deleted {
			deleted++
			freed += fileBytes(c)
		} else if err == nil {
			skips = append(skips, skipped{c.RootPath, c.Path, o.Reason, o.Failed})
			failed = failed || o.Failed
		}
		if selecting && err == nil {
			switch {
			case o.Deleted:
				r.Result(view.Done, "deleted")
			case o.Failed:
				r.Result(view.Refused, "not deleted: "+o.Reason)
			default:
				r.Result(view.Attention, "not deleted: "+o.Reason)
			}
		}
		if err != nil {
			runErr = err
			left += len(cands) - i // this file and the rest
			if o.Deleted {
				left--
			}
			break
		}
	}
	removed, problems := d.PruneDirs()
	finishErr := run.Finish(context.WithoutCancel(ctx), now())

	if selecting {
		r.Blank()
	}
	facts := []string{"deleted " + report.Plural(int64(deleted), "file", "files"), "freed " + report.Size(freed)}
	if len(removed) > 0 {
		facts = append(facts, "removed "+report.Plural(int64(len(removed)), "empty folder", "empty folders"))
	}
	r.Outcome("Done", facts...)
	if len(skips) > 0 {
		ps := make([]view.Problem, len(skips))
		for i, sk := range skips {
			ps[i] = view.Problem{Root: sk.root, Rel: sk.rel, Reason: sk.reason, Failed: sk.failed}
		}
		r.Blank()
		r.Problems(fmt.Sprintf("Skipped %s: nothing was changed", report.Count(int64(len(skips)))), view.Attention,
			ps)
	}
	if len(problems) > 0 {
		ps := make([]view.Problem, len(problems))
		for i, p := range problems {
			ps[i] = view.Problem{Reason: p.Error(), Failed: true}
		}
		r.Blank()
		r.Problems(fmt.Sprintf("Folders not removed: %s", report.Count(int64(len(problems)))), view.Attention, ps)
	}
	if renewed > 0 || left > 0 {
		r.Blank()
	}
	if renewed > 0 {
		r.Count(view.Neutral, report.Plural(int64(renewed), "file", "files")+" given a new retention")
	}
	if left > 0 {
		r.Count(view.Neutral, report.Plural(int64(left), "file", "files")+" left for later")
	}

	switch {
	case runErr != nil:
		return interrupted(runErr)
	case finishErr != nil:
		return finishErr
	case failed || len(problems) > 0:
		return errReported
	}
	return nil
}

// fileBytes is the space a candidate takes: its size if it is a regular file.
func fileBytes(c store.Candidate) int64 {
	if c.Kind == "file" {
		return c.Size
	}
	return 0
}

// interrupted words a cancelled run; other errors pass through.
func interrupted(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("interrupted; everything deleted so far is recorded")
	}
	return err
}

type action int

const (
	actDelete action = iota
	actRetention
	actSkip
	actQuit
)

// choice is select mode's answer besides delete, skip and quit: a new retention for a file past its retention. do
// carries it out at once; false (no error) asks again.
type choice struct {
	view.Choice
	act action
	do  func() (bool, error)
}

// choose asks what to do with one item in select mode, until the answer is one of delete, extra, skip or quit. The
// end of input is quit.
func (e *env) choose(ctx context.Context, pr *prompter, extra choice) (action, error) {
	choices := []view.Choice{{Key: "d", Word: "delete", Label: "delete"}, extra.Choice,
		{Key: "s", Word: "skip", Label: "skip"}, {Key: "q", Word: "quit", Label: "quit"}}
	for {
		ans, ok, err := pr.ask(ctx, func() { e.view.Choose(choices) })
		if err != nil {
			return actQuit, err
		}
		if !ok {
			return actQuit, nil
		}
		key, known := view.Pick(ans, choices, "")
		switch {
		case !known || key == "":
			e.view.Again(choices)
		case key == "d":
			return actDelete, nil
		case key == "s":
			return actSkip, nil
		case key == "q":
			return actQuit, nil
		default:
			done, err := extra.do()
			if err != nil || done {
				return extra.act, err
			}
		}
	}
}

// askRetention asks for a new retention until one is valid, and saves it. A blank answer goes back.
func (e *env) askRetention(ctx context.Context, pr *prompter, s *store.Store, c store.Candidate) (bool, error) {
	r := e.view
	question := "new retention: a period from file_date " + c.FileDate + " (such as 7y), forever, or YYYY-MM-DD; " +
		"blank to go back"
	if c.FileDate == "" {
		question = "new retention: forever or YYYY-MM-DD (a period needs a file_date, which this file has not got); " +
			"blank to go back"
	}
	for {
		ans, ok, err := pr.ask(ctx, func() { r.AskText(question) })
		if err != nil || !ok || ans == "" {
			return false, err
		}
		rt, err := retention.ParseRetention(ans)
		if err != nil {
			r.Result(view.Refused, fmt.Sprintf("%q is not a retention: use a period such as 7y, 18m, 6w or 30d, "+
				"forever, or a date YYYY-MM-DD", ans))
			continue
		}
		if _, err := store.KeepUntil(c.FileDate, rt); err != nil {
			r.Result(view.Refused, err.Error())
			continue
		}
		keep, err := s.SetRetention(ctx, c.FileID, rt, now())
		if err != nil {
			return false, err
		}
		if keep != store.Forever && keep <= today() {
			r.Result(view.Attention, "kept until "+keep+"; that is still past, so it will be listed again")
		} else {
			r.Result(view.Done, "kept until "+keep)
		}
		return true, nil
	}
}

// deleteList is what filo delete is about to delete, worked out once and drawn as often as needed: first as an
// overview, again in full for l or before a cut list's a.
type deleteList struct {
	f       store.Filter
	roots   []string       // the root of each file (Heading shows each once)
	expired []view.FileRow // past their retention
	exBytes int64
}

// buildDeleteList gathers the candidates past their retention.
func buildDeleteList(f store.Filter, cands []store.Candidate) *deleteList {
	l := &deleteList{f: f}
	for _, c := range cands {
		l.roots = append(l.roots, c.RootPath)
		l.exBytes += fileBytes(c)
		l.expired = append(l.expired, view.FileRow{Root: c.RootPath, Rel: c.Path,
			Cells: []string{c.KeepUntil, report.Size(c.Size)}})
	}
	return l
}

// draw writes the list. max > 0 shows that many entries and "… N more"; it reports whether anything was left out.
func (l *deleteList) draw(r *view.Renderer, max int) (cut bool) {
	r.Heading("Ready to delete", view.Where{Roots: l.roots, Under: l.f.Scope},
		report.Plural(int64(len(l.expired)), "file", "files"), report.Size(l.exBytes))
	r.Blank()
	r.Section("Past their retention", report.Plural(int64(len(l.expired)), "file", "files"), report.Size(l.exBytes))
	r.Files(view.FileList{Columns: []view.Column{{Header: "until"}, {Header: "size", Right: true}},
		Rows: l.expired, ByRoot: true, Max: max, More: "files", MoreOne: "file"})
	r.Blank()
	return max > 0 && len(l.expired) > max
}

// prompter asks questions on standard input. Each read runs in a goroutine, so Ctrl-C (a cancelled context) ends a
// wait for an answer.
type prompter struct {
	req   chan struct{}
	res   chan answer
	end   func()       // ends the question's line when no answer will be written after it
	check func() error // the output's first write error: a question that could not be shown is not asked
}

type answer struct {
	text string
	ok   bool // false at end of input
}

func newPrompter(in io.Reader) *prompter {
	p := &prompter{req: make(chan struct{}), res: make(chan answer, 1)}
	go func() {
		br := bufio.NewReader(in)
		for range p.req {
			line, err := br.ReadString('\n')
			if err != nil && line == "" {
				p.res <- answer{}
				continue
			}
			p.res <- answer{strings.TrimSpace(line), true}
		}
	}()
	return p
}

// close stops the reader once its current read, if any, returns.
func (p *prompter) close() { close(p.req) }

// ask shows the question (with show) and returns the answer, trimmed. ok is false at end of input. If the question
// could not be written (check reports an output error), it is not asked: the error is returned, so nothing is done
// on an answer to a question nobody saw.
func (p *prompter) ask(ctx context.Context, show func()) (string, bool, error) {
	show()
	if p.check != nil {
		if err := p.check(); err != nil {
			return "", false, err
		}
	}
	select {
	case p.req <- struct{}{}:
	case <-ctx.Done():
		p.newline()
		return "", false, ctx.Err()
	}
	select {
	case a := <-p.res:
		if !a.ok {
			p.newline()
		}
		return a.text, a.ok, nil
	case <-ctx.Done():
		p.newline()
		return "", false, ctx.Err()
	}
}

func (p *prompter) newline() {
	if p.end != nil {
		p.end()
	}
}
