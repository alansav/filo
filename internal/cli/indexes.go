package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tagindex"
	"github.com/alansav/filo/internal/view"
	"github.com/alansav/filo/internal/walk"
)

// filo indexes (docs/design.md, Tag index pages): HTML pages that list the files under a directory by tag, kept up to
// date by Filo. With no subcommand it lists the indexes; add declares one and writes its pages, remove stops keeping
// one and removes its files (asking first), refresh writes the pages again.

const indexesUsage = "filo indexes | add [DIR] [--name NAME] | remove [DIR] [--name NAME] [-y] | " +
	"refresh [DIR] [--name NAME]"

// defaultIndexName is the name of an index given none: index.html and .index/.
const defaultIndexName = "index"

func (e *env) indexes(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	fl := e.flags("indexes")
	db := dbFlag(fl, dbBefore)
	name := fl.String("name", "", "")
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "indexes", rest)
	if err != nil {
		return err
	}
	want := map[string]int{"": 0, "add": 1, "remove": 1, "refresh": 1}
	if _, known := want[sub]; !known {
		return usageError{fmt.Sprintf("unknown indexes command %q; see filo help indexes", sub)}
	}
	switch {
	case len(pos) > want[sub]:
		return usageError{"wrong number of arguments: " + indexesUsage}
	case sub == "" && *name != "":
		return usageError{"--name is for indexes add, remove and refresh"}
	case *yes && sub != "remove":
		return usageError{"-y is for indexes remove"}
	}
	if len(pos) > 0 {
		if err := checkTilde("DIR", pos[0]); err != nil {
			return err
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	e.indexesDone = true // each subcommand writes, or reports, the pages itself
	arg := "."
	if len(pos) > 0 {
		arg = pos[0]
	}
	switch sub {
	case "add":
		n := *name
		if n == "" {
			n = defaultIndexName
		}
		return e.indexAdd(ctx, s, arg, n)
	case "remove":
		return e.indexRemove(ctx, s, arg, *name, *yes)
	case "refresh":
		return e.indexRefresh(ctx, s, pos, *name)
	}
	return e.indexList(ctx, s)
}

// indexDir finds the root and the directory relative to it that DIR names, which must be a directory on the disk.
func indexDir(ctx context.Context, s *store.Store, arg string) (store.Root, string, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return store.Root{}, "", err
	}
	fi, err := os.Stat(p)
	switch {
	case err != nil:
		return store.Root{}, "", fmt.Errorf("%s: %w", p, err)
	case !fi.IsDir():
		return store.Root{}, "", fmt.Errorf("%s is not a directory: an index is for a directory's files", p)
	}
	return s.Resolve(ctx, p)
}

func (e *env) indexAdd(ctx context.Context, s *store.Store, arg, name string) error {
	if !store.ValidIndexName(name) {
		return usageError{fmt.Sprintf("--name %s: %v", name, store.ErrIndexName)}
	}
	root, dir, err := indexDir(ctx, s, arg)
	if err != nil {
		return err
	}
	if dir != "" && walk.SkipsPath(root.Path, dir, root.IncludeHidden, root.Ignore) {
		return fmt.Errorf("%s is skipped by its root's settings (hidden or ignored), so its files are not in the "+
			"register; filo roots set changes them", store.AbsPath(root.Path, dir))
	}
	ix := store.TagIndex{RootPath: root.Path, Dir: dir, Name: name}
	for _, p := range []string{ix.Page(), ix.PagesDir()} {
		if _, err := os.Lstat(ix.Abs(p)); err == nil {
			return fmt.Errorf("%s exists already: give another name with --name", e.indexPath(root.Path, ix.Rel(p)))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s cannot be looked at: %w", ix.Abs(p), err)
		}
	}
	ix, err = s.AddTagIndex(ctx, root.ID, dir, name, now())
	if errors.Is(err, store.ErrIndexExists) {
		return fmt.Errorf("%s has an index named %s already, in some case (filo indexes lists them)",
			store.AbsPath(root.Path, dir), name)
	}
	if err != nil {
		return err
	}
	o, err := tagindex.Update(ctx, s, ix, true, now())
	if err != nil {
		return err
	}
	r := e.view
	r.Heading("Index added", view.Where{Roots: []string{root.Path}}, ix.Rel(ix.Page()),
		report.Plural(int64(len(o.Data.Files)), "file", "files"), report.Plural(int64(o.Data.Tags()), "tag", "tags"),
		report.Plural(int64(o.Pages), "page", "pages"))
	r.Count(view.Neutral, "its pages are in "+ix.Rel(ix.PagesDir())+"/; scans skip both")
	if n := len(o.Data.Untagged); n > 0 {
		listed := ` no tags: they are listed on the page "no tags"`
		if n == 1 {
			listed = ` no tags: it is listed on the page "no tags"`
		}
		r.Count(view.Neutral, report.Plural(int64(n), "file has", "files have")+listed)
	}
	if o.Failed() {
		e.indexProblems(o.Result.Problems, ix)
		r.Blank()
		r.Next("Next: move aside what is listed, then filo indexes refresh " + shellQuote(ix.DirAbs()))
		return errReported
	}
	r.Blank()
	r.Next("Next: open " + r.Tilde(ix.Abs(ix.Page())) + " in a browser")
	return nil
}

