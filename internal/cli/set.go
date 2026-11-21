package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path"
	"path/filepath"
	"strconv"

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
	dry := fs.Bool("dry-run", false, "")
	yes := fs.Bool("y", false, "")
	pos, err := e.parse(fs, "set", args)
	if err != nil {
		return err
	}
	v, err := setValues(fs, *fileDate, *noFileDate, *ret, *noRetention)
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
		ok, err := e.confirmSet(ctx, v, res.Rows, *yes)
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
		total := len(refused) + len(res.Rows)
		r.Note(view.Refused, "Nothing was changed: "+strconv.Itoa(len(refused))+" of "+
			report.Plural(int64(total), "file was refused", "files were refused"))
		for _, rf := range refused {
			r.CountName(view.Refused, "", rf.path, "   "+rf.reason)
		}
		r.Blank()
		r.Next("Next: fix the files above, or leave them out, and run filo set again")
		return errReported
	}

	changed := res.Changed()
	unchanged := len(res.Rows) - changed
	facts := setFacts(v)
	roots := rowRoots(res.Rows)
	switch {
	case *dry:
		r.Heading("Dry run", view.Where{Roots: roots}, append(facts, report.Plural(int64(changed), "file", "files")+
			" would change")...)
		if changed > 0 {
			e.setTable(v, changedRows(res.Rows))
		}
	case changed == 0:
		r.Heading("Unchanged", view.Where{Roots: roots}, append(facts,
			report.Plural(int64(len(res.Rows)), "file has", "files have")+" these values already")...)
		return nil
	case !res.Applied:
		return nil // declined, or no terminal to ask in: confirmSet said so
	default:
		done := append(facts, report.Plural(int64(changed), "file", "files")+" changed")
		if unchanged > 0 {
			done = append(done, strconv.Itoa(unchanged)+" unchanged")
		}
		if asked {
			r.Blank()
			r.Outcome("Done", done...)
		} else {
			r.Heading("Done", view.Where{Roots: roots}, done...)
			e.setTable(v, changedRows(res.Rows))
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
		r.Files(view.FileList{Columns: []view.Column{{Header: "retention"}, {Header: "until"}}, Rows: rows})
	}
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
func setValues(fs *flag.FlagSet, fileDate string, noFileDate bool, ret string, noRetention bool) (store.SetValues,
	error) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var v store.SetValues
	switch {
	case given["file-date"] && noFileDate:
		return v, usageError{"--file-date and --no-file-date cannot be given together"}
	case given["retention"] && noRetention:
		return v, usageError{"--retention and --no-retention cannot be given together"}
	case !given["file-date"] && !noFileDate && !given["retention"] && !noRetention:
		return v, usageError{"set needs --file-date DATE, --no-file-date, --retention R or --no-retention"}
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
	return facts
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

// confirmSet shows the files whose stored values would be replaced, with was and now, and the others in a line each,
// then asks once (-y answers yes; with no terminal nothing changes, and it says so).
func (e *env) confirmSet(ctx context.Context, v store.SetValues, rows []store.SetRow, yes bool) (bool, error) {
	r := e.view
	var replaced, first, already []store.SetRow
	for _, row := range rows {
		switch {
		case row.Unchanged:
			already = append(already, row)
		case row.Replaces:
			replaced = append(replaced, row)
		default:
			first = append(first, row)
		}
	}
	changed := len(replaced) + len(first)
	clears := 0
	for _, row := range rows {
		if row.Clears {
			clears++
		}
	}
	r.Heading("Set", view.Where{Roots: rowRoots(rows)}, append(setFacts(v),
		report.Plural(int64(len(rows)), "file", "files"))...)
	r.Blank()
	r.Note(view.Attention, report.Plural(int64(len(replaced)), "file has values", "files have values")+
		" that would be replaced:")
	e.setTable(v, replaced)
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
	clearing := clears > 0
	if len(first) > 0 || len(already) > 0 || clearing {
		r.Blank()
	}
	if len(first) > 0 {
		r.Count(view.Neutral, report.Plural(int64(len(first)), "file gets them", "files get them")+" for the first time: "+
			names(first))
	}
	if len(already) > 0 {
		r.Count(view.Neutral, report.Plural(int64(len(already)), "file has them", "files have them")+" already: "+
			names(already))
	}
	if clearing {
		r.Count(view.Neutral, report.Plural(int64(clears), "file", "files")+" will need a retention again")
	}
	question := "Replace the values of " + report.Plural(int64(len(replaced)), "file", "files")
	if changed > len(replaced) {
		question += " and set " + strconv.Itoa(changed)
	}
	if clearing && clears == changed {
		question = "Clear the retention of " + report.Plural(int64(clears), "file", "files")
	}
	relist := func() { e.setTable(v, changedRows(rows)) }
	return e.confirmChange(ctx, question+"?", yes, relist)
}
