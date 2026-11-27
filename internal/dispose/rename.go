package dispose

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alansav/filo/internal/store"
)

// RenameRecorder writes a rename to the register straight after it is done (store.RenameRun).
type RenameRecorder interface {
	Renamed(ctx context.Context, c store.Candidate, name string, now time.Time) error
}

// errNoReplace means the platform has no rename that refuses to replace its target, so Filo does not rename there.
var errNoReplace = errors.New("renaming needs a rename that never replaces a file (renameat2 on Linux), which this " +
	"system does not have; rename it by hand")

// beforeRename, if set by a test, runs after the checks and before the rename.
var beforeRename func(path string)

// Rename renames the file c to name in its own directory (docs/design.md, Tags, "Renaming a file by its name cell"),
// if it is still the very file the register knows at that path: through the directory opened from the root down, the
// same kind, inode, size and modification time as the last scan recorded (a symlink: its own, and its target). It
// does not read the file. The rename never replaces anything (renameat2 with RENAME_NOREPLACE), so a name taken on
// disk is never lost. A file renamed is recorded through rec straight after. The caller has checked the name with
// CheckName; one that is not a single name is refused here too. Outcome.Done says it was renamed; the error is for a
// cancelled run or a register that could not be written.
func Rename(ctx context.Context, rec RenameRecorder, c store.Candidate, name string, now time.Time) (Outcome,
	error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	old := path.Base(c.Path)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return Outcome{Reason: fmt.Sprintf("%q is not a file name", name), Failed: true}, nil
	}
	p, o, ok := within(c)
	if !ok {
		if o.Failed {
			o.Reason = "its recorded path is not inside its root; the register may have been altered, so it is not " +
				"renamed"
		}
		return o, nil
	}
	changed := skip("changed since the last scan; run filo scan and export again")
	dir, why, err := openDir(c.RootPath, filepath.FromSlash(c.Path))
	switch {
	case why != "":
		return skip(why), nil
	case errors.Is(err, fs.ErrNotExist):
		return changed, nil
	case err != nil:
		return Outcome{Reason: "could not be renamed: " + store.ErrDetail(err), Failed: true}, nil
	}
	defer dir.Close()
	fi, err := dir.Lstat(old)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return changed, nil
	case err != nil:
		return Outcome{Reason: "could not be renamed: " + store.ErrDetail(err), Failed: true}, nil
	}
	if _, bad := sameKind(c, fi); bad || !sameIno(c.Ino, ino(fi)) || fi.Size() != c.Size ||
		fi.ModTime().UnixNano() != c.MtimeNS {
		return changed, nil
	}
	if c.Kind == "symlink" {
		if target, err := dir.Readlink(old); err != nil || target != c.LinkTarget {
			return changed, nil
		}
	}
	taken := skip(fmt.Sprintf("a file named %s already exists there; nothing was replaced", name))
	// A case-only rename on a filesystem that ignores case finds the file itself at the new name: not taken.
	if at, err := dir.Lstat(name); err == nil && !os.SameFile(at, fi) {
		return taken, nil
	}
	if beforeRename != nil {
		beforeRename(p)
	}
	if !dir.stillAt() {
		return changed, nil
	}
	switch err := renameInPlace(dir.Root, old, name); {
	case errors.Is(err, errNoReplace):
		return Outcome{Reason: err.Error(), Failed: true}, nil
	case errors.Is(err, syscall.EEXIST) && strings.EqualFold(name, old):
		return Outcome{Reason: "this filesystem ignores case, so the new name counts as taken; rename it by hand",
			Failed: true}, nil
	case errors.Is(err, syscall.EEXIST):
		return taken, nil
	case errors.Is(err, fs.ErrNotExist):
		return changed, nil
	case err != nil:
		return Outcome{Reason: "could not be renamed: " + store.ErrDetail(err), Failed: true}, nil
	}
	if err := rec.Renamed(ctx, c, name, now); err != nil {
		return Outcome{Done: true}, fmt.Errorf("%w (%s was renamed to %s; the next scan will record it as moved)",
			err, p, name)
	}
	return Outcome{Done: true}, nil
}
