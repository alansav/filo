package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// set is filo set FILE... (docs/design.md, Showing and setting a file's details): a file_date and a retention set by
// command, by the retention CSV's rules, all or nothing. It asks only when a stored value would be replaced (-y
// answers yes; with no terminal nothing changes); --dry-run shows what it would do.
func (e *env) set(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("set")
	db := dbFlag(fs, dbBefore)
	fileDate := fs.String("file-date", "", "")
	noFileDate := fs.Bool("no-file-date", false, "")
	ret := fs.String("retention", "", "")
	noRetention := fs.Bool("no-retention", false, "")
	dups := fs.String("duplicates", "", "")
	dry := fs.Bool("dry-run", false, "")
	yes := fs.Bool("y", false, "")
	pos, err := e.parse(fs, "set", args)
	if err != nil {
		return err
	}
	v, err := setValues(fs, *fileDate, *noFileDate, *ret, *noRetention, *dups)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"set takes one FILE or more"}
	}
	if err := checkTildes("FILE", pos...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)

	// The files, by their paths in the register; a path that names none is refused like a file that cannot be set.
	var ids []int64
	var refused []setRefusal
	seenPath := map[string]bool{}
	for _, arg := range pos {
		if abs, err := filepath.Abs(arg); err == nil {
			if seenPath[abs] {
				continue // a path given twice is one file
			}
			seenPath[abs] = true
		}
		id, why, err := e.fileID(ctx, s, arg)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if why != nil {
			refused = append(refused, *why)
			continue
		}
		ids = append(ids, id)
	}
	day := today()
	r := e.view
	asked := false
	opt := store.SetOptions{DryRun: *dry || len(refused) > 0}
	var askErr error
	opt.Confirm = func(res store.SetResult) bool {
		asked = true
		ok, err := e.confirmSet(ctx, v, res, *yes)
		askErr = err
		return ok
	}
	res, err := s.SetFiles(ctx, ids, v, now(), day, opt)
	if err == nil {
		err = askErr
	}
	if err != nil {
		return err
	}
	for _, rf := range res.Refusals {
		refused = append(refused, setRefusal{path: store.AbsPath(rf.RootPath, rf.Path), reason: rf.Reason})
	}
	if len(refused) > 0 {
		total := len(refused) - len(res.Refusals) + len(ids) // the files given: those found, and paths refused
		r.Note(view.Refused, "Nothing was changed: "+strconv.Itoa(len(refused))+" of "+
			report.Plural(int64(total), "file was refused", "files were refused"))
		for _, rf := range refused {
			r.CountName(view.Refused, "", rf.path, "   "+rf.reason)
		}
		r.Blank()
		r.Next("Next: fix the files above, or leave them out, and run filo set again")
		return errReported
	}

	retFlags := v.SetFileDate || v.SetRetention
	changed, dupChanged := res.Changed(), res.DupsChanged()
	unchanged := len(res.Rows) - changed
	facts := setFacts(v)
	roots := rowRoots(res.Rows)
	switch {
	case *dry:
		var counts []string
		if retFlags {
			counts = append(counts, report.Plural(int64(changed), "file", "files")+" would change")
		}
		if v.Duplicates != "" {
			counts = append(counts, report.Plural(int64(dupChanged), "content", "contents")+" would change")
		}
		r.Heading("Dry run", view.Where{Roots: roots}, append(facts, counts...)...)
		if changed > 0 {
			e.setTable(v, changedRows(res.Rows))
		}
		e.dupLists(res.Dups, roots, true, true)
		e.dupLists(res.Dups, roots, false, true)
	case changed+dupChanged == 0:
		var what []string
		if retFlags {
			what = append(what, report.Plural(int64(len(res.Rows)), "file has", "files have")+" these values already")
		}
		if v.Duplicates != "" {
			what = append(what, report.Plural(int64(len(res.Dups)), "content has", "contents have")+
				" this decision already")
		}
		r.Heading("Unchanged", view.Where{Roots: roots}, append(facts, what...)...)
		return nil
	case !res.Applied:
		return nil // declined, or no terminal to ask in: confirmSet said so
	default:
		done := facts
		if retFlags {
			done = append(done, report.Plural(int64(changed), "file", "files")+" changed")
			if unchanged > 0 {
				done = append(done, strconv.Itoa(unchanged)+" unchanged")
			}
		}
		if dupChanged > 0 {
			done = append(done, report.Plural(int64(dupChanged), "content", "contents"),
				"applies to "+report.Plural(int64(dupCopies(res.Dups)), "copy", "copies"))
		}
		if asked {
			r.Blank()
			r.Outcome("Done", done...)
		} else {
			r.Heading("Done", view.Where{Roots: roots}, done...)
			if changed > 0 {
				e.setTable(v, changedRows(res.Rows))
			}
			e.dupLists(res.Dups, roots, false, false)
		}
	}
	for _, name := range res.NewPolicies {
		if *dry {
			r.Count(view.Attention, "policy "+name+" would be created with no value: its files would need a "+
				"retention until it has one")
		} else {
			r.Count(view.Attention, "policy "+name+" was created with no value: its files need a retention until "+
				"it has one")
		}
	}
	nowPast(r, roots, res.NowPast, *dry, 0) // every one: a command shows every file it affected, unlike the CSV update
	switch {
	case *dry:
		r.Blank()
		r.Next("Dry run: nothing was changed.")
	case len(res.NewPolicies) > 0:
		r.Blank()
		next := "Next: filo policies set " + res.NewPolicies[0] + " VALUE"
		if len(res.NewPolicies) > 1 {
			next = "Next: filo policies set NAME VALUE for each"
		}
		if len(res.NowPast) > 0 {
			next = "Nothing has been deleted. " + next
		}
		r.Next(next)
	case len(res.NowPast) > 0:
		r.Blank()
		r.Next("Nothing has been deleted. Next: filo delete")
	}
	return nil
}