// indexProblems lists what writing or removing an index's files left alone.
func (e *env) indexProblems(ps []tagindex.Problem, ix store.TagIndex) {
	for _, p := range ps {
		where := ix.Rel(p.Path)
		if p.Path == "" {
			where = e.indexPath(ix.RootPath, ix.Rel(ix.Page()))
		}
		e.view.Count(view.Attention, where+" "+p.Why)
	}
}

// indexPath names a path of an index in a line: relative to the heading's root.
func (e *env) indexPath(root, rel string) string {
	if rel == "." || rel == "" {
		return e.view.Tilde(root)
	}
	return e.view.Path(root, rel)
}

func (e *env) indexList(ctx context.Context, s *store.Store) error {
	all, err := s.TagIndexes(ctx)
	if err != nil {
		return err
	}
	r := e.view
	if len(all) == 0 {
		r.Heading("Indexes", view.Where{}, "none yet")
		r.Blank()
		r.Next("Next: filo indexes add [DIR] [--name NAME] writes the pages of a directory's files by tag")
		return nil
	}
	var roots []string
	var rows []view.TableRow
	var flagged []store.TagIndex
	for _, ix := range all {
		roots = append(roots, ix.RootPath)
		files, err := s.IndexFiles(ctx, ix.RootID, ix.Dir)
		if err != nil {
			return err
		}
		d, err := tagindex.Build(ix, files)
		if err != nil {
			return err
		}
		written := "never written"
		if ix.WrittenUTC != "" {
			written = "written " + report.Time(ix.WrittenUTC)
		}
		row := view.TableRow{Cells: []string{report.Plural(int64(len(d.Files)), "file", "files"),
			report.Plural(int64(d.Tags()), "tag", "tags"), written}}
		if ix.Problem != "" {
			row.Attention = "not up to date"
			row.Below = []string{ix.Problem}
			flagged = append(flagged, ix)
		}
		rows = append(rows, row)
	}
	facts := []string{report.Count(int64(len(all)))}
	if len(flagged) > 0 {
		facts = append(facts, report.Plural(int64(len(flagged)), "needs attention", "need attention"))
	}
	r.Heading("Indexes", view.Where{Roots: roots}, facts...)
	for i, ix := range all { // after the heading: with one root, the names are relative to it
		rows[i].Name = r.Path(ix.RootPath, ix.Rel(ix.Page()))
	}
	r.Blank()
	r.Table(rows, true, true)
	r.Blank()
	switch len(flagged) {
	case 0:
		r.Next("Next: open an index's page in a browser")
	case 1:
		r.Next("Next: move aside what is flagged, then filo indexes refresh " + shellQuote(flagged[0].DirAbs()))
	default:
		r.Next("Next: move aside what is flagged, then filo indexes refresh")
	}
	return nil
}

// findIndex is the index on DIR named name, or its only one when name is "". With gone, a directory no longer on
// the disk (a root not mounted) is found by its path alone, so its index can still be removed.
func findIndex(ctx context.Context, s *store.Store, arg, name string, gone bool) (store.TagIndex, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return store.TagIndex{}, err
	}
	var root store.Root
	var dir string
	if _, statErr := os.Stat(p); statErr != nil && gone {
		root, dir, err = s.Locate(ctx, p)
	} else {
		root, dir, err = indexDir(ctx, s, p)
	}
	if err != nil {
		return store.TagIndex{}, err
	}
	all, err := s.TagIndexes(ctx)
	if err != nil {
		return store.TagIndex{}, err
	}
	var here []store.TagIndex
	var names []string
	for _, ix := range all {
		if ix.RootID == root.ID && ix.Dir == dir && (name == "" || strings.EqualFold(ix.Name, name)) {
			here = append(here, ix)
			names = append(names, ix.Name)
		}
	}
	where := store.AbsPath(root.Path, dir)
	switch {
	case len(here) == 1:
		return here[0], nil
	case len(here) == 0 && name != "":
		return store.TagIndex{}, fmt.Errorf("%s has no index named %s (filo indexes lists them)", where, name)
	case len(here) == 0:
		return store.TagIndex{}, fmt.Errorf("%s has no index (filo indexes lists them)", where)
	}
	return store.TagIndex{}, usageError{fmt.Sprintf("%s has %d indexes, %s: give one with --name", where, len(here),
		joinAnd(names))}
}

