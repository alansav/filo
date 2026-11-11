// Package walk lists the files under a root (or a scope inside it) without following symlinks or leaving the root's
// filesystem, and records exactly which directories it managed to list, so reconcile can tell which known files this
// walk would have seen.
package walk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/alansav/filo/internal/fsid"
)

// Kind is what an entry is.
type Kind string

const (
	KindFile    Kind = "file"
	KindSymlink Kind = "symlink"
)

// Entry is one regular file or symlink.
type Entry struct {
	Path       string // relative to the root, '/'-separated
	Kind       Kind
	Size       int64
	MtimeNS    int64  // Unix nanoseconds
	CtimeNS    int64  // status change time, Unix nanoseconds; 0 = not known
	Ino        uint64 // inode, from the lstat; 0 = not known
	LinkTarget string
	LinkBroken bool
}

// Problem is something the walk could not do. The affected path is never covered (see Result.Covers), except for a
// symlink whose target could not be checked, which is still recorded.
type Problem struct {
	Path string // relative to the root, '/'-separated
	Err  error
}

func (p Problem) Error() string { return p.Path + ": " + p.Err.Error() }

// Options control one walk.
type Options struct {
	Root          string        // absolute root, symlinks resolved
	Scope         string        // relative, '/'-separated; "" = the whole root
	IncludeHidden bool          // walk hidden entries too
	Ignore        []string      // entry names skipped at any depth
	Exclude       []string      // absolute paths never recorded (the database and its companion files)
	Seen          *atomic.Int64 // optional: incremented by the number of entries in each listed directory
}

// Result is what a walk found and what it did.
type Result struct {
	Entries    []Entry
	Problems   []Problem
	FailedDirs []string // directories that could not be listed, or only partly
	OtherFS    []string // directories skipped because they are on another filesystem

	scope   string
	listed  map[string]bool // directories listed completely
	failed  map[string]bool // directories whose listing failed, even partly
	skipped map[string]bool // entries not walked or not recorded: rules, other filesystem, revisits, lstat errors
}

// Names skipped everywhere, whatever the options.
var noise = map[string]bool{".filo": true, "node_modules": true, "__pycache__": true}

// statID is fsid.Stat, swappable in tests to simulate other filesystems and bind mounts.
var statID = fsid.Stat

// ErrNotFound is returned when the scope directory does not exist.
var ErrNotFound = errors.New("scope directory not found")

