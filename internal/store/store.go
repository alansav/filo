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
}

// commit commits tx and notes that this run changed the register.
func (s *Store) commit(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changed = true
	return nil
}

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

// OwnPaths are the paths the register keeps next to its database: the database, SQLite's companion files, the lock
// file and the snapshot directory. A scan never records them, wherever they are.
func (s *Store) OwnPaths() []string {
	db := s.dbPath
	return []string{db, db + "-wal", db + "-shm", db + "-journal", lockPath(db), SnapshotDir(db)}
}

// DBPath is the absolute path of the database file.
func (s *Store) DBPath() string { return s.dbPath }

// resolveDBPath returns the absolute database path. The default data directory is the only directory Filo creates;
// an explicit path must have an existing parent directory.
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
		return def, nil
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
	return abs, nil
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
	db, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	// One connection: the per-connection pragmas apply to it, and Filo has one writer anyway.
	db.SetMaxOpenConns(1)

	changed, err := initOrCheck(ctx, db, dbPath)
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
	return &Store{db: db, dbPath: dbPath, changed: changed}, nil
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
// whether it changed the database: created it, or upgraded its schema.
func initOrCheck(ctx context.Context, db *sql.DB, dbPath string) (bool, error) {
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return false, err
	}
	if tables == 0 {
		return true, create(ctx, db)
	}
	before, _ := metaValue(ctx, db, metaSchemaVersion) // check reports a missing or unreadable version
	if err := check(ctx, db, dbPath); err != nil {
		return false, err
	}
	return before != strconv.Itoa(SchemaVersion), nil
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

func check(ctx context.Context, db *sql.DB, dbPath string) error {
	var hasMeta int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&hasMeta); err != nil {
		return err
	}
	if hasMeta == 0 {
		return fmt.Errorf("%w: no meta table", ErrNotFiloDB)
	}

	version, err := metaValue(ctx, db, metaSchemaVersion)
	if err != nil {
		return err
	}

	v, err := strconv.Atoi(version)
	if err != nil {
		return fmt.Errorf("%w: unreadable value %q", ErrSchemaVersion, version)
	}
	if v > SchemaVersion {
		return fmt.Errorf("%w: database is version %d, this filo supports %d; upgrade filo", ErrSchemaVersion, v, SchemaVersion)
	}
	for m := v; m < SchemaVersion; m++ {
		if _, ok := migrations[m]; !ok {
			return fmt.Errorf("%w: database is version %d, this filo supports %d and has no migration", ErrSchemaVersion, v, SchemaVersion)
		}
	}

	// Hashes made with different algorithms must never be compared.
	var algo string
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaHashAlgorithm).Scan(&algo)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: database records no hash algorithm", ErrSchemaVersion)
	}
	if err != nil {
		return fmt.Errorf("read meta %s: %w", metaHashAlgorithm, err)
	}
	if algo != hasher.Algorithm {
		return fmt.Errorf("%w: database hashes are %s, this filo uses %s", ErrSchemaVersion, algo, hasher.Algorithm)
	}
	// A copy of the database as it was, kept outside the rotation, before anything is changed. No copy, no upgrade.
	if v < SchemaVersion {
		if err := snapshotBeforeUpgrade(ctx, db, dbPath, v, time.Now()); err != nil {
			return err
		}
	}
	for ; v < SchemaVersion; v++ {
		if err := migrate(ctx, db, v); err != nil {
			return err
		}
	}
	return nil
}

// migrate upgrades the database from version v to v+1 in one transaction. The caller holds the lock.
func migrate(ctx context.Context, db *sql.DB, v int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return fmt.Errorf("upgrade database from version %d to %d: %w", v, v+1, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(v+1),
		metaSchemaVersion); err != nil {
		return fmt.Errorf("upgrade database from version %d to %d: %w", v, v+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upgrade database from version %d to %d: %w", v, v+1, err)
	}
	return nil
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
