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

// schemaV3Roots is the roots table before schema version 4 gave roots their settings.
const schemaV3Roots = `
CREATE TABLE roots (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    mount_path TEXT,
    added_utc TEXT NOT NULL
);`

func TestMigrateFromV1ToCurrent(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db,
		`DROP TABLE reviews`, `DROP TABLE review_batches`, `DROP TABLE export_rows`, `DROP TABLE exports`,
		`DROP TABLE files`, schemaV1Files, `DROP TABLE roots`, schemaV3Roots,
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
	// The file has no keep_until yet, so it is undated.
	q, err := s.Files(context.Background(), "2026-11-11", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].Status != StatusUndated {
		t.Errorf("files after upgrade = %+v, want the one file, undated", q)
	}
}

// schemaV3Files is the files table of schema version 3, with review_date, action and retain_until.
const schemaV3Files = `
CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    size INTEGER NOT NULL,
    mtime_ns INTEGER NOT NULL,
    ctime_ns INTEGER NOT NULL DEFAULT 0,
    hash TEXT,
    unreadable_since_scan INTEGER REFERENCES scans(id),
    inode INTEGER,
    link_target TEXT,
    link_broken INTEGER NOT NULL DEFAULT 0,
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id),
    review_date TEXT,
    action TEXT,
    retain_until TEXT,
    reviewed_utc TEXT,
    reviewed_after_scan INTEGER REFERENCES scans(id),
    reviewed_hash TEXT
);
CREATE INDEX ix_files_hash ON files(hash);`

func TestMigrateFromV3(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db,
		`DROP TABLE reviews`, `DROP TABLE review_batches`, `DROP TABLE export_rows`, `DROP TABLE exports`,
		`DROP TABLE files`, schemaV3Files, `DROP TABLE roots`, schemaV3Roots, reviewSQLv3,
		`UPDATE meta SET value = '3' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, finished_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 1)`,
		// date: a review date; floor: both, the floor later; early-floor: both, the review date later; alone: a floor
		// alone; del: marked delete on 2026-11-05; bulk: set-date (no hash); none: undecided.
		`INSERT INTO files (id, kind, size, mtime_ns, hash, first_seen_scan, review_date, action, retain_until,
			reviewed_utc, reviewed_after_scan, reviewed_hash) VALUES
			(1, 'file', 1, 1, 'h1', 1, '2027-01-01', NULL, NULL, '2026-11-05T10:00:00Z', 1, 'h1'),
			(2, 'file', 1, 1, 'h2', 1, '2027-01-01', NULL, '2030-06-30', '2026-11-05T10:00:00Z', 1, 'h2'),
			(3, 'file', 1, 1, 'h3', 1, '2031-01-01', NULL, '2030-06-30', '2026-11-05T10:00:00Z', 1, 'h3'),
			(4, 'file', 1, 1, 'h4', 1, NULL, NULL, '2029-01-01', '2026-11-05T10:00:00Z', 1, 'h4'),
			(5, 'file', 1, 1, 'h5', 1, NULL, 'delete', NULL, '2026-11-05T10:00:00Z', 1, 'h5'),
			(6, 'file', 1, 1, 'h6', 1, '2027-06-30', NULL, NULL, NULL, NULL, NULL),
			(7, 'file', 1, 1, 'h7', 1, NULL, NULL, NULL, NULL, NULL, NULL)`,
		`INSERT INTO paths (file_id, root_id, path, valid_from_scan) VALUES (1, 1, 'date', 1), (2, 1, 'floor', 1),
			(3, 1, 'early-floor', 1), (4, 1, 'alone', 1), (5, 1, 'del', 1), (6, 1, 'bulk', 1), (7, 1, 'none', 1)`,
		`INSERT INTO exports (id, created_utc, after_scan) VALUES (1, '2026-11-05T09:00:00Z', 1)`,
		`INSERT INTO export_rows (export_id, file_id, reason, hash) VALUES (1, 5, 'undated', 'h5')`,
		`INSERT INTO review_batches (id, created_utc, command) VALUES (1, '2026-11-05T10:00:00Z', 'import'),
			(2, '2026-11-05T11:00:00Z', 'set-date')`,
		`INSERT INTO reviews (batch_id, file_id, export_id, review_date, action, retain_until, hash) VALUES
			(1, 5, 1, NULL, 'delete', NULL, 'h5'), (1, 2, 1, '2027-01-01', NULL, '2030-06-30', 'h2'),
			(2, 6, NULL, '2027-06-30', NULL, NULL, NULL)`,
	)

	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	q, err := s.Files(context.Background(), "2026-11-12", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range q {
		got[it.Path] = it.KeepUntil + " " + string(it.Status)
	}
	want := map[string]string{
		"date": "2027-01-01 kept", "floor": "2030-06-30 kept", "early-floor": "2031-01-01 kept",
		"alone": "2029-01-01 kept", "del": "2026-11-05 expired", "bulk": "2027-06-30 kept", "none": " undated",
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s after upgrade = %q, want %q", p, got[p], w)
		}
	}
	if v := rawQueryString(t, db, `SELECT group_concat(coalesce(keep_until, '-'), ' ') FROM (SELECT keep_until FROM
		reviews ORDER BY id)`); v != "2026-11-05 2030-06-30 2027-06-30" {
		t.Errorf("review history keep_until = %q", v)
	}
	if v := rawQueryString(t, db, `SELECT group_concat(command, ' ') FROM (SELECT command FROM review_batches ORDER BY id)`); v != "update set-date" {
		t.Errorf("batch commands = %q", v)
	}
	roots, err := s.Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || !roots[0].Settings.Equal(Settings{}) {
		t.Errorf("roots after upgrade = %+v, want default settings", roots)
	}
	// Version 5: each keep_until is the retention it was (a date, or forever), with no file_date.
	if v := rawQueryString(t, db, `SELECT count(*) FROM files WHERE coalesce(retention, '') != coalesce(keep_until, '')
		OR file_date IS NOT NULL`); v != "0" {
		t.Errorf("%s files whose retention is not their old keep_until", v)
	}
	if v := rawQueryString(t, db, `SELECT group_concat(coalesce(retention, '-'), ' ') FROM (SELECT retention FROM
		reviews ORDER BY id)`); v != "2026-11-05 2030-06-30 2027-06-30" {
		t.Errorf("review history retention = %q", v)
	}
	for _, col := range []string{"review_date", "action", "retain_until"} {
		if v := rawQueryString(t, db, `SELECT count(*) FROM pragma_table_info('files') WHERE name = '`+col+`'`); v != "0" {
			t.Errorf("files.%s still there", col)
		}
	}
}
