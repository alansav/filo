package store

import (
	"context"
	"maps"
	"slices"
	"strings"
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

// TestFindFilesLinks: a file's hard links are the other current paths in its root with the same inode, size and
// modified time, counted over the whole root whatever the scope; a file reusing a stale inode, one in another root,
// one with no inode and a missing path are not links.
func TestFindFilesLinks(t *testing.T) {
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
	f := func(p string, size, mtime int64, ino uint64) NewFile {
		return NewFile{Path: p, Kind: "file", Size: size, MtimeNS: mtime, Hash: "h", Ino: ino}
	}
	for _, sc := range []struct {
		root Root
		new  []NewFile
	}{
		{a, []NewFile{f("in/x.pdf", 5, 9, 42), f("out/x link.pdf", 5, 9, 42), f("out/gone.pdf", 5, 9, 42),
			f("in/stale.pdf", 6, 9, 42), f("in/no inode.pdf", 5, 9, 0), f("in/other no inode.pdf", 5, 9, 0)}},
		{b, []NewFile{f("x.pdf", 5, 9, 42)}},
	} {
		if _, err := s.ApplyScan(ctx, ScanRecord{RootID: sc.root.ID, Started: time.Now()},
			Changes{New: sc.new}); err != nil {
			t.Fatal(err)
		}
	}
	var gone int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM paths WHERE path = 'out/gone.pdf'`).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyScan(ctx, ScanRecord{RootID: a.ID, Started: time.Now()},
		Changes{Close: []int64{gone}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.FindFiles(ctx, a.ID, "in")
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]int{}
	for _, r := range got {
		links[r.Rel] = r.Links
	}
	want := map[string]int{"in/x.pdf": 1, "in/stale.pdf": 0, "in/no inode.pdf": 0, "in/other no inode.pdf": 0}
	if !maps.Equal(links, want) {
		t.Errorf("links = %v, want %v", links, want)
	}
}

// TestFindFilesLinkCountUsesInodeIndex: the hard-link count searches files by inode, not every current path of the
// root once per file (which took minutes on a root of 20,000 files). The plan is the bundled SQLite's, with the
// statistics a register has (none).
func TestFindFilesLinkCountUsesInodeIndex(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	r, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyScan(ctx, ScanRecord{RootID: r.ID, Started: time.Now()}, Changes{New: []NewFile{
		{Path: "a/x.pdf", Kind: "file", Size: 5, MtimeNS: 9, Hash: "h", Ino: 42},
		{Path: "b/x.pdf", Kind: "file", Size: 5, MtimeNS: 9, Hash: "h", Ino: 42},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		rootID int64
		scope  string
	}{
		{"every root", 0, ""},
		{"a root", r.ID, ""},
		{"a scope", r.ID, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, args := findFilesQuery(tc.rootID, tc.scope)
			rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			// The subquery's steps follow its line, up to the next line that is not a step: they must search files by
			// inode, then paths by file. The wording is the bundled SQLite's, and may change with its version.
			var steps []string
			if i := slices.IndexFunc(plan, func(l string) bool { return strings.Contains(l, "SUBQUERY") }); i >= 0 {
				for _, l := range plan[i+1:] {
					if !strings.HasPrefix(l, "SEARCH") && !strings.HasPrefix(l, "SCAN") {
						break
					}
					steps = append(steps, l)
				}
			}
			if len(steps) != 2 || !strings.Contains(steps[0], "lf") || !strings.Contains(steps[0], "ix_files_inode") ||
				!strings.Contains(steps[1], "lp") || !strings.Contains(steps[1], "ix_paths_file") {
				t.Errorf("the link count does not search files by inode, then their paths:\n%s", strings.Join(plan, "\n"))
			}
		})
	}
}
