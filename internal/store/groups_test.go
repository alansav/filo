package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// groupFixture is a register with one root (/r, id 1) and one scan, for testing which contents are duplicate groups
// (docs/design.md, Duplicates: "A group").
type groupFixture struct {
	t      *testing.T
	s      *Store
	nextID int64
}

func newGroupFixture(t *testing.T) *groupFixture {
	t.Helper()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	t.Cleanup(func() { mustClose(t, s) })
	g := &groupFixture{t: t, s: s, nextID: 1}
	g.exec(`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-11-15T00:00:00Z')`)
	g.exec(`INSERT INTO roots (id, path, added_utc) VALUES (2, '/s', '2026-11-15T00:00:00Z')`)
	g.exec(`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-11-15T00:00:00Z', 1)`)
	return g
}

func (g *groupFixture) exec(q string, args ...any) {
	g.t.Helper()
	if _, err := g.s.db.Exec(q, args...); err != nil {
		g.t.Fatalf("%s: %v", q, err)
	}
}

// file adds a present regular file with this content at each of paths ("root:rel", root 1 or 2): one file, so
// several paths are hard links. It returns the file's ID.
func (g *groupFixture) file(hash string, paths ...string) int64 {
	g.t.Helper()
	id := g.nextID
	g.nextID++
	g.exec(`INSERT INTO files (id, kind, size, mtime_ns, hash, inode, first_seen_scan) VALUES (?, 'file', 10, 1, ?, ?, 1)`,
		id, hash, id)
	for _, p := range paths {
		root, rel, _ := strings.Cut(p, ":")
		g.exec(`INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (?, ?, ?, 1)`, id, root, rel)
	}
	return id
}

// fileIno adds a present regular file with this content and inode at path ("root:rel"): two of them with one inode in
// one root are hard links held as two file rows, as a scan records them.
func (g *groupFixture) fileIno(hash string, ino int64, path string) int64 {
	g.t.Helper()
	id := g.nextID
	g.nextID++
	root, rel, _ := strings.Cut(path, ":")
	g.exec(`INSERT INTO files (id, kind, size, mtime_ns, hash, inode, first_seen_scan) VALUES (?, 'file', 10, 1, ?, ?, 1)`,
		id, hash, ino)
	g.exec(`INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (?, ?, ?, 1)`, id, root, rel)
	return id
}

// groups returns each duplicate group as its content and its copies' first paths ("root:rel"), sorted.
func (g *groupFixture) groups() map[string][]string {
	g.t.Helper()
	all, err := g.s.DuplicateGroups(context.Background(), Filter{})
	if err != nil {
		g.t.Fatal(err)
	}
	out := map[string][]string{}
	for _, d := range all {
		for _, c := range d.Copies {
			out[d.Hash] = append(out[d.Hash], fmt.Sprintf("%d:%s", c.RootID, c.Path))
		}
		slices.Sort(out[d.Hash])
	}
	return out
}

