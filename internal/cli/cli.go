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
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/walk"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

const usage = `filo keeps a register of your files, where they live, and how long to keep them.

Usage:
  filo roots                        list roots and their settings
  filo roots add DIR [flags]        register a directory as a root, then scan it
  filo roots set DIR [flags]        replace a root's settings
  filo scan [DIR]                   scan every root, or DIR inside a root
  filo report [--top N]             totals, files needing a retention, largest files, duplicates, events
  filo export retention [DIR] -o FILE
                                    write every file under DIR (or every root) with its retention, as CSV
  filo update FILE [--dry-run]      apply the retentions edited in an exported CSV
  filo delete [DIR] [-y]            delete the files past their retention, after asking
  filo help [COMMAND]

Every command accepts --db PATH (default: $FILO_DB, then the user data directory).
`

var commandUsage = map[string]string{
	"roots": `Usage:
  filo roots
  filo roots add DIR [--include-hidden] [--ignore NAME]... [--no-scan]
  filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...

A root's settings apply to every scan of it, whole or partial:
  --include-hidden   also scan hidden files and directories (names starting with .)
  --skip-hidden      skip them again (roots set only; the default)
  --ignore NAME      skip entries with this name, at any depth (repeatable); node_modules, __pycache__ and .filo
                     are always skipped
roots add scans the new root straight away; --no-scan only registers it. roots set replaces the whole set, so
name every --ignore you want to keep.

A root that contains existing roots on the same filesystem takes them in, keeping their files' history and
retentions; its settings then apply to all of it. A directory inside an existing root is already covered:
scan it with filo scan DIR.
`,
	"scan": `Usage:
  filo scan [DIR] [--workers N] [--allow-empty]

Scans DIR, which must be inside a root (a partial scan), or every root if DIR is omitted, with each root's
settings (see filo help roots).
  --workers N        files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)
  --allow-empty      record an empty scan of a root on a mounted filesystem
`,
	"report": `Usage:
  filo report [--top N]

Shows each root's totals and last scan, the files needing a retention and the directories holding most of them, the largest files, duplicate groups with reclaimable space, broken symlinks, unreadable files and recent
events (moves and missing files among them). --top sets how many entries each list shows (default 10).
`,
	"export": `Usage:
  filo export retention [DIR] -o FILE

Writes every file under DIR (inside a root), or under every root, as CSV (UTF-8) to FILE, which must not exist.
Columns: id, export, directory, name, size, modified, file_date, retention, keep_until. Fill in retention in a
spreadsheet, then run filo update FILE. A blank file_date is filled in from the file name when the name holds one
clear date (2023-03-14, 20230314, or 2023-03 for the month's last day); check it. On a large system, export one
directory at a time.
`,
	"update": `Usage:
  filo update FILE [--dry-run]

Reads an exported retention CSV and applies the file_date and retention you changed. retention is a period counted
from file_date (7y, 18m, 6w, 30d), forever, or a date (YYYY-MM-DD); file_date is a date. keep_until is worked out
by filo: leave it alone. Dates in the past are fine: the file is then past its retention (see filo delete). Rows can
be sorted, filtered or deleted, but not added. A blank retention clears a stored one; filo lists those files and
asks first, and without a terminal leaves them alone. A row is refused, with one line saying why, if a value does
not parse, a period has no file_date, the file changed since the export, or its retention was changed from a later
export. The other rows are still applied. --dry-run checks everything and writes nothing.
`,
	"delete": `Usage:
  filo delete [DIR] [-y]

Lists the files past their retention under DIR (inside a root), or under every root, whose content has not changed
since the retention was set, then asks: Delete [a]ll, [s]elect or [n]one? Select goes file by file:
[d]elete, [r]etention (give it a new one), [s]kip or [q]uit. Without a terminal it only lists; -y deletes them all
without asking. Deletion is permanent: there is no recycle bin, so keep backups.

Just before deleting, each file is read again. It is deleted only if it is still at its path as the same file and
its content is still the content its retention was set on; otherwise it is skipped with one line saying why. A file
that cannot be read is never deleted. Folders the run emptied are removed, up to the root. Exit code 1 if a file
could not be read or deleted.
`,
}

// env is one invocation's surroundings.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer // through safeText: what filo prints can never act as terminal control codes
	rawStderr      io.Writer // for the progress line's own control codes, and to ask whether it is a terminal
	getenv         func(string) string
}

