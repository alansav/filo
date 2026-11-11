package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestReportQueries(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	rootA, rootB := tree(t), tree(t)
	a, err := s.AddRoot(ctx, rootA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddRoot(ctx, rootB)
	if err != nil {
		t.Fatal(err)
	}
	file := func(p string, size int64, hash string) NewFile {
		return NewFile{Path: p, Kind: "file", Size: size, MtimeNS: 1, Hash: hash}
	}
	apply := func(root Root, ch Changes) {
		t.Helper()
		if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, ch); err != nil {
			t.Fatal(err)
		}
	}
	apply(a, Changes{New: []NewFile{
		file("big", 1000, "h1"), file("dup", 100, "h2"), file("sub/dup", 100, "h2"),
		file("empty1", 0, "e"), file("empty2", 0, "e"), file("gone-copy", 50, "h3"), file("kept-copy", 50, "h3"),
		{Path: "dead", Kind: "symlink", LinkTarget: "nowhere", LinkBroken: true},
		{Path: "live", Kind: "symlink", LinkTarget: "big"},
	}})
	apply(b, Changes{New: []NewFile{file("dup-in-b", 100, "h2"), file("small", 1, "h4")}})

	// gone-copy goes missing: its group no longer counts.
	known, err := s.Known(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range known {
		if k.Path == "gone-copy" {
			apply(a, Changes{Close: []int64{k.PathID}})
		}
	}

	d, err := s.Duplicates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if d.Groups != 1 || d.Copies != 3 || d.Reclaimable != 200 || len(d.Top) != 1 {
		t.Fatalf("duplicates = %+v, want one group of 3 x 100 bytes (200 reclaimable)", d)
	}
	wantPaths := []string{filepath.Join(rootA, "dup"), filepath.Join(rootA, "sub", "dup"), filepath.Join(rootB, "dup-in-b")}
	slices.Sort(wantPaths)
	if got := d.Top[0].Paths; !slices.Equal(got, wantPaths) {
		t.Errorf("duplicate paths = %v, want %v", got, wantPaths)
	}

	largest, err := s.Largest(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(largest) != 2 || largest[0].Size != 1000 || largest[1].Size != 100 {
		t.Errorf("largest = %+v", largest)
	}

	broken, total, err := s.BrokenLinks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(broken) != 1 || broken[0].Path != filepath.Join(rootA, "dead") || broken[0].Target != "nowhere" {
		t.Errorf("broken = %+v (total %d)", broken, total)
	}

	totals, err := s.RootTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRoot := map[string]RootTotal{}
	for _, rt := range totals {
		byRoot[rt.Root.Path] = rt
	}
	ta, tb := byRoot[rootA], byRoot[rootB]
	if ta.Files != 6 || ta.Links != 2 || ta.Bytes != 1250 || ta.LastScan == nil || ta.LastScan.ID != 3 {
		t.Errorf("root A totals = %+v", ta)
	}
	if tb.Files != 2 || tb.Bytes != 101 || tb.LastScan == nil || tb.LastScan.ID != 2 {
		t.Errorf("root B totals = %+v", tb)
	}
}

func TestInodeRoundTrip(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	r, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	const big = uint64(1)<<63 + 5 // beyond int64: must still round-trip
	if _, err := s.ApplyScan(ctx, ScanRecord{RootID: r.ID, Started: time.Now()}, Changes{New: []NewFile{
		{Path: "known", Kind: "file", Size: 1, Hash: "h", Ino: 42},
		{Path: "unknown", Kind: "file", Size: 1, Hash: "h"},
		{Path: "huge", Kind: "file", Size: 1, Hash: "h", Ino: big},
	}}); err != nil {
		t.Fatal(err)
	}
	known, err := s.Known(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint64{}
	for _, k := range known {
		got[k.Path] = k.Ino
	}
	if got["known"] != 42 || got["unknown"] != 0 || got["huge"] != big {
		t.Errorf("inodes = %v", got)
	}
	var nulls int
	if err := s.db.QueryRow(`SELECT count(*) FROM files WHERE inode IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Errorf("%d NULL inodes, want 1 (0 is stored as NULL)", nulls)
	}
}

func TestUnreadableFiles(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	rootA, rootB := tree(t), tree(t)
	a, err := s.AddRoot(ctx, rootA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddRoot(ctx, rootB)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(root Root, ch Changes) {
		t.Helper()
		if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, ch); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(root Root) map[string]Known {
		t.Helper()
		ks, err := s.Known(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Known{}
		for _, k := range ks {
			m[k.Path] = k
		}
		return m
	}

	// Scan 1: a/new-unreadable is flagged on arrival; the others read fine.
	apply(a, Changes{New: []NewFile{
		{Path: "new-unreadable", Kind: "file", Size: 1, MtimeNS: 1, Unreadable: true},
		{Path: "later", Kind: "file", Size: 1, MtimeNS: 1, Hash: "h1"},
		{Path: "goes", Kind: "file", Size: 1, MtimeNS: 1, Hash: "h2"},
		{Path: "recovers", Kind: "file", Size: 1, MtimeNS: 1, Hash: "h3"},
	}})
	// Scan 2: b/z is flagged on arrival.
	apply(b, Changes{New: []NewFile{{Path: "z", Kind: "file", Size: 1, MtimeNS: 1, Unreadable: true}}})
	// Scan 3: three files in a become unreadable; new-unreadable fails again and keeps scan 1.
	ka := ids(a)
	apply(a, Changes{Unreadable: []int64{ka["later"].FileID, ka["goes"].FileID, ka["recovers"].FileID,
		ka["new-unreadable"].FileID}})
	// Scan 4: goes is deleted (not present: not listed), recovers is read again (flag cleared).
	r := ka["recovers"]
	apply(a, Changes{Close: []int64{ka["goes"].PathID},
		Updates: []FileUpdate{{FileID: r.FileID, Size: r.Size, MtimeNS: r.MtimeNS, Hash: r.Hash}}})

	want := []UnreadableRow{
		{Path: filepath.Join(rootA, "new-unreadable"), Since: 1},
		{Path: filepath.Join(rootB, "z"), Since: 2},
		{Path: filepath.Join(rootA, "later"), Since: 3},
	}
	got, total, err := s.UnreadableFiles(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || !slices.Equal(got, want) {
		t.Errorf("UnreadableFiles = %+v (total %d), want %+v (total 3)", got, total, want)
	}

	got, total, err = s.UnreadableFiles(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || !slices.Equal(got, want[:2]) {
		t.Errorf("UnreadableFiles(2) = %+v (total %d), want %+v (total 3)", got, total, want[:2])
	}
}
