package store

// SchemaVersion is the schema this build reads and writes. It is stored in meta.schema_version when a database is
// created. An older database with a migration in migrations is upgraded when it is opened; any other version is
// refused.
const SchemaVersion = 4

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
    added_utc TEXT NOT NULL,
    include_hidden INTEGER NOT NULL DEFAULT 0,
    ignores TEXT NOT NULL DEFAULT '[]'
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
    ctime_ns INTEGER NOT NULL DEFAULT 0,
    hash TEXT,
    unreadable_since_scan INTEGER REFERENCES scans(id),
    inode INTEGER,
    link_target TEXT,
    link_broken INTEGER NOT NULL DEFAULT 0,
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id),
    keep_until TEXT,                    -- YYYY-MM-DD, or 'forever'
    reviewed_utc TEXT,
    reviewed_after_scan INTEGER REFERENCES scans(id),
    reviewed_hash TEXT
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
` + reviewSQL

// reviewSQL is the export and review tables, as of schema version 4.
const reviewSQL = `
CREATE TABLE exports (
    id INTEGER PRIMARY KEY,
    created_utc TEXT NOT NULL,
    path TEXT,
    after_scan INTEGER REFERENCES scans(id)
);

CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id),
    file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,
    keep_until TEXT,
    PRIMARY KEY (export_id, file_id)
);

CREATE TABLE review_batches (
    id INTEGER PRIMARY KEY,
    created_utc TEXT NOT NULL,
    command TEXT NOT NULL,
    detail TEXT
);

CREATE TABLE reviews (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    file_id INTEGER NOT NULL REFERENCES files(id),
    export_id INTEGER REFERENCES exports(id),
    keep_until TEXT,
    hash TEXT
);
CREATE INDEX ix_reviews_file ON reviews(file_id);
`

// migrations[v] upgrades a database from version v to v+1. Each runs in one transaction with the version bump.
var migrations = map[int]string{
	// 1 -> 2: status change time (0 = not known: filled in quietly by the next scan) and the unreadable flag.
	1: `
ALTER TABLE files ADD COLUMN ctime_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE files ADD COLUMN unreadable_since_scan INTEGER REFERENCES scans(id);
`,
	// 2 -> 3: the current review decision on each file, and the export and review history.
	2: `
ALTER TABLE files ADD COLUMN review_date TEXT;
ALTER TABLE files ADD COLUMN action TEXT;
ALTER TABLE files ADD COLUMN retain_until TEXT;
ALTER TABLE files ADD COLUMN reviewed_utc TEXT;
ALTER TABLE files ADD COLUMN reviewed_after_scan INTEGER REFERENCES scans(id);
ALTER TABLE files ADD COLUMN reviewed_hash TEXT;
` + reviewSQLv3,
	// 3 -> 4: one date per file. review_date and retain_until become keep_until (the later of the two, so nothing is
	// kept for less time); a recorded delete becomes keep_until = the day it was decided, so it is a deletion
	// candidate. Roots get their scan settings. Export rows keep the date they were exported with, not a reason.
	3: `
ALTER TABLE files ADD COLUMN keep_until TEXT;
UPDATE files SET keep_until = CASE
    WHEN action = 'delete' THEN coalesce(substr(reviewed_utc, 1, 10), review_date, retain_until)
    WHEN retain_until IS NOT NULL AND (review_date IS NULL OR retain_until > review_date) THEN retain_until
    ELSE review_date END;
ALTER TABLE files DROP COLUMN review_date;
ALTER TABLE files DROP COLUMN action;
ALTER TABLE files DROP COLUMN retain_until;

ALTER TABLE reviews ADD COLUMN keep_until TEXT;
UPDATE reviews SET keep_until = CASE
    WHEN action = 'delete' THEN (SELECT substr(b.created_utc, 1, 10) FROM review_batches b WHERE b.id = batch_id)
    WHEN retain_until IS NOT NULL AND (review_date IS NULL OR retain_until > review_date) THEN retain_until
    ELSE review_date END;
ALTER TABLE reviews DROP COLUMN review_date;
ALTER TABLE reviews DROP COLUMN action;
ALTER TABLE reviews DROP COLUMN retain_until;
UPDATE review_batches SET command = 'update' WHERE command = 'import';

ALTER TABLE export_rows DROP COLUMN reason;
ALTER TABLE export_rows ADD COLUMN keep_until TEXT;

ALTER TABLE roots ADD COLUMN include_hidden INTEGER NOT NULL DEFAULT 0;
ALTER TABLE roots ADD COLUMN ignores TEXT NOT NULL DEFAULT '[]';
`,
}

// reviewSQLv3 is the review tables as schema version 3 added them; migration 3 -> 4 reshapes them.
const reviewSQLv3 = `
CREATE TABLE exports (
    id INTEGER PRIMARY KEY,
    created_utc TEXT NOT NULL,
    path TEXT,
    after_scan INTEGER REFERENCES scans(id)
);

CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id),
    file_id INTEGER NOT NULL REFERENCES files(id),
    reason TEXT NOT NULL,
    hash TEXT,
    PRIMARY KEY (export_id, file_id)
);

CREATE TABLE review_batches (
    id INTEGER PRIMARY KEY,
    created_utc TEXT NOT NULL,
    command TEXT NOT NULL,
    detail TEXT
);

CREATE TABLE reviews (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    file_id INTEGER NOT NULL REFERENCES files(id),
    export_id INTEGER REFERENCES exports(id),
    review_date TEXT,
    action TEXT,
    retain_until TEXT,
    hash TEXT
);
CREATE INDEX ix_reviews_file ON reviews(file_id);
`