func (e *env) indexRemove(ctx context.Context, s *store.Store, arg, name string, yes bool) error {
	ix, err := findIndex(ctx, s, arg, name, true)
	if err != nil {
		return err
	}
	files, left := tagindex.Plan(ix)
	r := e.view
	r.Heading("Remove index", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()),
		report.Plural(int64(files), "file", "files"))
	r.Blank()
	inPages := 0
	mainPage := false
	for p, h := range ix.Files {
		switch {
		case h == "":
		case p == ix.Page():
			mainPage = true
		default:
			inPages++
		}
	}
	var rows []view.TableRow
	if mainPage {
		rows = append(rows, view.TableRow{Name: r.Path(ix.RootPath, ix.Rel(ix.Page()))})
	}
	if _, ok := ix.Files[ix.PagesDir()]; ok {
		rows = append(rows, view.TableRow{Name: r.Path(ix.RootPath, ix.Rel(ix.PagesDir())) + "/",
			Cells: []string{report.Plural(int64(inPages), "file", "files") + ": its pages and " + tagindex.Style}})
	}
	r.HeadedTable(nil, rows)
	pagesDir := ix.Rel(ix.PagesDir())
	for _, p := range left {
		text := r.Path(ix.RootPath, ix.Rel(p.Path)) + " " + p.Why
		if strings.HasPrefix(p.Path, ix.PagesDir()+"/") {
			text += ", and so does " + r.Path(ix.RootPath, pagesDir) + "/"
		}
		r.Count(view.Attention, text)
	}
	ok, err := e.confirmChange(ctx, "Remove the index and its "+report.Plural(int64(files), "file", "files")+"?",
		yes, nil)
	if err != nil || !ok {
		return err
	}
	res := tagindex.Remove(ix)
	if err := s.RemoveTagIndex(ctx, ix.ID); err != nil {
		return err
	}
	if len(res.Problems) == 0 {
		r.Outcome("Done", "removed "+r.Path(ix.RootPath, ix.Rel(ix.Page()))+" and "+r.Path(ix.RootPath, pagesDir)+"/")
		return nil
	}
	r.Outcome("Done", "removed "+report.Plural(int64(res.Removed), "file", "files"))
	e.indexProblems(res.Problems, ix)
	return errReported
}

func (e *env) indexRefresh(ctx context.Context, s *store.Store, pos []string, name string) error {
	var outs []tagindex.Outcome
	if len(pos) == 0 && name == "" {
		var err error
		if outs, err = tagindex.UpdateAll(ctx, s, true, now()); err != nil {
			return err
		}
	} else {
		arg := "."
		if len(pos) > 0 {
			arg = pos[0]
		}
		ix, err := findIndex(ctx, s, arg, name, false)
		if err != nil {
			return err
		}
		o, err := tagindex.Update(ctx, s, ix, true, now())
		if err != nil {
			return err
		}
		outs = []tagindex.Outcome{o}
	}
	r := e.view
	if len(outs) == 0 {
		r.Heading("Refreshed", view.Where{}, "no index yet")
		r.Blank()
		r.Next("Next: filo indexes add [DIR] [--name NAME]")
		return nil
	}
	var roots []string
	for _, o := range outs {
		roots = append(roots, o.Index.RootPath)
	}
	r.Heading("Refreshed", view.Where{Roots: roots}, report.Plural(int64(len(outs)), "index", "indexes"))
	names := make([]string, len(outs))
	w := 0
	for i, o := range outs {
		names[i] = r.Path(o.Index.RootPath, o.Index.Rel(o.Index.Page()))
		w = max(w, utf8.RuneCountInString(names[i]))
	}
	failed := false
	for i, o := range outs {
		pad := names[i] + strings.Repeat(" ", w-utf8.RuneCountInString(names[i])) + "   "
		res := o.Result
		switch {
		case o.Failed():
			failed = true
			r.Count(view.Attention, pad+"not brought up to date: "+res.ProblemIn(o.Index))
		case res.Written == 0 && res.Removed == 0:
			r.Count(view.Neutral, pad+"up to date")
		default:
			facts := []string{report.Plural(int64(res.Written), "page written", "pages written")}
			if res.Removed > 0 {
				facts = append(facts, report.Plural(int64(res.Removed), "removed", "removed"))
			}
			r.Count(view.Done, pad+r.Join(facts...))
		}
	}
	if failed {
		r.Blank()
		r.Next("Next: move aside what is listed, then filo indexes refresh again")
		return errReported
	}
	return nil
}

// refreshIndexes brings every index up to date at the end of a run that changed the register (docs/design.md, Tag
// index pages, "When the pages are written"), saying nothing when that works. An index that could not be brought up
// to date is one line on stderr, and the run exits 1; the run's own change stands.
func (e *env) refreshIndexes(ctx context.Context, s *store.Store) {
	if e.indexesDone || !s.Changed() {
		return
	}
	outs, err := tagindex.UpdateAll(ctx, s, false, now())
	if err != nil {
		e.errorLine("the change is made, but the tag indexes were not brought up to date: " + err.Error())
		e.indexFailed = true
		return
	}
	for _, o := range outs {
		if o.Failed() {
			e.errorLine("the change is made, but " + e.view.Tilde(o.Index.Abs(o.Index.Page())) +
				" was not brought up to date: " + o.Result.ProblemIn(o.Index) + " (filo indexes lists it)")
			e.indexFailed = true
		}
	}
}
