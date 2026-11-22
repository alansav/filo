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
	"sync"
	"sync/atomic"
	"time"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/rules"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

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
	if err := checkWorkers(fs, *workers, false); err != nil {
		return err
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)

	dir := ""
	if len(pos) == 1 {
		dir = pos[0]
	}
	return e.scanWhere(ctx, s, reconcile.Options{Workers: *workers, AllowEmpty: *allowEmpty}, dir)
}

// checkWorkers checks a command's --workers: 1 to hasher.MaxWorkers, and not given with --no-scan, which skips the
// scan it is for (filo scan, roots add, export).
func checkWorkers(fs *flag.FlagSet, workers int, noScan bool) error {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "workers" })
	if given && noScan {
		return usageError{"--workers is for the scan, and --no-scan skips it; give one or the other"}
	}
	if workers < 1 || workers > hasher.MaxWorkers {
		return usageError{fmt.Sprintf("--workers must be between 1 and %d", hasher.MaxWorkers)}
	}
	return nil
}

// errNoRoots is scanWhere's error for a register with no roots to scan.
var errNoRoots = errors.New("no roots yet; add one with: filo roots add DIR")

// scanWhere scans dir (inside a root) if it is not "", otherwise every root: filo scan, and the scan before an export.
func (e *env) scanWhere(ctx context.Context, s *store.Store, opt reconcile.Options, dir string) error {
	if dir != "" {
		target, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		opt.Target = target
		return e.scanTarget(ctx, s, opt)
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		return errNoRoots
	}
	return e.scanAll(ctx, s, opt, roots)
}

// runScan is reconcile.Scan; tests replace it to interrupt a scan at a chosen point.
var runScan = reconcile.Scan

// scanAll scans every root in turn, each in its own transaction and its own block of output (docs/design.md,
// Terminal output, Scan and roots). A root that fails is a block of its own on stdout, and the others are still
// scanned; the run then exits 1. Only cancellation stops it early.
func (e *env) scanAll(ctx context.Context, s *store.Store, opt reconcile.Options, roots []store.Root) error {
	v := e.view
	pr := e.startProgress(&opt)
	defer pr.stop()
	failed, attention := 0, false
	var failedRoots []string
	for i, r := range roots {
		o := opt
		o.Target = r.Path
		if len(roots) > 1 {
			pr.of(i+1, len(roots))
		}
		pr.label(r.Path)
		start := time.Now()
		sum, err := runScan(ctx, s, o)
		pr.clear()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if i > 0 {
			v.Blank()
		}
		if err != nil {
			v.Heading("Scan", view.Where{Roots: []string{r.Path}})
			if errors.Is(err, store.ErrNotMounted) && r.MountPath != "" {
				v.CountName(view.Refused, "not scanned: the root looks unmounted (", r.MountPath, " is not mounted)")
			} else {
				v.Count(view.Refused, "not scanned: "+err.Error())
			}
			failed++
			failedRoots = append(failedRoots, r.Path)
			continue
		}
		if renderScan(v, sum, time.Since(start)) {
			attention = true
		}
	}
	if len(roots) > 1 {
		v.Blank()
		facts := []string{report.Plural(int64(len(roots)), "root", "roots"),
			report.Count(int64(len(roots)-failed)) + " scanned"}
		if failed > 0 {
			facts = append(facts, report.Count(int64(failed))+" failed")
		}
		v.Outcome("Done", facts...)
	} else if attention || failed > 0 {
		v.Blank()
	}
	if attention || failed > 0 {
		v.Next("Next: filo report")
	}
	if failed > 0 {
		// The reason is on stdout, in its place; one line on stderr too, so a timer that keeps only stderr (filo
		// scan >/dev/null) still logs which roots failed.
		e.errorLine(report.Plural(int64(failed), "root", "roots") + " could not be scanned: " +
			strings.Join(failedRoots, ", "))
		return errReported
	}
	return nil
}