// setValues checks set's flags: at least one, no flag with its opposite, and each value as the CSV takes it.
func setValues(fs *flag.FlagSet, fileDate string, noFileDate bool, ret string, noRetention bool,
	dups string) (store.SetValues, error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var v store.SetValues
	switch {
	case given["file-date"] && noFileDate:
		return v, usageError{"--file-date and --no-file-date cannot be given together"}
	case given["retention"] && noRetention:
		return v, usageError{"--retention and --no-retention cannot be given together"}
	case !given["file-date"] && !noFileDate && !given["retention"] && !noRetention && !given["duplicates"]:
		return v, usageError{"set needs --file-date DATE, --no-file-date, --retention R, --no-retention or " +
			"--duplicates always|records|counted"}
	}
	if given["duplicates"] {
		switch d := strings.ToLower(strings.TrimSpace(dups)); d {
		case store.DupAlways, store.DupRecords, store.DupCounted:
			v.Duplicates = d
		default:
			return v, usageError{fmt.Sprintf("--duplicates %q: use always (the content belongs everywhere), records "+
				"(the files named are each a record) or counted (count them as duplicates again)", dups)}
		}
	}
	if given["file-date"] {
		d, err := retention.ParseDate(fileDate)
		if err != nil || d == "" {
			return v, usageError{fmt.Sprintf("--file-date %q is not a date: use YYYY-MM-DD, for example 2023-03-14 "+
				"(--no-file-date clears it)", fileDate)}
		}
		v.FileDate, v.SetFileDate = d, true
	}
	v.SetFileDate = v.SetFileDate || noFileDate
	if given["retention"] {
		rt, err := retention.ParseRetention(ret)
		if err != nil || rt == "" {
			return v, usageError{fmt.Sprintf("--retention %q is not a retention: use a period such as 7y, 18m, 6w "+
				"or 30d (counted from file_date), forever, a date YYYY-MM-DD, or a policy name (--no-retention "+
				"clears it)", ret)}
		}
		v.Retention, v.SetRetention = rt, true
	}
	v.SetRetention = v.SetRetention || noRetention
	return v, nil
}

// setRefusal is a file filo set refused: its path in full, and why.
type setRefusal struct{ path, reason string }

// fileID finds the file a typed path names in the register: its ID, or why it cannot be set (a directory, a path
// no file was registered at).
func (e *env) fileID(ctx context.Context, s *store.Store, arg string) (int64, *setRefusal, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return 0, nil, err
	}
	refuse := func(why string) (int64, *setRefusal, error) {
		return 0, &setRefusal{path: p, reason: why}, nil
	}
	root, rel, isDir, err := locatePath(ctx, s, p)
	if errors.As(err, new(outsideError)) {
		return refuse("outside every root")
	}
	if err != nil {
		return 0, nil, err
	}
	if isDir {
		return refuse("a directory: filo set takes files (filo rules gives a directory's files a policy)")
	}
	sh, err := s.ShowFile(ctx, root.ID, rel, today())
	if errors.Is(err, store.ErrNotInRegister) {
		if has, err := s.HasFilesUnder(ctx, root.ID, rel); err != nil {
			return 0, nil, err
		} else if has {
			return refuse("a directory: filo set takes files (filo rules gives a directory's files a policy)")
		}
		if skippedBy(rel, root.Settings) {
			return refuse("not in the register: its root's settings skip it")
		}
		return refuse("not in the register yet: run filo scan")
	}
	if err != nil {
		return 0, nil, err
	}
	// The path finds the file there now, or the last one there if it has none since (missing, deleted: refused by
	// the store). A file that has moved on is not set by its old path: that path names something else now, or
	// nothing.
	if sh.Present && (sh.RootID != root.ID || sh.Path != rel) {
		return refuse("not at this path any more: it moved to " + sh.AbsPath())
	}
	return sh.FileID, nil, nil
}

