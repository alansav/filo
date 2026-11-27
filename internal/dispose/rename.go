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
	"unicode"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/store"
)

// RenameRecorder writes a rename or move to the register straight after it is done (store.RenameRun): the file and
// its new path, relative to its root.
type RenameRecorder interface {
	Renamed(ctx context.Context, c store.Candidate, newPath string, now time.Time) error
}

// errNoReplace means the platform has no rename that refuses to replace its target, so Filo does not rename there.
var errNoReplace = errors.New("renaming needs a rename that never replaces a file (renameat2 on Linux), which this " +
	"system does not have; rename it by hand")

// beforeRename, if set by a test, runs after the checks and before the rename.
var beforeRename func(path string)

// Mover renames and moves files within their roots for filo update (docs/design.md, Tags, "Renaming a file by its
// name cell" and "Moving a file by its directory cell"), and remembers the directories it took files out of, to
// prune those the moves emptied.
type Mover struct {
	rec  RenameRecorder
	now  func() time.Time
	dirs map[string]string // directory a file was moved out of -> its root
}

// NewMover returns a Mover that records through rec and stamps its records with now.
func NewMover(rec RenameRecorder, now func() time.Time) *Mover {
	return &Mover{rec: rec, now: now, dirs: map[string]string{}}
}

// Move renames or moves rn's file to rn.NewPath() within its root, if it is still the very file the register knows
// at its path (see CheckMove). It does not read the file. The directories the new path needs are made just before,
// each through the one above it; if the move then fails, those it made are removed again while empty. The rename
// never replaces anything (renameat2 with RENAME_NOREPLACE, through the two opened directories), so a name taken on
// disk, even in the instant before, is never lost. A file moved is recorded through the recorder straight after.
// Outcome.Done says it was moved, Outcome.Made the directories made for it (absolute); the error is for a cancelled
// run or a register that could not be written.
func (m *Mover) Move(ctx context.Context, rn store.Rename) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	l, o, ok := lookBeforeMove(rn)
	if !ok {
		return o, nil
	}
	defer l.close()
	changed := skip("changed since the last scan; run filo scan and export again")
	if beforeRename != nil {
		beforeRename(l.path)
	}
	if !l.src.stillAt() || (l.dst != nil && !l.dst.stillAt()) {
		return changed, nil
	}
	made, err := l.makeDirs()
	if err != nil {
		l.unmake(made)
		return Outcome{Reason: "could not make the directory to move it into: " + store.ErrDetail(err),
			Failed: true}, nil
	}
	old := l.src.name
	err = renameBetween(l.src.Root, old, l.dst.Root, rn.Name)
	switch {
	case err == nil:
	case errors.Is(err, errNoReplace):
		o = Outcome{Reason: err.Error(), Failed: true}
	case errors.Is(err, syscall.EEXIST) && !rn.Moves() && strings.EqualFold(rn.Name, old):
		o = Outcome{Reason: "this filesystem ignores case, so the new name counts as taken; rename it by hand",
			Failed: true}
	case errors.Is(err, syscall.EEXIST):
		o = l.taken()
	case errors.Is(err, syscall.EXDEV):
		o = Outcome{Reason: "the new directory is on another filesystem; a move stays on the root's", Failed: true}
	case errors.Is(err, fs.ErrNotExist):
		o = changed
	default:
		o = Outcome{Reason: "could not be moved: " + store.ErrDetail(err), Failed: true}
	}
	if err != nil {
		l.unmake(made)
		return o, nil
	}
	if rn.Moves() {
		m.dirs[filepath.Dir(l.path)] = rn.RootPath
	}
	o = Outcome{Done: true, Made: made}
	// Recorded whatever happens to ctx now: the file is at its new path, and a Ctrl-C landing here must not leave the
	// register at the old one.
	if err := m.rec.Renamed(context.WithoutCancel(ctx), rn.Candidate, rn.NewPath(), m.now()); err != nil {
		return o, fmt.Errorf("%w (%s was moved to %s; the next scan will record it as moved)", err, l.path,
			rn.NewPath())
	}
	return o, nil
}

// PruneDirs removes the directories the moves emptied, as filo delete does (see Disposer.PruneDirs).
func (m *Mover) PruneDirs() (removed []string, problems []error) { return pruneDirs(m.dirs) }

// CheckMove says why rn's file cannot be renamed or moved now, looking at the disk without changing it; "" if it can,
// as far as can be told before the move itself (filo update asks it before its question). The file must be the very
// one the register knows at its path: through the directory opened from the root down, the same kind, inode, size
// and modification time as the last scan recorded (a symlink: its own, and its target). Each directory of the new
// path that exists must be a real directory on the root's filesystem, and no other file may have the new name there.
func CheckMove(rn store.Rename) string {
	l, o, ok := lookBeforeMove(rn)
	if !ok {
		return o.Reason
	}
	l.close()
	return ""
}

