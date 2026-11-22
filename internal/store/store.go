// Package store owns Filo's SQLite register: creating or opening it, checking its schema version, holding the
// single-writer lock, and managing the roots it covers.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/hasher"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// DBFileName is the database's file name in the default data directory.
const DBFileName = "filo.db"

var (
	// ErrLocked means another process holds the database's lock file.
	ErrLocked = errors.New("database is locked by another filo process")
	// ErrSchemaVersion means the database's schema version is not the one this build supports.
	ErrSchemaVersion = errors.New("unsupported schema version")
	// ErrNotFiloDB means the file is a SQLite database but not a Filo register.
	ErrNotFiloDB = errors.New("not a filo database")
)

// Options say which register to open.
type Options struct {
	DBPath string // optional; empty means DefaultDBPath()
}

// Store is an open register. It holds the writer lock until Close.
type Store struct {
	db     *sql.DB
	lock   *lockFile
	dbPath string
	// changed: this run committed a change to the register, so Close takes a snapshot. Every write goes through
	// commit or exec, which set it only once the change is committed: a rolled-back transaction changed nothing.
	changed bool
	notes   []string // what this run's schema upgrade dropped or changed, for a warning (UpgradeNotes)
}

// UpgradeNotes is what the schema upgrade this run made dropped or changed, a sentence each, for the command to show
// once as a warning: none when the register was already current.
func (s *Store) UpgradeNotes() []string { return s.notes }

// Changed reports whether this run committed a change to the register, which its end takes a snapshot of.
func (s *Store) Changed() bool { return s.changed }

// commit commits tx and notes that this run changed the register.
func (s *Store) commit(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed = true
	return nil
}

// commitRecord commits tx, whose only write is the record of a run that changed nothing else (a scan that found
// nothing), without noting a change: such a run takes no snapshot and leaves meta.last_write_utc alone
// (docs/design.md, Register snapshots, "When").
func (s *Store) commitRecord(tx *sql.Tx) error { return tx.Commit() }

// exec runs one write statement in its own transaction and notes the change if it changed a row.
func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return res, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		s.changed = true
	}
	return res, nil
}

// Open creates or opens the register. It takes the lock first, creates the schema in one transaction if the
// database is empty, and refuses a database that is not Filo's or has another schema version. On error nothing is
// left open or locked.
func Open(ctx context.Context, opt Options) (*Store, error) {
	dbPath, err := resolveDBPath(opt.DBPath)
	if err != nil {
		return nil, err
	}
	if err := checkDBDir(filepath.Dir(dbPath)); err != nil {
		return nil, err
	}

	lock, err := acquireLock(lockPath(dbPath))
	if err != nil {
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dbPath)
		}
		return nil, err
	}

	s, err := openLocked(ctx, dbPath)
	if err != nil {
		lock.release()
		return nil, err
	}
	s.lock = lock
	return s, nil
}

// Close takes a snapshot of the register if this run wrote to it (see snapshot.go), closes the database and then
// releases the lock. A failed snapshot is reported in the error, but the run's own writes stand.
func (s *Store) Close() error {
	var errs []error
	ctx := context.Background() // an interrupted run still gets its snapshot
	if s.changed {
		now := time.Now()
		if err := s.recordWrite(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("record the time of this change: %w", err))
		}
		switch err := s.Snapshot(ctx, now); {
		case errors.Is(err, errPrune):
			errs = append(errs, err)
		case err != nil:
			errs = append(errs, fmt.Errorf("register snapshot failed: %w", err))
		}
	}
	errs = append(errs, s.db.Close(), s.lock.release())
	return errors.Join(errs...)
}

func lockPath(dbPath string) string { return dbPath + ".lock" }

// OwnPaths are the paths of Filo's own files: those the register keeps next to its database (the database, SQLite's
// companion files, the lock file and the snapshot directory) and each tag index's main page and pages directory
// (docs/design.md, Tag index pages). A scan never records them, wherever they are.
func (s *Store) OwnPaths(ctx context.Context) ([]string, error) {
	db := s.dbPath
	own := []string{db, db + "-wal", db + "-shm", db + "-journal", lockPath(db), SnapshotDir(db)}
	ix, err := s.indexPaths(ctx)
	if err != nil {
		return nil, err
	}
	return append(own, ix...), nil
}

