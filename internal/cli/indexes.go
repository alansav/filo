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
	"refresh [DIR] [--name NAME] | take [DIR] [--name NAME] [-y]"

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
	want := map[string]int{"": 0, "add": 1, "remove": 1, "refresh": 1, "take": 1}
	if _, known := want[sub]; !known {
		return usageError{fmt.Sprintf("unknown indexes command %q; see filo help indexes", sub)}
	}
	switch {
	case len(pos) > want[sub]:
		return usageError{"wrong number of arguments: " + indexesUsage}
	case sub == "" && *name != "":
		return usageError{"--name is for indexes add, remove and refresh"}
	case *yes && sub != "remove" && sub != "take":
		return usageError{"-y is for indexes remove and indexes take"}
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
	case "take":
		return e.indexTake(ctx, s, arg, *name, *yes)
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
	if dir != "" && walk.SkipsPath(dir, root.IncludeHidden, root.Ignore) {
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
		where, why := ix.Rel(p.Path), p.Why
		if p.Path == "" {
			where, why = e.indexPath(ix.RootPath, ix.Rel(ix.Page())), "not written: "+p.Why
		}
		e.view.Count(view.Attention, where+" "+why)
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
		written := "never written"
		if ix.WrittenUTC != "" {
			written = "written " + report.Time(ix.WrittenUTC)
		}
		if ix.Writer != "" {
			written = "written by " + deviceName(ctx, s, ix.Writer)
		}
		problem := ix.Problem
		tagCount := ""
		// An index whose data cannot be built (two tags with one page) is listed all the same, with why: this is the
		// list that says what is wrong.
		if d, err := tagindex.Build(ix, files); err != nil {
			problem = err.Error()
		} else {
			tagCount = report.Plural(int64(d.Tags()), "tag", "tags")
		}
		row := view.TableRow{Cells: []string{report.Plural(int64(len(files)), "file", "files"), tagCount, written}}
		if problem != "" {
			row.Attention = "not up to date"
			row.Below = []string{problem}
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
// the disk, or on a root that cannot be reached (not mounted), is found by its path alone, so its index can still be
// removed.
func findIndex(ctx context.Context, s *store.Store, arg, name string, gone bool) (store.TagIndex, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return store.TagIndex{}, err
	}
	var root store.Root
	var dir string
	root, dir, err = indexDir(ctx, s, p)
	if err != nil && gone {
		// Not a directory on a root that can be reached (gone, or on a drive not mounted): found by its path alone.
		root, dir, err = s.Locate(ctx, p)
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
	if ix.Writer != "" {
		// Another device writes its pages: this one only forgets it, and touches none of them.
		if err := s.RemoveTagIndex(ctx, ix.ID); err != nil {
			return err
		}
		e.view.Heading("Index forgotten", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()))
		e.view.Count(view.Neutral, "its pages are written by "+deviceName(ctx, s, ix.Writer)+": left as they are")
		return nil
	}
	if done, err := e.indexRemoveAway(ctx, s, ix, yes); done || err != nil {
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
	// "Done   removed 12 files · records/.tags/ kept, with the 1 file that is not Filo's" (docs/design.md, Tag index
	// pages): a directory kept is named with what kept it, listed above the question; anything else left is listed.
	facts := []string{"removed " + report.Plural(int64(res.Removed), "file", "files")}
	var rest []tagindex.Problem
	for _, p := range res.Problems {
		if p.Path == "" || ix.Files[p.Path] != "" || !strings.HasPrefix(p.Why, "holds files") {
			rest = append(rest, p)
			continue
		}
		n := 0
		for _, l := range left {
			if strings.HasPrefix(l.Path, p.Path+"/") {
				n++
			}
		}
		kept := r.Path(ix.RootPath, ix.Rel(p.Path)) + "/ kept"
		if n > 0 {
			kept += ", with the " + report.Plural(int64(n), "file that is", "files that are") + " not Filo's"
		}
		facts = append(facts, kept)
	}
	r.Outcome("Done", facts...)
	e.indexProblems(rest, ix)
	return errReported
}

// indexRemoveAway removes an index whose files cannot be removed, and reports done: when its root cannot be reached
// (a drive not mounted, lost or formatted), only after saying so and asking, since its files, should they come back,
// are no longer Filo's and the next scan registers them; when its directory is gone from a root that is there, its
// files went with it, and removing it is only removing the record. Otherwise it does nothing and remove goes on.
func (e *env) indexRemoveAway(ctx context.Context, s *store.Store, ix store.TagIndex, yes bool) (bool, error) {
	roots, err := s.Roots(ctx)
	if err != nil {
		return false, err
	}
	var rootErr error
	why := ""
	for _, rt := range roots {
		if rt.ID == ix.RootID {
			rootErr = store.CheckRoot(rt)
			why = fmt.Sprint(rootErr)
			if errors.Is(rootErr, store.ErrNotMounted) {
				why = rt.MountPath + " is not mounted"
			}
		}
	}
	r := e.view
	if rootErr == nil {
		if _, err := os.Lstat(ix.DirAbs()); !errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		r.Heading("Remove index", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()), "0 files")
		r.Count(view.Neutral, e.indexPath(ix.RootPath, ix.Dir)+" is gone, and its pages with it")
		ok, err := e.confirmChange(ctx, "Remove the index?", yes, nil)
		if err != nil || !ok {
			return true, err
		}
		if err := s.RemoveTagIndex(ctx, ix.ID); err != nil {
			return true, err
		}
		r.Outcome("Done", "removed the index")
		return true, nil
	}
	r.Heading("Remove index", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()))
	r.Blank()
	r.Count(view.Attention, "its files cannot be removed: "+why)
	r.Count(view.Attention, "removing the index anyway leaves them: should they come back, they are no longer "+
		"Filo's, and the next scan registers them as your files")
	ok, err := e.confirmChange(ctx, "Remove the index anyway, leaving its files?", yes, nil)
	if err != nil || !ok {
		return true, err
	}
	if err := s.RemoveTagIndex(ctx, ix.ID); err != nil {
		return true, err
	}
	r.Outcome("Done", "removed the index", "its files were left where they were")
	return true, nil
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
		if ix.Writer != "" {
			return fmt.Errorf("%s is written by %s, not this device (filo indexes take makes this device write it)",
				e.indexPath(ix.RootPath, ix.Rel(ix.Page())), deviceName(ctx, s, ix.Writer))
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

// indexTake makes this device the one that writes an index in a synced directory, after asking: the other devices
// stop writing it once they read this device's journal.
func (e *env) indexTake(ctx context.Context, s *store.Store, arg, name string, yes bool) error {
	ix, err := findIndex(ctx, s, arg, name, false)
	if err != nil {
		return err
	}
	r := e.view
	if ix.Writer == "" {
		r.Heading("Index unchanged", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()),
			"this device writes it already")
		return nil
	}
	r.Heading("Take index", view.Where{Roots: []string{ix.RootPath}}, ix.Rel(ix.Page()))
	r.Count(view.Neutral, "written by "+deviceName(ctx, s, ix.Writer)+" until now; from now on by this device")
	r.Count(view.Neutral, "pages this device did not write are left alone and reported: move them aside, then "+
		"filo indexes refresh")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Write this index from this device?", yes, nil)
	if err != nil || !ok {
		return err
	}
	if err := s.SetIndexWriter(ctx, ix.ID, ""); err != nil {
		return err
	}
	ix.Writer = ""
	o, err := tagindex.Update(ctx, s, ix, true, now())
	if err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	r.Outcome("Taken", ix.Rel(ix.Page()))
	if o.Failed() {
		e.indexProblems(o.Result.Problems, ix)
		return errReported
	}
	return nil
}

// deviceName is a device's name as this register knows it, or its ID; "-" (no device, in a conflict) says so.
func deviceName(ctx context.Context, s *store.Store, dev string) string {
	if dev == "-" {
		return "no device (two took it: filo indexes take on one)"
	}
	if devs, err := s.KnownDevices(ctx); err == nil {
		for _, d := range devs {
			if d.Dev == dev {
				return d.Name
			}
		}
	}
	return dev
}
