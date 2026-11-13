package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
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
		return usageError{"delete takes at most one DIR"}
	}
	if err := checkTildes("DIR", pos...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	f, where, err := filterFor(ctx, s, pos)
	if err != nil {
		return err
	}
	cands, err := s.Candidates(ctx, today(), f)
	if err != nil {
		return err
	}

	w := e.stdout
	under := ""
	if where != "" {
		under = " under " + where
	}
	if len(cands) == 0 {
		fmt.Fprintf(w, "No files%s are past their retention.\n", under)
		return nil
	}
	var total int64
	for _, c := range cands {
		total += fileBytes(c)
	}
	fmt.Fprintf(w, "Past their retention%s: %s, %s\n", under, report.Plural(int64(len(cands)), "file", "files"),
		report.Size(total))
	for _, c := range cands {
		fmt.Fprintf(w, "  %s  %10s  %s\n", c.KeepUntil, report.Size(c.Size), c.AbsPath())
	}

	selecting := false
	var pr *prompter
	if !*yes {
		if !isTerminal(e.stdin) {
			fmt.Fprintln(w, "Nothing deleted: there is no terminal to ask in. Run filo delete in a terminal, or pass -y "+
				"to delete them all.")
			return nil
		}
		pr = newPrompter(e.stdin, w)
		defer pr.close()
		ans, _, err := pr.ask(ctx, "Delete [a]ll, [s]elect or [n]one? ")
		if err != nil {
			return interrupted(err)
		}
		switch strings.ToLower(ans) {
		case "a", "all":
		case "s", "select":
			selecting = true
		default:
			fmt.Fprintln(w, "Nothing deleted.")
			return nil
		}
	}

	run := s.BeginDeleteRun(now())
	d := dispose.New(run, now)
	type skipped struct{ path, reason string }
	var (
		deleted, renewed, left int
		freed                  int64
		skips                  []skipped
		failed                 bool
		runErr                 error
	)
files:
	for i, c := range cands {
		if selecting {
			fmt.Fprintf(w, "(%d/%d) %s  %s  %s\n", i+1, len(cands), c.KeepUntil, report.Size(c.Size), c.AbsPath())
			act, err := e.choose(ctx, pr, s, c)
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
				continue
			case actQuit:
				left += len(cands) - i
				break files
			}
		}
		o, err := d.Delete(ctx, c)
		if o.Deleted {
			deleted++
			freed += fileBytes(c)
		} else if err == nil {
			skips = append(skips, skipped{c.AbsPath(), o.Reason})
			failed = failed || o.Failed
		}
		if err != nil {
			runErr = err
			break
		}
	}
	removed, problems := d.PruneDirs()
	finishErr := run.Finish(context.WithoutCancel(ctx), now())

	fmt.Fprintf(w, "Deleted %s (%s).\n", report.Plural(int64(deleted), "file", "files"), report.Size(freed))
	if len(skips) > 0 {
		fmt.Fprintf(w, "Skipped %s:\n", report.Count(int64(len(skips))))
		for _, sk := range skips {
			fmt.Fprintf(w, "  %s: %s\n", sk.path, sk.reason)
		}
	}
	if renewed > 0 {
		fmt.Fprintf(w, "Gave %s a new retention.\n", report.Plural(int64(renewed), "file", "files"))
	}
	if left > 0 {
		fmt.Fprintf(w, "Left %s for later.\n", report.Plural(int64(left), "file", "files"))
	}
	if len(removed) > 0 {
		fmt.Fprintf(w, "Removed %s.\n", report.Plural(int64(len(removed)), "empty folder", "empty folders"))
	}
	for _, p := range problems {
		fmt.Fprintf(e.stderr, "filo: %s\n", p)
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

// choose asks what to do with one candidate in select mode. A new retention is saved at once.
func (e *env) choose(ctx context.Context, pr *prompter, s *store.Store, c store.Candidate) (action, error) {
	for {
		ans, ok, err := pr.ask(ctx, "[d]elete, [r]etention…, [s]kip, [q]uit? ")
		if err != nil {
			return actQuit, err
		}
		if !ok {
			return actQuit, nil
		}
		switch strings.ToLower(ans) {
		case "d", "delete":
			return actDelete, nil
		case "s", "skip":
			return actSkip, nil
		case "q", "quit":
			return actQuit, nil
		case "r", "retention":
			set, err := e.askRetention(ctx, pr, s, c)
			if err != nil || set {
				return actRetention, err
			}
		}
	}
}

// askRetention asks for a new retention until one is valid, and saves it. A blank answer goes back.
func (e *env) askRetention(ctx context.Context, pr *prompter, s *store.Store, c store.Candidate) (bool, error) {
	w := e.stdout
	prompt := "New retention: a period counted from file_date " + c.FileDate + " (such as 7y), forever, or a date " +
		"YYYY-MM-DD; blank to go back: "
	if c.FileDate == "" {
		prompt = "New retention: forever or a date YYYY-MM-DD (a period needs a file_date, which this file has not " +
			"got); blank to go back: "
	}
	for {
		ans, ok, err := pr.ask(ctx, prompt)
		if err != nil || !ok || ans == "" {
			return false, err
		}
		r, err := retention.ParseRetention(ans)
		if err != nil {
			fmt.Fprintf(w, "  %q is not a retention: use a period such as 7y, 18m, 6w or 30d, forever, or a date "+
				"YYYY-MM-DD\n", ans)
			continue
		}
		if _, err := store.KeepUntil(c.FileDate, r); err != nil {
			fmt.Fprintf(w, "  %s\n", err)
			continue
		}
		keep, err := s.SetRetention(ctx, c.FileID, r, now())
		if err != nil {
			return false, err
		}
		note := ""
		if keep != store.Forever && keep <= today() {
			note = "; that is still past, so it will be listed again"
		}
		fmt.Fprintf(w, "  kept until %s%s\n", keep, note)
		return true, nil
	}
}

// prompter asks questions on standard input. Each read runs in a goroutine, so Ctrl-C (a cancelled context) ends a
// wait for an answer.
type prompter struct {
	w   io.Writer
	req chan struct{}
	res chan answer
}

type answer struct {
	text string
	ok   bool // false at end of input
}

func newPrompter(in io.Reader, w io.Writer) *prompter {
	p := &prompter{w: w, req: make(chan struct{}), res: make(chan answer, 1)}
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

// ask prints prompt and returns the answer, trimmed. ok is false at end of input.
func (p *prompter) ask(ctx context.Context, prompt string) (string, bool, error) {
	fmt.Fprint(p.w, prompt)
	select {
	case p.req <- struct{}{}:
	case <-ctx.Done():
		fmt.Fprintln(p.w)
		return "", false, ctx.Err()
	}
	select {
	case a := <-p.res:
		if !a.ok {
			fmt.Fprintln(p.w)
		}
		return a.text, a.ok, nil
	case <-ctx.Done():
		fmt.Fprintln(p.w)
		return "", false, ctx.Err()
	}
}
