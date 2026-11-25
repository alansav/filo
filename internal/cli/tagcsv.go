package cli

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tagcsv"
	"github.com/alansav/filo/internal/view"
)

// exportTags is filo export tags [DIR] -o FILE (docs/design.md, Tags, "The tags CSV"), after export's checks and its
// scan: every present file under DIR (or every root) with its tags.
func (e *env) exportTags(ctx context.Context, s *store.Store, f store.Filter, where view.Where, csvPath string,
	asked fs.FileInfo, notScanned string) error {
	items, err := s.TagItems(ctx, f.RootID, f.Scope)
	if err != nil {
		return err
	}
	v := e.view
	if len(items) == 0 {
		v.Heading("Export (tags)", where, "no files")
		notScannedLine(v, notScanned)
		v.Next("Nothing exported.")
		return nil
	}
	id, err := writeExport(csvPath, asked, func(write func(int64) error) (int64, error) {
		return s.RecordTagsExport(ctx, csvPath, now(), items, write)
	}, func(w io.Writer, id int64) error { return tagcsv.WriteCSV(w, id, items, time.Local) })
	if err != nil {
		return err
	}
	tagged := 0
	for _, it := range items {
		if len(it.Tags) > 0 {
			tagged++
		}
	}
	v.Heading(fmt.Sprintf("Export #%d (tags)", id), where, report.Plural(int64(len(items)), "file", "files"))
	v.Blank()
	v.Count(view.Neutral, report.Count(int64(tagged))+" tagged, "+report.Count(int64(len(items)-tagged))+
		" without a tag")
	notScannedLine(v, notScanned)
	v.Blank()
	v.Next("Next: fill in tags (key:value or a bare value, separated by ;), then filo update " + shellQuote(csvPath))
	return nil
}

// updateTags is filo update FILE for a tags CSV: each row's cell replaces its file's tags, asking once before any
// tag is removed, unless yes (-y) answers.
func (e *env) updateTags(ctx context.Context, db, path string, data []byte, dry, yes bool) error {
	parsed, err := tagcsv.ParseCSV(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	s, err := e.open(ctx, db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
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
	title := "Tags update"
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
	confirm := func(cs []store.TagChange) bool {
		if yes {
			return true // the rows that remove tags are listed with the others, after the update
		}
		if !asking {
			return false
		}
		asked = true
		ok, err := e.confirmTagRemovals(ctx, pr, roots, cs)
		return ok && err == nil // interrupted: no, and the run says so after (a cancelled context writes nothing)
	}
	res, err := s.UpdateTags(ctx, parsed.Rows, now(), store.TagUpdateOptions{DryRun: dry, ConfirmRemovals: confirm})
	if err == nil && ctx.Err() != nil {
		err = ctx.Err() // interrupted at the question: nothing after it was written, though nothing failed
	}
	if err != nil {
		return interruptedUpdate(err)
	}

	refused := refusalProblems(roots, parsed.Problems, res.Refusals)
	if n := len(res.Changed); n > 0 {
		what := pluralWord(n, " had its tags changed", " had their tags changed")
		if dry {
			what = pluralWord(n, " would have its tags changed", " would have their tags changed")
		}
		r.Count(view.Done, report.Plural(int64(n), "file", "files")+what)
	}
	switch lost := res.Removing - res.Skipped; {
	case dry && res.Removing > 0:
		how := " (filo update asks first)"
		if yes {
			how = " (-y removes them without asking)"
		}
		r.Count(view.Neutral, report.Count(int64(res.Removing))+" of them would lose tags"+how)
	case lost > 0:
		// Counted, since the list below stops at a screenful: -y skips the question that lists every one.
		r.Count(view.Neutral, report.Count(int64(lost))+" of them lost tags")
	}
	if res.Unchanged > 0 {
		r.Count(view.Neutral, report.Plural(int64(res.Unchanged), "row", "rows")+" unchanged")
	}
	if res.Skipped > 0 {
		why := "not confirmed"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal, or with -y, to remove them"
		}
		r.Count(view.Attention, report.Plural(int64(res.Skipped), "row left as it was", "rows left as they were")+
			", though "+pluralWord(res.Skipped, "it removes", "they remove")+" tags ("+why+")")
	}
	if len(refused) > 0 {
		r.Count(view.Refused, report.Plural(int64(len(refused)), "row", "rows")+" refused")
	}
	if len(res.Changed)+res.Unchanged+res.Skipped+len(refused) == 0 {
		r.Count(view.Neutral, "nothing to change")
	}
	if len(res.Changed) > 0 {
		r.Blank()
		r.Files(view.FileList{Rows: tagChangeRows(roots, res.Changed), Max: r.Overview(), More: "files",
			MoreOne: "file"})
	}
	if len(refused) > 0 {
		r.Blank()
		r.Problems("Refused: fix these rows in the CSV and run filo update again", view.Refused, refused)
	}
	if dry {
		r.Blank()
		r.Next("Dry run: nothing was written.")
	}
	if len(refused) > 0 {
		return errReported
	}
	return nil
}

// confirmTagRemovals lists the files whose rows remove tags (the first ones, or all with l) and asks once. Only a
// yes confirms; the other rows are recorded either way.
func (e *env) confirmTagRemovals(ctx context.Context, pr *prompter, roots []string, cs []store.TagChange) (bool,
	error) {
	r := e.view
	rows := tagChangeRows(roots, cs)
	list := func(max int) {
		r.Files(view.FileList{Rows: rows, Max: max, More: "files", MoreOne: "file"})
		r.Blank()
	}
	r.Note(view.Attention, report.Plural(int64(len(cs)), "row removes", "rows remove")+" tags:")
	list(r.Overview())
	return e.askYesNo(ctx, pr, "Remove them? The other rows are recorded either way.", func() { list(0) })
}

// tagChangeRows lists what an update does to each file's tags: "added urgent; kind:x   removed type:bill".
func tagChangeRows(roots []string, cs []store.TagChange) []view.FileRow {
	rows := make([]view.FileRow, len(cs))
	for i, c := range cs {
		var what []string
		if len(c.Added) > 0 {
			what = append(what, "added "+store.JoinTags(c.Added))
		}
		if len(c.Removed) > 0 {
			what = append(what, "removed "+store.JoinTags(c.Removed))
		}
		row := view.FileRow{Note: strings.Join(what, "   ")}
		if c.Path == "" {
			row.Label = fmt.Sprintf("file %d, no current path", c.FileID)
		} else {
			row.Root, row.Rel = view.SplitRoot(roots, c.Path)
		}
		rows[i] = row
	}
	return rows
}