// moveLook is what the look before a move found: the open directory that holds the file and what lstat saw of it
// there; the deepest existing directory of the new path, open (dst, which is the new directory itself when missing
// is empty); and the directories still to make below it.
type moveLook struct {
	rn      store.Rename
	src     *heldDir
	fi      fs.FileInfo
	path    string
	dst     *heldDir
	same    bool     // dst is src (a rename in place)
	missing []string // the names of the directories to make, from dst down
}

func (l *moveLook) close() {
	l.src.Close()
	if l.dst != nil && !l.same {
		l.dst.Close()
	}
}

func (l *moveLook) taken() Outcome {
	return skip(fmt.Sprintf("a file named %s already exists there; nothing was replaced", l.rn.Name))
}

// makeDirs makes the missing directories, each in the one above it, opened and checked to be the very directory
// made (never a symlink swapped in); dst ends as the new directory. It returns the directories made (absolute).
func (l *moveLook) makeDirs() ([]string, error) {
	var made []string
	p := filepath.Join(l.rn.RootPath, filepath.FromSlash(strings.Join(l.dstParts(), "/")))
	for _, name := range l.missing {
		p = filepath.Join(p, name)
		if err := l.dst.Mkdir(name, 0o777); err != nil {
			return made, err
		}
		made = append(made, p)
		sub, fi, why, err := openSub(l.dst.Root, name, p)
		if why != "" {
			err = errors.New(why)
		}
		if err != nil {
			return made, err
		}
		if !l.same {
			l.dst.Close()
		}
		l.dst = &heldDir{Root: sub, chain: append(l.dst.chain[:len(l.dst.chain):len(l.dst.chain)], dirAt{p, fi})}
		l.same = false
	}
	l.missing = nil
	return made, nil
}

// dstParts are the directories of the new path that existed at the look, relative to the root.
func (l *moveLook) dstParts() []string {
	var parts []string
	if l.rn.Dir != "" {
		parts = strings.Split(l.rn.Dir, "/")
	}
	return parts[:len(parts)-len(l.missing)]
}

// unmake removes, deepest first, the directories this move made, each only while it is empty.
func (l *moveLook) unmake(made []string) {
	for i := len(made) - 1; i >= 0; i-- {
		if pruneDir(l.rn.RootPath, made[i]) != nil {
			return
		}
	}
}

// lookBeforeMove is the check before a move (see CheckMove), or (ok false) the outcome that stops it. The caller
// closes what it found.
func lookBeforeMove(rn store.Rename) (*moveLook, Outcome, bool) {
	c, name := rn.Candidate, rn.Name
	if !canRename {
		return nil, Outcome{Reason: errNoReplace.Error(), Failed: true}, false
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, Outcome{Reason: fmt.Sprintf("%q is not a file name", name), Failed: true}, false
	}
	p, o, ok := within(c)
	if !ok {
		if o.Failed {
			o.Reason = "its recorded path is not inside its root; the register may have been altered, so it is not " +
				"moved"
		}
		return nil, o, false
	}
	if rn.Dir != "" && (!filepath.IsLocal(filepath.FromSlash(rn.Dir)) || path.Clean(rn.Dir) != rn.Dir) {
		return nil, Outcome{Reason: fmt.Sprintf("%q is not a directory inside the root", rn.Dir), Failed: true}, false
	}
	changed := skip("changed since the last scan; run filo scan and export again")
	failed := func(err error) Outcome {
		return Outcome{Reason: "could not be moved: " + store.ErrDetail(err), Failed: true}
	}
	src, why, err := openDir(c.RootPath, filepath.FromSlash(c.Path))
	switch {
	case why != "":
		return nil, skip(why), false
	case errors.Is(err, fs.ErrNotExist):
		return nil, changed, false
	case err != nil:
		return nil, failed(err), false
	}
	l := &moveLook{rn: rn, src: src, path: p}
	stop := func(o Outcome) (*moveLook, Outcome, bool) {
		l.close()
		return nil, o, false
	}
	old := src.name
	if l.fi, err = src.Lstat(old); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return stop(changed)
		}
		return stop(failed(err))
	}
	fi := l.fi
	if _, bad := sameKind(c, fi); bad || !sameIno(c.Ino, ino(fi)) || fi.Size() != c.Size ||
		fi.ModTime().UnixNano() != c.MtimeNS {
		return stop(changed)
	}
	if c.Kind == "symlink" {
		if target, err := src.Readlink(old); err != nil || target != c.LinkTarget {
			return stop(changed)
		}
	}
	if !rn.Moves() {
		l.dst, l.same = src, true
	} else {
		// A file on another filesystem than its root's (one mounted inside it) can never be moved by a rename: said
		// now, before anything is made.
		if rootFi, err := os.Lstat(c.RootPath); err == nil && devOf(rootFi) != devOf(fi) {
			return stop(Outcome{Reason: "it is on another filesystem than its root's; a move stays on the root's",
				Failed: true})
		}
		if o, ok := l.openDst(); !ok {
			return stop(o)
		}
	}
	if len(l.missing) == 0 {
		// A case-only rename on a filesystem that ignores case finds the file itself at the new name: not taken.
		if at, err := l.dst.Lstat(name); err == nil && !os.SameFile(at, fi) {
			return stop(l.taken())
		}
	}
	return l, Outcome{}, true
}

