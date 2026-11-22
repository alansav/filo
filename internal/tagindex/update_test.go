package tagindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

type env struct {
	t    *testing.T
	ctx  context.Context
	s    *store.Store
	root string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(t.Context(), store.Options{DBPath: filepath.Join(t.TempDir(), "filo.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.AddRoot(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, ctx: t.Context(), s: s, root: root}
}

func (e *env) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) scan() {
	e.t.Helper()
	if _, err := reconcile.Scan(e.ctx, e.s, reconcile.Options{Target: e.root}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) tag(rel string, ts ...string) {
	e.t.Helper()
	all, err := e.s.FindFiles(e.ctx, 0, "")
	if err != nil {
		e.t.Fatal(err)
	}
	var found []store.FoundFile
	for _, f := range all {
		if f.Rel == rel {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		e.t.Fatalf("find %s: %v", rel, found)
	}
	var add []tags.Tag
	for _, s := range ts {
		add = append(add, tag(e.t, s))
	}
	if _, err := e.s.TagFiles(e.ctx, []int64{found[0].FileID}, add, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) index(dir, name string) store.TagIndex {
	e.t.Helper()
	all, err := e.s.TagIndexes(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, ix := range all {
		if ix.Dir == dir && ix.Name == name {
			return ix
		}
	}
	e.t.Fatalf("no index %s %s", dir, name)
	return store.TagIndex{}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	e.write("bills/gas.pdf", "gas")
	e.write("bills/water.pdf", "water")
	e.scan()
	e.tag("bills/gas.pdf", "sender:British Gas")
	now := time.Date(2026, 11, 22, 14, 3, 0, 0, time.UTC)
	rt, err := e.s.RootAt(e.ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AddTagIndex(e.ctx, rt.ID, "bills", "index", now); err != nil {
		t.Fatal(err)
	}

	o, err := Update(e.ctx, e.s, e.index("bills", "index"), false, now)
	if err != nil || o.Skipped || o.Failed() || o.Result.Written == 0 || len(o.Data.Files) != 2 || o.Data.Tags() != 1 {
		t.Fatalf("first update: %+v, %v", o, err)
	}
	page := filepath.Join(e.root, "bills", "index.html")
	if _, err := os.Stat(page); err != nil {
		t.Fatalf("main page: %v", err)
	}
	ix := e.index("bills", "index")
	if ix.DataHash != o.Data.Hash() || ix.WrittenUTC == "" || ix.Problem != "" || len(ix.Files) != len(o.Result.Files) {
		t.Errorf("recorded: %+v", ix)
	}

	// A scan after writing finds no new file: the pages are Filo's own.
	e.scan()
	if all, err := e.s.FindFiles(e.ctx, 0, ""); err != nil || len(all) != 2 {
		t.Errorf("files after a scan: %d, %v; want the 2 bills", len(all), err)
	}

	// Nothing changed: skipped. Forced: rendered and checked, nothing to write.
	if o, err := Update(e.ctx, e.s, ix, false, now); err != nil || !o.Skipped {
		t.Errorf("unchanged update: %+v, %v", o, err)
	}
	if o, err := Update(e.ctx, e.s, ix, true, now); err != nil || o.Skipped || o.Result.Written != 0 || o.Failed() {
		t.Errorf("forced update: %+v, %v", o, err)
	}

	// A tag changes: the pages are written again.
	e.tag("bills/water.pdf", "sender:Thames Water")
	o, err = Update(e.ctx, e.s, e.index("bills", "index"), false, now)
	if err != nil || o.Skipped || o.Result.Written == 0 || o.Data.Tags() != 2 {
		t.Errorf("update after tagging: %+v, %v", o, err)
	}

	// A page edited by hand: the update fails, says why, and the next one tries again.
	if err := os.WriteFile(page, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.tag("bills/water.pdf", "type:bill")
	o, err = Update(e.ctx, e.s, e.index("bills", "index"), false, now)
	if err != nil || !o.Failed() || o.Result.Problem() != "index.html "+whyChanged {
		t.Errorf("update with a page edited: %+v, %v", o, err)
	}
	ix = e.index("bills", "index")
	if ix.DataHash != "" || ix.Problem != "index.html "+whyChanged {
		t.Errorf("recorded after a failure: %+v", ix)
	}
	if b, _ := os.ReadFile(page); string(b) != "mine" {
		t.Errorf("the edited page was replaced: %q", b)
	}
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	outs, err := UpdateAll(e.ctx, e.s, false, now)
	if err != nil || len(outs) != 1 || outs[0].Skipped || outs[0].Failed() {
		t.Errorf("update after moving the page aside: %+v, %v", outs, err)
	}
	if ix = e.index("bills", "index"); ix.Problem != "" || ix.DataHash == "" {
		t.Errorf("recorded after the retry: %+v", ix)
	}
}
