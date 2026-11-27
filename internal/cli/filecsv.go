package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	pathpkg "path"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/filecsv"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// exportFiles is filo export [DIR] -o FILE, the combined CSV (docs/design.md, Tags, "The combined CSV"), after
// export's checks and its scan: every present file under DIR (or every root) with its retention and its tags.
func (e *env) exportFiles(ctx context.Context, s *store.Store, f store.Filter, where view.Where, csvPath string,
	asked fs.FileInfo, notScanned string) error {
	items, err := s.FilesItems(ctx, today(), f)
	if err != nil {
		return err
	}
	suggested := markNameDates(len(items), func(i int) *store.FileItem { return &items[i].FileItem })
	filecsv.SetNameCells(items) // recorded, so an edited name cell asks for a rename
	v := e.view
	if len(items) == 0 {
		v.Heading("Export", where, "no files")
		notScannedLine(v, notScanned)
		v.Next("Nothing exported.")
		return nil
	}
	id, err := writeExport(csvPath, asked, func(write func(int64) error) (int64, error) {
		return s.RecordFilesExport(ctx, csvPath, now(), items, write)
	}, func(w io.Writer, id int64) error { return filecsv.WriteCSV(w, id, items, time.Local) })
	if err != nil {
		return err
	}
	undated, tagged := 0, 0
	for _, it := range items {
		if it.Retention == "" {
			undated++
		}
		if len(it.Tags) > 0 {
			tagged++
		}
	}
	v.Heading(fmt.Sprintf("Export #%d", id), where, report.Plural(int64(len(items)), "file", "files"))
	v.Blank()
	v.Count(view.Neutral, report.Count(int64(undated))+" without a retention, "+
		report.Count(int64(len(items)-undated))+" with one")
	v.Count(view.Neutral, report.Count(int64(tagged))+" tagged, "+report.Count(int64(len(items)-tagged))+
		" without a tag")
	markedLine(v, suggested)
	notScannedLine(v, notScanned)
	v.Blank()
	v.Next("Next: fill in retention (7y, 18m, forever or a date) and tags (key:value or a bare value, separated " +
		"by ;), or correct a name, then filo update " + shellQuote(csvPath))
	return nil
}

// filesExports reports whether any of the exports is a combined one, and the first such.
func filesExports(ctx context.Context, s *store.Store, exports []int64) (int64, error) {
	kinds, err := s.ExportKinds(ctx, exports)
	if err != nil {
		return 0, err
	}
	for _, id := range exports {
		if kinds[id] == store.ExportFiles {
			return id, nil
		}
	}
	return 0, nil
}

// lostColumn refuses a retention or tags CSV whose rows name a combined export: a combined CSV that lost its
// column named missing (deleted or renamed in the spreadsheet), which would otherwise be read as the other kind
// (docs/design.md, Tags, "The combined CSV").
func lostColumn(ctx context.Context, s *store.Store, path string, exports []int64, missing string) error {
	id, err := filesExports(ctx, s, exports)
	if err != nil || id == 0 {
		return err
	}
	return fmt.Errorf("%s is from export #%d, a CSV with both retention and tags, but it has no %s column; put "+
		"it back with that name, or export again with filo export", path, id, missing)
}

