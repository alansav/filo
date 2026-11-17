package store

// SchemaVersion is the schema this build reads and writes. It is stored in meta.schema_version when a database is
// created. An older database with a migration in migrations is upgraded when it is opened; any other version is
// refused.
const SchemaVersion = 10

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
    kind TEXT NOT NULL DEFAULT 'scan',  -- 'scan', or 'delete' for a delete run's record of what it did in a root
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
    file_date TEXT,                     -- YYYY-MM-DD: the date a retention period counts from
    retention TEXT,                     -- as given: a period (7y), 'forever' or YYYY-MM-DD
    keep_until TEXT,                    -- worked out from the two: YYYY-MM-DD or 'forever'
    reviewed_utc TEXT,
    reviewed_after_scan INTEGER REFERENCES scans(id),
    reviewed_hash TEXT,
    deleted_utc TEXT,                   -- when filo deleted it; its path is closed. NULL = not deleted by filo
    policy_id INTEGER,                  -- its retention policy (policies.id; then retention is NULL), NULL = none
    rule_id INTEGER,                    -- the rule that set its policy (rules.id); NULL = none, the rule removed, or
                                        -- its retention since set by hand
    rule_dated_utc TEXT                 -- when a rule dated it; once set, no rule dates it again
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
` + reviewSQL + duplicatesSQL + policiesSQL + rulesSQL

// reviewSQL is the export and review tables, as of schema version 8.
const reviewSQL = `
CREATE TABLE exports (
    id INTEGER PRIMARY KEY,
    created_utc TEXT NOT NULL,
    path TEXT,
    after_scan INTEGER REFERENCES scans(id),
    kind TEXT NOT NULL DEFAULT 'retention'  -- 'retention' or 'duplicates' ('separate' in schema 7: no longer written)
);

CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id),
    file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,
    keep_until TEXT,
    file_date TEXT,
    retention TEXT,
    dup_group INTEGER,                      -- duplicates exports: the group number in the CSV
    path_id INTEGER REFERENCES paths(id),   -- duplicates exports: the path shown
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
    hash TEXT,
    file_date TEXT,
    retention TEXT
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
	// 4 -> 5: a retention (a period counted from file_date, forever or a date) works out keep_until. An existing
	// keep_until becomes the retention it was: its date, or forever.
	4: `
ALTER TABLE files ADD COLUMN file_date TEXT;
ALTER TABLE files ADD COLUMN retention TEXT;
UPDATE files SET retention = keep_until WHERE keep_until IS NOT NULL;
ALTER TABLE export_rows ADD COLUMN file_date TEXT;
ALTER TABLE export_rows ADD COLUMN retention TEXT;
UPDATE export_rows SET retention = keep_until WHERE keep_until IS NOT NULL;
ALTER TABLE reviews ADD COLUMN file_date TEXT;
ALTER TABLE reviews ADD COLUMN retention TEXT;
UPDATE reviews SET retention = keep_until WHERE keep_until IS NOT NULL;
`,
	// 5 -> 6: deletion. A file filo deleted keeps its row, marked deleted; a delete run is recorded as a scan of
	// kind 'delete' in each root it deleted from, so a deleted file's path can be closed and its event recorded.
	5: `
ALTER TABLE files ADD COLUMN deleted_utc TEXT;
ALTER TABLE scans ADD COLUMN kind TEXT NOT NULL DEFAULT 'scan';
`,
	// 6 -> 7: duplicates. Exports get a kind (existing ones are retention exports), duplicates exports record each
	// row's group and path, and the decisions have tables of their own.
	6: `
ALTER TABLE exports ADD COLUMN kind TEXT NOT NULL DEFAULT 'retention';
ALTER TABLE export_rows ADD COLUMN dup_group INTEGER;
ALTER TABLE export_rows ADD COLUMN path_id INTEGER REFERENCES paths(id);
` + duplicatesSQLv7,
	// 7 -> 8: the duplicates redesign (docs/design.md, Duplicates). No decisions to delete are stored: the marks and
	// the decision history go, with the review batches only they used. Separate contents become always. Sets of
	// records and excluded directories get tables. The marks not carried out are counted first, for the run's
	// warning (upgradeNote7).
	7: alwaysSQL + `
INSERT INTO always_hashes (hash, added_utc)
    SELECT sh.hash, b.created_utc FROM separate_hashes sh JOIN review_batches b ON b.id = sh.batch_id;
DROP TABLE separate_hashes;
DROP TABLE duplicate_marks;
DROP TABLE duplicate_reviews;
DELETE FROM review_batches WHERE id NOT IN (SELECT batch_id FROM reviews);
` + recordsSQL,
	// 8 -> 9: named retention policies (docs/design.md, Lifespan, Named retention policies). Only new tables and a
	// column: every file keeps its own retention, and no file is on a policy yet.
	8: `
ALTER TABLE files ADD COLUMN policy_id INTEGER;
` + policiesSQL,
	// 9 -> 10: directory rules (docs/design.md, Lifespan, Directory rules). Only a new table and columns: no file has
	// been dated by a rule yet.
	9: `
ALTER TABLE files ADD COLUMN rule_id INTEGER;
ALTER TABLE files ADD COLUMN rule_dated_utc TEXT;
` + rulesSQL,
}