// TestDuplicateGroups: a group is a content held by two or more copies, not under an excluded directory, the content
// not always, and not all of them one set of records (scenarios F2 and F3).
func TestDuplicateGroups(t *testing.T) {
	tests := []struct {
		name  string
		setup func(g *groupFixture)
		want  []string // the copies of content "h", or nil for no group
	}{
		{"two copies", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "1:b")
		}, []string{"1:a", "1:b"}},
		{"hard links are one copy", func(g *groupFixture) {
			g.file("h", "1:a", "1:b")
		}, nil},
		{"F2: a content always is not a group", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "1:b")
			g.exec(`INSERT INTO always_hashes (hash, added_utc) VALUES ('h', 'x')`)
		}, nil},
		{"F2: a copy under an excluded directory does not count", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "1:backup/a")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, nil},
		{"two copies outside an excluded directory are still a group, of the two", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "1:b")
			g.file("h", "1:backup/a")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, []string{"1:a", "1:b"}},
		{"an excluded directory is a whole subtree, not a name prefix", func(g *groupFixture) {
			g.file("h", "1:backup/a")
			g.file("h", "1:backups/a")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, nil},
		{"an exclusion is in its own root only", func(g *groupFixture) {
			g.file("h", "1:backup/a")
			g.file("h", "2:backup/a")
			g.file("h", "2:b")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, []string{"2:b", "2:backup/a"}},
		{"a whole root excluded", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "2:a")
			g.file("h", "2:b")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (2, '', 'x')`)
		}, nil},
		{"a file with one path excluded counts through its other path", func(g *groupFixture) {
			g.file("h", "1:a")
			g.file("h", "1:backup/b", "1:b")
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, []string{"1:a", "1:b"}},
		{"all copies one set of records: not a group", func(g *groupFixture) {
			a, b := g.file("h", "1:a"), g.file("h", "1:b")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'h', 1, 'x')`,
				a, b)
		}, nil},
		{"F3: a new copy of a set of records makes a group, with the records", func(g *groupFixture) {
			a, b := g.file("h", "1:a"), g.file("h", "1:b")
			g.file("h", "1:c")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'h', 1, 'x')`,
				a, b)
		}, []string{"1:a", "1:b", "1:c"}},
		{"records of two different sets are a group", func(g *groupFixture) {
			a, b := g.file("h", "1:a"), g.file("h", "1:b")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'h', 2, 'x')`,
				a, b)
		}, []string{"1:a", "1:b"}},
		{"a record edited since leaves its set", func(g *groupFixture) {
			a, b := g.file("h", "1:a"), g.file("h", "1:b")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'old', 1, 'x')`,
				a, b)
		}, []string{"1:a", "1:b"}},
		{"a record held on a hard link's second path is still a record", func(g *groupFixture) {
			g.fileIno("h", 50, "1:a/x") // a/x and b/x: one file on disk, two rows; a/x comes first
			b := g.fileIno("h", 50, "1:b/x")
			c := g.fileIno("h", 51, "1:c/x")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'h', 1, 'x')`,
				b, c)
		}, nil},
		{"records with a member under an excluded directory: the rest is one set", func(g *groupFixture) {
			a, b := g.file("h", "1:a"), g.file("h", "1:b")
			g.file("h", "1:backup/c")
			g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'h', 1, 'x'), (?, 'h', 1, 'x')`,
				a, b)
			g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x')`)
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newGroupFixture(t)
			tc.setup(g)
			got := g.groups()["h"]
			if !slices.Equal(got, tc.want) {
				t.Errorf("group of h = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNotCounted: the report's "Not counted" line counts the excluded directories, and the contents always and sets of
// records that would otherwise be a group: two or more copies. One with a single copy left keeps nothing out.
func TestNotCounted(t *testing.T) {
	g := newGroupFixture(t)
	g.file("lic", "1:a/LICENSE")
	g.file("lic", "1:b/LICENSE")
	a, b := g.file("terms", "1:2020/terms.pdf"), g.file("terms", "1:2021/terms.pdf")
	solo, last := g.file("solo", "1:solo.txt"), g.file("deed", "1:deed.pdf")
	g.exec(`INSERT INTO always_hashes (hash, added_utc) VALUES ('lic', 'x'), ('gone', 'x'), ('solo', 'x')`)
	g.exec(`INSERT INTO duplicate_records (file_id, hash, set_id, added_utc) VALUES (?, 'terms', 1, 'x'), (?, 'terms', 1, 'x'),
		(?, 'deed', 2, 'x')`, a, b, last)
	_ = solo
	g.exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (1, 'backup', 'x'), (2, '', 'x')`)
	d, err := g.s.Duplicates(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.NotCounted; got != (NotCounted{Dirs: 2, Always: 1, RecordSets: 1}) {
		t.Errorf("NotCounted = %+v, want 2 directories, 1 content always, 1 set of records", got)
	}
}

// TestAbsorbMovesExclusions: a root taken in by a new root brings its excluded directories with it, under its place
// in the new root.
func TestAbsorbMovesExclusions(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	base := tree(t, "home/Documents/backup")
	home, docs := filepath.Join(base, "home"), filepath.Join(base, "home", "Documents")
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	ctx := context.Background()
	d, err := s.AddRoot(ctx, docs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO excluded_dirs (root_id, path, added_utc) VALUES (?, 'backup', 'x'), (?, '', 'x')`,
		d.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	h, _, err := s.AddRootAbsorb(ctx, home, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queryAll(ctx, s.db, func(r *sql.Rows) (string, error) {
		var root int64
		var p string
		err := r.Scan(&root, &p)
		return strconv.FormatInt(root, 10) + ":" + p, err
	}, `SELECT root_id, path FROM excluded_dirs ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("%d:Documents", h.ID), fmt.Sprintf("%d:Documents/backup", h.ID)}
	if !slices.Equal(rows, want) {
		t.Errorf("exclusions after absorbing = %q, want %q", rows, want)
	}
}

// TestMigrateFromV7: the first design's duplicates tables go. Separate contents become always, marks and decision
// history are dropped (and the batches only they used), and the run that upgrades says what was dropped.
func TestMigrateFromV7(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropPolicies, dropDuplicates, duplicatesSQLv7,
		`UPDATE meta SET value = '7' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO files (id, kind, size, mtime_ns, hash, first_seen_scan) VALUES
			(1, 'file', 3, 1, 'a', 1), (2, 'file', 3, 1, 'b', 1), (3, 'file', 3, 1, 'c', 1)`,
		`UPDATE files SET deleted_utc = '2026-02-01T00:00:00Z' WHERE id = 2`,
		`INSERT INTO review_batches (id, created_utc, command) VALUES
			(1, '2026-01-02T00:00:00Z', 'update'), (2, '2026-01-03T00:00:00Z', 'update duplicates')`,
		`INSERT INTO reviews (batch_id, file_id) VALUES (1, 1)`,
		// Three marks: one not carried out, one carried out (its file deleted), one stale (the file changed since).
		`INSERT INTO duplicate_marks (file_id, hash, batch_id) VALUES (1, 'a', 2), (2, 'b', 2), (3, 'old', 2)`,
		`INSERT INTO duplicate_reviews (batch_id, file_id, hash, decision) VALUES (2, 1, 'a', 'delete')`,
		`INSERT INTO separate_hashes (hash, batch_id) VALUES ('s1', 2), ('s2', 2)`,
	)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	var always []string
	rows, err := s.db.Query(`SELECT hash || ' ' || added_utc FROM always_hashes ORDER BY hash`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		always = append(always, a)
	}
	rows.Close()
	if want := []string{"s1 2026-01-03T00:00:00Z", "s2 2026-01-03T00:00:00Z"}; !slices.Equal(always, want) {
		t.Errorf("always_hashes = %q, want %q", always, want)
	}
	for _, gone := range []string{"duplicate_marks", "duplicate_reviews", "separate_hashes"} {
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, gone).
			Scan(&n); err != nil || n != 0 {
			t.Errorf("table %s after upgrade: %d (%v), want gone", gone, n, err)
		}
	}
	var batches string
	if err := s.db.QueryRow(`SELECT group_concat(id) FROM review_batches`).Scan(&batches); err != nil || batches != "1" {
		t.Errorf("review batches after upgrade = %q (%v), want only 1, the retention update's", batches, err)
	}
	want := []string{"the upgrade dropped 1 duplicate decision not carried out (kept in the pre-upgrade snapshot); " +
		"export duplicates again to decide it; 2 contents marked separate are now always"}
	if got := s.UpgradeNotes(); !slices.Equal(got, want) {
		t.Errorf("UpgradeNotes = %q, want %q", got, want)
	}
	if _, err := s.RowCounts(context.Background()); err != nil {
		t.Errorf("the tables after upgrade: %v", err)
	}
}

// TestMigrateFromV7Quiet: an upgrade with nothing dropped and nothing separate says nothing.
func TestMigrateFromV7Quiet(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropPolicies, dropDuplicates, duplicatesSQLv7, `UPDATE meta SET value = '7' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if got := s.UpgradeNotes(); len(got) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", got)
	}
}