// updateFiles is filo update FILE for a combined CSV: both halves of each row, asking once before anything is
// written if any row clears a retention, removes a tag or names a new policy, unless yes (-y) answers.
func (e *env) updateFiles(ctx context.Context, s *store.Store, path string, parsed filecsv.Parsed, dry,
	yes bool) error {
	roots, err := registeredRoots(ctx, s)
	if err != nil {
		return err
	}
	r := e.view
	var exports []int64
	for _, row := range parsed.Rows {
		if !slices.Contains(exports, row.ExportID) {
			exports = append(exports, row.ExportID)
		}
	}
	title := "Update"
	if dry {
		title += " (dry run)"
	}
	total := len(parsed.Rows) + len(parsed.Problems)
	r.Heading(title, view.Where{Roots: roots}, csvFacts(path, exports, report.Plural(int64(total), "row", "rows"))...)
	r.Blank()

	asking := e.canPrompt() && !yes
	var pr *prompter
	if asking {
		pr = newPrompter(e.stdin)
		pr.end, pr.check = r.Blank, e.outputErr
		defer pr.close()
	}
	asked := false
	var askErr error // the question could not be asked (output failed): nothing is recorded, and the run says why
	confirm := func(p store.FilesPending) bool {
		if yes {
			return true
		}
		if !asking {
			return false
		}
		asked = true
		var ok bool
		ok, askErr = e.confirmPending(ctx, pr, roots, p)
		return ok && askErr == nil
	}
	res, err := s.UpdateFiles(ctx, parsed.Rows, now(), today(), path, store.FilesUpdateOptions{DryRun: dry,
		Confirm: confirm, CheckName: dispose.CheckName, CheckDisk: dispose.CheckRename})
	switch {
	case err != nil:
	case ctx.Err() != nil:
		err = ctx.Err() // interrupted at the question: nothing was written, though nothing failed
	case askErr != nil:
		return fmt.Errorf("nothing recorded: %w", askErr)
	}
	if err != nil {
		return interruptedUpdate(err)
	}
	if dry && !res.Pending.Empty() {
		e.showPending(roots, res.Pending, r.Overview())
	}

	// The renames, after the register's changes (docs/design.md, Tags, "Renaming a file by its name cell").
	var renames renameResult
	if !dry && !res.Declined {
		renames, err = e.makeRenames(ctx, s, res.Pending.Renames)
		if err != nil && ctx.Err() == nil {
			return err
		}
	}
	refused := refusalProblems(roots, parsed.Problems, res.Refusals)
	if res.Declined {
		why := "not confirmed; edit the CSV and run filo update again"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal, or with -y"
		}
		r.Count(view.Attention, "nothing recorded: "+why)
	} else {
		e.filesCounts(res, dry, len(refused), renames)
	}
	if len(refused) > 0 {
		r.Count(view.Refused, report.Plural(int64(len(refused)), "row", "rows")+" refused")
	}
	if !res.Declined && len(res.TagsChanged) > 0 {
		r.Blank()
		r.Files(view.FileList{Rows: tagChangeRows(roots, res.TagsChanged), Max: r.Overview(), More: "files",
			MoreOne: "file"})
	}
	if len(refused) > 0 {
		r.Blank()
		r.Problems("Refused: fix these rows in the CSV and run filo update again", view.Refused, refused)
	}
	if len(renames.skipped) > 0 {
		r.Blank()
		r.Problems("Not renamed: run filo scan, then export again", view.Attention, renames.skipped)
	}
	if !res.Declined {
		nowPast(r, roots, res.NowPast, dry, r.Overview())
	}
	if ctx.Err() != nil { // Ctrl-C between renames: those done are recorded, the rest were not made
		return fmt.Errorf("interrupted: %d of %d renames made and recorded; the others were not made",
			renames.done, len(res.Pending.Renames))
	}
	switch {
	case dry:
		r.Blank()
		r.Next("Dry run: nothing was written.")
	case !res.Declined && len(res.NowPast) > 0:
		r.Blank()
		r.Next("Nothing has been deleted. Next: filo delete")
	}
	if len(refused) > 0 || len(renames.skipped) > 0 || (res.Declined && !asked) { // nothing done, no one was asked
		return errReported
	}
	return nil
}

// filesCounts are a combined update's count lines: what it recorded (or, in a dry run, would record).
func (e *env) filesCounts(res store.FilesUpdateResult, dry bool, refused int, renames renameResult) {
	r := e.view
	would := func(done, dryRun string) string {
		if dry {
			return dryRun
		}
		return done
	}
	if n := len(res.Pending.NewPolicies); dry && n > 0 {
		r.Count(view.Neutral, report.Plural(int64(n), "policy", "policies")+" would be created")
	}
	if res.PoliciesCreated > 0 {
		names := make([]string, len(res.Pending.NewPolicies))
		for i, p := range res.Pending.NewPolicies {
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
			" would be cleared"))
	}
	if n := len(res.TagsChanged); n > 0 {
		r.Count(view.Done, report.Plural(int64(n), "file", "files")+would(pluralWord(n, " had its tags changed",
			" had their tags changed"), pluralWord(n, " would have its tags changed", " would have their tags changed")))
	}
	if n := len(res.Pending.Renames); dry && n > 0 {
		r.Count(view.Neutral, report.Plural(int64(n), "file", "files")+" would be renamed")
	}
	if renames.done > 0 {
		r.Count(view.Done, report.Plural(int64(renames.done), "file", "files")+" renamed")
	}
	if n := len(renames.skipped); n > 0 {
		r.Count(view.Attention, report.Plural(int64(n), "rename", "renames")+" not made")
	}
	if res.Unchanged > 0 {
		r.Count(view.Neutral, report.Plural(int64(res.Unchanged), "row", "rows")+" unchanged")
	}
	if res.Set+res.Cleared+len(res.TagsChanged)+len(res.Pending.Renames)+res.Unchanged+refused == 0 {
		r.Count(view.Neutral, "nothing to change")
	}
}

