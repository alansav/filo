package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alansav/filo/internal/find"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// find is filo find [DIR] [--name PATTERN] [-0] [--no-scan] [--workers N] (docs/design.md, Finding files): a scan
// first, then the files present under DIR (or every root) whose base name matches, for reading in a terminal or, piped,
// as a list of paths for other tools.
func (e *env) find(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("find")
	db := dbFlag(fl, dbBefore)
	name := fl.String("name", "", "")
	nul := fl.Bool("0", false, "")
	noScan := fl.Bool("no-scan", false, "")
	workers := fl.Int("workers", reconcile.DefaultWorkers, "")
	pos, err := e.parse(fl, "find", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"find takes at most one DIR: a search of two places is two runs"}
	}
	if err := checkWorkers(fl, *workers, *noScan); err != nil {
		return err
	}
	if *nul && e.view.Terminal() {
		return usageError{"-0 writes raw names for a pipe, such as xargs -0"}
	}
	if err := checkTildes("DIR", pos...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)

	place, err := findPlace(ctx, s, pos)
	if err != nil {
		return err
	}
	v := e.view
	notScanned := ""
	if *noScan {
		if notScanned, err = lastScanLine(ctx, s, place.filter); err != nil {
			return err
		}
	} else if err := e.findScan(ctx, s, place.scan, *workers); err != nil {
		return err
	}

	rows, err := s.FindFiles(ctx, place.filter.RootID, place.filter.Scope)
	if err != nil {
		return err
	}
	files := find.Select(rows, *name)
	if v.Terminal() {
		find.Render(v, files, *name, place.where, notScanned)
		return nil
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.AbsPath()
	}
	if *nul {
		v.RawPaths(paths)
		return nil
	}
	if n := v.PathLines(paths); n > 0 {
		e.errorLine(fmt.Sprintf("%s escaped and will not match on disk; use -0",
			pluralWord(n, "1 name was", report.Count(int64(n))+" names were")))
		return errReported
	}
	return nil
}

// findWhere is where filo find searches: the filter on the register, the place its heading names, and what its scan
// covers ("" for every root).
type findWhere struct {
	filter store.Filter
	where  view.Where
	scan   string
}

// findPlace looks DIR up in the register as filo show does: a directory on disk, or one gone from it (which just
// holds no file present now), inside a root. With no DIR, every root. A DIR outside every root, or a file, is a usage
// error. A DIR gone from the disk is scanned through its root, so its files are found missing.
func findPlace(ctx context.Context, s *store.Store, pos []string) (findWhere, error) {
	if len(pos) == 0 {
		roots, err := s.Roots(ctx)
		if err != nil {
			return findWhere{}, err
		}
		var w view.Where
		for _, r := range roots {
			w.Roots = append(w.Roots, r.Path)
		}
		return findWhere{where: w}, nil
	}
	p, err := filepath.Abs(pos[0])
	if err != nil {
		return findWhere{}, err
	}
	root, rel, isDir, err := locatePath(ctx, s, p)
	var outside outsideError
	if errors.As(err, &outside) {
		return findWhere{}, usageError{p + " is not inside a root (filo roots lists them)"}
	}
	if err != nil {
		return findWhere{}, err
	}
	place := findWhere{filter: store.Filter{RootID: root.ID, Scope: rel},
		where: view.Where{Roots: []string{root.Path}, Under: rel}, scan: p}
	if isDir {
		return place, nil
	}
	if _, err := os.Lstat(p); err == nil {
		return findWhere{}, usageError{p + " is a file: use filo show"}
	}
	if _, err := s.ShowFile(ctx, root.ID, rel, today()); err == nil {
		return findWhere{}, usageError{p + " is a file: use filo show"}
	} else if !errors.Is(err, store.ErrNotInRegister) {
		return findWhere{}, err
	}
	place.scan = root.Path // gone from the disk: its root's scan finds its files missing
	return place, nil
}

// findScan is filo find's scan first, as an export's (docs/design.md, Lifespan, "A scan first"): of dir, or every
// root when dir is "". In a terminal its output comes first; piped, nothing of it is on stdout, so the list stays a
// list of paths, and a root that fails is named on stderr. A root that fails stops the command with nothing listed.
func (e *env) findScan(ctx context.Context, s *store.Store, dir string, workers int) error {
	shown := e.view
	if !shown.Terminal() {
		e.view = view.New(view.Options{Out: io.Discard})
		defer func() { e.view = shown }()
	}
	err := e.scanWhere(ctx, s, reconcile.Options{Workers: workers}, dir)
	switch {
	case errors.Is(err, errNoRoots):
		return nil
	case err == nil:
		if shown.Terminal() {
			shown.Blank()
		}
		return nil
	case ctx.Err() != nil:
		return errors.New("interrupted; nothing was listed")
	case errors.Is(err, errReported): // the failed roots are on stdout in a terminal, and named on stderr by scanAll
		return errors.New("nothing listed: a root could not be scanned; fix it, or find with --no-scan from the " +
			"register as it is")
	}
	return fmt.Errorf("nothing listed: %w", err)
}