// usageError is reported with exit code 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// Run executes one command line (without the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	e := &env{stdin: stdin, stdout: safeText{stdout}, stderr: safeText{stderr}, rawStderr: stderr, getenv: getenv}
	err := e.run(ctx, args)
	var ue usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, flag.ErrHelp):
		return ExitOK
	case errors.As(err, &ue):
		fmt.Fprintf(e.stderr, "filo: %s\n", ue.msg)
		fmt.Fprint(e.stderr, "Run 'filo help' for usage.\n")
		return ExitUsage
	case errors.Is(err, errReported):
		return ExitError
	default:
		fmt.Fprintf(e.stderr, "filo: %s\n", err)
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
	case "export":
		return e.export(ctx, *db, rest)
	case "update":
		return e.update(ctx, *db, rest)
	case "delete":
		return e.delete(ctx, *db, rest)
	case "review":
		return usageError{"review was replaced by export retention and update; see filo help"}
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
	what := "--db"
	if db == "" {
		db, what = e.getenv("FILO_DB"), "FILO_DB"
	}
	if err := checkTilde(what, db); err != nil {
		return nil, err
	}
	return store.Open(ctx, store.Options{DBPath: db})
}

// closeStore closes the register. The run's work is already written, so a problem here (most likely a snapshot that
// could not be written) is a warning, one line on stderr.
func (e *env) closeStore(s *store.Store) {
	if err := s.Close(); err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintf(e.stderr, "filo: warning: %s\n", line)
		}
	}
}

// rootsSub finds a roots subcommand among args, so it may follow a flag (filo roots --db X add DIR), and returns it
// with the other arguments. Only the first positional argument can be one; "" means none.
func rootsSub(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", args
		}
		if strings.HasPrefix(a, "-") {
			if (a == "--db" || a == "-db") && i+1 < len(args) {
				i++ // its value
			}
			continue
		}
		return a, append(slices.Clone(args[:i]), args[i+1:]...)
	}
	return "", args
}

func (e *env) roots(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	switch sub {
	case "":
		return e.rootsList(ctx, dbBefore, rest)
	case "add":
		return e.rootsAdd(ctx, dbBefore, rest)
	case "set":
		return e.rootsSet(ctx, dbBefore, rest)
	}
	return usageError{fmt.Sprintf("unknown roots command %q", sub)}
}

func (e *env) rootsList(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots")
	db := dbFlag(fs, dbBefore)
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{"unknown roots command"}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	totals, err := s.RootTotals(ctx)
	if err != nil {
		return err
	}
	if len(totals) == 0 {
		fmt.Fprintln(e.stdout, "No roots yet. Add one with: filo roots add DIR")
		return nil
	}
	for _, t := range totals {
		fmt.Fprintf(e.stdout, "%s\n", t.Root.Path)
		fmt.Fprintf(e.stdout, "  settings: %s\n", settingsText(t.Root.Settings))
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
}

// settingsText describes a root's settings in a few words.
func settingsText(set store.Settings) string {
	parts := []string{"hidden files skipped"}
	if set.IncludeHidden {
		parts[0] = "hidden files included"
	}
	if len(set.Ignore) > 0 {
		parts = append(parts, "ignoring "+strings.Join(set.Ignore, ", "))
	}
	return strings.Join(parts, "; ")
}

// settingsFlags adds the root settings flags to fs. skip adds --skip-hidden too (roots set).
func settingsFlags(fs *flag.FlagSet, skip bool) (include, skipHidden *bool, ignore *stringList) {
	include = fs.Bool("include-hidden", false, "")
	if skip {
		skipHidden = fs.Bool("skip-hidden", false, "")
	}
	ignore = &stringList{}
	fs.Var(ignore, "ignore", "")
	return include, skipHidden, ignore
}

func checkIgnores(names []string) error {
	for _, n := range names {
		if err := walk.CheckIgnore(n); err != nil {
			return usageError{"--" + err.Error()}
		}
	}
	return nil
}

func (e *env) rootsAdd(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots add")
	db := dbFlag(fs, dbBefore)
	include, _, ignore := settingsFlags(fs, false)
	noScan := fs.Bool("no-scan", false, "")
	workers := fs.Int("workers", reconcile.DefaultWorkers, "")
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"roots add takes exactly one DIR"}
	}
	if err := checkTilde("DIR", pos[0]); err != nil {
		return err
	}
	if err := checkIgnores(*ignore); err != nil {
		return err
	}
	if *workers < 1 || *workers > hasher.MaxWorkers {
		return usageError{fmt.Sprintf("--workers must be between 1 and %d", hasher.MaxWorkers)}
	}
	set := store.Settings{IncludeHidden: *include, Ignore: *ignore}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	r, absorbed, err := s.AddRootAbsorb(ctx, pos[0], set)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "Added root %s (%s)\n", r.Path, settingsText(r.Settings))
	if len(absorbed) > 0 {
		var paths []string
		for _, a := range absorbed {
			paths = append(paths, a.Path)
		}
		fmt.Fprintf(e.stdout, "It now includes the %s %s, with %s history and keep_until dates.\n",
			pluralWord(len(absorbed), "root", "roots"), joinAnd(paths), pluralWord(len(absorbed), "its", "their"))
		for _, a := range absorbed {
			if skippedBy(a.Prefix, r.Settings) {
				fmt.Fprintf(e.stdout, "Note: %s is skipped by the settings of %s, so its files are no longer "+
					"scanned (neither checked nor marked missing). Change that with filo roots set %s.\n",
					a.Path, r.Path, r.Path)
			} else if !a.Settings.Equal(r.Settings) {
				fmt.Fprintf(e.stdout, "Note: %s had other settings (%s); the settings of %s apply to it now.\n",
					a.Path, settingsText(a.Settings), r.Path)
			}
		}
	}
	if r.MountPath != "" {
		fmt.Fprintf(e.stdout, "It is on the filesystem mounted at %s; scans are refused while that is not mounted.\n",
			r.MountPath)
	}
	if *noScan {
		fmt.Fprintf(e.stdout, "Scan it with: filo scan %s\n", r.Path)
		return nil
	}
	return e.scanTarget(ctx, s, reconcile.Options{Target: r.Path, Workers: *workers})
}

