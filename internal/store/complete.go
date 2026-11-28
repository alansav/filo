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
)

// ErrNoRegister means there is no register at the path OpenReadOnly was given: it never creates one.
var ErrNoRegister = errors.New("no register")

// OpenReadOnly opens the register at dbPath ("" for the default) only to read it, for bash completion
// (docs/design.md, Bash completion, "Read only, and fast"): it never creates a database or a directory, never
// upgrades the schema, takes no lock and waits for nothing, and its Close writes no snapshot. A register with
// another schema version is refused (ErrSchemaVersion), as is one that is not there (ErrNoRegister). The database
// and its directory get the same checks as Open's (no symlinked database, no directory others can write to).
func OpenReadOnly(ctx context.Context, dbPath string) (*Store, error) {
	if dbPath == "" {
		def, err := DefaultDBPath()
		if err != nil {
			return nil, err
		}
		dbPath = def
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoRegister, dbPath)
	}
	dbPath = filepath.Join(dir, filepath.Base(abs))
	if _, err := os.Lstat(dbPath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoRegister, dbPath)
	}
	if err := checkDBDir(dir); err != nil {
		return nil, err
	}
	if err := checkDBFiles(dbPath); err != nil {
		return nil, err
	}
	_, err = os.Lstat(dbPath + "-wal")
	db, err := sql.Open("sqlite", readOnlyDSN(dbPath, errors.Is(err, fs.ErrNotExist)))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	v, err := metaValue(ctx, db, metaSchemaVersion)
	if err == nil && v != strconv.Itoa(SchemaVersion) {
		err = fmt.Errorf("%w: %s", ErrSchemaVersion, v)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", dbPath, err)
	}
	return &Store{db: db, dbPath: dbPath}, nil
}

// readOnlyDSN opens the database read only (mode=ro: SQLite then writes nothing to it, not even a checkpoint on
// close), failing at once rather than waiting, with stored triggers kept from calling functions as in dsn. With no
// WAL file (no run is writing, and nothing waits to be checkpointed) it is opened immutable as well, since a
// read-only open of a database in WAL mode would otherwise make the WAL and shared-memory files, and leave them.
func readOnlyDSN(path string, immutable bool) string {
	q := url.Values{}
	q.Add("mode", "ro")
	if immutable {
		q.Add("immutable", "1")
	}
	q.Add("_pragma", "busy_timeout(0)")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "trusted_schema(0)")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: q.Encode()}
	return u.String()
}

// Entry is a name bash completion offers in a directory of a root: a file, or a directory (Dir) holding registered
// paths.
type Entry struct {
	Name string
	Dir  bool
}

// pathsAbove is above every path that starts with the text before it: no byte of a name is 0xff in UTF-8, and SQLite
// compares text byte by byte.
const pathsAbove = "\xff"

// EntriesIn returns the names directly in dir (relative to the root, '/'-separated, "" for the root itself) that
// start with prefix, each once (docs/design.md, Bash completion, "Read only, and fast"): first the names of current
// paths, in order, one range query per name on the index of current paths (a directory's whole tree is stepped over
// in one); then the names of the paths of files not present (missing, or deleted by filo), from the absent of those
// paths registered last: no index covers them, so the table is read from its newest row back, stopping there.
func (s *Store) EntriesIn(ctx context.Context, rootID int64, dir, prefix string, absent int) ([]Entry, error) {
	under := ""
	if dir != "" {
		under = dir + "/"
	}
	lo, hi := under+prefix, under+prefix+pathsAbove
	var out []Entry
	seen := map[string]bool{}
	add := func(p string) (name string, isDir bool) {
		name, _, isDir = strings.Cut(strings.TrimPrefix(p, under), "/")
		if !seen[name] {
			seen[name] = true
			out = append(out, Entry{Name: name, Dir: isDir})
		}
		return name, isDir
	}
	for {
		var p string
		err := s.db.QueryRowContext(ctx, `SELECT path FROM paths
			WHERE root_id = ? AND valid_to_scan IS NULL AND path >= ? AND path < ? ORDER BY path LIMIT 1`,
			rootID, lo, hi).Scan(&p)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("complete %s: %w", lo, err)
		}
		name, isDir := add(p)
		next := p + "\x00" // the next path after this one: none holds a NUL
		if isDir {
			next = under + name + "/" + pathsAbove // past the directory's whole tree
		}
		if next <= p {
			next = p + "\x00" // a name holding a 0xff byte (not UTF-8): one path at a time there
		}
		lo = next
	}
	if absent <= 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.path FROM paths p
		WHERE p.root_id = ? AND p.valid_to_scan IS NOT NULL AND p.path >= ? AND p.path < ?
		  AND NOT EXISTS (SELECT 1 FROM paths c WHERE c.file_id = p.file_id AND c.valid_to_scan IS NULL)
		ORDER BY p.id DESC LIMIT ?`, rootID, under+prefix, hi, absent)
	if err != nil {
		return nil, fmt.Errorf("complete %s: %w", under+prefix, err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		add(p)
	}
	return out, rows.Err()
}
