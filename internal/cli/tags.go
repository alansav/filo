package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
	"github.com/alansav/filo/internal/view"
)

// tags is filo tags, every tag with its count of files, and filo tags rename OLD NEW (docs/design.md, Tags).
func (e *env) tags(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("tags")
	db := dbFlag(fl, dbBefore)
	pos, err := e.parse(fl, "tags", args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		return e.listTags(ctx, *db)
	case pos[0] == "rename":
		if len(pos) != 3 {
			return usageError{"tags rename takes OLD and NEW"}
		}
		return e.renameTag(ctx, *db, pos[1], pos[2])
	}
	return usageError{fmt.Sprintf("unknown tags command %q: filo tags lists the tags, filo tags rename OLD NEW "+
		"renames one", pos[0])}
}

// listTags shows every tag in use with its count of files, and how many files have a tag and how many have none.
func (e *env) listTags(ctx context.Context, db string) error {
	s, err := e.open(ctx, db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	sum, err := s.TagSummary(ctx)
	if err != nil {
		return err
	}
	r := e.view
	untagged := report.Count(sum.Untagged) + " untagged"
	if len(sum.Tags) == 0 {
		r.Heading("Tags", view.Where{}, "none", untagged)
		r.Blank()
		r.Next("Next: filo tag FILE... TAG... tags files")
		return nil
	}
	r.Heading("Tags", view.Where{}, report.Count(int64(len(sum.Tags))),
		report.Plural(sum.Tagged, "file tagged", "files tagged"), untagged)
	r.Blank()
	rows := make([]view.TableRow, len(sum.Tags))
	for i, c := range sum.Tags {
		rows[i] = view.TableRow{Name: c.Tag.String(), Cells: []string{report.Plural(c.Files, "file", "files")}}
	}
	r.Table(rows, true)
	return nil
}

// renameTag is filo tags rename OLD NEW, across every file that has OLD.
func (e *env) renameTag(ctx context.Context, db, oldArg, newArg string) error {
	old, err := tags.ParseRemoval(oldArg)
	if err != nil {
		return usageError{"OLD " + err.Error()}
	}
	to, err := tags.ParseRemoval(newArg)
	if err != nil {
		return usageError{"NEW " + err.Error()}
	}
	if old.AnyValue != to.AnyValue {
		return usageError{"give both as key: to rename a key (filo tags rename sendr: sender:), or both as tags"}
	}
	if old.AnyValue && old.Key == to.Key {
		return usageError{"OLD and NEW are the same key: nothing to rename"}
	}
	s, err := e.open(ctx, db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	res, err := s.RenameTag(ctx, old, to, now())
	if errors.Is(err, store.ErrNoSuchTag) {
		what := "the tag " + old.String()
		if old.AnyValue {
			what = "a tag with the key " + old.String()
		}
		return fmt.Errorf("no file has %s (filo tags lists them)", what)
	}
	if err != nil {
		return err
	}
	r := e.view
	if res.Files+res.Respelled == 0 {
		r.Outcome("Unchanged", to.String()+" is spelled so already")
		return nil
	}
	facts := []string{"renamed " + old.String() + " to " + to.String(), report.Plural(int64(res.Files), "file",
		"files")}
	if res.Merged > 0 {
		had := " had " + to.String() + " already"
		if to.AnyValue {
			had = " had the same value under " + to.String() + " already"
		}
		facts = append(facts, report.Plural(int64(res.Merged), "file", "files")+had)
	}
	if res.Respelled > 0 {
		facts = append(facts, report.Plural(int64(res.Respelled), "other file", "other files")+" respelled")
	}
	r.Outcome("Done", facts...)
	return nil
}
