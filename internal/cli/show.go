package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/alansav/filo/internal/show"
	"github.com/alansav/filo/internal/store"
)

// show is filo show PATH... (docs/design.md, Showing and setting a file's details): everything the register holds
// about each file or directory, one after another. Read-only. A path that cannot be shown is an error line on
// stderr; the others are still shown, and the exit status is 1.
func (e *env) show(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("show")
	db := dbFlag(fs, dbBefore)
	history := fs.Bool("history", false, "")
	verify := fs.Bool("verify", false, "")
	var recursive bool
	fs.BoolVar(&recursive, "r", false, "")
	fs.BoolVar(&recursive, "recursive", false, "")
	pos, err := e.parse(fs, "show", args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"show takes one PATH or more"}
	}
	if err := checkTildes("PATH", pos...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	o := showOptions{day: today(), history: *history, verify: *verify, recursive: recursive}
	failed := false
	shown := 0
	for i, arg := range pos {
		render, err := e.lookUp(ctx, s, arg, o)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.errorLine(err.Error())
			failed = true
			continue
		}
		if shown > 0 {
			e.view.Blank()
		}
		render(i+1, len(pos))
		shown++
	}
	if failed {
		return errReported
	}
	return nil
}

// showOptions are filo show's flags, and the day its statuses are worked out for.
type showOptions struct {
	day                        string
	history, verify, recursive bool
}

// lookUp finds the file or directory a typed path names in the register, looks at it on disk, and returns what
// renders it as item n of total.
func (e *env) lookUp(ctx context.Context, s *store.Store, arg string, o showOptions) (func(n, total int), error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return nil, err
	}
	root, rel, isDir, err := locatePath(ctx, s, p)
	if err != nil {
		return nil, err
	}
	dir := func() (func(n, total int), error) {
		d, err := s.ShowDir(ctx, root.ID, rel, o.day)
		if err != nil {
			return nil, err
		}
		shown := show.LookDir(d, root)
		return func(n, total int) { shown.Render(e.view, n, total, o.recursive) }, nil
	}
	if isDir {
		return dir()
	}
	sh, err := s.ShowFile(ctx, root.ID, rel, o.day)
	if errors.Is(err, store.ErrNotInRegister) {
		// Not a file filo knows: a directory gone from the disk whose files it still holds, or not known at all.
		if has, err := s.HasFilesUnder(ctx, root.ID, rel); err != nil {
			return nil, err
		} else if has {
			return dir()
		}
		if skippedBy(rel, root.Settings) {
			return nil, fmt.Errorf("%s is not in the register: its root's settings skip it (filo roots lists them)", p)
		}
		return nil, fmt.Errorf("%s is not in the register yet: run filo scan", p)
	}
	if err != nil {
		return nil, err
	}
	f := show.Look(ctx, sh, root, o.verify)
	return func(n, total int) { f.Render(e.view, n, total, o.day, o.history) }, nil
}

// locatePath maps an absolute path to its root and its path in it, and says whether it is a directory on disk (a
// root is one). A path on disk is found through its directory (itself, for a directory), which resolves symlinks
// above it as a scan saw them. One not on disk (missing, deleted by filo), or that cannot be looked at (in a
// directory filo cannot read), through the deepest directory above it that is on disk, resolved the same way, so a
// symlinked or bind-mounted way into a root finds it either way; failing that, by the path alone.
func locatePath(ctx context.Context, s *store.Store, p string) (store.Root, string, bool, error) {
	outside := func(err error) error {
		if errors.Is(err, store.ErrNoRoot) {
			return fmt.Errorf("%s is not in the register (outside every root)", p)
		}
		return err
	}
	fi, err := os.Lstat(p)
	switch {
	case err == nil && fi.IsDir():
		root, rel, err := s.Resolve(ctx, p)
		return root, rel, true, outside(err)
	case err == nil:
		root, dir, err := s.Resolve(ctx, filepath.Dir(p))
		if err != nil {
			return store.Root{}, "", false, outside(err)
		}
		return root, path.Join(dir, filepath.Base(p)), false, nil
	}
	root, rel, err := locateGone(ctx, s, p)
	return root, rel, rel == "" && err == nil, outside(err)
}

// locateGone finds the root of a path not on disk: through the deepest directory above it that is, if that resolves
// to a root, else by the path alone (store.Locate).
func locateGone(ctx context.Context, s *store.Store, p string) (store.Root, string, error) {
	rest := []string{filepath.Base(p)}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			if root, rel, err := s.Resolve(ctx, dir); err == nil {
				return root, path.Join(append([]string{rel}, rest...)...), nil
			}
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
	return s.Locate(ctx, p)
}
