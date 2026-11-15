package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// exportDuplicates writes the duplicate groups with a copy under the filter (or, with separate, the contents marked
// separate, every delete cell filled in N) to csvPath.
func (e *env) exportDuplicates(ctx context.Context, s *store.Store, f store.Filter, where, csvPath string,
	separate bool) error {
	var groups []store.DupContent
	var err error
	kind, prefill := store.ExportDuplicates, ""
	if separate {
		kind, prefill = store.ExportSeparate, "N"
		groups, err = s.SeparateContents(ctx, f)
	} else {
		groups, err = s.DuplicateGroups(ctx, f)
	}
	if err != nil {
		return err
	}
	if !separate {
		groups = distinctOnDisk(groups)
	}
	if where != "" {
		where = " under " + where
	}
	what := "duplicate groups"
	if separate {
		what = "contents marked separate"
	}
	if len(groups) == 0 {
		fmt.Fprintf(e.stdout, "No %s%s; nothing exported.\n", what, where)
		return nil
	}
	id, err := writeExport(csvPath, func(write func(int64) error) (int64, error) {
		return s.RecordDuplicatesExport(ctx, csvPath, now(), kind, groups, write)
	}, func(w io.Writer, id int64) error { return duplicates.WriteCSV(w, id, groups, prefill, time.Local) })
	if err != nil {
		return err
	}
	var copies int64
	for _, g := range groups {
		copies += int64(len(g.Copies))
	}
	w := e.stdout
	if separate {
		fmt.Fprintf(w, "Exported %s marked separate%s (%s) to %s (export %d).\n",
			report.Plural(int64(len(groups)), "content", "contents"), where, report.Plural(copies, "copy", "copies"),
			csvPath, id)
		fmt.Fprintln(w, "Blank the delete cells of a group to make it a duplicate group again, then run:")
	} else {
		fmt.Fprintf(w, "Exported %s%s (%s) to %s (export %d).\n", report.Plural(int64(len(groups)), "duplicate group",
			"duplicate groups"), where, report.Plural(copies, "copy", "copies"), csvPath, id)
		fmt.Fprintln(w, "In each group you decide on, write Y under delete for the copies to delete and N for the "+
			"ones to keep (two or more N: the copies are meant to exist, and are no longer duplicates). Then run:")
	}
	fmt.Fprintf(w, "  filo update %s\n", csvPath)
	return nil
}

// distinctOnDisk merges the copies of each group that are one file on disk (the same file seen through a bind mount in
// two roots, which the register cannot tell apart) and drops the groups left with one copy. Copies that cannot be
// looked at are kept: update and delete check again.
func distinctOnDisk(groups []store.DupContent) []store.DupContent {
	out := groups[:0]
	for _, g := range groups {
		var copies []store.DupCopy
	next:
		for _, c := range g.Copies {
			for i := range copies {
				if same, err := dispose.SameFile(copies[i].AbsPath(), c.AbsPath()); err == nil && same {
					copies[i].Links = append(copies[i].Links, c.AbsPath())
					continue next
				}
			}
			copies = append(copies, c)
		}
		if len(copies) > 1 {
			g.Copies = copies
			out = append(out, g)
		}
	}
	return out
}

