package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func mustOpen(t *testing.T, opt Options) *Store {
	t.Helper()
	s, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("Open(%+v): %v", opt, err)
	}
	return s
}

func mustClose(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// tempDB returns a database path in a fresh temp directory. Tests never use the default location.
func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "filo.db")
}

// rawExec runs statements on a database file directly, bypassing Store (and its lock).
func rawExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func rawQueryString(t *testing.T, path, q string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(q).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func readMeta(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT key, value FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		if _, dup := m[k]; dup {
			t.Fatalf("duplicate meta key %q", k)
		}
		m[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDefaultDBPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func(h string) func() (string, error) { return func() (string, error) { return h, nil } }
	noHome := func() (string, error) { return "", errors.New("no home") }

	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		home    func() (string, error)
		want    string
		wantErr bool
	}{
		{"linux XDG_DATA_HOME", "linux", map[string]string{"XDG_DATA_HOME": "/data"}, home("/h"),
			filepath.Join("/data", "filo", "filo.db"), false},
		{"linux relative XDG_DATA_HOME ignored", "linux", map[string]string{"XDG_DATA_HOME": "rel"}, home("/h"),
			filepath.Join("/h", ".local", "share", "filo", "filo.db"), false},
		{"linux home fallback", "linux", nil, home("/h"), filepath.Join("/h", ".local", "share", "filo", "filo.db"), false},
		{"freebsd home fallback", "freebsd", nil, home("/h"), filepath.Join("/h", ".local", "share", "filo", "filo.db"), false},
		{"linux no home", "linux", nil, noHome, "", true},
		{"darwin", "darwin", map[string]string{"XDG_DATA_HOME": "/data"}, home("/Users/a"),
			filepath.Join("/Users/a", "Library", "Application Support", "filo", "filo.db"), false},
		{"darwin no home", "darwin", nil, noHome, "", true},
		{"windows", "windows", map[string]string{"LocalAppData": "/lad"}, noHome, filepath.Join("/lad", "filo", "filo.db"), false},
		{"windows unset", "windows", nil, home("/h"), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultDBPath(tc.goos, env(tc.env), tc.home)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenCreates(t *testing.T) {
	tests := []struct {
		name    string
		useXDG  bool // open with no DBPath, XDG_DATA_HOME pointing at a temp dir
		wantDir bool // the filo data directory is created
	}{
		{name: "explicit path"},
		{name: "default path via XDG_DATA_HOME", useXDG: true, wantDir: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opt := Options{DBPath: tempDB(t)}
			want := opt.DBPath
			if tc.useXDG {
				data := t.TempDir()
				t.Setenv("XDG_DATA_HOME", data)
				opt = Options{}
				want = filepath.Join(data, "filo", DBFileName)
			}

			s := mustOpen(t, opt)
			defer mustClose(t, s)

			if s.DBPath() != want {
				t.Errorf("DBPath() = %q, want %q", s.DBPath(), want)
			}
			if tc.wantDir && runtime.GOOS != "windows" {
				fi, err := os.Stat(filepath.Dir(want))
				if err != nil {
					t.Fatal(err)
				}
				if perm := fi.Mode().Perm(); perm&^0o700 != 0 {
					t.Errorf("data dir mode = %v, want no bits beyond 0700", perm)
				}
			}

			for _, obj := range []struct{ typ, name string }{
				{"table", "meta"}, {"table", "roots"}, {"table", "scans"}, {"table", "files"}, {"table", "paths"},
				{"table", "events"}, {"index", "ux_paths_current"}, {"index", "ix_paths_file"}, {"index", "ix_files_hash"},
			} {
				var n int
				if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`,
					obj.typ, obj.name).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("%s %s missing", obj.typ, obj.name)
				}
			}

			wantCols := map[string][]string{
				"scans": {"root_id", "scope", "include_hidden", "ignores"},
				"paths": {"root_id", "path", "valid_from_scan", "valid_to_scan"},
				"roots": {"path", "mount_path", "added_utc"},
			}
			for table, cols := range wantCols {
				have := map[string]bool{}
				rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var c string
					if err := rows.Scan(&c); err != nil {
						t.Fatal(err)
					}
					have[c] = true
				}
				rows.Close()
				for _, c := range cols {
					if !have[c] {
						t.Errorf("%s.%s missing", table, c)
					}
				}
			}

			m := readMeta(t, s)
			if m[metaSchemaVersion] != strconv.Itoa(SchemaVersion) {
				t.Errorf("meta.schema_version = %q", m[metaSchemaVersion])
			}
			if m[metaHashAlgorithm] != "sha256" {
				t.Errorf("meta.hash_algorithm = %q, want sha256", m[metaHashAlgorithm])
			}
			if m[metaCreatedUTC] == "" {
				t.Error("meta.created_utc empty")
			}
			if _, ok := m["root"]; ok {
				t.Error("meta.root should no longer exist")
			}

			var fk int
			if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if fk != 1 {
				t.Errorf("foreign_keys = %d, want 1", fk)
			}
			var jm string
			if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&jm); err != nil {
				t.Fatal(err)
			}
			if jm != "wal" {
				t.Errorf("journal_mode = %q, want wal", jm)
			}
			if runtime.GOOS != "windows" {
				// The queries above have opened the WAL and SHM files.
				for _, p := range []string{want, want + "-wal", want + "-shm", want + ".lock"} {
					fi, err := os.Stat(p)
					if err != nil {
						t.Errorf("stat %s: %v", filepath.Base(p), err)
						continue
					}
					if perm := fi.Mode().Perm(); perm&^0o600 != 0 {
						t.Errorf("%s mode = %v, want no bits beyond 0600", filepath.Base(p), perm)
					}
				}
			}
		})
	}
}

func TestOpenBadPaths(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		opt  Options
	}{
		{"db parent missing", Options{DBPath: filepath.Join(base, "nodir", "x.db")}},
		{"db parent is a file", Options{DBPath: filepath.Join(file, "x.db")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(context.Background(), tc.opt)
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want error")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(base, "nodir")); !os.IsNotExist(err) {
		t.Errorf("Open created a directory for an explicit DBPath (stat err %v)", err)
	}
}

func TestReopen(t *testing.T) {
	tests := []struct {
		name     string
		withRoot bool
	}{
		{name: "empty register"},
		{name: "with a root", withRoot: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opt := Options{DBPath: tempDB(t)}
			s := mustOpen(t, opt)
			first := readMeta(t, s)
			if tc.withRoot {
				if _, err := s.AddRoot(context.Background(), t.TempDir()); err != nil {
					t.Fatal(err)
				}
			}
			mustClose(t, s)

			s = mustOpen(t, opt)
			defer mustClose(t, s)
			second := readMeta(t, s)
			if len(second) != len(first) {
				t.Errorf("meta has %d keys after reopen, want %d", len(second), len(first))
			}
			for k, v := range first {
				if second[k] != v {
					t.Errorf("meta.%s changed on reopen: %q -> %q", k, v, second[k])
				}
			}
			roots, err := s.Roots(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{false: 0, true: 1}[tc.withRoot]; len(roots) != want {
				t.Errorf("%d roots after reopen, want %d", len(roots), want)
			}
		})
	}
}

func TestSchemaVersionMismatch(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{"older", "0"},
		{"newer", strconv.Itoa(SchemaVersion + 1)},
		{"garbage", "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := tempDB(t)
			mustClose(t, mustOpen(t, Options{DBPath: db}))
			rawExec(t, db, `UPDATE meta SET value = '`+tc.version+`' WHERE key = 'schema_version'`)

			s, err := Open(context.Background(), Options{DBPath: db})
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want ErrSchemaVersion")
			}
			if !errors.Is(err, ErrSchemaVersion) {
				t.Fatalf("err = %v, want ErrSchemaVersion", err)
			}
			if got := rawQueryString(t, db, `SELECT value FROM meta WHERE key = 'schema_version'`); got != tc.version {
				t.Errorf("schema_version changed to %q by a refused open", got)
			}
		})
	}
}

func TestHashAlgorithmMismatch(t *testing.T) {
	tests := []struct {
		name string
		stmt string
	}{
		{"other algorithm", `UPDATE meta SET value = 'xxh3-128' WHERE key = 'hash_algorithm'`},
		{"missing", `DELETE FROM meta WHERE key = 'hash_algorithm'`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := tempDB(t)
			mustClose(t, mustOpen(t, Options{DBPath: db}))
			rawExec(t, db, tc.stmt)
			before := rawQueryString(t, db, `SELECT group_concat(key || '=' || value, ',') FROM meta`)

			s, err := Open(context.Background(), Options{DBPath: db})
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want ErrSchemaVersion")
			}
			if !errors.Is(err, ErrSchemaVersion) {
				t.Fatalf("err = %v, want ErrSchemaVersion", err)
			}
			if after := rawQueryString(t, db, `SELECT group_concat(key || '=' || value, ',') FROM meta`); after != before {
				t.Errorf("refused open changed meta: %s -> %s", before, after)
			}
		})
	}
}

func TestNotFiloDB(t *testing.T) {
	tests := []struct {
		name  string
		stmts []string
	}{
		{"unrelated table", []string{`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`}},
		{"meta without keys", []string{`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "foreign.db")
			rawExec(t, db, tc.stmts...)
			if err := os.Chmod(db, 0o644); err != nil {
				t.Fatal(err)
			}
			before := rawQueryString(t, db, `PRAGMA journal_mode`)

			s, err := Open(context.Background(), Options{DBPath: db})
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want ErrNotFiloDB")
			}
			if !errors.Is(err, ErrNotFiloDB) {
				t.Fatalf("err = %v, want ErrNotFiloDB", err)
			}
			if after := rawQueryString(t, db, `PRAGMA journal_mode`); after != before {
				t.Errorf("refused open changed journal_mode %q -> %q", before, after)
			}
			fi, err := os.Stat(db)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o644 {
				t.Errorf("refused open changed file mode to %v", fi.Mode().Perm())
			}
		})
	}
}

func TestLock(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, db string)
	}{
		{
			name: "second open is refused while first is open",
			run: func(t *testing.T, db string) {
				s := mustOpen(t, Options{DBPath: db})
				defer mustClose(t, s)
				s2, err := Open(context.Background(), Options{DBPath: db})
				if err == nil {
					s2.Close()
					t.Fatal("second Open succeeded, want ErrLocked")
				}
				if !errors.Is(err, ErrLocked) {
					t.Fatalf("err = %v, want ErrLocked", err)
				}
			},
		},
		{
			name: "open succeeds after close",
			run: func(t *testing.T, db string) {
				mustClose(t, mustOpen(t, Options{DBPath: db}))
				mustClose(t, mustOpen(t, Options{DBPath: db}))
			},
		},
		{
			name: "failed open releases the lock",
			run: func(t *testing.T, db string) {
				mustClose(t, mustOpen(t, Options{DBPath: db}))
				rawExec(t, db, `UPDATE meta SET value = '99' WHERE key = 'schema_version'`)
				if _, err := Open(context.Background(), Options{DBPath: db}); !errors.Is(err, ErrSchemaVersion) {
					t.Fatalf("err = %v, want ErrSchemaVersion", err)
				}
				rawExec(t, db, `UPDATE meta SET value = '`+strconv.Itoa(SchemaVersion)+`' WHERE key = 'schema_version'`)
				mustClose(t, mustOpen(t, Options{DBPath: db}))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, tempDB(t))
		})
	}
}