func (e *env) rootsSet(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots set")
	db := dbFlag(fs, dbBefore)
	include, skip, ignore := settingsFlags(fs, true)
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"roots set takes exactly one DIR"}
	}
	if *include && *skip {
		return usageError{"give --include-hidden or --skip-hidden, not both"}
	}
	if err := checkTilde("DIR", pos[0]); err != nil {
		return err
	}
	if err := checkIgnores(*ignore); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	set := store.Settings{IncludeHidden: *include, Ignore: *ignore}
	old, err := s.SetSettings(ctx, pos[0], set)
	if err != nil {
		return err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	for _, r := range roots {
		if r.ID != old.ID {
			continue
		}
		if r.Settings.Equal(old.Settings) {
			fmt.Fprintf(e.stdout, "%s: settings unchanged (%s)\n", r.Path, settingsText(r.Settings))
			return nil
		}
		fmt.Fprintf(e.stdout, "%s: settings were %s; now %s.\n", r.Path, settingsText(old.Settings),
			settingsText(r.Settings))
		fmt.Fprintln(e.stdout, "The next scan uses them. Files a scan skips are neither checked nor marked missing.")
	}
	return nil
}

// skippedBy reports whether a scan with set skips something on this '/'-separated path inside a root.
func skippedBy(prefix string, set store.Settings) bool {
	for _, name := range strings.Split(prefix, "/") {
		if walk.Skips(name, set.IncludeHidden, set.Ignore) {
			return true
		}
	}
	return false
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
	allowEmpty := fs.Bool("allow-empty", false, "")
	workers := fs.Int("workers", reconcile.DefaultWorkers, "")
	pos, err := e.parse(fs, "scan", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"scan takes at most one DIR"}
	}
	if err := checkTildes("DIR", pos...); err != nil {
		return err
	}
	if *workers < 1 || *workers > hasher.MaxWorkers {
		return usageError{fmt.Sprintf("--workers must be between 1 and %d", hasher.MaxWorkers)}
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)

	opt := reconcile.Options{Workers: *workers, AllowEmpty: *allowEmpty}
	if len(pos) == 1 {
		if opt.Target, err = filepath.Abs(pos[0]); err != nil {
			return err
		}
		return e.scanTarget(ctx, s, opt)
	}
	stop := e.startProgress(&opt)
	defer stop()

	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return errors.New("no roots yet; add one with: filo roots add DIR")
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

// scanTarget scans one directory with a progress line, and prints the summary.
func (e *env) scanTarget(ctx context.Context, s *store.Store, opt reconcile.Options) error {
	stop := e.startProgress(&opt)
	defer stop()
	start := time.Now()
	sum, err := reconcile.Scan(ctx, s, opt)
	stop()
	if err != nil {
		return err
	}
	e.printSummary(sum, time.Since(start))
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
	defer e.closeStore(s)
	r, err := report.Build(ctx, s, *top, today())
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
		{reconcile.SameAsDeleted, "same content as a file filo deleted", sum.SameAsDeleted},
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

// progressInterval is how often the progress line is redrawn.
var progressInterval = 500 * time.Millisecond

// startProgress shows a progress line on stderr while a scan runs, if stderr is a terminal. The returned function
// stops it and clears the line; it is safe to call more than once.
func (e *env) startProgress(opt *reconcile.Options) (stop func()) {
	if !isTerminal(e.rawStderr) {
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
					fmt.Fprint(e.rawStderr, "\r\x1b[K") // its own control codes; the line itself is escaped
					fmt.Fprint(e.stderr, line)
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
	if isTerminal(e.rawStderr) {
		fmt.Fprint(e.rawStderr, "\r\x1b[K")
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
