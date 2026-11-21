package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/alansav/filo/internal/show"
	"github.com/alansav/filo/internal/store"
)

// show is filo show PATH... (docs/design.md, Showing and setting a file's details): everything the register holds
// about each file, one after another. Read-only. A path that cannot be shown is an error line on stderr; the others
// are still shown, and the exit status is 1.
func (e *env) show(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("show")
	db := dbFlag(fs, dbBefore)
	history := fs.Bool("history", false, "")
	verify := fs.Bool("verify", false, "")
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
	day := today()
	failed := false
	shown := 0
	for i, arg := range pos {
		f, err := e.lookUp(ctx, s, arg, day, *verify)
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
		f.Render(e.view, i+1, len(pos), day, *history)
		shown++
	}
	if failed {
		return errReported
	}
	return nil
}

// lookUp finds the file a typed path names in the register and looks at it on disk.
func (e *env) lookUp(ctx context.Context, s *store.Store, arg, day string, verify bool) (show.File, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return show.File{}, err
	}
	root, rel, err := locateFile(ctx, s, p)
	if err != nil {
		return show.File{}, err
	}
	sh, err := s.ShowFile(ctx, root.ID, rel, day)
	if errors.Is(err, store.ErrNotInRegister) {
		if skippedBy(rel, root.Settings) {
			return show.File{}, fmt.Errorf("%s is not in the register: its root's settings skip it (filo roots "+
				"lists them)", p)
		}
		return show.File{}, fmt.Errorf("%s is not in the register yet: run filo scan", p)
	}
	if err != nil {
		return show.File{}, err
	}
	return show.Look(ctx, sh, root, verify), nil
}

// locateFile maps an absolute path to its root and its path in it. A path on disk is found through its directory,
// which resolves symlinks above it as a scan saw them; one no longer on disk (missing, deleted by filo) by the path
// alone. A directory is refused: showing one is not built yet.
func locateFile(ctx context.Context, s *store.Store, p string) (store.Root, string, error) {
	fi, err := os.Lstat(p)
	switch {
	case err == nil && fi.IsDir():
		return store.Root{}, "", fmt.Errorf("%s is a directory: filo show takes files for now", p)
	case err == nil:
		root, dir, err := s.Resolve(ctx, filepath.Dir(p))
		if errors.Is(err, store.ErrNoRoot) {
			return store.Root{}, "", fmt.Errorf("%s is not in the register (outside every root)", p)
		}
		if err != nil {
			return store.Root{}, "", err
		}
		return root, path.Join(dir, filepath.Base(p)), nil
	case errors.Is(err, fs.ErrNotExist):
		root, rel, err := s.Locate(ctx, p)
		if errors.Is(err, store.ErrNoRoot) {
			return store.Root{}, "", fmt.Errorf("%s is not in the register (outside every root)", p)
		}
		if err == nil && rel == "" {
			return store.Root{}, "", fmt.Errorf("%s is a root: filo show takes files for now", p)
		}
		return root, rel, err
	default:
		return store.Root{}, "", err
	}
}