// DBPath is the absolute path of the database file.
func (s *Store) DBPath() string { return s.dbPath }

// resolveDBPath returns the absolute database path, its directory with symlinks resolved, so one database reached
// through two symlinked directories has one lock file. The default data directory is the only directory Filo
// creates; an explicit path must have an existing parent directory.
func resolveDBPath(dbPath string) (string, error) {
	if dbPath == "" {
		def, err := DefaultDBPath()
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(def)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create %s: %w", dir, err)
		}
		dbPath = def
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("database %s: %w", dbPath, err)
	}
	parent := filepath.Dir(abs)
	fi, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("database directory %s: %w", parent, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("database directory %s is not a directory", parent)
	}
	dir, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("database directory %s: %w", parent, err)
	}
	return filepath.Join(dir, filepath.Base(abs)), nil
}

// checkDBDir refuses a database directory (dir, symlinks resolved) that belongs to another user or that others may
// write to. SQLite opens the database and its companion files by path, so whoever can write to the directory could
// swap any of them for a symlink or another file in the instant between checkDBFiles and SQLite's open, or create
// a -wal file before SQLite does (issue #22). Only a directory no one else can change closes that gap.
func checkDBDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("database directory %s: %w", dir, err)
	}
	switch {
	case !fi.IsDir():
		return fmt.Errorf("database directory %s is not a directory", dir)
	case !ownedByUser(fi):
		return fmt.Errorf("database directory %s belongs to another user; keep the database in a directory of "+
			"your own", dir)
	case writableByOthers(fi):
		return fmt.Errorf("database directory %s can be written to by other users; keep the database in a "+
			"directory only you can write to (chmod go-w %s)", dir, dir)
	}
	return nil
}

// checkDBFiles refuses a database, or a WAL, SHM or journal file next to it, that is a symlink, not a regular file,
// or owned by another user: SQLite opens them by path and would follow a symlink planted in their place (with --db
// in a shared directory such as /tmp) to a file somewhere else. Files that do not exist yet are fine; SQLite creates
// them with the database's mode.
func checkDBFiles(dbPath string) error {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"} {
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("database file %s: %w", p, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("database file %s is a symlink; give the path of the database itself", p)
		case !fi.Mode().IsRegular():
			return fmt.Errorf("database file %s is not a regular file", p)
		case !ownedByUser(fi):
			return fmt.Errorf("database file %s belongs to another user", p)
		}
	}
	return nil
}

func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// Triggers and views stored in the database may not call functions with side effects: a database that was
	// altered or swapped can carry its own SQL, which then runs whenever filo reads or writes.
	q.Add("_pragma", "trusted_schema(0)")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: q.Encode()}
	return u.String()
}

func openLocked(ctx context.Context, dbPath string) (*Store, error) {
	if err := precreate(dbPath); err != nil {
		return nil, err
	}
	if err := checkDBFiles(dbPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	// One connection: the per-connection pragmas apply to it, and Filo has one writer anyway.
	db.SetMaxOpenConns(1)

	changed, notes, err := initOrCheck(ctx, db, dbPath)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", dbPath, err)
	}
	// WAL is persistent in the file, so it is set only once the database is known to be ours: a foreign database
	// passed with --db must be left untouched.
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: set WAL mode: %w", dbPath, err)
	}
	return &Store{db: db, dbPath: dbPath, changed: changed, notes: notes}, nil
}

// precreate makes a new database file with mode 0600 so the register is private to its owner (SQLite would use
// 0644). SQLite treats the empty file as a new database and gives its -wal and -shm files the same mode. An existing
// file is left exactly as it is: it may not be ours.
func precreate(dbPath string) error {
	f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create database %s: %w", dbPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create database %s: %w", dbPath, err)
	}
	return nil
}

// initOrCheck creates the register in an empty database or checks (and upgrades) an existing one. It reports
// whether it changed the database (created it, or upgraded its schema) and what an upgrade dropped or changed.
func initOrCheck(ctx context.Context, db *sql.DB, dbPath string) (bool, []string, error) {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return false, nil, err
	}
	if tables == 0 {
		return true, nil, create(ctx, db)
	}
	before, _ := metaValue(ctx, db, metaSchemaVersion) // check reports a missing or unreadable version
	notes, err := check(ctx, db, dbPath)
	if err != nil {
		return false, nil, err
	}
	return before != strconv.Itoa(SchemaVersion), notes, nil
}