// scanTarget scans one directory (a root, or a directory inside one) with a progress line, and shows what it did.
func (e *env) scanTarget(ctx context.Context, s *store.Store, opt reconcile.Options) error {
	pr := e.startProgress(&opt)
	defer pr.stop()
	pr.label(opt.Target)
	start := time.Now()
	sum, err := runScan(ctx, s, opt)
	pr.stop()
	if err != nil {
		return err
	}
	if renderScan(e.view, sum, time.Since(start)) {
		e.view.Blank()
		e.view.Next("Next: filo report")
	}
	return nil
}

// scanKind is one kind of change a scan reports: its count line and its events.
type scanKind struct {
	event     string                      // its event kind; "" for new files, which have no events of their own
	mark      view.Mark                   // the count line's marker
	total     func(reconcile.Summary) int // how many this scan found
	text      func(n int) string          // the count line after the number; nil for none (counted with new)
	one, many string                      // the nouns for "… N more"
	change    bool                        // a change to the register; a directory on another filesystem is not one
}

func fixed(text string) func(int) string { return func(int) string { return text } }

// scanKinds are the kinds in the order their count lines and their events are shown.
var scanKinds = []scanKind{
	{"", view.Neutral, func(s reconcile.Summary) int { return s.New }, fixed("new"), "", "", true},
	{reconcile.Changed, view.Neutral, func(s reconcile.Summary) int { return s.Changed }, fixed("changed"),
		"changed", "changed", true},
	{reconcile.Moved, view.Neutral, func(s reconcile.Summary) int { return s.Moved }, fixed("moved"), "moved", "moved",
		true},
	{reconcile.Missing, view.Neutral, func(s reconcile.Summary) int { return s.Missing }, fixed("missing"),
		"missing", "missing", true},
	{reconcile.PossibleMove, view.Attention, func(s reconcile.Summary) int { return s.PossibleMoves },
		func(n int) string { return pluralWord(n, "possible move", "possible moves") + ", not applied" },
		"possible move", "possible moves", true},
	{reconcile.SameAsDeleted, view.Neutral, func(s reconcile.Summary) int { return s.SameAsDeleted }, nil,
		"with the content of a deleted file", "with the content of deleted files", false}, // counted with new
	{reconcile.CopyTags, view.Attention, func(s reconcile.Summary) int { return s.CopyTags },
		func(n int) string {
			return pluralWord(n, "new file", "new files") + " took the tags of copies that differed: check with filo show"
		},
		"that took differing tags", "that took differing tags", false}, // counted with new
	{reconcile.Error, view.Refused, func(s reconcile.Summary) int { return s.Errors },
		fixed("could not be read: none marked missing"),
		"error", "errors", true},
	{reconcile.OtherFS, view.Neutral, func(s reconcile.Summary) int { return len(s.OtherFS) },
		func(n int) string {
			return pluralWord(n, "directory", "directories") + " on another filesystem, not scanned"
		},
		"on another filesystem", "on other filesystems", false},
}

