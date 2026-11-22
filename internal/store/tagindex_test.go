package store

import (
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Tag index pages (docs/design.md, Tag index pages): the register's indexes and the files written for them.

var indexNow = time.Date(2026, 11, 22, 14, 3, 0, 0, time.UTC)

func TestMigrateFromV11(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropTagIndexes, `UPDATE meta SET value = '11' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) || SchemaVersion != 12 {
		t.Fatalf("schema_version = %s after upgrade, want 12", v)
	}
	counts, err := s.RowCounts(t.Context())
	if err != nil || counts["tag_indexes"] != 0 || counts["tag_index_files"] != 0 {
		t.Errorf("row counts after upgrade: %v, %v", counts, err)
	}
	if notes := s.UpgradeNotes(); len(notes) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", notes)
	}
}

func TestValidIndexName(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"index", true}, {"index-tags", true}, {"Tags_2026", true}, {"9", true},
		{"", false}, {"-index", false}, {"_index", false}, {".index", false}, {"my index", false},
		{"a/b", false}, {"a.b", false}, {"café", false}, {string(make([]byte, 65)), false},
	} {
		if got := ValidIndexName(c.name); got != c.ok {
			t.Errorf("ValidIndexName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestAddTagIndex(t *testing.T) {
	e := newReviewEnv(t)
	other, err := e.s.AddRoot(e.ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		root      int64
		dir, name string
		err       error
	}{
		{e.root.ID, "bills", "index", nil},
		{e.root.ID, "bills", "tags", nil},
		{e.root.ID, "", "index", nil},
		{other.ID, "bills", "index", nil},
		{e.root.ID, "bills", "INDEX", ErrIndexExists}, // one file on a case-insensitive file system
		{e.root.ID, "bills/", "x", ErrRuleDir},
		{e.root.ID, "bills", ".x", ErrIndexName},
		{e.root.ID + other.ID + 1, "bills", "x", ErrRuleRoot},
	} {
		ix, err := e.s.AddTagIndex(e.ctx, c.root, c.dir, c.name, indexNow)
		if !errors.Is(err, c.err) {
			t.Errorf("AddTagIndex(%d, %q, %q) = %v, want %v", c.root, c.dir, c.name, err, c.err)
			continue
		}
		if err == nil && (ix.Dir != c.dir || ix.Name != c.name || ix.DataHash != "" || ix.WrittenUTC != "" ||
			len(ix.Files) != 0) {
			t.Errorf("AddTagIndex(%q, %q) = %+v", c.dir, c.name, ix)
		}
	}
	all, err := e.s.TagIndexes(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ix := range all {
		got = append(got, filepath.Base(ix.RootPath)+" "+ix.Rel(ix.Page())+" "+ix.Rel(ix.PagesDir()))
	}
	first, second := filepath.Base(e.root.Path), filepath.Base(other.Path)
	if e.root.Path > other.Path {
		t.Fatalf("roots not in creation order: %s, %s", e.root.Path, other.Path)
	}
	want := []string{
		first + " index.html .index",
		first + " bills/index.html bills/.index",
		first + " bills/tags.html bills/.tags",
		second + " bills/index.html bills/.index",
	}
	if !slices.Equal(got, want) {
		t.Errorf("TagIndexes =\n%q\nwant\n%q", got, want)
	}
}

func TestRecordAndRemoveTagIndex(t *testing.T) {
	e := newReviewEnv(t)
	ix, err := e.s.AddTagIndex(e.ctx, e.root.ID, "bills", "index", indexNow)
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]string{"index.html": "h1", ".index": "", ".index/style.css": "h2"}
	if err := e.s.RecordTagIndex(e.ctx, ix.ID, IndexWrite{Files: full, DataHash: "d1"}, indexNow); err != nil {
		t.Fatal(err)
	}
	got, err := e.s.TagIndex(e.ctx, ix.ID)
	if err != nil || got.DataHash != "d1" || got.WrittenUTC != utc(indexNow) || got.Problem != "" ||
		!maps.Equal(got.Files, full) {
		t.Fatalf("after a full write: %+v, %v", got, err)
	}

	// A failed write keeps the time of the last full one, clears the data hash and records the files written.
	later := indexNow.Add(time.Hour)
	partial := map[string]string{"index.html": "h3", ".index": "", ".index/style.css": "h2"}
	if err := e.s.RecordTagIndex(e.ctx, ix.ID, IndexWrite{Files: partial, Problem: "no room"}, later); err != nil {
		t.Fatal(err)
	}
	got, err = e.s.TagIndex(e.ctx, ix.ID)
	if err != nil || got.DataHash != "" || got.WrittenUTC != utc(indexNow) || got.Problem != "no room" ||
		!maps.Equal(got.Files, partial) {
		t.Fatalf("after a failed write: %+v, %v", got, err)
	}

	// The next full write clears the problem.
	if err := e.s.RecordTagIndex(e.ctx, ix.ID, IndexWrite{Files: full, DataHash: "d2"}, later); err != nil {
		t.Fatal(err)
	}
	if got, err = e.s.TagIndex(e.ctx, ix.ID); err != nil || got.Problem != "" || got.WrittenUTC != utc(later) {
		t.Fatalf("after a second full write: %+v, %v", got, err)
	}

	if err := e.s.RemoveTagIndex(e.ctx, ix.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.TagIndex(e.ctx, ix.ID); !errors.Is(err, ErrNoIndex) {
		t.Errorf("TagIndex after remove = %v, want ErrNoIndex", err)
	}
	counts, err := e.s.RowCounts(e.ctx)
	if err != nil || counts["tag_indexes"] != 0 || counts["tag_index_files"] != 0 {
		t.Errorf("row counts after remove: %v, %v", counts, err)
	}
	if err := e.s.RemoveTagIndex(e.ctx, ix.ID); !errors.Is(err, ErrNoIndex) {
		t.Errorf("RemoveTagIndex again = %v, want ErrNoIndex", err)
	}
	if err := e.s.RecordTagIndex(e.ctx, ix.ID, IndexWrite{DataHash: "d"}, later); !errors.Is(err, ErrNoIndex) {
		t.Errorf("RecordTagIndex of a removed index = %v, want ErrNoIndex", err)
	}
}

func TestOwnPathsHoldIndexes(t *testing.T) {
	e := newReviewEnv(t)
	if _, err := e.s.AddTagIndex(e.ctx, e.root.ID, "bills", "tags", indexNow); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.AddTagIndex(e.ctx, e.root.ID, "", "index", indexNow); err != nil {
		t.Fatal(err)
	}
	own, err := e.s.OwnPaths(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		e.s.DBPath(),
		filepath.Join(e.root.Path, "bills", "tags.html"), filepath.Join(e.root.Path, "bills", ".tags"),
		filepath.Join(e.root.Path, "index.html"), filepath.Join(e.root.Path, ".index"),
	} {
		if !slices.Contains(own, p) {
			t.Errorf("OwnPaths lacks %s: %q", p, own)
		}
	}
}
