package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// now is the clock for keep_until dates; tests replace it.
var now = time.Now

func today() string { return retention.Today(now().Local()) }

// exportKinds are the CSVs filo can export. Each names what the CSV is for.
var exportKinds = []string{"retention", "duplicates"}

// filterFor turns the DIR argument, if any, into a filter on the register and the place a heading names: the root
// DIR is in and the folder under it, or every root when there is no DIR.
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
	pos, err := e.parse(fl, "export", args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"export needs what to export: filo export retention|duplicates [DIR] -o FILE"}
	}
	kind := pos[0]
	if !slices.Contains(exportKinds, kind) {
		return usageError{fmt.Sprintf("unknown export %q; the exports are retention and duplicates", kind)}
	}
	pos = pos[1:]
	if len(pos) > 1 {
		return usageError{"export " + kind + " takes at most one DIR"}
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
	if kind == "duplicates" {
		return e.exportDuplicates(ctx, s, f, where, csvPath, asked)
	}
	items, err := s.Files(ctx, today(), f)
	if err != nil {
		return err
	}
	// Fill in a blank file_date from the file name where it holds one unambiguous date. The export records it as
	// written, so an untouched suggestion is no change; it takes effect only through a retention period typed later.
	suggested := 0
	names := retention.NewDateReader(today()) // one day for the whole export
	for i := range items {
		if items[i].FileDate == "" {
			if d := names.Read(path.Base(items[i].Path)); d != "" {
				items[i].FileDate = d
				suggested++
			}
		}
	}
	v := e.view
	if len(items) == 0 {
		v.Heading("Retention export", where, "no files")
		v.Next("Nothing exported.")
		return nil
	}

	id, err := writeExport(csvPath, asked, func(write func(int64) error) (int64, error) {
		return s.RecordExport(ctx, csvPath, now(), items, write)
	}, func(w io.Writer, id int64) error { return retention.WriteCSV(w, id, items, time.Local) })
	if err != nil {
		return err
	}
	undated := 0
	for _, it := range items {
		if it.Retention == "" {
			undated++
		}
	}
	// docs/design.md, Terminal output, Export.
	v.Heading("Retention export", where, fmt.Sprintf("export %d", id), report.Plural(int64(len(items)), "file", "files"))
	v.Blank()
	v.Count(view.Neutral, report.Count(int64(undated))+" without a retention, "+
		report.Count(int64(len(items)-undated))+" with one")
	if suggested > 0 {
		v.Count(view.Attention, report.Plural(int64(suggested), "file_date", "file_dates")+
			" filled in from the file name: check "+pluralWord(suggested, "it", "them")+" before counting from "+
			pluralWord(suggested, "it", "them"))
	}
	v.Blank()
	v.Next("Next: fill in retention (7y, 18m, forever or a date), then filo update " + shellQuote(csvPath))
	return nil
}

