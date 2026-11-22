package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
	"github.com/alansav/filo/internal/view"
)

// tag is filo tag FILE... TAG... (docs/design.md, Tags): gives each file the tags, all or nothing, registering a
// file the register has not seen yet by itself.
func (e *env) tag(ctx context.Context, dbBefore string, args []string) error {
	return e.tagFiles(ctx, dbBefore, args, true)
}

// untag is filo untag FILE... TAG...: removes the tags (key: for every value of the key), all or nothing.
func (e *env) untag(ctx context.Context, dbBefore string, args []string) error {
	return e.tagFiles(ctx, dbBefore, args, false)
}

// tagTarget is one file filo tag or untag was given: where it is, and its id in the register, or the registration
// it waits for.
type tagTarget struct {
	root    store.Root
	rel     string
	id      int64
	pending *reconcile.Pending
}

func (e *env) tagFiles(ctx context.Context, dbBefore string, args []string, add bool) error {
	cmd := "tag"
	if !add {
		cmd = "untag"
	}
	fl := e.flags(cmd)
	db := dbFlag(fl, dbBefore)
	pos, err := e.parse(fl, cmd, args)
	if err != nil {
		return err
	}
	files, typed, err := splitFilesTags(pos)
	if err != nil {
		return err
	}
	var adds []tags.Tag
	var removes []tags.Search
	for _, t := range typed {
		if add {
			tg, err := tags.Parse(t)
			if err != nil {
				return usageError{err.Error()}
			}
			adds = append(adds, tg)
		} else {
			q, err := tags.ParseRemoval(t)
			if err != nil {
				return usageError{err.Error()}
			}
			removes = append(removes, q)
		}
	}
	if err := checkTildes("FILE", files...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)

	var targets []tagTarget
	var refused []setRefusal
	seen := map[string]bool{}
	for _, arg := range files {
		p, err := filepath.Abs(arg)
		if err != nil {
			return err
		}
		if seen[p] {
			continue // a path given twice is one file
		}
		seen[p] = true
		t, why, err := tagTargetOf(ctx, s, p, add)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if why != "" {
			refused = append(refused, setRefusal{path: p, reason: why})
			continue
		}
		targets = append(targets, t)
	}
	r := e.view
	if len(refused) > 0 {
		r.Note(view.Refused, "Nothing was changed: "+strconv.Itoa(len(refused))+" of "+
			report.Plural(int64(len(refused)+len(targets)), "file was refused", "files were refused"))
		for _, rf := range refused {
			r.CountName(view.Refused, "", rf.path, "   "+rf.reason)
		}
		r.Blank()
		r.Next("Next: fix the files above, or leave them out, and run filo " + cmd + " again")
		return errReported
	}

	// Every file can be tagged: register those not seen yet, then tag.
	registered, copyTags := 0, 0
	for i := range targets {
		t := &targets[i]
		if t.pending == nil {
			continue
		}
		sum, err := t.pending.Register(ctx, s)
		if err != nil {
			return err
		}
		sh, err := s.ShowFile(ctx, t.root.ID, t.rel, today())
		if err != nil {
			return err
		}
		t.id = sh.FileID
		registered++
		copyTags += sum.CopyTags
	}
	ids := make([]int64, len(targets))
	for i, t := range targets {
		ids[i] = t.id
	}
	var res store.TagResult
	if add {
		res, err = s.TagFiles(ctx, ids, adds, now())
	} else {
		res, err = s.UntagFiles(ctx, ids, removes, now())
	}
	if err != nil {
		return err
	}
	current, err := s.FileTags(ctx, ids)
	if err != nil {
		return err
	}

	var roots []string
	var rows []view.FileRow
	for _, t := range targets {
		roots = append(roots, t.root.Path)
		if res.Changed[t.id] {
			rows = append(rows, view.FileRow{Root: t.root.Path, Rel: t.rel, Note: tagNote(current[t.id])})
		}
	}
	var facts []string
	if add {
		for _, tg := range respell(adds, res.Respelled) {
			facts = append(facts, tg.String())
		}
	} else {
		for _, q := range removes {
			facts = append(facts, q.String())
		}
	}
	where := view.Where{Roots: roots}
	left := len(targets) - len(res.Changed)
	if len(res.Changed) == 0 {
		these := pluralWord(len(facts), "this tag", "these tags")
		what := report.Plural(int64(len(targets)), "file has", "files have") + " " + these + " already"
		switch {
		case !add && len(facts) == 1:
			what = report.Plural(int64(len(targets)), "file does not have", "files do not have") + " this tag"
		case !add:
			what = report.Plural(int64(len(targets)), "file has", "files have") + " none of these tags"
		}
		r.Heading("Unchanged", where, append(facts, what)...)
	} else {
		title := "Tagged"
		if !add {
			title = "Untagged"
		}
		head := append([]string{report.Plural(int64(len(res.Changed)), "file", "files")}, facts...)
		if left > 0 {
			what := strconv.Itoa(left) + pluralWord(len(facts), " had it already", " had them already")
			if !add {
				what = strconv.Itoa(left) + pluralWord(len(facts), " did not have it", " had none of them")
			}
			head = append(head, what)
		}
		r.Heading(title, where, head...)
		r.Blank()
		r.Files(view.FileList{Rows: rows})
	}
	for _, sp := range res.Respelled {
		r.Count(view.Neutral, sp.Typed.String()+" is written "+sp.Spelled.String()+
			", as first typed (filo tags rename changes it)")
	}
	if registered > 0 {
		r.Count(view.Neutral, "registered "+report.Plural(int64(registered), "new file", "new files"))
	}
	if copyTags > 0 {
		r.Count(view.Attention, pluralWord(copyTags, "it", "they")+" took the tags of copies that differed: check "+
			"with filo show")
	}
	return nil
}

