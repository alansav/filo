package reconcile

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// Tag index pages (docs/design.md, Tag index pages, "Scans skip them"): an index's main page and pages directory are
// Filo's own files, which no scan records, even when the root includes hidden files; a file of the same name
// elsewhere is the person's.
func TestTagIndexFilesNotRecorded(t *testing.T) {
	e := newEnv(t, nil)
	e.write("bills/a.pdf", "a")
	e.write("bills/index.html", "<p>filo's</p>")
	e.write("bills/.index/style.css", "body{}")
	e.write("bills/.index/sender/index.html", "<p>filo's</p>")
	e.write("site/index.html", "<p>the person's</p>")
	e.write("site/.index/page.html", "<p>the person's</p>")
	if _, err := e.s.AddTagIndex(context.Background(), e.root.ID, "bills", "index", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, after, err := e.s.SetSettings(context.Background(), e.base, store.Settings{IncludeHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	e.root = after
	e.scan()
	var got []string
	for p := range e.known() {
		got = append(got, p)
	}
	slices.Sort(got)
	if want := []string{"bills/a.pdf", "site/.index/page.html", "site/index.html"}; !slices.Equal(got, want) {
		t.Errorf("registered %q, want %q", got, want)
	}
	for _, rel := range []string{"bills/index.html", "bills/.index/style.css"} {
		if p, why := e.prepare(rel); p != nil || why != "one of filo's own files" {
			t.Errorf("PrepareFile(%s) = %v, %q; want refused as filo's own", rel, p, why)
		}
	}
}