// openDst opens the new directory, or the deepest of it that exists (the rest are missing, to be made), from the root
// down, each step a real directory and on the root's filesystem.
func (l *moveLook) openDst() (Outcome, bool) {
	root := l.rn.RootPath
	other := Outcome{Reason: "the new directory is on another filesystem mounted inside the root; a move stays on " +
		"the root's", Failed: true}
	rootFi, err := os.Lstat(root)
	if err != nil {
		return Outcome{Reason: "could not be moved: " + store.ErrDetail(err), Failed: true}, false
	}
	parts := strings.Split(l.rn.Dir, "/")
	if l.rn.Dir == "" {
		parts = nil
	}
	// The first part's directory, opened through openDir (which opens the root and every directory down to it).
	var dst *heldDir
	if len(parts) == 0 {
		h, why, err := openDir(root, "x")
		if why != "" || err != nil {
			return Outcome{Reason: "could not be moved: the root cannot be opened", Failed: true}, false
		}
		dst = h
	} else {
		h, why, err := openDir(root, filepath.FromSlash(parts[0]))
		if why != "" || err != nil {
			return Outcome{Reason: "could not be moved: the root cannot be opened", Failed: true}, false
		}
		dst = h
		p := root
		for i, name := range parts {
			p = filepath.Join(p, name)
			fi, err := dst.Lstat(name)
			if errors.Is(err, fs.ErrNotExist) {
				l.missing = parts[i:]
				break
			}
			if err != nil {
				dst.Close()
				return Outcome{Reason: "could not be moved: " + store.ErrDetail(err), Failed: true}, false
			}
			if why := notRealDir(p, fi); why != "" {
				dst.Close()
				return skip(strings.Replace(why, "a directory on its path", "a directory on the new path", 1)), false
			}
			if devOf(fi) != devOf(rootFi) {
				dst.Close()
				return other, false
			}
			if onDisk, ok := nameOnDisk(dst.Root, name, fi); !ok {
				dst.Close()
				return Outcome{Reason: fmt.Sprintf("the directory %s is named %s on disk, and this filesystem ignores "+
					"case; give the directory as it is named", name, onDisk), Failed: true}, false
			}
			sub, fi, why, err := openSub(dst.Root, name, p)
			dst.Close()
			if why != "" || err != nil {
				return Outcome{Reason: "could not be moved: a directory on the new path changed", Failed: true}, false
			}
			dst = &heldDir{Root: sub, chain: append(dst.chain, dirAt{p, fi})}
		}
	}
	l.dst = dst
	return Outcome{}, true
}

// devOf is the device a file is on.
func devOf(fi fs.FileInfo) uint64 { id, _ := fsid.FromFileInfo(fi); return id.Dev }

// nameOnDisk checks that name, which lstat found in dir (fi), is spelled so on disk: on a filesystem that ignores
// case, lstat finds Received for received, and a move into it would be recorded under the wrong name. Only when the
// name with its case swapped is the same directory (the filesystem ignores case) is the directory listed to find
// the name as written. ok is false, with that name, when it is spelled otherwise.
func nameOnDisk(dir *os.Root, name string, fi fs.FileInfo) (string, bool) {
	swapped := strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, name)
	if swapped == name {
		return name, true
	}
	if at, err := dir.Lstat(swapped); err != nil || !os.SameFile(at, fi) {
		return name, true // the filesystem tells the two apart
	}
	f, err := dir.Open(".")
	if err != nil {
		return name, true
	}
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	for _, n := range names {
		if n == name {
			return name, true
		}
	}
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return n, false
		}
	}
	return name, true
}