// rulesSQL is the directory rules, added in schema version 10. A rule names a policy for the files under a directory
// of a root ('/'-separated, relative to the root, the empty path for the root itself) whose name matches its pattern
// (as typed; '*' for none). pattern_key is the pattern in lower case: a directory's patterns are unique in any case.
// letters is the pattern's length, every character but * (rules.Length; the column was named before a ? counted),
// stored for reading the register by hand: matching works it out again. A rule keeps its policy in use: the store
// refuses to remove a policy a rule names. files.rule_id is not declared a foreign key: removing a rule sets it to
// NULL, and files.rule_dated_utc stays, so no rule dates that file again.
const rulesSQL = `
CREATE TABLE rules (
    id INTEGER PRIMARY KEY,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    dir TEXT NOT NULL,
    pattern TEXT NOT NULL,
    pattern_key TEXT NOT NULL,
    letters INTEGER NOT NULL,
    policy_id INTEGER NOT NULL REFERENCES policies(id),
    added_utc TEXT NOT NULL,
    UNIQUE (root_id, dir, pattern_key)
);
CREATE INDEX ix_files_rule ON files(rule_id);
`

// policiesSQL is the named retention policies, added in schema version 9. A file on a policy has files.policy_id set
// and files.retention NULL; its keep_until is worked out from the policy's value and stored, as for a retention of
// its own. files.policy_id is not declared a foreign key, so the store keeps it pointing at a policy (a policy in use
// is never removed). policy_changes is the history: kind 'add', 'set', 'rename', 'move' or 'remove', the policy's
// name at the time, its old and new value (for 'rename' and 'move', new_value is the policy moved to) and how many
// files the change touched.
const policiesSQL = `
CREATE TABLE policies (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,              -- lower case: letters, digits, - and _, starting with a letter
    value TEXT,                             -- a period (7y), a date (YYYY-MM-DD) or 'forever'; NULL = none yet
    added_utc TEXT NOT NULL,
    changed_utc TEXT NOT NULL
);
CREATE INDEX ix_files_policy ON files(policy_id);

CREATE TABLE policy_changes (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    policy TEXT NOT NULL,
    old_value TEXT,
    new_value TEXT,
    files INTEGER NOT NULL DEFAULT 0,
    changed_utc TEXT NOT NULL
);
`

// duplicatesSQL is the duplicates tables as of schema version 8. A content is always (never a duplicate group) while
// its hash is in always_hashes. A file is one of a set of records while its row's hash is the file's hash, so an
// edited record leaves its set by itself. An excluded directory is a path relative to its root ('/'-separated, the
// empty path for the whole root): files under it, the whole subtree, never count as duplicates.
const duplicatesSQL = alwaysSQL + `
CREATE INDEX ix_files_inode ON files(inode);
` + recordsSQL

// alwaysSQL is the contents always, added in schema version 8 (from version 7's separate_hashes).
const alwaysSQL = `
CREATE TABLE always_hashes (hash TEXT PRIMARY KEY, added_utc TEXT NOT NULL);
`

// recordsSQL is the sets of records and the excluded directories, added in schema version 8.
const recordsSQL = `
CREATE TABLE duplicate_records (
    file_id INTEGER PRIMARY KEY REFERENCES files(id),
    hash TEXT NOT NULL,
    set_id INTEGER NOT NULL,
    added_utc TEXT NOT NULL
);
CREATE INDEX ix_duplicate_records_set ON duplicate_records(set_id);

CREATE TABLE excluded_dirs (
    root_id INTEGER NOT NULL REFERENCES roots(id),
    path TEXT NOT NULL,
    added_utc TEXT NOT NULL,
    PRIMARY KEY (root_id, path)
);
`

// duplicatesSQLv7 is the duplicates tables of schema version 7, the first design: a mark counted only while its
// hash was the file's hash. Migration 6 -> 7 creates them; 7 -> 8 replaces them.
const duplicatesSQLv7 = `
CREATE TABLE duplicate_marks (
    file_id INTEGER PRIMARY KEY REFERENCES files(id),
    hash TEXT NOT NULL,
    export_id INTEGER REFERENCES exports(id),
    batch_id INTEGER NOT NULL REFERENCES review_batches(id)
);

CREATE TABLE separate_hashes (
    hash TEXT PRIMARY KEY,
    export_id INTEGER REFERENCES exports(id),
    batch_id INTEGER NOT NULL REFERENCES review_batches(id)
);

CREATE TABLE duplicate_reviews (
    id INTEGER PRIMARY KEY,
    batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    export_id INTEGER REFERENCES exports(id),
    file_id INTEGER REFERENCES files(id),
    hash TEXT NOT NULL,
    decision TEXT NOT NULL
);
CREATE INDEX ix_duplicate_reviews_file ON duplicate_reviews(file_id);
CREATE INDEX ix_files_inode ON files(inode);
`

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