// updateDuplicates reads an edited duplicates CSV and records its decisions. It never deletes.
func (e *env) updateDuplicates(ctx context.Context, db, path string, data []byte, dry bool) error {
	parsed, err := duplicates.ReadCSV(bytes.NewReader(data))
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
	groups := map[[2]int64]bool{}
	for _, row := range parsed.Rows {
		if !slices.Contains(exports, row.ExportID) {
			exports = append(exports, row.ExportID)
		}
		groups[[2]int64{row.ExportID, row.Group}] = true
	}
	title := "Duplicates update"
	if dry {
		title += " (dry run)"
	}
	r.Heading(title, view.Where{Roots: roots}, csvFacts(path, exports,
		report.Plural(int64(len(groups)), "group", "groups"))...)
	r.Blank()

	asking := e.canPrompt()
	var pr *prompter
	if asking {
		pr = newPrompter(e.stdin)
		pr.end = r.Blank
		defer pr.close()
	}
	// show lists what needs a look before anything is recorded: the refusals, and the contents to be marked separate
	// (a fill-down of N would mark every group). It is shown once, at the question or at the end.
	shown, asked := false, false
	refused := func(res store.DupResult) []view.Problem {
		return refusalProblems(roots, parsed.Problems, res.Refusals)
	}
	separate := func(res store.DupResult, max int) {
		rows := make([]view.FileRow, len(res.Separate))
		for i, p := range res.Separate {
			rows[i].Root, rows[i].Rel = splitRoot(roots, p)
		}
		r.Files(view.FileList{Rows: rows, Max: max, More: "contents", MoreOne: "content"})
	}
	show := func(res store.DupResult, max int) {
		if ps := refused(res); len(ps) > 0 {
			r.Problems("Refused: nothing in these groups is recorded", view.Refused, ps)
			r.Blank()
		}
		if n := len(res.Separate); n > 0 {
			verb := " to be marked separate"
			if dry {
				verb = " would be marked separate"
			}
			r.Note(view.Kept, report.Plural(int64(n), "content", "contents")+verb+" (identical copies meant to "+
				"exist; no longer listed as duplicates):")
			separate(res, max)
			r.Blank()
		}
	}
	confirm := func(res store.DupResult) bool {
		show(res, r.Overview())
		shown = true
		if !asking {
			return false
		}
		asked = true
		question := fmt.Sprintf("Record these decisions (%s)?", dupSummary(res))
		for {
			key, err := e.askChoice(ctx, pr, question, yesNoList, "n")
			if err != nil {
				return false // a cancelled context also stops the update itself: nothing is recorded
			}
			r.Blank()
			if key != "l" {
				return key == "y"
			}
			show(res, 0)
		}
	}
	res, err := s.UpdateDuplicates(ctx, parsed.Rows, len(parsed.Problems), now(), path, dry, confirm)
	if err != nil {
		return interruptedUpdate(err)
	}
	if !shown {
		show(res, r.Overview())
	}

	nRefused := len(refused(res))
	if !res.NotRecorded {
		would := func(done, dryRun string) string {
			if dry {
				return dryRun
			}
			return done
		}
		if res.Marked > 0 {
			r.Count(view.Done, report.Plural(int64(res.Marked), "copy", "copies")+would(" marked", " would be marked")+
				" for deletion in "+report.Plural(int64(res.MarkedGroups), "group", "groups"))
		}
		if n := len(res.Separate); n > 0 {
			r.Count(view.Kept, report.Plural(int64(n), "content", "contents")+would(" marked", " would be marked")+
				" separate")
		}
		if n := len(res.NotSeparate); n > 0 {
			r.Count(view.Neutral, report.Plural(int64(n), "content", "contents")+would(" no longer", " would no longer be")+
				" separate")
		}
		if res.Unmarked > 0 {
			r.Count(view.Neutral, report.Plural(int64(res.Unmarked), "mark", "marks")+would(" removed", " would be removed"))
		}
	}
	if res.Undecided > 0 {
		r.Count(view.Neutral, report.Plural(int64(res.Undecided), "group", "groups")+" left alone (no Y or N)")
	}
	if res.Appeared > 0 {
		r.Count(view.Neutral, report.Plural(int64(res.Appeared), "copy", "copies")+" of these contents appeared since "+
			"the export; they count as kept copies")
	}
	if nRefused > 0 {
		r.Count(view.Refused, report.Plural(int64(nRefused), "row or group", "rows or groups")+" refused")
	}
	r.Blank()
	switch {
	case dry:
		if res.NeedsConfirm() {
			r.Next("Dry run: nothing was written. filo update would ask before recording, because of what is listed " +
				"above.")
		} else {
			r.Next("Dry run: nothing was written.")
		}
	case res.NotRecorded:
		why := "not confirmed"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal"
		}
		r.Next(fmt.Sprintf("Nothing recorded (%s). Fix the CSV and run filo update again.", why))
	case res.Marked > 0:
		r.Next(fmt.Sprintf("Nothing has been deleted. Next: filo delete (%s to review)",
			report.Plural(int64(res.Marked), "copy", "copies")))
	}
	if len(res.Refusals) > 0 || len(parsed.Problems) > 0 {
		return errReported
	}
	return nil
}

// dupSummary says what an update records.
func dupSummary(res store.DupResult) string {
	var parts []string
	if res.Marked > 0 {
		parts = append(parts, fmt.Sprintf("%s to delete in %s", report.Plural(int64(res.Marked), "copy", "copies"),
			report.Plural(int64(res.MarkedGroups), "group", "groups")))
	}
	if n := len(res.Separate); n > 0 {
		parts = append(parts, report.Plural(int64(n), "content", "contents")+" marked separate")
	}
	if n := len(res.NotSeparate); n > 0 {
		parts = append(parts, report.Plural(int64(n), "content", "contents")+" no longer separate")
	}
	if res.Unmarked > 0 {
		parts = append(parts, report.Plural(int64(res.Unmarked), "mark", "marks")+" removed")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// markedGroups returns the duplicate copies marked for deletion under the filter, without those that are also past
// their retention (listed there instead), and how many marks no longer count.
func markedGroups(ctx context.Context, s *store.Store, f store.Filter, expired map[int64]bool) ([]store.MarkedGroup,
	int, error) {
	groups, stale, err := s.MarkedGroups(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	out := groups[:0]
	for _, g := range groups {
		g.Delete = slices.DeleteFunc(g.Delete, func(c store.Candidate) bool { return expired[c.FileID] })
		if len(g.Delete) > 0 {
			out = append(out, g)
		}
	}
	return out, stale, nil
}

// retentionNote shows a copy's retention next to its path: "forever", "until 2027-03-31", or nothing.
func retentionNote(c store.Candidate) string {
	switch c.KeepUntil {
	case "":
		return ""
	case store.Forever:
		return "forever"
	}
	return "until " + c.KeepUntil
}

// findKept checks the group's unmarked copies on disk, longest retention first, and returns the first that is still
// the same content, or why none is. A copy on this run's retention list (expired) never counts: it may be deleted
// later in the run. Nor does a copy that is on disk the same file as one of the copies to delete (chosen).
func findKept(ctx context.Context, g store.MarkedGroup, chosen []store.Candidate,
	expired map[int64]bool) (*dispose.Kept, string, error) {
	if len(g.Keep) == 0 {
		return nil, "no other copy of this content is left, so it is not deleted", nil
	}
	var whys []string
copies:
	for _, k := range g.Keep {
		if expired[k.FileID] {
			whys = append(whys, k.AbsPath()+": past its own retention, so this run may delete it too")
			continue
		}
		kept, why, err := dispose.CheckKept(ctx, k)
		if err != nil {
			return nil, "", err
		}
		if kept == nil {
			whys = append(whys, k.AbsPath()+": "+why)
			continue
		}
		// A copy to delete that cannot be looked at is skipped by its own check; only the same file matters here.
		for _, c := range chosen {
			if same, err := dispose.SameFile(kept.Path(), c.AbsPath()); err == nil && same {
				whys = append(whys, k.AbsPath()+": the same file as "+c.AbsPath()+" (a hard link or a bind mount)")
				continue copies
			}
		}
		return kept, "", nil
	}
	return nil, "no other copy could be checked, so it is not deleted (" + strings.Join(whys, "; ") + ")", nil
}
