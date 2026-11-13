package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
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

	printed, asked := false, false
	show := func(res store.DupResult) {
		if printed {
			return
		}
		printed = true
		e.printDupRefusals(path, parsed, res)
		e.printSeparate(res, dry)
	}
	confirm := func(res store.DupResult) bool {
		show(res)
		if !isTerminal(e.stdin) {
			return false
		}
		asked = true
		return e.askYesNo(fmt.Sprintf("Record these decisions (%s)? [y/n] ", dupSummary(res)))
	}
	res, err := s.UpdateDuplicates(ctx, parsed.Rows, len(parsed.Problems), now(), path, dry, confirm)
	if err != nil {
		return err
	}
	show(res)

	w := e.stdout
	switch {
	case dry:
		fmt.Fprintln(w, "Dry run: nothing was written.")
		fmt.Fprintf(w, "Would record: %s.\n", dupSummary(res))
		if res.NeedsConfirm() {
			fmt.Fprintln(w, "filo update would ask before recording, because of what is listed above.")
		}
	case res.NotRecorded:
		why := "not confirmed"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal"
		}
		fmt.Fprintf(w, "Nothing recorded (%s). Fix the CSV and run filo update again.\n", why)
	default:
		fmt.Fprintf(w, "Recorded: %s.\n", dupSummary(res))
		if res.Marked > 0 {
			fmt.Fprintln(w, "Nothing has been deleted. Run filo delete to review and delete the copies marked Y.")
		}
	}
	if res.Appeared > 0 {
		fmt.Fprintf(w, "%s of these contents appeared since the export; they count as kept copies.\n",
			report.Plural(int64(res.Appeared), "copy", "copies"))
	}
	if res.Undecided > 0 {
		fmt.Fprintf(w, "%s left alone (no Y or N).\n", report.Plural(int64(res.Undecided), "group", "groups"))
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

// printDupRefusals lists what was refused, in CSV line order, one line each.
func (e *env) printDupRefusals(path string, parsed duplicates.Parsed, res store.DupResult) {
	if n := e.printRefusals(path, parsed.Problems, res.Refusals); n > 0 {
		fmt.Fprintf(e.stdout, "%s refused (listed above); nothing in a refused group is recorded.\n",
			report.Plural(int64(n), "row or group", "rows or groups"))
	}
}

// printSeparate lists the contents an update marks separate, so a fill-down of N is seen before it is confirmed.
func (e *env) printSeparate(res store.DupResult, dry bool) {
	if len(res.Separate) == 0 {
		return
	}
	w := e.stdout
	verb := "to be"
	if dry {
		verb = "would be"
	}
	fmt.Fprintf(w, "%s %s marked separate (identical copies meant to exist; no longer listed as duplicates):\n",
		report.Plural(int64(len(res.Separate)), "content", "contents"), verb)
	for i, p := range res.Separate {
		if i == clearsShown {
			fmt.Fprintf(w, "  and %s more\n", report.Count(int64(len(res.Separate)-clearsShown)))
			break
		}
		fmt.Fprintf(w, "  %s\n", p)
	}
}

// markedGroups returns the duplicate copies marked for deletion under the filter, without those that are also past
// their retention (listed there instead), and how many marks no longer count.
func markedGroups(ctx context.Context, s *store.Store, f store.Filter, cands []store.Candidate) ([]store.MarkedGroup,
	int, error) {
	groups, stale, err := s.MarkedGroups(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	expired := map[int64]bool{}
	for _, c := range cands {
		expired[c.FileID] = true
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

// listMarked prints the duplicates section of filo delete's list.
func (e *env) listMarked(ctx context.Context, s *store.Store, groups []store.MarkedGroup, stale int,
	under string) error {
	w := e.stdout
	var targets []string
	var files int
	var total int64
	for _, g := range groups {
		for _, c := range g.Delete {
			targets = append(targets, c.AbsPath())
			files++
			total += c.Size
		}
	}
	links, err := s.SymlinksTo(ctx, targets)
	if err != nil {
		return err
	}
	if len(groups) > 0 {
		fmt.Fprintf(w, "Duplicate copies marked for deletion%s: %s in %s, %s\n", under,
			report.Plural(int64(files), "file", "files"), report.Plural(int64(len(groups)), "group", "groups"),
			report.Size(total))
	}
	for _, g := range groups {
		fmt.Fprintf(w, "  %s, %s each\n", filepath.Base(g.Delete[0].AbsPath()), report.Size(g.Size))
		if len(g.Keep) == 0 {
			fmt.Fprintln(w, "    keep    (no other copy is left; this group will be skipped)")
		}
		for _, k := range g.Keep {
			fmt.Fprintf(w, "    keep    %s%s\n", k.AbsPath(), retentionNote(k))
		}
		for _, c := range g.Delete {
			fmt.Fprintf(w, "    delete  %s%s\n", c.AbsPath(), retentionNote(c))
			for _, l := range links[c.AbsPath()] {
				fmt.Fprintf(w, "            the symlink %s points to it and would break\n", l)
			}
		}
	}
	if stale > 0 {
		fmt.Fprintf(w, "%s no longer %s: the copy changed since it was marked (export duplicates again).\n",
			report.Plural(int64(stale), "mark", "marks"), pluralWord(stale, "counts", "count"))
	}
	return nil
}

// retentionNote shows a copy's retention in the duplicates list.
func retentionNote(c store.Candidate) string {
	switch c.KeepUntil {
	case "":
		return ""
	case store.Forever:
		return "  (kept forever)"
	}
	return "  (kept until " + c.KeepUntil + ")"
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