func create(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	meta := [][2]string{
		{metaSchemaVersion, strconv.Itoa(SchemaVersion)},
		{metaCreatedUTC, time.Now().UTC().Format(time.RFC3339)},
		{metaHashAlgorithm, hasher.Algorithm},
	}
	for _, kv := range meta {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("write meta: %w", err)
		}
	}
	return tx.Commit()
}

func check(ctx context.Context, db *sql.DB, dbPath string) ([]string, error) {
	var hasMeta int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&hasMeta); err != nil {
		return nil, err
	}
	if hasMeta == 0 {
		return nil, fmt.Errorf("%w: no meta table", ErrNotFiloDB)
	}

	version, err := metaValue(ctx, db, metaSchemaVersion)
	if err != nil {
		return nil, err
	}

	v, err := strconv.Atoi(version)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable value %q", ErrSchemaVersion, version)
	}
	if v > SchemaVersion {
		return nil, fmt.Errorf("%w: database is version %d, this filo supports %d; upgrade filo", ErrSchemaVersion, v, SchemaVersion)
	}
	for m := v; m < SchemaVersion; m++ {
		if _, ok := migrations[m]; !ok {
			return nil, fmt.Errorf("%w: database is version %d, this filo supports %d and has no migration", ErrSchemaVersion, v, SchemaVersion)
		}
	}

	// Hashes made with different algorithms must never be compared.
	var algo string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaHashAlgorithm).Scan(&algo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: database records no hash algorithm", ErrSchemaVersion)
	}
	if err != nil {
		return nil, fmt.Errorf("read meta %s: %w", metaHashAlgorithm, err)
	}
	if algo != hasher.Algorithm {
		return nil, fmt.Errorf("%w: database hashes are %s, this filo uses %s", ErrSchemaVersion, algo, hasher.Algorithm)
	}
	// A copy of the database as it was, kept outside the rotation, before anything is changed. No copy, no upgrade.
	if v < SchemaVersion {
		if err := snapshotBeforeUpgrade(ctx, db, dbPath, v, time.Now()); err != nil {
			return nil, err
		}
	}
	var notes []string
	for ; v < SchemaVersion; v++ {
		note, err := migrate(ctx, db, v)
		if err != nil {
			return nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	return notes, nil
}

// migrate upgrades the database from version v to v+1 in one transaction, and returns what the upgrade dropped or
// changed that the person should hear about ("" for nothing). The caller holds the lock.
func migrate(ctx context.Context, db *sql.DB, v int) (string, error) {
	fail := func(err error) (string, error) {
		return "", fmt.Errorf("upgrade database from version %d to %d: %w", v, v+1, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	note := ""
	if v == 7 {
		if note, err = upgradeNote7(ctx, tx); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(v+1),
		metaSchemaVersion); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return note, nil
}

// upgradeNote7 counts, before the upgrade to schema 8 drops them, the duplicate decisions not carried out (marks on
// a copy that is still there with the content it was marked on) and the contents marked separate, which become
// always (docs/design.md, Duplicates, Register).
func upgradeNote7(ctx context.Context, tx *sql.Tx) (string, error) {
	var marks, separate int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM duplicate_marks m JOIN files f ON f.id = m.file_id
		WHERE f.hash = m.hash AND f.deleted_utc IS NULL`).Scan(&marks); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM separate_hashes`).Scan(&separate); err != nil {
		return "", err
	}
	var parts []string
	if marks > 0 {
		parts = append(parts, fmt.Sprintf("the upgrade dropped %d duplicate %s not carried out (kept in the "+
			"pre-upgrade snapshot); export duplicates again to decide %s", marks, plural(marks, "decision", "decisions"),
			plural(marks, "it", "them")))
	}
	if separate > 0 {
		parts = append(parts, fmt.Sprintf("%d %s now always", separate,
			plural(separate, "content marked separate is", "contents marked separate are")))
	}
	return strings.Join(parts, "; "), nil
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func metaValue(ctx context.Context, db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: meta has no %s", ErrNotFiloDB, key)
	}
	if err != nil {
		return "", fmt.Errorf("read meta %s: %w", key, err)
	}
	return v, nil
}