// showPending lists what a combined update asks about: the retentions cleared, the tags removed and the new
// policies, max of each before "… N more" (0: all), each part followed by a blank line.
func (e *env) showPending(roots []string, p store.FilesPending, max int) {
	r := e.view
	if n := len(p.Clears); n > 0 {
		r.Note(view.Attention, report.Plural(int64(n), "retention is", "retentions are")+" cleared by a blank cell:")
		clearList(r, roots, p.Clears, max)
		r.Blank()
	}
	if n := len(p.Removals); n > 0 {
		r.Note(view.Attention, report.Plural(int64(n), "file loses", "files lose")+" tags:")
		r.Files(view.FileList{Rows: tagChangeRows(roots, p.Removals), Max: max, More: "files", MoreOne: "file"})
		r.Blank()
	}
	if len(p.NewPolicies) > 0 {
		showNewPolicies(r, p.NewPolicies)
		r.Blank()
	}
	if n := len(p.Renames); n > 0 {
		r.Note(view.Attention, report.Plural(int64(n), "file is", "files are")+" renamed:")
		r.Renames(view.RenameList{Rows: renameRows(p.Renames), Max: max})
		r.Blank()
	}
}

// renameRows are the renames as the summary lists them: by root and directory, each with a note on a rename that
// may be an accident (only spaces at the start or end changed, or the extension).
func renameRows(rs []store.Rename) []view.RenameRow {
	rows := make([]view.RenameRow, len(rs))
	for i, rn := range rs {
		dir, old := pathpkg.Split(rn.Path)
		row := view.RenameRow{Root: rn.RootPath, Dir: strings.TrimSuffix(dir, "/"), Old: old, New: rn.Name}
		switch {
		case strings.TrimSpace(old) == strings.TrimSpace(rn.Name):
			row.Note = "only spaces changed"
		case pathpkg.Ext(old) != pathpkg.Ext(rn.Name):
			row.Note = "extension changed"
		}
		rows[i] = row
	}
	slices.SortStableFunc(rows, func(a, b view.RenameRow) int {
		return strings.Compare(a.Root+"\x00"+a.Dir, b.Root+"\x00"+b.Dir)
	})
	return rows
}

// renameResult is what making an update's renames did: how many were made, and those not made, with why.
type renameResult struct {
	done    int
	skipped []view.Problem
}

// makeRenames makes each rename, checked again on disk just before, and records each straight after; one not made
// is listed with why. Ctrl-C stops after the current one (the error is then the context's).
func (e *env) makeRenames(ctx context.Context, s *store.Store, rs []store.Rename) (renameResult, error) {
	var out renameResult
	if len(rs) == 0 {
		return out, nil
	}
	roots, err := registeredRoots(ctx, s)
	if err != nil {
		return out, err
	}
	run := s.BeginRenameRun(now())
	defer func() {
		if err := run.Finish(context.WithoutCancel(ctx), now()); err != nil {
			e.errorLine(err.Error())
		}
	}()
	for _, rn := range rs {
		o, err := dispose.Rename(ctx, run, rn.Candidate, rn.Name, now())
		if err != nil {
			return out, err
		}
		if o.Done {
			out.done++
			continue
		}
		p := view.Problem{Reason: o.Reason}
		p.Root, p.Rel = view.SplitRoot(roots, rn.AbsPath())
		out.skipped = append(out.skipped, p)
	}
	return out, nil
}

// confirmPending shows what the update asks about (the first ones, or all with l) and asks once. Only a yes
// confirms; anything else records nothing.
func (e *env) confirmPending(ctx context.Context, pr *prompter, roots []string, p store.FilesPending) (bool, error) {
	e.showPending(roots, p, e.view.Overview())
	return e.askYesNo(ctx, pr, "Apply the update? Nothing is recorded otherwise.", func() { e.showPending(roots, p, 0) })
}

// maybeUpdateFiles updates a CSV with both retention and tags columns as a combined CSV if its rows name a combined
// export; done is false when they do not (a retention CSV with a tags column of the person's own), and the CSV is
// then read as a retention CSV.
func (e *env) maybeUpdateFiles(ctx context.Context, db, path string, data []byte, dry, yes bool) (done bool,
	err error) {
	parsed, err := filecsv.ParseCSV(data)
	if err != nil {
		return true, fmt.Errorf("%s: %w", path, err)
	}
	s, err := e.open(ctx, db)
	if err != nil {
		return true, err
	}
	defer e.closeStore(s)
	id, err := filesExports(ctx, s, parsed.Exports) // refused rows' too: a CSV whose rows are all refused is still one
	if err != nil {
		return true, err
	}
	if id == 0 {
		return false, nil
	}
	return true, e.updateFiles(ctx, s, path, parsed, dry, yes)
}
