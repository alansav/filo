package tagindex

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alansav/filo/internal/store"
)

// Problem is a file or directory of an index that Filo left alone, and why.
type Problem struct {
	Path string // relative to the index's directory, '/'-separated; "" for the directory itself
	Why  string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Why
	}
	return p.Path + " " + p.Why
}

// Result is what writing or removing an index's files did.
type Result struct {
	Written, Removed int
	Problems         []Problem
	// Files is every file and directory of the index Filo knows it wrote, after this: a file's SHA-256, "" for a
	// directory (store.IndexWrite.Files).
	Files map[string]string
}

// Problem is the problems as one phrase, for a line or the register: the first, and how many more.
func (r Result) Problem() string {
	switch len(r.Problems) {
	case 0:
		return ""
	case 1:
		return r.Problems[0].String()
	default:
		return fmt.Sprintf("%s (and %d more)", r.Problems[0], len(r.Problems)-1)
	}
}

// ProblemIn is Problem with each path relative to the index's root, as a line naming no index shows it.
func (r Result) ProblemIn(ix store.TagIndex) string {
	moved := r
	moved.Problems = nil
	for _, p := range r.Problems {
		if p.Path == "" {
			p.Path = ix.Rel(ix.Page())
			p.Why = "not written: " + p.Why
		} else {
			p.Path = ix.Rel(p.Path)
		}
		moved.Problems = append(moved.Problems, p)
	}
	return moved.Problem()
}

const (
	whyChanged  = "was changed since Filo wrote it: not replaced"
	whyNotFilos = "is not Filo's: not replaced"
)

// Write writes an index's pages into its directory (docs/design.md, Tag index pages, "Filo's own files"): a page
// whose content is unchanged is left as it is; one at a path where nothing is is created; one Filo wrote and that is
// still exactly what it wrote is replaced, through a temporary file renamed over it. Anything else at a page's path
// (a file changed since, a file or directory Filo did not write, a symlink) is left alone and reported. Then the
// files and directories Filo wrote before and no longer needs are removed, each only while still what Filo wrote
// (a directory only when empty). ix.Files is what Filo wrote before.
func Write(ix store.TagIndex, p Pages) Result {
	w := writer{ix: ix, res: Result{Files: maps.Clone(ix.Files)}}
	if w.res.Files == nil {
		w.res.Files = map[string]string{}
	}
	if !w.reachable() {
		return w.res
	}
	defer w.close()
	failed := map[string]bool{} // directories that could not be made: their pages are not written
	for _, d := range p.Dirs {
		if failed[path.Dir(d)] || !w.dir(d) {
			failed[d] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Files)) {
		if failed[path.Dir(name)] {
			continue
		}
		w.file(name, p.Files[name])
	}
	keep := map[string]bool{}
	for _, d := range p.Dirs {
		keep[d] = true
	}
	for name := range p.Files {
		keep[name] = true
	}
	w.removeAll(func(name string) bool { return !keep[name] })
	return w.res
}

// Remove removes every file and directory of an index that Filo wrote and that is still what it wrote (a directory
// only when empty), for filo indexes remove. What is left is reported.
func Remove(ix store.TagIndex) Result {
	w := writer{ix: ix, res: Result{Files: maps.Clone(ix.Files)}}
	if w.res.Files == nil {
		w.res.Files = map[string]string{}
	}
	if !w.reachable() {
		return w.res
	}
	defer w.close()
	w.removeAll(func(string) bool { return true })
	for _, name := range []string{ix.Page(), ix.PagesDir()} {
		if _, ok := ix.Files[name]; ok {
			continue
		}
		if _, err := w.dirs["."].Lstat(name); err == nil {
			w.problem(name, "is not Filo's: it stays")
		}
	}
	return w.res
}

