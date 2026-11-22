package tagindex

import (
	"cmp"
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
	w.removeAll(func(string) bool { return true })
	for _, name := range []string{ix.Page(), ix.PagesDir()} {
		if _, ok := ix.Files[name]; ok {
			continue
		}
		if _, err := os.Lstat(ix.Abs(name)); err == nil {
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
}

func (w *writer) problem(name, why string) {
	w.res.Problems = append(w.res.Problems, Problem{Path: name, Why: why})
}

// reachable checks that the index's directory is there and is a directory (not a symlink to one).
func (w *writer) reachable() bool {
	fi, err := os.Lstat(w.ix.DirAbs())
	switch {
	case err != nil:
		w.problem("", "its directory cannot be reached: "+errText(err))
	case !fi.IsDir():
		w.problem("", "its directory is not a directory any more")
	default:
		return true
	}
	return false
}

// dir makes the directory name, or checks that it is one Filo made.
func (w *writer) dir(name string) bool {
	abs := w.ix.Abs(name)
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(abs, 0o755); err != nil {
			w.problem(name, "could not be made: "+errText(err))
			return false
		}
		w.res.Files[name] = ""
		return true
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
	return true
}

// file writes content to name, unless what is there is not Filo's.
func (w *writer) file(name string, content []byte) {
	abs := w.ix.Abs(name)
	sum := hashOf(content)
	fi, err := os.Lstat(abs)
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
		if !sameFile(abs, was) {
			delete(w.res.Files, name)
			w.problem(name, whyChanged)
			return
		}
		if was == sum {
			return
		}
	}
	if err := writeFile(abs, content); err != nil {
		w.problem(name, "could not be written: "+errText(err))
		return
	}
	w.res.Files[name] = sum
	w.res.Written++
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
		abs := w.ix.Abs(name)
		fi, err := os.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(w.res.Files, name)
		case err != nil:
			w.problem(name, "cannot be looked at: "+errText(err))
		case !fi.Mode().IsRegular() || !sameFile(abs, h):
			delete(w.res.Files, name)
			w.problem(name, "was changed since Filo wrote it: it stays")
		default:
			if err := os.Remove(abs); err != nil {
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
		abs := w.ix.Abs(name)
		fi, err := os.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			delete(w.res.Files, name)
		case err != nil:
			w.problem(name, "cannot be looked at: "+errText(err))
		case !fi.IsDir():
			delete(w.res.Files, name)
			w.problem(name, "is not a directory Filo made any more: it stays")
		default:
			if err := os.Remove(abs); err != nil {
				delete(w.res.Files, name)
				w.problem(name, "holds files that are not Filo's: it stays")
				continue
			}
			delete(w.res.Files, name)
		}
	}
}

// sameFile reports whether the regular file at abs (not a symlink) holds exactly the content whose SHA-256 is sum.
func sameFile(abs, sum string) bool {
	fi, err := os.Lstat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(abs)
	return err == nil && hashOf(b) == sum
}

// writeFile writes content to abs through a temporary file in its directory, flushed and renamed into place, so a
// page is never seen half written and a power cut cannot leave one torn (which would then not be Filo's to replace).
func writeFile(abs string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".filo-*.tmp")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		return err
	}
	done = true
	return nil
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