// setFacts are a heading's facts for the values given: "file_date 2024-03-31", "retention cleared".
func setFacts(v store.SetValues) []string {
	var facts []string
	if v.SetFileDate {
		if v.FileDate == "" {
			facts = append(facts, "file_date cleared")
		} else {
			facts = append(facts, "file_date "+v.FileDate)
		}
	}
	if v.SetRetention {
		if v.Retention == "" {
			facts = append(facts, "retention cleared")
		} else {
			facts = append(facts, "retention "+v.Retention)
		}
	}
	switch v.Duplicates {
	case store.DupAlways, store.DupRecords:
		facts = append(facts, v.Duplicates)
	case store.DupCounted:
		facts = append(facts, "counted as duplicates")
	}
	return facts
}

// dupWord names a duplicates decision in a table: "always", "a separate record", "counted as a duplicate".
func dupWord(d string) string {
	switch d {
	case store.DupRecords:
		return "a separate record"
	case store.DupCounted:
		return "counted as a duplicate"
	}
	return d
}

// dupCopies counts the copies the changed decisions cover.
func dupCopies(dups []store.DupChange) int {
	n := 0
	for _, d := range dups {
		if !d.Unchanged {
			n += len(d.Copies)
		}
	}
	return n
}

// dupLists writes each changed decision (of replaced ones only when replaced is set, of the others when not): every
// copy of its content, those named marked, then what it means.
func (e *env) dupLists(dups []store.DupChange, roots []string, replaced, dry bool) {
	r := e.view
	for _, d := range dups {
		if d.Unchanged || d.Replaces != replaced {
			continue
		}
		rows := make([]view.FileRow, len(d.Copies))
		for i, p := range d.Copies {
			root, rel := view.SplitRoot(roots, p)
			rows[i] = view.FileRow{Root: root, Rel: rel}
			if slices.Contains(d.Named, p) {
				rows[i].Note = "(named)"
			}
		}
		r.Files(view.FileList{Rows: rows})
		var what string
		switch d.Now {
		case store.DupAlways:
			what = "these copies no longer count as duplicates; filo set FILE --duplicates counted undoes it"
			if dry {
				what = "these copies would no longer count as duplicates"
			}
		case store.DupRecords:
			what = "the copies named are kept as separate records, each with its own retention; filo set FILE " +
				"--duplicates counted undoes it"
			if dry {
				what = "the copies named would be kept as separate records, each with its own retention"
			}
		default:
			what = "these copies count as duplicates again"
			if dry {
				what = "these copies would count as duplicates again"
			}
		}
		r.Count(view.Neutral, what)
	}
}

// rowRoots are the roots of the rows, each once.
func rowRoots(rows []store.SetRow) []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range rows {
		if !seen[row.RootPath] {
			seen[row.RootPath] = true
			out = append(out, row.RootPath)
		}
	}
	return out
}

func changedRows(rows []store.SetRow) []store.SetRow {
	var out []store.SetRow
	for _, row := range rows {
		if !row.Unchanged {
			out = append(out, row)
		}
	}
	return out
}

