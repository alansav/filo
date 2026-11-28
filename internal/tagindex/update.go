package tagindex

import (
	"context"
	"time"

	"github.com/alansav/filo/internal/store"
)

// Outcome is what bringing one index up to date did.
type Outcome struct {
	Index   store.TagIndex // as it was before
	Data    Data           // what its pages show now
	Pages   int            // how many pages it has (the stylesheet not counted); 0 when Skipped
	Skipped bool           // its data was the one last written in full: nothing was rendered or written
	Result  Result         // what writing its files did (nothing when Skipped)
}

// Failed reports whether the index's pages were not brought up to date.
func (o Outcome) Failed() bool { return len(o.Result.Problems) > 0 }

// Update brings an index's pages up to date (docs/design.md, Tag index pages, "When the pages are written"): it
// builds its data from the register and, unless force is set, does nothing when the data's hash is the one last
// written in full; otherwise it renders and writes the pages and records what was written, with the data's hash
// when every page was written, or why not (its paths relative to the root). An error is the register's; a page that could not be written is in the
// outcome.
func Update(ctx context.Context, s *store.Store, ix store.TagIndex, force bool, now time.Time) (Outcome, error) {
	out := Outcome{Index: ix}
	files, err := s.IndexFiles(ctx, ix.RootID, ix.Dir)
	if err != nil {
		return out, err
	}
	d, err := Build(ix, files)
	if err != nil {
		out.Result = Result{Files: ix.Files, Problems: []Problem{{Why: err.Error()}}}
		return out, s.RecordTagIndex(ctx, ix.ID, store.IndexWrite{Files: ix.Files, Problem: out.Result.ProblemIn(ix)},
			now)
	}
	out.Data = d
	hash := d.Hash()
	if !force && hash == ix.DataHash {
		out.Skipped = true // nothing rendered: most runs that change the register do not change what an index shows
		return out, nil
	}
	p, err := Render(d)
	if err != nil {
		return out, err
	}
	out.Pages = len(p.Files) - 1
	out.Result = Write(ix, p)
	w := store.IndexWrite{Files: out.Result.Files}
	if out.Failed() {
		w.Problem = out.Result.ProblemIn(ix)
	} else {
		w.DataHash = hash
	}
	return out, s.RecordTagIndex(ctx, ix.ID, w, now)
}

// UpdateAll brings every index's pages up to date, in the order store.TagIndexes gives them.
func UpdateAll(ctx context.Context, s *store.Store, force bool, now time.Time) ([]Outcome, error) {
	all, err := s.TagIndexes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Outcome
	for _, ix := range all {
		if ix.Writer != "" {
			continue // another device writes its pages (docs/design.md, Sharing decisions between devices)
		}
		o, err := Update(ctx, s, ix, force, now)
		if err != nil {
			return out, err
		}
		out = append(out, o)
	}
	return out, nil
}

// Tags is how many tags the data has: every value of every key, and the bare tags.
func (d Data) Tags() int {
	n := len(d.Bare.Values)
	for _, k := range d.Keys {
		n += len(k.Values)
	}
	return n
}