func (e *env) update(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("update")
	db := dbFlag(fs, dbBefore)
	dry := fs.Bool("dry-run", false, "")
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
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	// No decisions to delete are stored any more (docs/design.md, Duplicates): a duplicates CSV is carried out by
	// filo delete, which checks it against the disk at the time.
	if duplicates.IsDuplicatesCSV(data) {
		return fmt.Errorf("%s is a duplicates CSV: filo update records retentions only; carry it out with filo delete %s",
			path, shellQuote(path))
	}
	parsed, err := retention.ReadCSV(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	roots, err := registeredRoots(ctx, s)
	if err != nil {
		return err
	}
	r := e.view
	total := len(parsed.Rows) + len(parsed.Problems)
	title := "Retention update"
	if *dry {
		title += " (dry run)"
	}
	r.Heading(title, view.Where{Roots: roots}, csvFacts(path, retentionExports(parsed.Rows),
		report.Plural(int64(total), "row", "rows"))...)
	r.Blank()

	asking := e.canPrompt()
	var pr *prompter
	if asking {
		pr = newPrompter(e.stdin)
		pr.end, pr.check = r.Blank, e.outputErr
		defer pr.close()
	}
	asked := false
	confirm := func(cs []store.Clear) bool {
		if !asking {
			return false
		}
		asked = true
		ok, err := e.confirmClears(ctx, pr, roots, cs)
		return ok && err == nil // a cancelled context also stops the update itself: nothing is recorded
	}
	askedPolicies := false
	confirmPolicies := func(ps []store.NewPolicy) bool {
		if !asking {
			return false
		}
		askedPolicies = true
		ok, err := e.confirmPolicies(ctx, pr, ps)
		return ok && err == nil // a cancelled context also stops the update itself: nothing is recorded
	}
	res, err := s.UpdateRetentionWith(ctx, parsed.Rows, now(), today(), path, store.UpdateOptions{DryRun: *dry,
		ConfirmClears: confirm, ConfirmPolicies: confirmPolicies})
	if err != nil {
		return interruptedUpdate(err)
	}
	if *dry && len(res.NewPolicies) > 0 {
		showNewPolicies(r, res.NewPolicies)
		r.Blank()
	}

	would := func(done, dryRun string) string {
		if *dry {
			return dryRun
		}
		return done
	}
	refused := refusalProblems(roots, parsed.Problems, res.Refusals)
	if n := len(res.NewPolicies); *dry && n > 0 {
		r.Count(view.Neutral, report.Plural(int64(n), "policy", "policies")+" would be created (filo update asks "+
			"first)")
	}
	if res.PoliciesCreated > 0 {
		names := make([]string, len(res.NewPolicies))
		for i, p := range res.NewPolicies {
			names[i] = p.Name
		}
		r.Count(view.Done, report.Plural(int64(res.PoliciesCreated), "policy", "policies")+" created: "+
			strings.Join(names, ", "))
	}
	if n := res.Set - res.NoValue; n > 0 {
		r.Count(view.Done, report.Plural(int64(n), "file", "files")+would(" given a retention",
			" would be given a retention"))
	}
	if res.NoValue > 0 {
		r.Count(view.Attention, report.Plural(int64(res.NoValue), "file", "files")+would(" put on", " would be put on")+
			" a policy with no value yet: they need a retention until it has one (filo policies set NAME VALUE)")
	}
	if res.Cleared > 0 {
		r.Count(view.Done, report.Plural(int64(res.Cleared), "retention", "retentions")+would(" cleared",
			" would be cleared (filo update asks first)"))
	}
	if res.Unchanged > 0 {
		r.Count(view.Neutral, report.Plural(int64(res.Unchanged), "row", "rows")+" unchanged")
	}
	if res.ClearsSkipped > 0 {
		why := "not confirmed"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal to clear them"
		}
		r.Count(view.Attention, report.Plural(int64(res.ClearsSkipped), "retention left as it was",
			"retentions left as they were")+", though the cell is blank ("+why+")")
	}
	if res.PolicyRowsSkipped > 0 {
		why := "not confirmed"
		if !askedPolicies {
			why = "there is no terminal to ask in; run filo update in a terminal to create them"
		}
		r.Count(view.Attention, report.Plural(int64(res.PolicyRowsSkipped), "row left as it was", "rows left as "+
			"they were")+": they name policies not created ("+why+")")
	}
	if len(refused) > 0 {
		r.Count(view.Refused, report.Plural(int64(len(refused)), "row", "rows")+" refused")
	}
	if res.Set+res.Cleared+res.Unchanged+res.ClearsSkipped+res.PolicyRowsSkipped+len(refused) == 0 {
		r.Count(view.Neutral, "nothing to change")
	}
	if len(refused) > 0 {
		r.Blank()
		r.Problems("Refused: fix these rows in the CSV and run filo update again", view.Refused, refused)
	}
	if n := len(res.NowPast); n > 0 {
		rows := make([]view.FileRow, n)
		for i, p := range res.NowPast {
			root, rel := view.SplitRoot(roots, p.Path)
			rows[i] = view.FileRow{Root: root, Rel: rel, Cells: []string{p.Retention, p.KeepUntil}}
		}
		r.Blank()
		what := report.Plural(int64(n), "file is now past its", "files are now past their")
		if *dry {
			what = report.Plural(int64(n), "file would be past its", "files would be past their")
		}
		r.Note(view.Attention, what+" retention. Check for a typo, such as 7d for 7y:")
		r.Files(view.FileList{Columns: []view.Column{{Header: "retention"}, {Header: "until"}}, Rows: rows,
			Max: r.Overview(), More: "files", MoreOne: "file"})
	}
	r.Blank()
	switch {
	case *dry:
		r.Next("Dry run: nothing was written.")
	case len(res.NowPast) > 0:
		r.Next("Nothing has been deleted. Next: filo delete")
	}
	if len(refused) > 0 {
		return errReported
	}
	return nil
}

// csvFacts are an update heading's facts: the CSV's name, its export (when the rows name one), and a count.
func csvFacts(path string, exports []int64, count string) []string {
	facts := []string{filepath.Base(path)}
	if len(exports) == 1 {
		facts = append(facts, fmt.Sprintf("export %d", exports[0]))
	}
	return append(facts, count)
}

// retentionExports lists the exports a retention CSV's rows name, each once.
func retentionExports(rows []store.RetentionRow) []int64 {
	var out []int64
	for _, row := range rows {
		if !slices.Contains(out, row.ExportID) {
			out = append(out, row.ExportID)
		}
	}
	return out
}

// refusalProblems turns the rows the CSV reader refused and the rows or groups the register refused into one list in
// CSV line order, each file shown relative to its root and each group in the group column. When the CSV mixes rows
// from several exports, a group's reason names its export too, since a group number alone would be ambiguous.
func refusalProblems(roots []string, problems []retention.Problem, refusals []store.Refusal) []view.Problem {
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
			p.Reason = fmt.Sprintf("export %d: %s", rf.Export, rf.Reason)
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

// confirmClears lists the retentions blank cells would clear (the first ones, or all with l) and asks once. Only a
// yes confirms; the other rows are recorded either way.
func (e *env) confirmClears(ctx context.Context, pr *prompter, roots []string, cs []store.Clear) (bool, error) {
	r := e.view
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
	list := func(max int) {
		r.Files(view.FileList{Columns: []view.Column{{Header: "retention"}, {Header: "until"}}, Rows: rows,
			Max: max, More: "files", MoreOne: "file"})
		r.Blank()
	}
	r.Note(view.Attention, report.Plural(int64(len(cs)), "row has", "rows have")+" a blank retention where one is "+
		"stored. Recording clears it:")
	list(r.Overview())
	question := "Clear this retention? The other rows are recorded either way."
	if len(cs) > 1 {
		question = fmt.Sprintf("Clear these %s retentions? The other rows are recorded either way.",
			report.Count(int64(len(cs))))
	}
	return e.askYesNo(ctx, pr, question, func() { list(0) })
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

// confirmPolicies lists the policies a CSV would create and asks once. Only a yes creates them; declined, the rows
// naming them are left alone and the other rows are recorded either way.
func (e *env) confirmPolicies(ctx context.Context, pr *prompter, ps []store.NewPolicy) (bool, error) {
	r := e.view
	showNewPolicies(r, ps)
	r.Blank()
	return e.askYesNo(ctx, pr, "Create "+report.Plural(int64(len(ps)), "policy", "policies")+"?", nil)
}

// interruptedUpdate words a cancelled update: the transaction was not committed.
func interruptedUpdate(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("interrupted; nothing was recorded")
	}
	return err
}
