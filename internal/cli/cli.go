// Package cli is Filo's command line: argument parsing, dispatch and printing. cmd/filo only calls Run.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

const usage = `filo keeps a register of your files and where they live.

Usage:
  filo roots                    list roots
  filo roots add PATH           register a directory as a root
  filo scan [PATH] [flags]      scan PATH (inside a root), or every root
  filo report [--top N]         totals, largest files, duplicates, broken symlinks, recent events
  filo help [COMMAND]

Every command accepts --db PATH (default: $FILO_DB, then the user data directory).
`

var commandUsage = map[string]string{
	"roots": `Usage:
  filo roots [--db PATH]            list roots
  filo roots add PATH [--db PATH]   register a directory as a root
`,
	"scan": `Usage:
  filo scan [PATH] [flags]

Scans PATH, which must be inside a root (a partial scan), or every root if PATH is omitted.
Flags:
  --include-hidden   also scan hidden files and directories
  --ignore NAME      skip entries with this name, at any depth (repeatable)
  --workers N        files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)
  --allow-empty      record an empty scan of a root on a mounted filesystem
  --db PATH          the register to use
`,
	"report": `Usage:
  filo report [--top N] [--db PATH]

Shows each root's totals and last scan, the largest files, duplicate groups with reclaimable space, broken symlinks
and recent events. --top sets how many entries each list shows (default 10).
`,
}

// env is one invocation's surroundings.
type env struct {
	stdout, stderr io.Writer
	getenv         func(string) string
}

// usageError is reported with exit code 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run executes one command line (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	e := &env{stdout: stdout, stderr: stderr, getenv: getenv}
	err := e.run(ctx, args)
	var ue usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "filo: %s\n", ue.msg)
		fmt.Fprint(stderr, "Run 'filo help' for usage.\n")
		return ExitUsage
	case errors.Is(err, errReported):
		return ExitError
	default:
		fmt.Fprintf(stderr, "filo: %s\n", err)
		return ExitError
	}
}

// errReported means the details were already printed.
var errReported = errors.New("reported")

func (e *env) run(ctx context.Context, args []string) error {
	global := e.flags("filo")
	db := global.String("db", "", "")
	pos, err := parseLeading(global, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(e.stdout, usage)
		return err
	}
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		fmt.Fprint(e.stdout, usage)
		return nil
	}
	cmd, rest := pos[0], pos[1:]
	switch cmd {
	case "help", "-h", "--help":
		if len(rest) > 0 {
			if u, ok := commandUsage[rest[0]]; ok {
				fmt.Fprint(e.stdout, u)
				return nil
			}
			return usageError{fmt.Sprintf("no help for %q", rest[0])}
		}
		fmt.Fprint(e.stdout, usage)
		return nil
	case "roots":
		return e.roots(ctx, *db, rest)
	case "scan":
		return e.scan(ctx, *db, rest)
	case "report":
		return e.report(ctx, *db, rest)
	default:
		return usageError{fmt.Sprintf("unknown command %q", cmd)}
	}
}

// flags makes a FlagSet that reports problems as usage errors instead of printing.
func (e *env) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseLeading parses flags that come before the command, leaving the command and everything after it.
func parseLeading(fs *flag.FlagSet, args []string) ([]string, error) {
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
	if db == "" {
		db = e.getenv("FILO_DB")
	}
	return store.Open(ctx, store.Options{DBPath: db})
}

func (e *env) roots(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots")
	db := dbFlag(fs, dbBefore)
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		s, err := e.open(ctx, *db)
		if err != nil {
			return err
		}
		defer s.Close()
		totals, err := s.RootTotals(ctx)
		if err != nil {
			return err
		}
		if len(totals) == 0 {
			fmt.Fprintln(e.stdout, "No roots yet. Add one with: filo roots add PATH")
			return nil
		}
		for _, t := range totals {
			fmt.Fprintf(e.stdout, "%s\n", t.Root.Path)
			if t.Root.MountPath != "" {
				fmt.Fprintf(e.stdout, "  mounted filesystem: %s\n", t.Root.MountPath)
			}
			last := "never scanned"
			if t.LastScan != nil {
				last = "last scan " + report.Time(t.LastScan.FinishedUTC)
			}
			fmt.Fprintf(e.stdout, "  %s, %s; %s\n", report.Plural(t.Files, "file", "files"), report.Size(t.Bytes), last)
		}
		return nil
	case pos[0] == "add" && len(pos) == 2:
		s, err := e.open(ctx, *db)
		if err != nil {
			return err
		}
		defer s.Close()
		r, err := s.AddRoot(ctx, pos[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "Added root %s\n", r.Path)
		if r.MountPath != "" {
			fmt.Fprintf(e.stdout, "It is on the filesystem mounted at %s; scans are refused while that is not mounted.\n",
				r.MountPath)
		}
		fmt.Fprintf(e.stdout, "Scan it with: filo scan %s\n", r.Path)
		return nil
	case pos[0] == "add":
		return usageError{"roots add takes exactly one PATH"}
	default:
		return usageError{fmt.Sprintf("unknown roots command %q", pos[0])}
	}
}

