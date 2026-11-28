// Package cli is Filo's command line: argument parsing, dispatch and printing. cmd/filo only calls Run.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// env is one invocation's surroundings.
type env struct {
	out       *firstError // stdout, keeping its first write error
	stdin     io.Reader
	view      *view.Renderer // stdout: the terminal output components (docs/design.md, Terminal output)
	rawStderr io.Writer      // stderr: only through errorLine, and the progress line (scan.go)
	getenv    func(string) string
	cmd       string // the command run, for a usage error's pointer to its help
	// indexesDone: the command wrote or reported the tag index pages itself (filo indexes), so the end of the run
	// does not; indexFailed: an index was not brought up to date at the end of the run, which then exits 1.
	indexesDone, indexFailed bool
}

// usageError is reported with exit code 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run executes one command line (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	// Every command's output goes through out, which keeps the first write error: output that did not get through
	// (a full disk, a closed pipe) is not success.
	out := &firstError{w: stdout}
	e := &env{out: out, stdin: stdin, view: newView(stdout, out, getenv), rawStderr: stderr, getenv: getenv}
	err := e.run(ctx, args)
	if err == nil && out.err != nil {
		err = fmt.Errorf("write output: %w", out.err)
	}
	if err == nil && e.indexFailed {
		err = errReported // the line is written already (refreshIndexes)
	}
	var ue usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &ue):
		see := "filo help"
		if _, ok := commandHelps[e.cmd]; ok {
			see += " " + e.cmd
		}
		e.errorLine(ue.msg + " (see " + see + ")")
		return ExitUsage
	case errors.Is(err, errReported):
		return ExitError
	default:
		e.errorLine(err.Error())
		return ExitError
	}
}

// errorLine writes text to stderr as one line, plain (docs/design.md, Terminal output, Help and errors): "filo:
// <text>", escaped with view.Escape (a newline in it too, since a file name can hold one) and written in one write.
// Filo's own errors of several parts are split by the caller, part by part (closeStore). It and the progress line
// are the only writers to stderr (TestOutputGoesThroughTheRenderer).
func (e *env) errorLine(text string) {
	io.WriteString(e.rawStderr, "filo: "+view.Escape(text)+"\n")
}

// newView makes the Renderer, writing to out (stdout through firstError), and judging from stdout itself: rich and as wide as the terminal when stdout is one (colour unless NO_COLOR
// is set or TERM is dumb), plain otherwise. A character device that is not a terminal is plain too: reading the
// window size tells them apart. Where it cannot be read (Windows), a terminal stays one, without colour.
func newView(stdout, out io.Writer, getenv func(string) string) *view.Renderer {
	o := view.Options{Out: out, Terminal: isTerminal(stdout), Home: getenv("HOME")}
	color := true
	if f, ok := stdout.(*os.File); ok && o.Terminal {
		if view.CanCheck {
			o.Width, o.Terminal = view.Size(f)
		} else {
			color = false // a terminal, but an older console would show colour codes as text
		}
	}
	o.Color = color && o.Terminal && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
	return view.New(o)
}

// outputErr is stdout's first write error: a command checks it before
// acting on what it showed (a question, a deletion).
func (e *env) outputErr() error {
	if e.out.err != nil {
		return fmt.Errorf("write output: %w", e.out.err)
	}
	return e.view.Err()
}

// firstError passes writes to w until one fails, keeps that error, and writes nothing after it.
type firstError struct {
	w   io.Writer
	err error
}

func (f *firstError) Write(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	n, err := f.w.Write(p)
	if err != nil {
		f.err = err
	}
	return n, err
}

// errReported means the details were already printed.
var errReported = errors.New("reported")

func (e *env) run(ctx context.Context, args []string) error {
	global := e.flags("filo")
	db := global.String("db", "", "")
	pos, err := parseLeading(global, args)
	if errors.Is(err, flag.ErrHelp) {
		showOverview(e.view)
		return err
	}
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		showOverview(e.view)
		return nil
	}
	cmd, rest := pos[0], pos[1:]
	e.cmd = cmd
	switch cmd {
	case "help", "-h", "--help":
		if len(rest) > 0 {
			if h, ok := commandHelps[rest[0]]; ok {
				showCommandHelp(e.view, h)
				return nil
			}
			return usageError{fmt.Sprintf("no help for %q", rest[0])}
		}
		showOverview(e.view)
		return nil
	case "roots":
		return e.roots(ctx, *db, rest)
	case "scan":
		return e.scan(ctx, *db, rest)
	case "report":
		return e.report(ctx, *db, rest)
	case "export":
		return e.export(ctx, *db, rest)
	case "update":
		return e.update(ctx, *db, rest)
	case "delete":
		return e.delete(ctx, *db, rest)
	case "duplicates":
		return e.duplicates(ctx, *db, rest)
	case "policies":
		return e.policies(ctx, *db, rest)
	case "rules":
		return e.rules(ctx, *db, rest)
	case "show":
		return e.show(ctx, *db, rest)
	case "set":
		return e.set(ctx, *db, rest)
	case "get":
		return e.get(ctx, *db, rest)
	case "find":
		return e.find(ctx, *db, rest)
	case "tag":
		return e.tag(ctx, *db, rest)
	case "untag":
		return e.untag(ctx, *db, rest)
	case "tags":
		return e.tags(ctx, *db, rest)
	case "indexes":
		return e.indexes(ctx, *db, rest)
	case "review":
		return usageError{"review was replaced by export and update; see filo help"}
	default:
		return usageError{fmt.Sprintf("unknown command %q", cmd)}
	}
}