// Plan is what Remove would remove: the files Filo wrote, and the ones at the index's paths that it did not write or
// that were changed since (which it leaves). It changes nothing.
func Plan(ix store.TagIndex) (files int, left []Problem) {
	for name, h := range ix.Files {
		if h == "" {
			continue
		}
		if sameFile(ix.Abs(name), h) {
			files++
		} else if _, err := os.Lstat(ix.Abs(name)); err == nil {
			left = append(left, Problem{Path: name, Why: "was changed since Filo wrote it: it stays"})
		}
	}
	walkForeign(ix, func(name string) {
		left = append(left, Problem{Path: name, Why: "is not Filo's: it stays"})
	})
	slices.SortFunc(left, func(a, b Problem) int { return cmp.Compare(a.Path, b.Path) })
	return files, left
}

// walkForeign calls found with each entry at the index's paths that Filo did not write (a file in .NAME/ that is not
// one of its pages, for instance).
func walkForeign(ix store.TagIndex, found func(name string)) {
	if _, ok := ix.Files[ix.Page()]; !ok {
		if _, err := os.Lstat(ix.Abs(ix.Page())); err == nil {
			found(ix.Page())
		}
	}
	root := ix.Abs(ix.PagesDir())
	fi, err := os.Lstat(root)
	if err != nil {
		return
	}
	if _, ok := ix.Files[ix.PagesDir()]; !ok || !fi.IsDir() {
		found(ix.PagesDir())
		return
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, rerr := filepath.Rel(ix.DirAbs(), p)
		if rerr != nil {
			return nil
		}
		name := filepath.ToSlash(rel)
		if _, ok := ix.Files[name]; !ok {
			found(name)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
}

type writer struct {
	ix  store.TagIndex
	res Result
	// dirs are the directories opened, by path relative to the index's directory ('/'-separated, "." for itself):
	// every look and change goes through the one that holds the file (see open).
	dirs map[string]*os.Root
}

func (w *writer) problem(name, why string) {
	w.res.Problems = append(w.res.Problems, Problem{Path: name, Why: why})
}

// close closes the directories opened.
func (w *writer) close() {
	for _, r := range w.dirs {
		r.Close()
	}
}

// errDirChanged means a directory on a file's path is no longer the real directory Filo saw there (a symlink
// swapped in, say), so nothing is done through it.
var errDirChanged = errors.New("a directory on its path is not the directory it was (a symlink?)")

// beforeWrite, if set by a test, runs before each page is looked at and written, after the directories are made.
var beforeWrite func(name string)

// reachable checks that the index's directory is there and is a directory (not a symlink to one), and opens it:
// from then on everything goes through it, never by path.
func (w *writer) reachable() bool {
	abs := w.ix.DirAbs()
	fi, err := os.Lstat(abs)
	switch {
	case err != nil:
		w.problem("", "its directory cannot be reached: "+errText(err))
		return false
	case !fi.IsDir():
		w.problem("", "its directory is not a directory any more")
		return false
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		w.problem("", "its directory cannot be reached: "+errText(err))
		return false
	}
	if got, err := r.Stat("."); err != nil || !os.SameFile(fi, got) {
		r.Close()
		w.problem("", "its directory changed as it was opened")
		return false
	}
	w.dirs = map[string]*os.Root{".": r}
	return true
}

// open returns the directory dir (relative to the index's directory, '/'-separated; "." for itself), opened from the
// index's directory down: each directory through the one above it, only if lstat sees a real directory there (never
// a symlink) and it is the very one lstat saw. Each is opened once, so a directory swapped for a symlink later is
// never followed: the open one is used.
func (w *writer) open(dir string) (*os.Root, error) {
	if r, ok := w.dirs[dir]; ok {
		return r, nil
	}
	if dir == "." || dir == "" {
		return nil, errDirChanged // reachable opens the index's directory first
	}
	parent, err := w.open(path.Dir(dir))
	if err != nil {
		return nil, err
	}
	base := path.Base(dir)
	fi, err := parent.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errDirChanged
	}
	r, err := parent.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	if got, err := r.Stat("."); err != nil || !os.SameFile(fi, got) {
		r.Close()
		return nil, errDirChanged
	}
	w.dirs[dir] = r
	return r, nil
}

// forget closes and drops the open directory dir, once it is removed.
func (w *writer) forget(dir string) {
	if r, ok := w.dirs[dir]; ok {
		r.Close()
		delete(w.dirs, dir)
	}
}

// dir makes the directory name, or checks that it is one Filo made.
func (w *writer) dir(name string) bool {
	parent, err := w.open(path.Dir(name))
	if err != nil {
		w.problem(name, "could not be made: "+errText(err))
		return false
	}
	base := path.Base(name)
	fi, err := parent.Lstat(base)
	if errors.Is(err, fs.ErrNotExist) {
		if err := parent.Mkdir(base, 0o755); err != nil {
			w.problem(name, "could not be made: "+errText(err))
			return false
		}
		w.res.Files[name] = ""
		return w.opened(name)
	}
	if err != nil {
		w.problem(name, "cannot be looked at: "+errText(err))
		return false
	}
	if h, ok := w.ix.Files[name]; !ok || h != "" || !fi.IsDir() {
		delete(w.res.Files, name)
		w.problem(name, whyNotFilos)
		return false
	}
	return w.opened(name)
}

// opened opens the directory name straight after dir made or checked it, so its pages go into the very directory
// looked at, whatever is put at its path later.
func (w *writer) opened(name string) bool {
	if _, err := w.open(name); err != nil {
		w.problem(name, "cannot be opened: "+errText(err))
		return false
	}
	return true
}

// file writes content to name, unless what is there is not Filo's.
func (w *writer) file(name string, content []byte) {
	if beforeWrite != nil {
		beforeWrite(name)
	}
	sum := hashOf(content)
	parent, err := w.open(path.Dir(name))
	if err != nil {
		w.problem(name, "could not be written: "+errText(err))
		return
	}
	base := path.Base(name)
	fi, err := parent.Lstat(base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		w.problem(name, "cannot be looked at: "+errText(err))
		return
	default:
		was, ok := w.ix.Files[name]
		if !ok || was == "" || !fi.Mode().IsRegular() {
			delete(w.res.Files, name)
			w.problem(name, whyNotFilos)
			return
		}
		if !sameIn(parent, base, was) {
			delete(w.res.Files, name)
			w.problem(name, whyChanged)
			return
		}
		if was == sum {
			return
		}
	}
	if err := w.writeFile(name, content); err != nil {
		w.problem(name, "could not be written: "+errText(err))
		return
	}
	w.res.Files[name] = sum
	w.res.Written++
}

// tempDir is the directory, relative to the index's directory, where the temporary file of name is written: name's
// own directory, except for a page in the index's directory itself (NAME.html), whose temporary file goes in the
// pages directory when Filo made it, so that one a crash leaves is never one a scan sees (the pages directory is
// Filo's own).
func (w *writer) tempDir(name string) string {
	dir := path.Dir(name)
	if h, ok := w.res.Files[w.ix.PagesDir()]; ok && h == "" && dir == "." {
		return w.ix.PagesDir()
	}
	return dir
}

// removeAll removes the files and directories Filo wrote for which drop holds: files first, then directories,
// deepest first.
func (w *writer) removeAll(drop func(name string) bool) {
	var files, dirs []string
	for name, h := range w.res.Files {
		if !drop(name) {
			continue
		}
		if h == "" {
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	for _, name := range files {
		h := w.res.Files[name]
		base := path.Base(name)
		parent, err := w.open(path.Dir(name))
		var fi fs.FileInfo
		if err == nil {
			fi, err = parent.Lstat(base)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(w.res.Files, name)
		case err != nil:
			w.problem(name, "cannot be looked at: "+errText(err))
		case !fi.Mode().IsRegular() || !sameIn(parent, base, h):
			delete(w.res.Files, name)
			w.problem(name, "was changed since Filo wrote it: it stays")
		default:
			if err := parent.Remove(base); err != nil {
				w.problem(name, "could not be removed: "+errText(err))
				continue
			}
			delete(w.res.Files, name)
			w.res.Removed++
		}
	}
	slices.SortFunc(dirs, func(a, b string) int {
		if c := cmp.Compare(strings.Count(b, "/"), strings.Count(a, "/")); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	for _, name := range dirs {
		base := path.Base(name)
		parent, err := w.open(path.Dir(name))
		var fi fs.FileInfo
		if err == nil {
			fi, err = parent.Lstat(base)
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(w.res.Files, name)
		case err != nil:
			w.problem(name, "cannot be looked at: "+errText(err))
		case !fi.IsDir():
			delete(w.res.Files, name)
			w.problem(name, "is not a directory Filo made any more: it stays")
		case w.holdsFilos(name):
			// A file of Filo's in it could not be removed (reported already): the directory stays Filo's, and the
			// next run tries again.
		default:
			w.forget(name)
			err := parent.Remove(base)
			switch {
			case err == nil:
				delete(w.res.Files, name)
			case !emptyIn(parent, base):
				delete(w.res.Files, name)
				w.problem(name, "holds files that are not Filo's: it stays")
			default:
				w.problem(name, "could not be removed: "+errText(err))
			}
		}
	}
}

// holdsFilos reports whether a file or directory Filo wrote is still recorded under the directory name.
func (w *writer) holdsFilos(name string) bool {
	for p := range w.res.Files {
		if strings.HasPrefix(p, name+"/") {
			return true
		}
	}
	return false
}

// emptyIn reports whether the directory name in dir holds nothing (or cannot be read: then it is not taken to hold
// files that are not Filo's).
func emptyIn(dir *os.Root, name string) bool {
	f, err := dir.Open(name)
	if err != nil {
		return true
	}
	defer f.Close()
	entries, err := f.ReadDir(1)
	return err != nil || len(entries) == 0
}

// sameFile reports whether the regular file at abs (not a symlink) holds exactly the content whose SHA-256 is sum.
// It looks by path: only for Plan, which changes nothing.
func sameFile(abs, sum string) bool {
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(abs)
	return err == nil && hashOf(b) == sum
}

// sameIn is sameFile for the file name in the open directory dir.
func sameIn(dir *os.Root, name, sum string) bool {
	fi, err := dir.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := dir.ReadFile(name)
	return err == nil && hashOf(b) == sum
}

// writeFile writes content to name through a temporary file (see tempDir), made where nothing is (O_EXCL), flushed
// and renamed into place, so a page is never seen half written and a power cut cannot leave one torn (which would
// then not be Filo's to replace). Both go through the open directories: the rename within name's own directory, or
// for NAME.html with its temporary file in the pages directory, within the index's directory.
func (w *writer) writeFile(name string, content []byte) error {
	dir, base := path.Dir(name), path.Base(name)
	tdir := w.tempDir(name)
	tr, err := w.open(tdir)
	if err != nil {
		return err
	}
	tmp, f, err := createTemp(tr, base)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			tr.Remove(tmp)
		}
	}()
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if tdir == dir {
		err = tr.Rename(tmp, base)
	} else {
		err = w.dirs["."].Rename(path.Join(tdir, tmp), name)
	}
	if err != nil {
		return err
	}
	done = true
	return nil
}

// createTemp makes a new temporary file for the page base in dir, with mode 0600, never over anything there: a
// short random suffix, tried again if taken.
func createTemp(dir *os.Root, base string) (string, *os.File, error) {
	for range 10 {
		name := "." + base + ".filo-" + rand.Text()[:10] + ".tmp"
		f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, f, nil
	}
	return "", nil, fs.ErrExist
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// errText is an error without the path, which the problem names already.
func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}