// none shows an empty value.
func none(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// retentionText shows a retention: a policy with its value ("bills (7y)"), or the retention itself.
func retentionText(ret, policyValue string, policy bool) string {
	switch {
	case ret == "":
		return "none"
	case !policy:
		return ret
	case policyValue == "":
		return ret + " (no value yet)"
	}
	return ret + " (" + policyValue + ")"
}

// setTable lists rows with was and now for each field given, and until, the file last.
func (e *env) setTable(v store.SetValues, rows []store.SetRow) {
	var over []view.Over
	var cols []view.Column
	pair := func(name string) {
		over = append(over, view.Over{Header: name, Columns: 2})
		cols = append(cols, view.Column{Header: "was"}, view.Column{Header: "now"})
	}
	if v.SetFileDate {
		pair("file_date")
	}
	if v.SetRetention {
		pair("retention")
	}
	pair("until")
	list := make([]view.FileRow, len(rows))
	for i, row := range rows {
		var cells []string
		if v.SetFileDate {
			cells = append(cells, none(row.OldFileDate), none(row.FileDate))
		}
		if v.SetRetention {
			cells = append(cells, retentionText(row.OldRetention, row.OldPolicyValue, store.IsPolicyRef(row.OldRetention)),
				retentionText(row.Retention, row.PolicyValue, store.IsPolicyRef(row.Retention)))
		}
		cells = append(cells, none(row.OldKeepUntil), none(row.KeepUntil))
		fr := view.FileRow{Root: row.RootPath, Rel: row.Path, Cells: cells}
		if rule := row.RulePath(); rule != "" {
			fr.Note = "(set by rule " + rule + ")"
		}
		list[i] = fr
	}
	e.view.Files(view.FileList{Columns: cols, Over: over, Rows: list, ByRoot: true})
}

// confirmSet shows what would be replaced, with was and now: the files' stored values, and the contents' duplicates
// decisions; then the files and contents given values for the first time, and those that have them already; then it
// asks once (-y answers yes; with no terminal nothing changes, and it says so).
func (e *env) confirmSet(ctx context.Context, v store.SetValues, res store.SetResult, yes bool) (bool, error) {
	r := e.view
	rows := res.Rows
	retFlags := v.SetFileDate || v.SetRetention
	var replaced, first, already []store.SetRow
	clears := 0
	for _, row := range rows {
		switch {
		case row.Unchanged:
			already = append(already, row)
		case row.Replaces:
			replaced = append(replaced, row)
		default:
			first = append(first, row)
		}
		if row.Clears {
			clears++
		}
	}
	var dupReplaced []store.DupChange
	for _, d := range res.Dups {
		if d.Replaces {
			dupReplaced = append(dupReplaced, d)
		}
	}
	changed := len(replaced) + len(first)
	r.Heading("Set", view.Where{Roots: rowRoots(rows)}, append(setFacts(v),
		report.Plural(int64(len(rows)), "file", "files"))...)
	if len(replaced) > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(len(replaced)), "file has values", "files have values")+
			" that would be replaced:")
		e.setTable(v, replaced)
	}
	if len(dupReplaced) > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(len(dupReplaced)), "content has a duplicates decision",
			"contents have a duplicates decision")+" that would be replaced:")
		var list []view.FileRow
		for _, d := range dupReplaced {
			for _, p := range d.Named {
				root, rel := view.SplitRoot(rowRoots(rows), p)
				list = append(list, view.FileRow{Root: root, Rel: rel, Cells: []string{dupWord(d.Was), dupWord(d.Now)}})
			}
		}
		r.Files(view.FileList{Columns: []view.Column{{Header: "was"}, {Header: "now"}},
			Over: []view.Over{{Header: "duplicates", Columns: 2}}, Rows: list, ByRoot: true})
		if n := dupCopies(dupReplaced); n > 0 {
			r.Count(view.Neutral, "about the content: it applies to "+report.Plural(int64(n), "copy", "copies"))
		}
	}
	if res.DupsChanged() > len(dupReplaced) {
		r.Blank()
		e.dupLists(res.Dups, rowRoots(rows), false, true)
	}
	names := func(rs []store.SetRow) string {
		var out []string
		for i, row := range rs {
			if i == 10 {
				out = append(out, fmt.Sprintf("%d more", len(rs)-10))
				break
			}
			out = append(out, path.Base(row.Path))
		}
		return joinAnd(out)
	}
	if retFlags && (len(first) > 0 || len(already) > 0 || clears > 0) {
		r.Blank()
		if len(first) > 0 {
			r.Count(view.Neutral, report.Plural(int64(len(first)), "file gets them", "files get them")+
				" for the first time: "+names(first))
		}
		if len(already) > 0 {
			r.Count(view.Neutral, report.Plural(int64(len(already)), "file has them", "files have them")+
				" already: "+names(already))
		}
		if clears > 0 {
			r.Count(view.Neutral, report.Plural(int64(clears), "file", "files")+" will need a retention again")
		}
	}
	var parts []string
	switch {
	case len(replaced) == 0:
	case clears > 0 && clears == changed:
		parts = append(parts, "clear the retention of "+report.Plural(int64(clears), "file", "files"))
	case changed > len(replaced):
		parts = append(parts, "replace the values of "+report.Plural(int64(len(replaced)), "file", "files")+
			" and set "+strconv.Itoa(changed))
	default:
		parts = append(parts, "replace the values of "+report.Plural(int64(len(replaced)), "file", "files"))
	}
	if len(dupReplaced) > 0 {
		parts = append(parts, "replace the duplicates decision of "+report.Plural(int64(len(dupReplaced)), "content",
			"contents"))
	}
	question := strings.Join(parts, ", and ")
	question = strings.ToUpper(question[:1]) + question[1:]
	relist := func() {
		if changed > 0 {
			e.setTable(v, changedRows(rows))
		}
		e.dupLists(res.Dups, rowRoots(rows), true, true)
		e.dupLists(res.Dups, rowRoots(rows), false, true)
	}
	return e.confirmChange(ctx, question+"?", yes, relist)
}
