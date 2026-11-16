package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// duplicates is filo duplicates: what is not counted as duplicates, and exclude and include to change it
// (docs/design.md, Duplicates, "filo duplicates" and Output).
func (e *env) duplicates(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	switch sub {
	case "":
		return e.duplicatesList(ctx, dbBefore, rest)
	case "exclude", "include":
		fl := e.flags("duplicates " + sub)
		db := dbFlag(fl, dbBefore)
		pos, err := e.parse(fl, "duplicates", rest)
		if err != nil {
			return err
		}
		what := "DIR"
		if sub == "include" {
			what = "PATH"
		}
		if len(pos) != 1 {
			return usageError{"duplicates " + sub + " takes exactly one " + what}
		}
		if err := checkTilde(what, pos[0]); err != nil {
			return err
		}
		s, err := e.open(ctx, *db)
		if err != nil {
			return err
		}
		defer e.closeStore(s)
		if sub == "exclude" {
			return e.duplicatesExclude(ctx, s, pos[0])
		}
		return e.duplicatesInclude(ctx, s, pos[0])
	}
	return usageError{fmt.Sprintf("unknown duplicates command %q", sub)}
}

// duplicatesList lists the whole register's excluded directories, contents always and sets of records.
func (e *env) duplicatesList(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("duplicates")
	db := dbFlag(fl, dbBefore)
	pos, err := e.parse(fl, "duplicates", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{"unknown duplicates command"}
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
	dirs, err := s.ExcludedDirs(ctx)
	if err != nil {
		return err
	}
	always, records, err := s.AlwaysAndRecords(ctx)
	if err != nil {
		return err
	}
	r := e.view
	var facts []string
	if len(dirs) > 0 {
		facts = append(facts, report.Plural(int64(len(dirs)), "excluded directory", "excluded directories"))
	}
	if len(always) > 0 {
		facts = append(facts, report.Plural(int64(len(always)), "content always", "contents always"))
	}
	if len(records) > 0 {
		facts = append(facts, report.Plural(int64(len(records)), "set of records", "sets of records"))
	}
	if len(facts) == 0 {
		r.Heading("Not counted as duplicates", view.Where{Roots: roots}, "none")
		r.Blank()
		r.Next("Next: filo duplicates exclude DIR to leave out a folder that is a copy on purpose (a backup)")
		return nil
	}
	r.Heading("Not counted as duplicates", view.Where{Roots: roots}, facts...)
	if len(dirs) > 0 {
		r.Blank()
		r.Section("Excluded directories")
		for _, d := range dirs {
			r.Entry(2, store.AbsPath(d.RootPath, d.Path), report.Plural(d.Files, "file", "files"), report.Size(d.Bytes))
		}
	}
	if len(always) > 0 {
		r.Blank()
		r.Section("Contents always")
		for _, c := range always {
			g := view.DupGroup{Name: path.Base(c.Copies[0].Path), Each: report.Size(c.Size),
				Facts: []string{report.Plural(int64(len(c.Copies)), "copy", "copies")}}
			for _, cp := range c.Copies[:min(3, len(c.Copies))] { // they are often everywhere
				g.Copies = append(g.Copies, view.Copy{Root: cp.RootPath, Rel: cp.Path})
			}
			r.Dup(g)
			if more := len(c.Copies) - len(g.Copies); more > 0 {
				r.More(more, "copy", "copies")
			}
		}
	}
	if len(records) > 0 {
		r.Blank()
		r.Section("Sets of records")
		for _, set := range records {
			g := view.DupGroup{Name: path.Base(set.Copies[0].Path), Each: report.Size(set.Size),
				Facts: []string{report.Plural(int64(len(set.Copies)), "record", "records")}}
			for _, cp := range set.Copies { // a set is about those exact files
				g.Copies = append(g.Copies, view.Copy{Root: cp.RootPath, Rel: cp.Path, Note: keepNote(cp.Candidate)})
			}
			r.Dup(g)
		}
	}
	r.Blank()
	r.Next("Next: filo duplicates include PATH to count one as a duplicate again")
	return nil
}

// verb is one for a count of 1, else many.
func verb(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// groupChange words how many duplicate groups a change made: "41 duplicate groups fewer: 256 left", "1 duplicate
// group more: 297", "duplicate groups unchanged: 256".
func groupChange(before, after int) string {
	switch {
	case after < before:
		return report.Plural(int64(before-after), "duplicate group", "duplicate groups") + " fewer: " +
			report.Count(int64(after)) + " left"
	case after > before:
		return report.Plural(int64(after-before), "duplicate group", "duplicate groups") + " more: " +
			report.Count(int64(after))
	}
	return "duplicate groups unchanged: " + report.Count(int64(after))
}

// duplicatesExclude makes the files under DIR never count as duplicates. Nothing is deleted and one command undoes
// it, so it does not ask.
func (e *env) duplicatesExclude(ctx context.Context, s *store.Store, arg string) error {
	root, dir, err := s.Resolve(ctx, arg)
	if err != nil {
		return err
	}
	abs := store.AbsPath(root.Path, dir)
	r := e.view
	if covering, ok, err := s.Exclusion(ctx, root.ID, dir); err != nil {
		return err
	} else if ok {
		r.Heading("Already excluded", view.Where{Roots: []string{abs}})
		if covering != dir {
			r.CountName(view.Neutral, "through ", store.AbsPath(root.Path, covering), "")
		}
		return nil
	}
	before, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	replaced, err := s.Exclude(ctx, root.ID, dir, now())
	if err != nil {
		return err
	}
	after, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	files, _, err := s.FilesUnder(ctx, root.ID, dir)
	if err != nil {
		return err
	}
	r.Heading("Excluded", view.Where{Roots: []string{abs}})
	r.Count(view.Neutral, report.Plural(files, "file", "files")+" under it no longer "+verb(files, "counts", "count")+
		" as duplicates (still scanned, retentions unchanged)")
	if len(replaced) > 0 {
		r.Count(view.Neutral, "replaces "+report.Plural(int64(len(replaced)), "exclusion", "exclusions")+
			" under it: "+strings.Join(replaced, ", "))
	}
	r.Count(view.Neutral, groupChange(before, after))
	r.Blank()
	r.Next("Next: filo duplicates include " + shellQuote(abs) + " to undo it")
	return nil
}

// duplicatesInclude undoes whichever applies to PATH: a directory's exclusion, or, for a file, its content always
// or its whole set of records. One command never undoes two things. PATH need not be on disk any more: an excluded
// folder since deleted or unmounted, or a record whose folder went, is found by its path alone (store.Locate).
func (e *env) duplicatesInclude(ctx context.Context, s *store.Store, arg string) error {
	p, err := filepath.Abs(arg)
	if err != nil {
		return err
	}
	fi, statErr := os.Stat(p)
	if statErr == nil && fi.IsDir() {
		root, dir, err := s.Resolve(ctx, p)
		if err != nil {
			return err
		}
		return e.includeDir(ctx, s, root, dir)
	}
	var root store.Root
	var rel string
	if statErr == nil {
		// A file on disk: found through its folder, which resolves symlinks above it as a scan saw them.
		var dir string
		if root, dir, err = s.Resolve(ctx, filepath.Dir(p)); err != nil {
			return err
		}
		rel = path.Join(dir, filepath.Base(p))
	} else {
		if root, rel, err = s.Locate(ctx, p); err != nil {
			return err
		}
		// Gone from the disk: its own exclusion is still to be undone.
		if covering, ok, err := s.Exclusion(ctx, root.ID, rel); err != nil {
			return err
		} else if ok && covering == rel {
			return e.includeDir(ctx, s, root, rel)
		}
	}
	abs := store.AbsPath(root.Path, rel)
	f, err := s.FileAt(ctx, root.ID, rel)
	if errors.Is(err, store.ErrNoFile) {
		return fmt.Errorf("%s: %w", abs, err)
	} else if err != nil {
		return err
	}
	r := e.view
	before, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	name, each := path.Base(rel), report.Size(f.Size)+" each"
	var heading, facts, what string
	switch {
	case f.RecordSet != 0:
		if err := s.RemoveRecordSet(ctx, f.RecordSet); err != nil {
			return err
		}
		heading, facts = "Duplicates again", report.Plural(f.SetSize, "record", "records")
		what = "the set of " + report.Plural(f.SetSize, "record", "records") + " is removed"
	case f.Always:
		if err := s.RemoveAlways(ctx, f.Hash); err != nil {
			return err
		}
		heading, facts, what = "Duplicate again", report.Plural(f.Copies, "copy", "copies"), "no longer always"
	default:
		excluded, ok, err := s.Exclusion(ctx, root.ID, rel)
		if err != nil {
			return err
		}
		r.Heading("Nothing to include", view.Where{Roots: []string{abs}})
		if ok {
			r.CountName(view.Neutral, "not always or a record; ", store.AbsPath(root.Path, excluded),
				" is excluded: filo duplicates include "+shellQuote(store.AbsPath(root.Path, excluded))+" counts it again")
			return nil
		}
		r.Count(view.Neutral, "not excluded, always or a record: it already counts as a duplicate")
		return nil
	}
	after, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	r.Heading(heading, view.Where{}, name, each, facts)
	r.Count(view.Neutral, what+": "+groupChange(before, after))
	if f.RecordSet != 0 && f.Always {
		r.Count(view.Neutral, "its content is still always: filo duplicates include "+shellQuote(abs)+
			" again counts it as a duplicate")
	}
	if excluded, ok, err := s.Exclusion(ctx, root.ID, rel); err != nil {
		return err
	} else if ok {
		dirAbs := store.AbsPath(root.Path, excluded)
		r.CountName(view.Neutral, "", dirAbs, " is still excluded: filo duplicates include "+shellQuote(dirAbs)+
			" counts it again")
	}
	return nil
}

// includeDir removes a directory's own exclusion, or says which exclusion covers it.
func (e *env) includeDir(ctx context.Context, s *store.Store, root store.Root, dir string) error {
	abs := store.AbsPath(root.Path, dir)
	r := e.view
	covering, ok, err := s.Exclusion(ctx, root.ID, dir)
	if err != nil {
		return err
	}
	switch {
	case ok && covering != dir:
		parent := store.AbsPath(root.Path, covering)
		r.Heading("Not excluded itself", view.Where{Roots: []string{abs}})
		r.CountName(view.Neutral, "", parent, " is: filo duplicates include "+shellQuote(parent)+
			" counts all of it again")
		return nil
	case !ok:
		under, err := s.ExclusionsUnder(ctx, root.ID, dir)
		if err != nil {
			return err
		}
		r.Heading("Nothing to include", view.Where{Roots: []string{abs}})
		if len(under) > 0 {
			r.Count(view.Neutral, "not excluded itself; "+report.Plural(int64(len(under)), "exclusion",
				"exclusions")+" under it: "+strings.Join(under, ", ")+" (filo duplicates lists them)")
		} else {
			r.Count(view.Neutral, "not excluded: its files already count as duplicates")
		}
		return nil
	}
	before, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	if _, err := s.Include(ctx, root.ID, dir); err != nil {
		return err
	}
	after, err := s.GroupCount(ctx)
	if err != nil {
		return err
	}
	files, _, err := s.FilesUnder(ctx, root.ID, dir)
	if err != nil {
		return err
	}
	r.Heading("Included again", view.Where{Roots: []string{abs}})
	r.Count(view.Neutral, report.Plural(files, "file", "files")+" under it "+verb(files, "counts", "count")+
		" as duplicates again")
	r.Count(view.Neutral, groupChange(before, after))
	r.Blank()
	r.Next("Next: filo export duplicates -o FILE to decide them")
	return nil
}