// schemaV1Files is the files table of schema version 1, before ctime_ns and unreadable_since_scan.
const schemaV1Files = `
CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    size INTEGER NOT NULL,
    mtime_ns INTEGER NOT NULL,
    hash TEXT,
    inode INTEGER,
    link_target TEXT,
    link_broken INTEGER NOT NULL DEFAULT 0,
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id)
);
CREATE INDEX ix_files_hash ON files(hash);`

func TestMigrateFromV1ToCurrent(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db,
		`DROP TABLE reviews`, `DROP TABLE review_batches`, `DROP TABLE export_rows`, `DROP TABLE exports`,
		`DROP TABLE files`, schemaV1Files,
		`UPDATE meta SET value = '1' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO files (id, kind, size, mtime_ns, hash, inode, first_seen_scan) VALUES (1, 'file', 3, 42, 'abc', 7, 1)`,
		`INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (1, 1, 'a', 1)`,
	)

	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	ks, err := s.Known(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := Known{PathID: 1, FileID: 1, Path: "a", Kind: "file", Size: 3, MtimeNS: 42, Hash: "abc", Ino: 7}
	if len(ks) != 1 || ks[0] != want {
		t.Errorf("Known = %+v, want [%+v]: the row survives with ctime 0 and no unreadable flag", ks, want)
	}
	// Version 3: the file has no review decision yet, so it is queued as undated.
	q, err := s.Queue(context.Background(), "2026-11-11", QueueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].Reason != ReasonUndated {
		t.Errorf("queue after upgrade = %+v, want the one file, undated", q)
	}
}
