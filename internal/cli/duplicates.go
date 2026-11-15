package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// exportDuplicates writes the duplicate groups with a copy under the filter to csvPath.
func (e *env) exportDuplicates(ctx context.Context, s *store.Store, f store.Filter, where view.Where,
	csvPath string) error {
	groups, err := s.DuplicateGroups(ctx, f)
	if err != nil {
		return err
	}
	groups = distinctOnDisk(groups)
	// docs/design.md, Terminal output, Export. The duplicates redesign rewords it and keeps the shape.
	v := e.view
	if len(groups) == 0 {
		v.Heading("Duplicates export", where, "no groups")
		v.Next("Nothing exported.")
		return nil
	}
	id, err := writeExport(csvPath, func(write func(int64) error) (int64, error) {
		return s.RecordDuplicatesExport(ctx, csvPath, now(), groups, write)
	}, func(w io.Writer, id int64) error { return duplicates.WriteCSV(w, id, groups, "", time.Local) })
	if err != nil {
		return err
	}
	var copies int64
	for _, g := range groups {
		copies += int64(len(g.Copies))
	}
	v.Heading("Duplicates export", where, fmt.Sprintf("export %d", id),
		report.Plural(int64(len(groups)), "group", "groups"), report.Plural(copies, "copy", "copies"))
	v.Blank()
	v.Count(view.Neutral, "in each group you decide on: Y under delete for the copies to delete, N for those to keep")
	v.Count(view.Neutral, "two or more N: identical copies meant to exist, no longer listed as duplicates")
	v.Blank()
	v.Next("Next: filo update " + shellQuote(csvPath))
	return nil
}

// distinctOnDisk merges the copies of each group that are one file on disk (the same file seen through a bind mount in
// two roots, which the register cannot tell apart), then decides again which are groups (store.DupContent.IsGroup):
// one left with a single copy, or with only the records of one set, is not. A merged copy is a record if any of its
// paths is. Copies that cannot be looked at are kept: delete checks again.
func distinctOnDisk(groups []store.DupContent) []store.DupContent {
	out := groups[:0]
	for _, g := range groups {
		var copies []store.DupCopy
	next:
		for _, c := range g.Copies {
			for i := range copies {
				if same, err := dispose.SameFile(copies[i].AbsPath(), c.AbsPath()); err == nil && same {
					copies[i].Links = append(copies[i].Links, c.AbsPath())
					if copies[i].RecordSet == 0 {
						copies[i].RecordSet = c.RecordSet
					}
					continue next
				}
			}
			copies = append(copies, c)
		}
		g.Copies = copies
		if g.IsGroup() {
			out = append(out, g)
		}
	}
	return out
}