// flagSetMade, if set by a test, is told of every flag set a command makes, once its flags are defined (when it is
// parsed): TestHelpListsTheRealFlags compares them with the help.
var flagSetMade func(*flag.FlagSet)

// flags makes a FlagSet that reports problems as usage errors instead of printing.
func (e *env) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseLeading parses flags that come before the command, leaving the command and everything after it.
func parseLeading(fs *flag.FlagSet, args []string) ([]string, error) {
	if flagSetMade != nil {
		flagSetMade(fs)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, usageError{err.Error()}
	}
	return fs.Args(), nil
}

// parseInterspersed parses flags anywhere among the positional arguments, so `filo scan PATH --include-hidden`
// works. Everything after "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	if flagSetMade != nil {
		flagSetMade(fs)
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{err.Error()}
		}
		rest := fs.Args()
		// fs.Parse stops at the first non-flag, or consumes "--" and stops after it.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// dbFlag adds --db to a command's flags, defaulting to the value given before the command.
func dbFlag(fs *flag.FlagSet, before string) *string {
	return fs.String("db", before, "")
}

func (e *env) open(ctx context.Context, db string) (*store.Store, error) {
	what := "--db"
	if db == "" {
		db, what = e.getenv("FILO_DB"), "FILO_DB"
	}
	if err := checkTilde(what, db); err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, store.Options{DBPath: db})
	if err != nil {
		return nil, err
	}
	// What the schema upgrade this run made dropped or changed, said once (docs/design.md, Duplicates, Register).
	for _, note := range s.UpgradeNotes() {
		e.errorLine("warning: " + note)
	}
	return s, nil
}

// closeStore closes the register, first bringing the tag indexes up to date if the run changed it (before the
// snapshot, which then holds what was written). The run's work is already written, so a problem with the snapshot is
// a warning, one line on stderr; an index not brought up to date makes the run exit 1.
func (e *env) closeStore(s *store.Store) {
	e.refreshIndexes(context.Background(), s) // the run's change is committed: its pages follow, even after Ctrl-C
	if err := s.Close(); err != nil {
		for _, p := range joinedParts(err) {
			e.errorLine("warning: " + p.Error())
		}
	}
}

// joinedParts splits an error made by errors.Join, at any depth, into its parts: a warning line each.
func joinedParts(err error) []error {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []error{err}
	}
	var out []error
	for _, p := range joined.Unwrap() {
		out = append(out, joinedParts(p)...)
	}
	return out
}

// parse runs parseInterspersed and prints the command's usage for -h.
func (e *env) parse(fs *flag.FlagSet, cmd string, args []string) ([]string, error) {
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		showCommandHelp(e.view, commandHelps[cmd])
	}
	return pos, err
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func (e *env) report(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("report")
	db := dbFlag(fs, dbBefore)
	top := fs.Int("top", 10, "")
	pos, err := e.parse(fs, "report", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{"report takes no arguments"}
	}
	if *top < 1 {
		return usageError{"--top must be at least 1"}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	r, err := report.Build(ctx, s, *top, today())
	if err != nil {
		return err
	}
	r.Render(e.view)
	return nil
}

// terminal is implemented by writers that know whether they are a terminal (tests use it).
type terminal interface{ IsTerminal() bool }

func isTerminal(w any) bool {
	if t, ok := w.(terminal); ok {
		return t.IsTerminal()
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// realTerminal reports whether f is a terminal, and, where the window size can be read, one whose size can (a
// character device such as /dev/null is not). Where it cannot be read (Windows), a terminal is trusted. A question is
// asked only on one, and the progress line drawn only on one.
func realTerminal(f any) bool {
	if !isTerminal(f) {
		return false
	}
	if file, ok := f.(*os.File); ok && view.CanCheck {
		_, terminal := view.Size(file)
		return terminal
	}
	return true
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// joinAnd joins items as "a", "a and b", or "a, b and c".
func joinAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
