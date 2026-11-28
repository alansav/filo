package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestOpenReadOnly: completion's open (docs/design.md, Bash completion, "Read only, and fast") creates nothing when
// there is no register, reads one another run holds open (no lock), refuses another schema version, and writes
// nothing at all, not even a snapshot.
func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	missing := filepath.Join(dir, "sub", "filo.db")
	if _, err := OpenReadOnly(ctx, missing); !errors.Is(err, ErrNoRegister) {
		t.Errorf("no register: %v, want ErrNoRegister", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sub")); err == nil {
		t.Error("the open made a directory")
	}
	if _, err := OpenReadOnly(ctx, filepath.Join(dir, "filo.db")); !errors.Is(err, ErrNoRegister) {
		t.Errorf("no database file: %v, want ErrNoRegister", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the open made %v", entries)
	}

	db := filepath.Join(dir, "filo.db")
	w := mustOpen(t, Options{DBPath: db})
	root, err := w.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	// Read while the writer holds the lock and the register is open.
	r, err := OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatalf("read beside a writer: %v", err)
	}
	if roots, err := r.Roots(ctx); err != nil || len(roots) != 1 || roots[0].ID != root.ID {
		t.Errorf("roots = %v, %v", roots, err)
	}
	mustClose(t, r)
	mustClose(t, w)

	// Once the writer has closed (and SQLite removed its WAL files), a read writes nothing.
	before := listing(t, dir)
	r, err = OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatalf("read after the writer closed: %v", err)
	}
	if roots, err := r.Roots(ctx); err != nil || len(roots) != 1 {
		t.Errorf("roots = %v, %v", roots, err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM roots`); err == nil {
		t.Error("a write went through the read-only open")
	}
	mustClose(t, r)
	if after := listing(t, dir); !slices.Equal(before, after) {
		t.Errorf("the read changed the directory:\n%q\nwant\n%q", after, before)
	}

	rawExec(t, db, `UPDATE meta SET value = '99' WHERE key = 'schema_version'`)
	if _, err := OpenReadOnly(ctx, db); !errors.Is(err, ErrSchemaVersion) {
		t.Errorf("another schema version: %v, want ErrSchemaVersion", err)
	}
}

// listing is every entry under dir, with its size and modification time.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, err := d.Info()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %d %d", p, fi.Size(), fi.ModTime().UnixNano()))
		return nil
	})
	return out
}

// TestEntriesIn: the names in a directory that start with a prefix, a directory once however much it holds, a prefix
// matched as text (% and _ are no wildcards), files not present after the current ones, those registered last first
// and capped, and never the old path of a file that moved.
func TestEntriesIn(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	root, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	file := func(p string, n int64) NewFile {
		return NewFile{Path: p, Kind: "file", Size: n, MtimeNS: 7, Hash: "h" + p, Ino: uint64(n + 100)}
	}
	apply := func(ch Changes) {
		t.Helper()
		if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, ch); err != nil {
			t.Fatal(err)
		}
	}
	apply(Changes{New: []NewFile{file("docs/a.txt", 1), file("docs/b c.txt", 2), file("docs/sub/x", 3),
		file("docs/sub/deep/y", 4), file("docs/sub!", 5), file("docsx/d", 6), file("top", 7), file("a%b", 8),
		file("axb", 9), file("docs/old.txt", 10), file("docs/gone/z", 11), file("docs/moved.txt", 12)}})
	k := knownPaths(t, s, root.ID)
	apply(Changes{Close: []int64{k["docs/old.txt"].PathID}})
	apply(Changes{Close: []int64{k["docs/gone/z"].PathID}}) // registered after old.txt, so offered first
	apply(Changes{Close: []int64{k["docs/moved.txt"].PathID}, Open: []OpenPath{{FileID: k["docs/moved.txt"].FileID,
		Path: "docs/new.txt"}}})

	for _, tc := range []struct {
		dir, prefix string
		absent      int
		want        string
	}{
		{"", "", 100, "a%b axb docs/ docsx/ top"},
		{"", "a%", 100, "a%b"},
		{"", "docs", 100, "docs/ docsx/"},
		{"docs", "", 100, "a.txt b\x20c.txt new.txt sub! sub/ gone/ old.txt"},
		{"docs", "", 0, "a.txt b\x20c.txt new.txt sub! sub/"},
		{"docs", "", 1, "a.txt b\x20c.txt new.txt sub! sub/ gone/"},
		{"docs", "o", 100, "old.txt"},
		{"docs", "mov", 100, ""},
		{"docs/sub", "", 100, "deep/ x"},
		{"nowhere", "", 100, ""},
	} {
		got, err := s.EntriesIn(ctx, root.ID, tc.dir, tc.prefix, tc.absent)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range got {
			if e.Dir {
				e.Name += "/"
			}
			names = append(names, e.Name)
		}
		if strings.Join(names, " ") != tc.want {
			t.Errorf("EntriesIn(%q, %q, %d) = %q, want %q", tc.dir, tc.prefix, tc.absent, names, tc.want)
		}
	}
}

// knownPaths maps each current path of a root to what the register knows of it.
func knownPaths(t testing.TB, s *Store, rootID int64) map[string]Known {
	t.Helper()
	ks, err := s.Known(context.Background(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Known{}
	for _, k := range ks {
		out[k.Path] = k
	}
	return out
}

// TestEntriesInUsesTheIndex: the lookup of current paths is a search of the index of current paths, never a scan
// of the table, so it stays fast on a large register.
func TestEntriesInUsesTheIndex(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT path FROM paths
		WHERE root_id = ? AND valid_to_scan IS NULL AND path >= ? AND path < ? ORDER BY path LIMIT 1`, 1, "a", "b")
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
	if len(plan) != 1 || !strings.HasPrefix(plan[0], "SEARCH paths USING") ||
		!strings.Contains(plan[0], "INDEX ux_paths_current (root_id=? AND path>? AND path<?)") {
		t.Errorf("plan = %q, want a search of ux_paths_current", plan)
	}
}

// BenchmarkEntriesIn: completing in a register of 100,000 current files and 20,000 paths of files not present.
func BenchmarkEntriesIn(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	s, err := Open(ctx, Options{DBPath: filepath.Join(dir, "filo.db")})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	base, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	root, err := s.AddRoot(ctx, base)
	if err != nil {
		b.Fatal(err)
	}
	var files []NewFile
	for i := range 120_000 {
		p := fmt.Sprintf("d%02d/e%02d/f%06d.pdf", i%50, (i/50)%40, i)
		files = append(files, NewFile{Path: p, Kind: "file", Size: int64(i), MtimeNS: 7, Hash: fmt.Sprint(i),
			Ino: uint64(i + 1)})
	}
	if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, Changes{New: files}); err != nil {
		b.Fatal(err)
	}
	k := knownPaths(b, s, root.ID)
	var gone []int64
	for _, f := range files[:20_000] {
		gone = append(gone, k[f.Path].PathID)
	}
	if _, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Started: time.Now()}, Changes{Close: gone}); err != nil {
		b.Fatal(err)
	}
	for _, bc := range []struct {
		name, dir string
		absent    int
	}{{"root", "", 100}, {"root current only", "", 0}, {"deep", "d07/e03", 100}, {"deep current only", "d07/e03", 0}} {
		b.Run(bc.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := s.EntriesIn(ctx, root.ID, bc.dir, "", bc.absent); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
