package cli

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/filecsv"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// now is the clock for keep_until dates; tests replace it.
var now = time.Now

func today() string { return retention.Today(now().Local()) }

// exportKinds are the CSVs filo can export besides the combined one. Each names what the CSV is for.
var exportKinds = []string{"duplicates"}

// filterFor turns the DIR argument, if any, into a filter on the register and the place a heading names: the root
// DIR is in and the directory under it, or every root when there is no DIR.
func filterFor(ctx context.Context, s *store.Store, pos []string) (store.Filter, view.Where, error) {
	if len(pos) == 0 {
		roots, err := s.Roots(ctx)
		if err != nil {
			return store.Filter{}, view.Where{}, err
		}
		var w view.Where
		for _, r := range roots {
			w.Roots = append(w.Roots, r.Path)
		}
		return store.Filter{}, w, nil
	}
	root, scope, err := s.Resolve(ctx, pos[0])
	if err != nil {
		return store.Filter{}, view.Where{}, err
	}
	return store.Filter{RootID: root.ID, Scope: scope}, view.Where{Roots: []string{root.Path}, Under: scope}, nil
}

func (e *env) export(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("export")
	db := dbFlag(fl, dbBefore)
	out := fl.String("o", "", "")
	noScan := fl.Bool("no-scan", false, "")
	workers := fl.Int("workers", reconcile.DefaultWorkers, "")
	pos, err := e.parse(fl, "export", args)
	if err != nil {
		return err
	}
	// No word is the combined CSV (docs/design.md, Tags, "The combined CSV"); a directory named like an export is
	// given as ./duplicates.
	kind := ""
	if len(pos) > 0 && slices.Contains(exportKinds, pos[0]) {
		kind, pos = pos[0], pos[1:]
	}
	if len(pos) > 1 {
		return usageError{strings.TrimSpace("export "+kind) + " takes at most one DIR"}
	}
	if err := checkWorkers(fl, *workers, *noScan); err != nil {
		return err
	}
	if *out == "" {
		return usageError{"export needs -o FILE: the CSV is written to a file, never to the terminal"}
	}
	if err := checkTildes("DIR", pos...); err != nil {
		return err
	}
	if err := checkTilde("-o", *out); err != nil {
		return err
	}
	csvPath, err := filepath.Abs(*out)
	if err != nil {
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
	// Asked once the command is known to be able to run, and before any work, so a no costs nothing (docs/design.md,
	// Lifespan).
	asked, ok, err := e.askReplace(ctx, csvPath, s.DBPath())
	if err != nil || !ok {
		return err
	}
	// A scan first, so the CSV holds the files as they are now (docs/design.md, Lifespan, "A scan first").
	var notScanned string
	if *noScan {
		held := f // a combined export holds DIR; a duplicates export every root (a group's copies can be anywhere)
		if kind == "duplicates" {
			held = store.Filter{}
		}
		if notScanned, err = lastScanLine(ctx, s, held); err != nil {
			return err
		}
	} else {
		scanned, err := e.scanFirst(ctx, s, kind, pos, *workers)
		if err != nil {
			return err
		}
		if scanned {
			e.view.Blank()
		}
	}
	if kind == "duplicates" {
		return e.exportDuplicates(ctx, s, f, where, csvPath, asked, notScanned)
	}
	return e.exportFiles(ctx, s, f, where, csvPath, asked, notScanned)
}

// markNameDates fills in each blank file_date of the n items (item(i) is the i-th) from the file name where it holds
// one unambiguous date, marked (!2023-07-14) so it counts only once the person removes the mark, and returns how many
// it filled in. The export records them as written, so an untouched one is no change.
func markNameDates(n int, item func(int) *store.FileItem) int {
	suggested := 0
	names := retention.NewDateReader(today()) // one day for the whole export
	for i := range n {
		it := item(i)
		if it.FileDate == "" {
			if d := names.Read(path.Base(it.Path)); d != "" {
				it.FileDate = store.MarkedDate(d)
				suggested++
			}
		}
	}
	return suggested
}

// markedLine is the line an export shows when it read file_dates from the file names.
func markedLine(v *view.Renderer, suggested int) {
	if suggested > 0 {
		v.Count(view.Attention, report.Plural(int64(suggested), "file_date", "file_dates")+
			" read from the file name, marked !: remove the ! "+pluralWord(suggested, "to accept it",
			"from each one you accept"))
	}
}

// scanFirst is an export's scan (docs/design.md, Lifespan, "A scan first"): a combined export of DIR scans DIR; a
// duplicates export scans every root, since a duplicate group's copies can be anywhere. Its output is exactly filo
// scan's, Next: filo report included when it found something, so nothing it found is hidden. A root that fails to
// scan stops the export, so a missing disk never gives a CSV that looks complete. scanned is false when there was
// nothing to scan (no roots), so nothing was shown.
func (e *env) scanFirst(ctx context.Context, s *store.Store, kind string, pos []string, workers int) (scanned bool,
	err error) {
	dir := ""
	if kind != "duplicates" && len(pos) == 1 {
		dir = pos[0]
	}
	err = e.scanWhere(ctx, s, reconcile.Options{Workers: workers}, dir)
	switch {
	case errors.Is(err, errNoRoots):
		return false, nil
	case err == nil:
		return true, nil
	case ctx.Err() != nil: // whatever error the interrupted scan returned (SQLite has its own)
		return false, errors.New("interrupted; nothing was exported")
	case errors.Is(err, errReported): // the failed roots are on stdout, and named on stderr by scanAll
		return false, errors.New("nothing exported: a root could not be scanned; fix it, or export with --no-scan " +
			"from the register as it is")
	}
	return false, fmt.Errorf("nothing exported: %w", err)
}

// lastScanLine is the line an export or filo find with --no-scan shows: what it shows is as of the last scan that
// covered the place f names, a directory in a root or every root (the oldest of them, when there are several), or
// says that one was never scanned. A scan of a directory inside a root covers only that directory.
func lastScanLine(ctx context.Context, s *store.Store, f store.Filter) (string, error) {
	roots, err := s.Roots(ctx)
	if err != nil {
		return "", err
	}
	oldest := ""
	for _, r := range roots {
		scope := ""
		if f.RootID != 0 {
			if r.ID != f.RootID {
				continue
			}
			scope = f.Scope
		}
		finished, err := s.LastScanCovering(ctx, r.ID, scope)
		if err != nil {
			return "", err
		}
		if finished == "" {
			return "not scanned first, and " + r.Path + " has never been scanned whole", nil
		}
		if oldest == "" || report.Time(finished) < report.Time(oldest) {
			oldest = finished
		}
	}
	if oldest == "" {
		return "", nil
	}
	return "not scanned first: as of the last scan, " + report.Time(oldest), nil
}

// notScannedLine shows lastScanLine's line, if there is one.
func notScannedLine(v *view.Renderer, line string) {
	if line != "" {
		v.Count(view.Neutral, line)
	}
}

func (e *env) update(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("update")
	db := dbFlag(fs, dbBefore)
	dry := fs.Bool("dry-run", false, "")
	yes := fs.Bool("y", false, "")
	pos, err := e.parse(fs, "update", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"update takes exactly one FILE"}
	}
	if err := checkTilde("FILE", pos[0]); err != nil {
		return err
	}
	path, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	data, err := sheet.ReadFile(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	// No decisions to delete are stored any more (docs/design.md, Duplicates): a duplicates CSV is carried out by
	// filo delete, which checks it against the disk at the time.
	if duplicates.IsDuplicatesCSV(data) {
		return fmt.Errorf("%s is a duplicates CSV: filo update records retentions and tags only; carry it out with "+
			"filo delete %s", path, shellQuote(path))
	}
	// A combined CSV that lost its retention or tags column is told by the exports its rows name (docs/design.md,
	// Tags, "The combined CSV").
	parsed, err := filecsv.ParseCSV(data)
	var missing string
	var exports []int64
	if errors.Is(err, filecsv.ErrNotFilesCSV) {
		missing, exports = filecsv.LostColumn(data)
	}
	if err != nil && missing == "" {
		return fmt.Errorf("%s: %w", path, err)
	}
	s, openErr := e.open(ctx, *db)
	if openErr != nil {
		return openErr
	}
	defer e.closeStore(s)
	if err != nil {
		if lost := lostColumn(ctx, s, path, exports, missing); lost != nil {
			return lost
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return e.updateFiles(ctx, s, path, parsed, *dry, *yes)
}

// csvFacts are an update heading's facts: the CSV's name, its export (when the rows name one), and a count.
func csvFacts(path string, exports []int64, count string) []string {
	facts := []string{filepath.Base(path)}
	if len(exports) == 1 {
		facts = append(facts, fmt.Sprintf("export #%d", exports[0]))
	}
	return append(facts, count)
}

// nowPast lists the files a change left past their retention, with the hint to check for a typo (docs/design.md,
// Retention CSV columns, "Past dates are accepted"), after a blank line; nothing when there are none. It shows limit of
// them before "… N more" (0: all).
func nowPast(r *view.Renderer, roots []string, past []store.Past, dry bool, limit int) {
	n := len(past)
	if n == 0 {
		return
	}
	rows := make([]view.FileRow, n)
	for i, p := range past {
		root, rel := view.SplitRoot(roots, p.Path)
		rows[i] = view.FileRow{Root: root, Rel: rel, Cells: []string{p.Retention, p.KeepUntil}}
	}
	r.Blank()
	what := report.Plural(int64(n), "file is now past its", "files are now past their")
	if dry {
		what = report.Plural(int64(n), "file would be past its", "files would be past their")
	}
	r.Note(view.Attention, what+" retention. Check for a typo, such as 7d for 7y:")
	r.Files(view.FileList{Columns: []view.Column{{Header: "retention"}, {Header: "until"}}, Rows: rows,
		Max: limit, More: "files", MoreOne: "file"})
}

// refusalProblems turns the rows the CSV reader refused and the rows or groups the register refused into one list in
// CSV line order, each file shown relative to its root and each group in the group column. When the CSV mixes rows
// from several exports, a group's reason names its export too, since a group number alone would be ambiguous.
func refusalProblems(roots []string, problems []sheet.Problem, refusals []store.Refusal) []view.Problem {
	exports := map[int64]bool{}
	for _, rf := range refusals {
		if rf.Export != 0 {
			exports[rf.Export] = true
		}
	}
	var out []view.Problem
	for _, p := range problems {
		out = append(out, view.Problem{Line: p.Line, Reason: p.Msg})
	}
	for _, rf := range refusals {
		p := view.Problem{Line: rf.Line, Group: int(rf.Group), Reason: rf.Reason}
		if rf.Export != 0 && len(exports) > 1 {
			p.Reason = fmt.Sprintf("export #%d: %s", rf.Export, rf.Reason)
		}
		if rf.Path == "" {
			p.Reason = fmt.Sprintf("file %d: %s", rf.FileID, rf.Reason)
		} else {
			p.Root, p.Rel = view.SplitRoot(roots, rf.Path)
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b view.Problem) int { return a.Line - b.Line })
	return out
}

// clearList lists the retentions blank cells clear, each with the retention and date it had, max of them before
// "… N more" (0: all).
func clearList(r *view.Renderer, roots []string, cs []store.Clear, max int) {
	rows := make([]view.FileRow, len(cs))
	for i, c := range cs {
		row := view.FileRow{Cells: []string{c.Retention, c.KeepUntil}}
		if c.Path == "" {
			row.Label = fmt.Sprintf("file %d, no current path", c.FileID)
		} else {
			row.Root, row.Rel = view.SplitRoot(roots, c.Path)
		}
		rows[i] = row
	}
	r.Files(view.FileList{Columns: []view.Column{{Header: "retention"}, {Header: "until"}}, Rows: rows,
		Max: max, More: "files", MoreOne: "file"})
}

// showNewPolicies lists the policies a CSV names that do not exist yet (docs/design.md, Named retention policies).
func showNewPolicies(r *view.Renderer, ps []store.NewPolicy) {
	names := make([]string, len(ps))
	width := 0
	for i, p := range ps {
		names[i] = p.Name
		width = max(width, len(p.Name))
	}
	r.Section("New policies", report.Count(int64(len(ps))), strings.Join(names, ", "))
	for _, p := range ps {
		r.Count(view.Neutral, p.Name+strings.Repeat(" ", width-len(p.Name)+3)+report.Plural(int64(p.Rows), "row",
			"rows"))
	}
	r.Count(view.Neutral, "they have no value yet: their files need a retention until filo policies set NAME VALUE")
}

// interruptedUpdate words a cancelled update: the transaction was not committed.
func interruptedUpdate(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("interrupted; nothing was recorded")
	}
	return err
}