// renderScan shows one scan: its heading, a count line per kind found, and its events, the first
// reconcile.SampleSize of each kind with "… N more" for the rest. A scan that found nothing is two lines, the
// heading and "no changes". It reports whether anything changed, so the caller adds the next step; a directory on
// another filesystem is found by every scan and is not a change.
func renderScan(v *view.Renderer, sum reconcile.Summary, took time.Duration) bool {
	v.Heading("Scan", view.Where{Roots: []string{sum.Root}, Under: sum.Scope}, fmt.Sprintf("scan %d", sum.ScanID),
		report.Plural(int64(sum.Seen), "file seen", "files seen"), duration(took))
	changes, found := 0, 0
	var rows []view.FileRow
	for _, k := range scanKinds {
		n := k.total(sum)
		if k.change {
			changes += n
		}
		if k.text != nil {
			found += n
		}
		for _, ev := range sum.Samples[k.event] {
			rows = append(rows, report.EventRow(sum.Root, ev))
		}
	}
	set, ties := len(sum.Rules.Dated), len(sum.Rules.Ties)
	changes += set
	found += set + ties
	if len(rows) > 0 || set+ties > 0 {
		v.Blank() // a longer report: its counts and its sections are apart
	}
	if found == 0 {
		v.Count(view.Done, "no changes")
	}
	for _, k := range scanKinds {
		if n := k.total(sum); n > 0 && k.text != nil {
			v.Count(k.mark, report.Count(int64(n))+" "+k.text(n))
		}
	}
	if set > 0 {
		v.Count(view.Neutral, report.Count(int64(set))+" given a policy by rules")
	}
	if ties > 0 {
		v.Count(view.Attention, report.Count(int64(ties))+" not given a policy: "+report.TieWhy(sum.Rules.Ties))
	}
	renderRules(v, sum.Root, sum.Rules)
	changes += ties // not a change, but a warning: it gets the next step too
	if len(rows) > 0 {
		v.Blank()
		v.Section("Events", fmt.Sprintf("up to %d of each kind", reconcile.SampleSize))
		v.Files(view.FileList{Columns: []view.Column{{Header: "event"}}, Rows: rows})
		for _, k := range scanKinds {
			shown := len(sum.Samples[k.event])
			if n := k.total(sum) - shown; n > 0 && shown > 0 {
				v.More(n, k.one, k.many)
			}
		}
	}
	return changes > 0
}

// renderRules shows what a scan's directory rules did: the policies they set, a line per rule with its directory,
// pattern and policy lined up, and every file they tie on, with the rules that match it equally.
func renderRules(v *view.Renderer, root string, run store.RuleRun) {
	if len(run.Dated) > 0 {
		type tally struct {
			rule         store.Rule
			files, needs int64
		}
		var order []*tally
		byID := map[int64]*tally{}
		for _, d := range run.Dated {
			t, ok := byID[d.Rule.ID]
			if !ok {
				t = &tally{rule: d.Rule}
				byID[d.Rule.ID] = t
				order = append(order, t)
			}
			t.files++
			if d.NeedsFileDate() {
				t.needs++
			}
		}
		slices.SortFunc(order, func(a, b *tally) int { return rules.Compare(a.rule.Matcher(), b.rule.Matcher()) })
		rows := make([]view.TableRow, len(order))
		for i, t := range order {
			note := ""
			if t.needs > 0 {
				note = report.Plural(t.needs, "needs a file_date", "need a file_date")
			}
			rows[i] = view.TableRow{Name: ruleWhere(v, root, t.rule.Dir), Cells: []string{t.rule.Pattern,
				t.rule.Policy, report.Plural(t.files, "file", "files"), note}}
		}
		v.Blank()
		v.Section("Policies set by rules")
		v.HeadedTable([]string{"directory", "pattern", "policy"}, rows, false, false, true)
	}
	showRuleTies(v, run.Ties)
}

// duration shows how long a scan took: "under 0.1 s", "1.2 s", "3 min 12 s", "1 h 5 min". Each form is chosen
// after rounding to its own unit, so 59.97 s is "1 min 0 s", not "60.0 s".
func duration(d time.Duration) string {
	if d < 100*time.Millisecond {
		return "under 0.1 s"
	}
	if r := d.Round(100 * time.Millisecond); r < time.Minute {
		return fmt.Sprintf("%.1f s", r.Seconds())
	}
	if r := d.Round(time.Second); r < time.Hour {
		return fmt.Sprintf("%d min %d s", int(r.Minutes()), int(r.Seconds())%60)
	}
	r := d.Round(time.Minute)
	return fmt.Sprintf("%d h %d min", int(r.Hours()), int(r.Minutes())%60)
}

// progressInterval is how often the progress line is redrawn.
var progressInterval = 500 * time.Millisecond

// progress draws a scan's progress line on stderr while it runs (only when stderr is a terminal).
type progress struct {
	e        *env
	p        *reconcile.Progress // nil when stderr is not a terminal: every method does nothing
	n, total atomic.Int32        // "root n of total" when scanning every root
	where    atomic.Value        // the root or directory being scanned (string)
	mu       sync.Mutex          // a redraw and a clear never overlap
	done     chan struct{}
	finished chan struct{}
	once     sync.Once
}

