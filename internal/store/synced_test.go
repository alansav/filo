package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func syncedTestStore(t *testing.T) (*Store, Root) {
	t.Helper()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	t.Cleanup(func() { mustClose(t, s) })
	dir := t.TempDir()
	root, err := s.AddRoot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func TestMigrateFromV14(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropSynced, `UPDATE meta SET value = '14' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	if _, err := s.SyncedDirs(context.Background()); err != nil {
		t.Fatalf("SyncedDirs after the upgrade: %v", err)
	}
}

func TestApplySynced(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	add := SyncedDir{SyncID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", RootID: root.ID, Dir: "Sync", State: SyncedJoined,
		Mark: []byte("mark"), AddedUTC: "2026-11-28T00:00:00Z"}
	if _, err := s.SetFoundMarks(ctx, root.ID, "", []FoundMark{{Dir: "Sync", SyncID: add.SyncID}}, "t"); err != nil {
		t.Fatal(err)
	}
	id, err := s.ApplySynced(ctx, SyncedChange{
		Add:     &add,
		Devices: []KnownDevice{{Dev: "dev1", Key: "k1", Name: "laptop", TrustedUTC: "t"}, {Dev: "dev2", Key: "k2", Name: "desktop"}},
		Journals: []JournalUpdate{{Dev: "dev1", Lines: []JournalLine{{1, []byte("l1")}, {2, []byte("l2")}}, Seq: 2,
			Offset: 6, Applied: map[string]int64{"dev1": 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySynced(ctx, SyncedChange{Add: &add}); !errors.Is(err, ErrSyncedExists) {
		t.Errorf("adding it again: %v, want ErrSyncedExists", err)
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil || len(dirs) != 1 || dirs[0].ID != id || dirs[0].Abs() != filepath.Join(root.Path, "Sync") ||
		string(dirs[0].Mark) != "mark" {
		t.Fatalf("SyncedDirs = %+v, %v", dirs, err)
	}
	if !dirs[0].Contains(root.ID, "Sync/a") || !dirs[0].Contains(root.ID, "Sync") || dirs[0].Contains(root.ID, "Syncx") {
		t.Error("Contains is wrong")
	}
	if found, _ := s.FoundMarks(ctx); len(found) != 0 {
		t.Errorf("the mark joined is still found: %+v", found)
	}
	devs, _ := s.KnownDevices(ctx)
	if len(devs) != 2 || devs[0].Name != "desktop" || devs[0].Trusted() || !devs[1].Trusted() {
		t.Fatalf("KnownDevices = %+v", devs)
	}
	// Reading on: the line kept already is not written again; new ones are; the state moves on.
	_, err = s.ApplySynced(ctx, SyncedChange{SyncedID: id, Journals: []JournalUpdate{{Dev: "dev1",
		Lines: []JournalLine{{2, []byte("l2")}, {3, []byte("l3")}}, Seq: 3, Offset: 9, SeenUTC: "seen"}}})
	if err != nil {
		t.Fatal(err)
	}
	js, _ := s.Journals(ctx, id)
	if len(js) != 1 || js[0].Seq != 3 || js[0].Offset != 9 || string(js[0].Last) != "l3" || js[0].SeenUTC != "seen" {
		t.Fatalf("Journals = %+v", js)
	}
	lines, _ := s.JournalLines(ctx, id, "dev1")
	if len(lines) != 3 || string(lines[2].Raw) != "l3" {
		t.Fatalf("JournalLines = %+v", lines)
	}
	// The same number with other bytes is refused, and nothing of the change is written.
	_, err = s.ApplySynced(ctx, SyncedChange{SyncedID: id, Journals: []JournalUpdate{{Dev: "dev1",
		Lines: []JournalLine{{3, []byte("other")}}, Seq: 3}}})
	if err == nil {
		t.Fatal("a line kept with other bytes was accepted")
	}
	// A device updated: retired, at its line 3 here.
	devs[1].RetiredUTC, devs[1].RetiredBy = "t", "dev2"
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Devices: []KnownDevice{devs[1]},
		Journals: []JournalUpdate{{Dev: "dev1", Seq: 3, Offset: 9, RetiredSeq: 3}}}); err != nil {
		t.Fatal(err)
	}
	devs, _ = s.KnownDevices(ctx)
	if !devs[1].Retired() || devs[1].RetiredBy != "dev2" {
		t.Errorf("after retiring: %+v", devs[1])
	}
	if js, _ := s.Journals(ctx, id); js[0].RetiredSeq != 3 {
		t.Errorf("the journal's retired line = %d, want 3", js[0].RetiredSeq)
	}
	// Moving and leaving.
	if err := s.MoveSynced(ctx, id, root.ID, "Files"); err != nil {
		t.Fatal(err)
	}
	if dirs, _ := s.SyncedDirs(ctx); dirs[0].Dir != "Files" {
		t.Errorf("after moving: %+v", dirs[0])
	}
	if err := s.RemoveSynced(ctx, id); err != nil {
		t.Fatal(err)
	}
	if dirs, _ := s.SyncedDirs(ctx); len(dirs) != 0 {
		t.Errorf("after leaving: %+v", dirs)
	}
	if lines, _ := s.JournalLines(ctx, id, "dev1"); len(lines) != 0 {
		t.Errorf("lines kept after leaving: %d", len(lines))
	}
	if !s.Changed() {
		t.Error("no change noted")
	}
}

func TestSetFoundMarks(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	marks := []FoundMark{{Dir: "Sync", SyncID: "a"}, {Dir: "x/y", Problem: "no mark"}}
	if wrote, err := s.SetFoundMarks(ctx, root.ID, "", marks, "t1"); err != nil || !wrote {
		t.Fatalf("first: %v, %v", wrote, err)
	}
	if wrote, err := s.SetFoundMarks(ctx, root.ID, "", marks, "t2"); err != nil || wrote {
		t.Fatalf("the same again wrote: %v, %v", wrote, err)
	}
	// A scan of x finds nothing there: only x/y goes.
	if wrote, err := s.SetFoundMarks(ctx, root.ID, "x", nil, "t3"); err != nil || !wrote {
		t.Fatalf("scope x: %v, %v", wrote, err)
	}
	got, _ := s.FoundMarks(ctx)
	if len(got) != 1 || got[0].Dir != "Sync" || got[0].FoundUTC != "t1" || got[0].Abs() != filepath.Join(root.Path, "Sync") {
		t.Fatalf("FoundMarks = %+v", got)
	}
}

func TestAbsorbCarriesSyncedDirs(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	top := t.TempDir()
	inner := filepath.Join(top, "Sync")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := s.AddRoot(ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySynced(ctx, SyncedChange{Add: &SyncedDir{SyncID: "a", RootID: r.ID, Dir: "", State: SyncedJoined,
		AddedUTC: "t"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFoundMarks(ctx, r.ID, "", []FoundMark{{Dir: "sub", SyncID: "b"}}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddRootAbsorb(ctx, top, Settings{}); err != nil {
		t.Fatal(err)
	}
	dirs, _ := s.SyncedDirs(ctx)
	if len(dirs) != 1 || dirs[0].Dir != "Sync" || dirs[0].Abs() != inner {
		t.Fatalf("after absorbing: %+v", dirs)
	}
	if found, _ := s.FoundMarks(ctx); len(found) != 1 || found[0].Dir != "Sync/sub" {
		t.Fatalf("found marks after absorbing: %+v", found)
	}
}
