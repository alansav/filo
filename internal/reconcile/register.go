package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/walk"
)

// Pending is one file filo tag registers by itself, not its directory (docs/design.md, Tags, "A file Filo has not
// seen yet"): checked and hashed by PrepareFile, written by Register, as a scan whose scope is the file.
type Pending struct {
	root    store.Root
	rel     string
	started time.Time
	ch      store.Changes
	copies  int // CopyTags events
}

// Rel is the file's path relative to its root.
func (p *Pending) Rel() string { return p.rel }

// PrepareFile checks that the file at rel in root, which the register has no current path for, can be registered by
// itself, and hashes it. A file that cannot be is refused with why, in a phrase (no error): one a scan of the root
// skips (its settings, filo's own files, another filesystem), one that is not a regular file, one that cannot be
// read, and one that looks moved from a file the register has (the same inode, or the same content, at a path no
// longer on the disk; the content of a file missing since an earlier scan): a scan pairs those, and filo never
// guesses. Writing nothing, it can run for every file before any is registered.
func PrepareFile(ctx context.Context, s *store.Store, root store.Root, rel string) (*Pending, string, error) {
	abs := filepath.Join(root.Path, filepath.FromSlash(rel))
	if walk.SkipsPath(root.Path, rel, root.IncludeHidden, root.Ignore) {
		return nil, "its root's settings skip it (hidden or ignored); filo roots set changes them", nil
	}
	if ownPath(s, abs) {
		return nil, "one of filo's own files", nil
	}
	started := time.Now()
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "not on the disk", nil
	case err != nil:
		return nil, "cannot be looked at: " + err.Error(), nil
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, "a symlink: give the file it points to", nil
	case !fi.Mode().IsRegular():
		return nil, "not a regular file", nil
	}
	if id, err := fsid.Stat(abs); err != nil {
		return nil, "cannot be looked at: " + err.Error(), nil
	} else if rid, err := fsid.Stat(root.Path); err != nil {
		return nil, "", fmt.Errorf("root %s: %w", root.Path, err)
	} else if id.Dev != rid.Dev {
		return nil, "on another filesystem than its root, which a scan does not enter", nil
	}

	outs, err := hasher.HashAll(ctx, []hasher.Job{{Path: abs, Root: root.Path}}, 1, nil)
	if err != nil {
		return nil, "", err
	}
	o := outs[0]
	if o.Err != nil {
		return nil, "could not be read: " + o.Err.Error(), nil
	}
	if from, err := movedFrom(ctx, s, root, rel, o.Result); err != nil || from != "" {
		if from != "" {
			return nil, "looks moved from " + from + "; run filo scan, which pairs it", err
		}
		return nil, "", err
	}

	p := &Pending{root: root, rel: rel, started: started}
	p.ch.New = []store.NewFile{{Path: rel, Kind: string(walk.KindFile), Size: o.Size, MtimeNS: o.MtimeNS,
		CtimeNS: o.CtimeNS, Hash: string(o.Sum), Ino: o.Ino}}
	if p.copies, err = copiesTags(ctx, s, &p.ch); err != nil {
		return nil, "", err
	}
	if o.Size > 0 {
		deleted, err := s.DeletedByHash(ctx, []string{string(o.Sum)})
		if err != nil {
			return nil, "", err
		}
		if when, ok := deleted[string(o.Sum)]; ok {
			p.ch.Events = append(p.ch.Events, store.Event{Kind: SameAsDeleted, Path: rel,
				Detail: "same content as a file filo deleted on " + localDate(when) + "; a new file, it needs a retention"})
		}
	}
	return p, "", nil
}

// Register writes a prepared file as a new file, in a scan of its own whose scope is the file, so the next scan
// treats it as known; the directory rules date it as in any scan. It returns that scan's summary.
func (p *Pending) Register(ctx context.Context, s *store.Store) (Summary, error) {
	rec := store.ScanRecord{RootID: p.root.ID, Scope: p.rel, IncludeHidden: p.root.IncludeHidden,
		Ignores: p.root.Ignore, Started: p.started, FilesSeen: 1, NewCount: 1}
	sum := Summary{Root: p.root.Path, Scope: p.rel, Seen: 1, New: 1, CopyTags: p.copies,
		Samples: map[string][]store.Event{}}
	var err error
	if sum.ScanID, sum.Rules, err = s.ApplyScanRules(ctx, rec, p.ch); err != nil {
		return Summary{}, err
	}
	for _, e := range p.ch.Events {
		sum.Samples[e.Kind] = append(sum.Samples[e.Kind], e)
		if e.Kind == SameAsDeleted {
			sum.SameAsDeleted++
		}
	}
	return sum, nil
}

// movedFrom is the path of a file the register has that the file at rel (read as r) looks moved from, "" if none: a
// file in the root with its inode whose path is no longer that inode on the disk (a hard link is another path of
// the same file, not a move), a file present with its content at a path no longer on the disk, or a file with its
// content missing since an earlier scan.
func movedFrom(ctx context.Context, s *store.Store, root store.Root, rel string, r hasher.Result) (string, error) {
	onDisk := func(rootPath, p string) (fs.FileInfo, bool) {
		fi, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(p)))
		return fi, err == nil
	}
	here, ok := onDisk(root.Path, rel)
	if !ok {
		return "", nil // gone since it was hashed: the scan that registers nothing else finds it
	}
	known, err := s.Known(ctx, root.ID)
	if err != nil {
		return "", err
	}
	for _, k := range known {
		if r.Ino == 0 || k.Ino != r.Ino || k.Kind != string(walk.KindFile) || k.Path == rel {
			continue
		}
		if fi, ok := onDisk(root.Path, k.Path); !ok || !os.SameFile(fi, here) {
			return filepath.Join(root.Path, filepath.FromSlash(k.Path)), nil
		}
	}
	if r.Size == 0 {
		return "", nil // every empty file has one content: no copy, no move
	}
	h := string(r.Sum)
	copies, err := s.Copies(ctx, []string{h})
	if err != nil {
		return "", err
	}
	for _, c := range copies[h] {
		if _, ok := onDisk(c.RootPath, c.Path); !ok {
			return c.AbsPath(), nil
		}
	}
	gone, err := s.MissingByHash(ctx, []string{h})
	if err != nil || len(gone) == 0 {
		return "", err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return "", err
	}
	for _, rt := range roots {
		if rt.ID == gone[0].LastRootID {
			return filepath.Join(rt.Path, filepath.FromSlash(gone[0].LastPath)), nil
		}
	}
	return gone[0].LastPath, nil
}

// ownPath reports whether abs is one of the files the register keeps beside its database, which no scan records.
func ownPath(s *store.Store, abs string) bool {
	canon := abs
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		canon = filepath.Join(dir, filepath.Base(abs))
	}
	for _, own := range s.OwnPaths() {
		o := filepath.Clean(own)
		if dir, err := filepath.EvalSymlinks(filepath.Dir(o)); err == nil {
			o = filepath.Join(dir, filepath.Base(o))
		}
		if canon == o || within(canon, o) {
			return true
		}
	}
	return false
}

// within reports whether p is below dir.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