// Walk lists opt.Scope under opt.Root. Directories that fail to list, entries that fail to lstat and the like are
// recorded in the Result; the error is only for bad options, an unusable scope directory, or cancellation.
func Walk(ctx context.Context, opt Options) (*Result, error) {
	if !filepath.IsAbs(opt.Root) {
		return nil, fmt.Errorf("root %q is not absolute", opt.Root)
	}
	scope, err := cleanScope(opt.Scope)
	if err != nil {
		return nil, err
	}
	ignore := map[string]bool{}
	for _, n := range opt.Ignore {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\`) {
			return nil, fmt.Errorf("ignore %q: must be a plain file or directory name", n)
		}
		ignore[n] = true
	}

	rootID, err := statID(opt.Root)
	if err != nil {
		return nil, fmt.Errorf("root %s: %w", opt.Root, err)
	}
	start := filepath.Join(opt.Root, filepath.FromSlash(scope))
	fi, err := os.Lstat(start)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, start)
	}
	if err != nil {
		return nil, fmt.Errorf("scope %s: %w", start, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("scope %s is not a directory", start)
	}
	startID, err := statID(start)
	if err != nil {
		return nil, fmt.Errorf("scope %s: %w", start, err)
	}
	if startID.Dev != rootID.Dev {
		return nil, fmt.Errorf("scope %s is on a different filesystem from root %s", start, opt.Root)
	}

	w := &walker{
		opt:     opt,
		ignore:  ignore,
		exclude: canonicalExcludes(opt.Exclude),
		dev:     rootID.Dev,
		visited: map[fsid.ID]bool{startID: true},
		res: &Result{
			scope:   scope,
			listed:  map[string]bool{},
			failed:  map[string]bool{},
			skipped: map[string]bool{},
		},
	}
	if err := w.dir(ctx, scope, start); err != nil {
		return nil, err
	}
	return w.res, nil
}

type walker struct {
	opt     Options
	ignore  map[string]bool
	exclude map[string]bool
	dev     uint64
	visited map[fsid.ID]bool
	res     *Result
}

func (w *walker) problem(rel string, err error) {
	w.res.Problems = append(w.res.Problems, Problem{Path: rel, Err: err})
}

func (w *walker) dir(ctx context.Context, rel, abs string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		// Even a partial listing counts as failed: nothing under this directory may be marked missing. The entries
		// that were returned are still recorded.
		w.res.FailedDirs = append(w.res.FailedDirs, rel)
		w.res.failed[rel] = true
		w.problem(rel, err)
	} else {
		w.res.listed[rel] = true
	}
	if w.opt.Seen != nil {
		w.opt.Seen.Add(int64(len(entries)))
	}

	hasProject := false
	for _, de := range entries {
		if de.Type().IsRegular() && isProjectFile(de.Name()) {
			hasProject = true
			break
		}
	}

	for _, de := range entries {
		name := de.Name()
		childRel := joinRel(rel, name)
		childAbs := filepath.Join(abs, name)
		if w.skipName(name, de, hasProject) || w.exclude[childAbs] {
			w.res.skipped[childRel] = true
			continue
		}

		info, err := de.Info() // lstat
		if errors.Is(err, fs.ErrNotExist) {
			continue // vanished since the listing: simply not seen
		}
		if err != nil {
			w.res.skipped[childRel] = true
			w.problem(childRel, err)
			continue
		}

		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := w.subdir(ctx, childRel, childAbs); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			w.symlink(childRel, childAbs, info)
		case mode.IsRegular():
			w.res.Entries = append(w.res.Entries, Entry{
				Path: childRel, Kind: KindFile, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
				CtimeNS: fsid.ChangeTimeNS(info), Ino: inode(info),
			})
		default:
			// FIFOs, sockets, devices: not files Filo manages.
			w.res.skipped[childRel] = true
		}
	}
	return nil
}

func (w *walker) subdir(ctx context.Context, rel, abs string) error {
	id, err := statID(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.res.skipped[rel] = true
		w.problem(rel, err)
		return nil
	}
	if id.Dev != w.dev {
		w.res.skipped[rel] = true
		w.res.OtherFS = append(w.res.OtherFS, rel)
		return nil
	}
	if w.visited[id] {
		w.res.skipped[rel] = true
		w.problem(rel, errors.New("directory already visited in this scan (bind mount or loop); skipped"))
		return nil
	}
	w.visited[id] = true
	return w.dir(ctx, rel, abs)
}

func (w *walker) symlink(rel, abs string, info fs.FileInfo) {
	target, err := os.Readlink(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		w.res.skipped[rel] = true
		w.problem(rel, err)
		return
	}
	broken := false
	if _, err := os.Stat(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) || isLoop(err) {
			broken = true
		} else {
			// The target exists or not; we cannot tell. Record the link and say so rather than guess "broken".
			w.problem(rel, fmt.Errorf("cannot check symlink target: %w", err))
		}
	}
	w.res.Entries = append(w.res.Entries, Entry{
		Path: rel, Kind: KindSymlink, Size: info.Size(), MtimeNS: info.ModTime().UnixNano(),
		CtimeNS: fsid.ChangeTimeNS(info), Ino: inode(info), LinkTarget: target, LinkBroken: broken,
	})
}

func (w *walker) skipName(name string, de fs.DirEntry, hasProject bool) bool {
	if noise[name] || w.ignore[name] {
		return true
	}
	if !w.opt.IncludeHidden && isHidden(name) {
		return true
	}
	return hasProject && de.IsDir() && (name == "bin" || name == "obj")
}

// Covers reports whether this walk would have seen the file at rel ('/'-separated, relative to the root) if it
// existed. Only covered files may be marked missing. It is answered from what the walk actually did: every
// directory from the scope down to rel's parent must have been listed completely, and rel must not have been skipped.
// A directory on that chain that was never listed, inside a listed parent that did not skip it, no longer exists as a
// directory, so everything that was below it is covered.
func (r *Result) Covers(rel string) bool {
	rel = path.Clean(rel)
	if rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	rest := rel
	if r.scope != "" {
		if !strings.HasPrefix(rel, r.scope+"/") {
			return false
		}
		rest = strings.TrimPrefix(rel, r.scope+"/")
	}
	if !r.listed[r.scope] {
		return false
	}
	parts := strings.Split(rest, "/")
	cur := r.scope
	for _, c := range parts[:len(parts)-1] {
		cur = joinRel(cur, c)
		if r.skipped[cur] {
			return false
		}
		if !r.listed[cur] {
			// Either the listing failed (then it is in FailedDirs and not listed) or the directory is gone.
			return !r.failed[cur]
		}
	}
	return !r.skipped[rel]
}

func cleanScope(scope string) (string, error) {
	if scope == "" {
		return "", nil
	}
	c := path.Clean(scope)
	if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || strings.Contains(scope, `\`) {
		return "", fmt.Errorf("scope %q must be a '/'-separated path inside the root", scope)
	}
	if c == "." {
		return "", nil
	}
	return c, nil
}

// canonicalExcludes resolves symlinks in each excluded path's directory (the files themselves may not exist yet),
// so a database reached through a symlinked directory is still recognised.
func canonicalExcludes(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		out[p] = true
		if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			out[filepath.Join(dir, filepath.Base(p))] = true
		}
	}
	return out
}

func inode(info fs.FileInfo) uint64 {
	id, _ := fsid.FromFileInfo(info)
	return id.Ino
}

func isProjectFile(name string) bool {
	return strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".fsproj")
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