// startProgress shows a progress line on stderr while a scan runs, if stderr is a terminal: it attaches a
// reconcile.Progress to opt. Stop it with stop, which is safe to call more than once.
func (e *env) startProgress(opt *reconcile.Options) *progress {
	pr := &progress{e: e}
	// Only on a terminal that redraws a line: not TERM=dumb, and not where the terminal cannot be checked
	// (Windows), since an older console shows the control codes as text.
	if !realTerminal(e.rawStderr) || !view.CanCheck || e.getenv("TERM") == "dumb" {
		return pr
	}
	pr.p = &reconcile.Progress{}
	opt.Progress = pr.p
	pr.done, pr.finished = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(pr.finished)
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-pr.done:
				return
			case <-t.C:
				pr.draw()
			}
		}
	}()
	return pr
}

// of sets "root n of total".
func (pr *progress) of(n, total int) { pr.n.Store(int32(n)); pr.total.Store(int32(total)) }

// label sets the root or directory the line names.
func (pr *progress) label(where string) { pr.where.Store(where) }

func (pr *progress) draw() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	where, _ := pr.where.Load().(string) // set by every caller before its scan
	of := ""
	if t := pr.total.Load(); t > 1 {
		of = fmt.Sprintf("root %d of %d", pr.n.Load(), t)
	}
	if line := progressLine(pr.p, of, pr.e.view.Tilde(where)); line != "" {
		// Its own control codes, then the line, escaped and cut to the width in one write: a line that wrapped
		// could not be cleared.
		io.WriteString(pr.e.rawStderr, "\r\x1b[K"+view.Fit(line, pr.width()))
	}
}

// width is stderr's width now (the window may have been resized since the last redraw), or view.DefaultWidth when
// it cannot be read: the line is always cut, since one that wrapped could not be cleared.
func (pr *progress) width() int {
	if f, ok := pr.e.rawStderr.(*os.File); ok && view.CanCheck {
		if cols, _ := view.Size(f); cols > 0 {
			return cols
		}
	}
	return view.DefaultWidth
}

// clear clears the line, so output can follow; the next redraw comes with the next scan's phase.
func (pr *progress) clear() {
	if pr.p == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	io.WriteString(pr.e.rawStderr, "\r\x1b[K")
}

// stop stops the redraws and clears the line.
func (pr *progress) stop() {
	if pr.p == nil {
		return
	}
	pr.once.Do(func() {
		close(pr.done)
		<-pr.finished
		pr.clear()
	})
}

// progressLine is the progress line for p's phase, numbers first and where last, so cutting it to the width costs
// only the end of the path: "hashing · 1,204 / 52,310 files · 3.0 / 118 GiB · root 2 of 3 · ~/Pictures". "" when idle.
func progressLine(p *reconcile.Progress, of, where string) string {
	var parts []string
	switch p.Phase.Load() {
	case reconcile.PhaseWalking:
		parts = []string{"walking", report.Plural(p.Walked.Load(), "entry", "entries")}
	case reconcile.PhaseHashing:
		parts = []string{"hashing",
			report.Count(p.Hash.Files.Load()) + " / " + report.Count(p.HashFiles.Load()) + " files",
			sizePair(p.Hash.Bytes.Load(), p.HashBytes.Load())}
	case reconcile.PhaseWriting:
		parts = []string{"writing the register"}
	default:
		return ""
	}
	if of != "" {
		parts = append(parts, of)
	}
	return strings.Join(append(parts, where), " · ")
}

// sizePair shows done / total, with the unit once when both have the same one: "3.0 / 118 GiB".
func sizePair(done, total int64) string {
	a, b := report.Size(done), report.Size(total)
	if i, j := strings.LastIndexByte(a, ' '), strings.LastIndexByte(b, ' '); i > 0 && j > 0 && a[i:] == b[j:] {
		a = a[:i]
	}
	return a + " / " + b
}