// parse runs parseInterspersed and prints the command's usage for -h.
func (e *env) parse(fs *flag.FlagSet, cmd string, args []string) ([]string, error) {
	pos, err := parseInterspersed(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(e.stdout, commandUsage[cmd])
	}
	return pos, err
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func (e *env) scan(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("scan")
	db := dbFlag(fs, dbBefore)
	includeHidden := fs.Bool("include-hidden", false, "")
	allowEmpty := fs.Bool("allow-empty", false, "")
	workers := fs.Int("workers", reconcile.DefaultWorkers, "")
	var ignore stringList
	fs.Var(&ignore, "ignore", "")
	pos, err := e.parse(fs, "scan", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"scan takes at most one PATH"}
	}
	if *workers < 1 || *workers > hasher.MaxWorkers {
		return usageError{fmt.Sprintf("--workers must be between 1 and %d", hasher.MaxWorkers)}
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()

	opt := reconcile.Options{IncludeHidden: *includeHidden, Ignore: ignore, Workers: *workers, AllowEmpty: *allowEmpty}
	stop := e.startProgress(&opt)
	defer stop()

	if len(pos) == 1 {
		target, err := filepath.Abs(pos[0])
		if err != nil {
			return err
		}
		opt.Target = target
		start := time.Now()
		sum, err := reconcile.Scan(ctx, s, opt)
		stop()
		if err != nil {
			return err
		}
		e.printSummary(sum, time.Since(start))
		return nil
	}

	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return errors.New("no roots yet; add one with: filo roots add PATH")
	}
	failed := false
	for _, r := range roots {
		o := opt
		o.Target = r.Path
		start := time.Now()
		sum, err := reconcile.Scan(ctx, s, o)
		e.clearProgress()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(e.stderr, "filo: %s: %s\n", r.Path, err)
			failed = true
			continue
		}
		e.printSummary(sum, time.Since(start))
	}
	if failed {
		return errReported
	}
	return nil
}

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
	defer s.Close()
	r, err := report.Build(ctx, s, *top)
	if err != nil {
		return err
	}
	return r.WriteText(e.stdout)
}

// printSummary prints a scan's counts and samples (design section 4, step 9).
func (e *env) printSummary(sum reconcile.Summary, took time.Duration) {
	w := e.stdout
	where := sum.Root
	if sum.Scope != "" {
		where = filepath.Join(sum.Root, filepath.FromSlash(sum.Scope))
	}
	fmt.Fprintf(w, "%s: %s seen in %s (scan %d)\n", where, report.Plural(int64(sum.Seen), "file", "files"),
		took.Round(10*time.Millisecond), sum.ScanID)
	fmt.Fprintf(w, "  new %s   changed %s   moved %s   missing %s   possible moves %s   errors %s\n",
		report.Count(int64(sum.New)), report.Count(int64(sum.Changed)), report.Count(int64(sum.Moved)),
		report.Count(int64(sum.Missing)), report.Count(int64(sum.PossibleMoves)), report.Count(int64(sum.Errors)))
	sections := []struct {
		kind, title string
		total       int
	}{
		{reconcile.Moved, "moved", sum.Moved},
		{reconcile.Changed, "changed", sum.Changed},
		{reconcile.Missing, "missing", sum.Missing},
		{reconcile.PossibleMove, "possible moves (not applied)", sum.PossibleMoves},
		{reconcile.Error, "errors", sum.Errors},
		{reconcile.OtherFS, "other filesystems, not scanned", len(sum.OtherFS)},
	}
	for _, sec := range sections {
		samples := sum.Samples[sec.kind]
		if len(samples) == 0 {
			continue
		}
		fmt.Fprintf(w, "  %s:\n", sec.title)
		for _, ev := range samples {
			fmt.Fprintf(w, "    %s\n", report.EventText(ev))
		}
		if sec.total > len(samples) {
			fmt.Fprintf(w, "    and %s more; see filo report\n", report.Count(int64(sec.total-len(samples))))
		}
	}
}

// terminal is implemented by writers that know whether they are a terminal (tests use it).
type terminal interface{ IsTerminal() bool }

func isTerminal(w io.Writer) bool {
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

// progressInterval is how often the progress line is redrawn.
var progressInterval = 500 * time.Millisecond

// startProgress shows a progress line on stderr while a scan runs, if stderr is a terminal. The returned function
// stops it and clears the line; it is safe to call more than once.
func (e *env) startProgress(opt *reconcile.Options) (stop func()) {
	if !isTerminal(e.stderr) {
		return func() {}
	}
	p := &reconcile.Progress{}
	opt.Progress = p
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if line := progressLine(p); line != "" {
					fmt.Fprintf(e.stderr, "\r\x1b[K%s", line)
				}
			}
		}
	}()
	var stopped bool
	return func() {
		if !stopped {
			stopped = true
			close(done)
			<-finished
			e.clearProgress()
		}
	}
}

func (e *env) clearProgress() {
	if isTerminal(e.stderr) {
		fmt.Fprint(e.stderr, "\r\x1b[K")
	}
}

func progressLine(p *reconcile.Progress) string {
	root := p.Root()
	switch p.Phase.Load() {
	case reconcile.PhaseWalking:
		return fmt.Sprintf("scanning %s: walking, %s entries listed", root, report.Count(p.Walked.Load()))
	case reconcile.PhaseHashing:
		return fmt.Sprintf("scanning %s: hashing %s / %s files, %s / %s", root,
			report.Count(p.Hash.Files.Load()), report.Count(p.HashFiles.Load()),
			report.Size(p.Hash.Bytes.Load()), report.Size(p.HashBytes.Load()))
	case reconcile.PhaseWriting:
		return fmt.Sprintf("scanning %s: writing the register", root)
	}
	return ""
}