// splitFilesTags splits filo tag's arguments: the FILEs are the arguments on the disk, from the first, and the
// first that is not starts the TAGs. A path after a tag is a usage error, and so is a run with no FILE or no TAG.
func splitFilesTags(pos []string) (files, typed []string, err error) {
	onDisk := func(a string) bool {
		_, err := os.Lstat(a)
		return err == nil
	}
	i := 0
	for i < len(pos) && onDisk(pos[i]) {
		i++
	}
	files, typed = pos[:i], pos[i:]
	switch {
	case len(pos) == 0:
		return nil, nil, usageError{"give one FILE or more, then one TAG or more"}
	case len(files) == 0:
		return nil, nil, usageError{"give the files first, then the tags: " + pos[0] + " is not on the disk"}
	case len(typed) == 0:
		return nil, nil, usageError{"give one TAG or more after the files (a tag that is also the name of " +
			"something here is read as a FILE)"}
	}
	for _, t := range typed {
		if onDisk(t) {
			return nil, nil, usageError{t + " is a path here, after a tag: give the files first, then the tags"}
		}
	}
	return files, typed, nil
}

// tagTargetOf finds the file at p (absolute, on the disk) in the register: its id where the register has it at this
// path now, or, for filo tag, the registration it needs (refused when that cannot be done by itself). Why is the
// reason it is refused, "" if it is not.
func tagTargetOf(ctx context.Context, s *store.Store, p string, add bool) (tagTarget, string, error) {
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return tagTarget{}, "not on the disk", nil
	case fi.Mode()&os.ModeSymlink != 0:
		return tagTarget{}, "a symlink: give the file it points to", nil
	case fi.IsDir():
		return tagTarget{}, "a directory: filo tag takes files", nil
	case !fi.Mode().IsRegular():
		return tagTarget{}, "not a regular file", nil
	}
	root, rel, _, err := locatePath(ctx, s, p)
	if errors.As(err, new(outsideError)) {
		return tagTarget{}, "outside every root", nil
	}
	if err != nil {
		return tagTarget{}, "", err
	}
	t := tagTarget{root: root, rel: rel}
	sh, err := s.ShowFile(ctx, root.ID, rel, today())
	switch {
	case err == nil && sh.Present && sh.RootID == root.ID && sh.Path == rel:
		t.id = sh.FileID
		return t, "", nil
	case err != nil && !errors.Is(err, store.ErrNotInRegister):
		return tagTarget{}, "", err
	case !add:
		return tagTarget{}, "not in the register at this path: it has no tags", nil
	}
	// Not at this path in the register now: a file filo has not seen yet, registered by itself.
	pending, why, err := reconcile.PrepareFile(ctx, s, root, rel)
	if err != nil || why != "" {
		return tagTarget{}, why, err
	}
	t.pending = pending
	return t, "", nil
}

// tagNote is a file's tags after a path: "sender:British Gas; type:bill", or "no tags".
func tagNote(ts []tags.Tag) string {
	if len(ts) == 0 {
		return "no tags"
	}
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.String()
	}
	return strings.Join(names, "; ")
}

// respell is the tags typed, each once, with the spelling it was given.
func respell(typed []tags.Tag, rs []store.Respelled) []tags.Tag {
	var out []tags.Tag
	for _, t := range typed {
		for _, r := range rs {
			if r.Typed == t {
				t = r.Spelled
			}
		}
		dup := false
		for _, o := range out {
			dup = dup || o.Same(t)
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}
