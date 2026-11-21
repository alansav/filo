package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestFindFiles: the regular files at their current paths in a scope, ordered by root and then path; not missing
// files, files deleted by filo or symlinks; unreadable and empty files included (docs/design.md, Finding files).
func TestFindFiles(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	a, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.Path < a.Path {
		a, b = b, a // a sorts first
	}
	file := func(p string, size int64) NewFile {
		return NewFile{Path: p, Kind: "file", Size: size, MtimeNS: 7, Hash: "h" + p, Ino: uint64(size + 100)}
	}
	apply := func(root Root, ch Changes) {
		t.Helper()
		if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, ch); err != nil {
			t.Fatal(err)
		}
	}
	apply(a, Changes{New: []NewFile{file("docs/b.txt", 2), file("docs/a.txt", 1), file("empty", 0),
		file("gone.txt", 3), file("deleted.txt", 4), file("docs/sub/c.txt", 5), file("docsx/d.txt", 6),
		{Path: "link", Kind: "symlink", LinkTarget: "docs/a.txt"}}})
	apply(b, Changes{New: []NewFile{file("z.txt", 8)}})
	pathID := func(rel string) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM paths WHERE root_id = ? AND path = ?`, a.ID,
			rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	fileID := func(rel string) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT file_id FROM paths WHERE root_id = ? AND path = ?`, a.ID,
			rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	deleted := fileID("deleted.txt")
	apply(a, Changes{Close: []int64{pathID("gone.txt"), pathID("deleted.txt")},
		Unreadable: []int64{fileID("docs/b.txt")}})
	if _, err := s.exec(ctx, `UPDATE files SET deleted_utc = '2026-11-21T00:00:00Z' WHERE id = ?`, deleted); err != nil {
		t.Fatal(err)
	}

	type row struct {
		root, rel  string
		unreadable bool
	}
	for _, tc := range []struct {
		name  string
		root  int64
		scope string
		want  []row
	}{
		{"every root", 0, "", []row{{a.Path, "docs/a.txt", false}, {a.Path, "docs/b.txt", true},
			{a.Path, "docs/sub/c.txt", false}, {a.Path, "docsx/d.txt", false}, {a.Path, "empty", false},
			{b.Path, "z.txt", false}}},
		{"one root", b.ID, "", []row{{b.Path, "z.txt", false}}},
		{"a directory and below, not a name sharing its start", a.ID, "docs", []row{{a.Path, "docs/a.txt", false},
			{a.Path, "docs/b.txt", true}, {a.Path, "docs/sub/c.txt", false}}},
		{"a directory holding nothing present", a.ID, "old", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.FindFiles(ctx, tc.root, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			var rows []row
			for _, f := range got {
				rows = append(rows, row{f.Root, f.Rel, f.Unreadable})
				if f.MtimeNS != 7 || f.Inode != f.Size+100 || f.AbsPath() != f.Root+"/"+f.Rel {
					t.Errorf("%s: mtime %d, inode %d, abs %s", f.Rel, f.MtimeNS, f.Inode, f.AbsPath())
				}
			}
			if !slices.Equal(rows, tc.want) {
				t.Errorf("FindFiles = %v\nwant %v", rows, tc.want)
			}
		})
	}
}
