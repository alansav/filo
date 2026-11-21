package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/alansav/filo/internal/show"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// get is filo get FILE... --FIELD... (docs/design.md, Showing and setting a file's details): the read-only pair of
// filo set, for scripts and quick checks. The fields come out in the order given. One field of one file is its bare
// value; several fields of one file are name and value lines; several files are one row each, the values in order
// and the path last (piped: tab-separated, the path absolute). Everything is escaped, as everywhere. A
// path that cannot be read is an error line; the others are still printed, and the exit status is 1.
func (e *env) get(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("get")
	db := dbFlag(fs, dbBefore)
	var order []string
	for _, f := range show.Fields {
		fs.Var(&fieldFlag{flag: f.Flag, order: &order}, f.Flag, "")
	}
	pos, err := e.parse(fs, "get", args)
	if err != nil {
		return err
	}
	switch {
	case len(order) == 0:
		return usageError{"get needs one field or more, such as --retention or --keep-until"}
	case len(pos) == 0:
		return usageError{"get takes one FILE or more"}
	}
	if err := checkTildes("FILE", pos...); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	day := today()
	failed := false
	var files []show.File
	for _, arg := range pos {
		f, err := getFile(ctx, s, arg, day)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.errorLine(err.Error())
			failed = true
			continue
		}
		files = append(files, f)
	}
	r := e.view
	name := func(flag string) string {
		i := slices.IndexFunc(show.Fields, func(f struct{ Flag, Name string }) bool { return f.Flag == flag })
		return show.Fields[i].Name
	}
	// The layout follows what was asked, not what was found, so a script sees one format whatever fails.
	switch {
	case len(pos) == 1 && len(order) == 1:
		for _, f := range files {
			r.Line(f.Value(order[0]))
		}
	case len(pos) == 1:
		for _, f := range files {
			pairs := make([][2]string, len(order))
			for i, fl := range order {
				pairs[i] = [2]string{name(fl), f.Value(fl)}
			}
			r.Pairs(pairs)
		}
	case !r.Terminal():
		for _, f := range files {
			rec := make([]string, 0, len(order)+1)
			for _, fl := range order {
				rec = append(rec, f.Value(fl))
			}
			r.Record(rec, f.AbsPath())
		}
	default:
		cols := make([]view.Column, len(order))
		for i, fl := range order {
			cols[i] = view.Column{Header: name(fl), Right: fl == "size"}
		}
		rows := make([]view.FileRow, len(files))
		for i, f := range files {
			cells := make([]string, len(order))
			for j, fl := range order {
				cells[j] = f.Value(fl)
			}
			rows[i] = view.FileRow{Root: f.RootPath, Rel: f.Path, Cells: cells}
		}
		r.Files(view.FileList{Columns: cols, Rows: rows})
	}
	if failed {
		return errReported
	}
	return nil
}

// getFile finds the file a typed path names in the register, as filo show does (a missing file, or one deleted by
// filo, by its last path; a moved one by its old path), or says why it cannot.
func getFile(ctx context.Context, s *store.Store, arg, day string) (show.File, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return show.File{}, err
	}
	root, rel, isDir, err := locatePath(ctx, s, p)
	if err != nil {
		return show.File{}, err
	}
	if isDir {
		return show.File{}, fmt.Errorf("%s is a directory: filo get takes files", p)
	}
	sh, err := s.ShowFile(ctx, root.ID, rel, day)
	if errors.Is(err, store.ErrNotInRegister) {
		if has, err := s.HasFilesUnder(ctx, root.ID, rel); err != nil {
			return show.File{}, err
		} else if has {
			return show.File{}, fmt.Errorf("%s is a directory: filo get takes files", p)
		}
		if skippedBy(rel, root.Settings) {
			return show.File{}, fmt.Errorf("%s is not in the register: its root's settings skip it", p)
		}
		return show.File{}, fmt.Errorf("%s is not in the register yet: run filo scan", p)
	}
	if err != nil {
		return show.File{}, err
	}
	return show.File{Shown: sh}, nil
}

// fieldFlag is a field of filo get: a bool flag that, set, records its place among the fields given.
type fieldFlag struct {
	flag  string
	order *[]string
}

func (f *fieldFlag) String() string   { return "" }
func (f *fieldFlag) IsBoolFlag() bool { return true }
func (f *fieldFlag) Set(v string) error {
	on, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	i := slices.Index(*f.order, f.flag)
	switch {
	case on && i < 0:
		*f.order = append(*f.order, f.flag)
	case !on && i >= 0:
		*f.order = slices.Delete(*f.order, i, i+1)
	}
	return nil
}
