package store

// SchemaVersion is the schema this build reads and writes. It is stored in meta.schema_version when a database is
// created. There are no migrations yet: a database with any other version is refused.
const SchemaVersion = 1

// Meta keys.
const (
	metaSchemaVersion = "schema_version"
	metaCreatedUTC    = "created_utc"
	metaHashAlgorithm = "hash_algorithm"
)

// schemaSQL creates an empty register. See docs/design.md section 3.
const schemaSQL = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE roots (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    mount_path TEXT,
    added_utc TEXT NOT NULL
);

CREATE TABLE scans (
    id INTEGER PRIMARY KEY,
    started_utc TEXT NOT NULL,
    finished_utc TEXT,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    scope TEXT NOT NULL DEFAULT '',
    include_hidden INTEGER NOT NULL DEFAULT 0,
    ignores TEXT,
    files_seen INTEGER,
    new_count INTEGER,
    changed_count INTEGER,
    moved_count INTEGER,
    missing_count INTEGER,
    error_count INTEGER,
    elapsed_ms INTEGER
);

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

CREATE TABLE paths (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id),
    root_id INTEGER NOT NULL REFERENCES roots(id),
    path TEXT NOT NULL,
    valid_from_scan INTEGER NOT NULL REFERENCES scans(id),
    valid_to_scan INTEGER REFERENCES scans(id)
);
CREATE UNIQUE INDEX ux_paths_current ON paths(root_id, path) WHERE valid_to_scan IS NULL;
CREATE INDEX ix_paths_file ON paths(file_id);
CREATE INDEX ix_files_hash ON files(hash);

CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    scan_id INTEGER NOT NULL REFERENCES scans(id),
    kind TEXT NOT NULL,
    file_id INTEGER,
    path TEXT,
    other_path TEXT,
    detail TEXT
);
`
