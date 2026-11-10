# Filo: design

Status: iteration 1 (scan, register, report) is not yet implemented in Go. A C# prototype exists as a reference
spec only; its schema and reconcile rules are carried over below. Last updated: 2026-11-10.

This file records settled decisions, the planned data model and the open questions. When a decision changes,
change it here in the same commit.

## 1. Problem and principles

Files go into storage and never leave. Notes, documents and received PDFs pile up, which makes backups bigger and
finding things harder, feeds a hoarding habit ("keep it in case someone asks"), and creates demand for the person
who can find any old file. Filo is a register and review workflow that makes disposal a normal, safe, routine step.

Principles. These are not negotiable:

1. **A retain-until date is a floor, not a trigger.** Passing it makes a file eligible for review, never for deletion.
2. **Holds override everything.** Legal, tax and dispute holds veto any disposal and are checked last, at the moment
   of disposal, not when the date was set.
3. **Disposal is staged and reversible:** review, then quarantine, then (much later) deletion.
4. **Fail safe.** A file that loses its metadata, or whose state is ambiguous, becomes "new, needs review", never
   "eligible for deletion".
5. **Record only what is known.** At creation that is at most a floor date and a link to an asset or project.
   Never invent a delete date.
6. **Nothing destructive before its iteration.** Destructive commands default to dry-run.
7. **Never guess.** Duplicates and ambiguous moves are reported, not resolved.

## 2. Settled decisions

### Language and environment
- Go. The earlier C# prototype is a reference spec only.
- Dev environment: a Podman container defined in the repo (`.devcontainer/Containerfile`), driven by
  `scripts/dev.sh` (`build`, `shell`, `run CMD...`, `clean`). Only the repo is mounted at `/workspace`. The container
  has its own home directory, so a scan of "home" inside it can never reach the real one. Git runs on the host.
- Claude Code is intended to run inside the container with normal permission prompts. Until then, Go commands go
  through `scripts/dev.sh run`.
- OS-specific behaviour (hidden-file rules, inode lookup, symlinks, path separators) lives behind small functions so a
  Windows build is possible later. No platform is prioritised.

### Register
- SQLite, one row per file identity, at `<root>/.filo/filo.db` by default. `--root` and `--db` override
  (`FILO_ROOT` and `FILO_DB` as fallbacks). Created automatically on first run.
- One database per device, **never synced** (add `.filo` to Syncthing's ignore list in any shared folder). That is why
  there is no device column. Revisit only if a shared register is ever wanted.
- Paths are stored relative to the root, with `/` separators.
- Location history: each file's paths are stored as intervals (`valid_from_scan`, `valid_to_scan`, NULL = current),
  so the tree can be reconstructed at any scan and directory moves can be reverted.
- One writer at a time (lock file beside the database). Each scan is applied in a single transaction.
- The database, its WAL/SHM files and the lock file are never scanned.

### Scan
- Skips hidden entries by default (`--include-hidden` to override) plus noise: `node_modules`, `__pycache__`, `.filo`,
  user-supplied `--ignore NAME`, and `bin`/`obj` beside a `.csproj` or `.fsproj`.
- Symlinks are recorded with their target and a broken flag, never followed. Checking them is a later iteration.
- **Unreadable directories never cause files to be marked missing.** Entries under a directory that failed to list are
  left untouched.
- If a walk finds nothing but the database knows many files, abort and change nothing (likely an unmounted root).
- Scans are read-only on user files and safe to run while files are changing. A file caught mid-write appears
  correctly on the next scan.
- Size and mtime decide whether a file is rehashed; only new or changed files are hashed. Hashing is parallel with a
  configurable thread count (default 4; use 1 on a spinning disk).

### Move detection
- Hash match: a missing file and a new file with the same hash are one file that moved (or was renamed).
- Empty files are never matched. Duplicates are never guessed: with several candidates, pair only when the file name is
  unique on both sides, otherwise report as new plus missing.
- Same name, different content (edited then moved?) is reported as a **possible move** and never applied.
- Inode matching is a later optimisation (cheap in Go via `syscall.Stat_t`).

### Review (iteration 2)
- Export a CSV, review it in a spreadsheet, import it back. A UI replaces the CSV later.
- The CSV holds the **queue only**: files with no review date, files past their review date, and possible moves. Not the
  whole database.
- Rows are keyed on the database row ID. The import touches only the decision columns, never path or hash.
- Separate `review_date` and `action` columns. ISO dates, UTF-8, proper quoting.
- The hash at export is stored; it is **re-checked at action time**. A file edited since review goes back to the queue.
- Every reviewed file ends with one of two outcomes: a new review date, or an action (delete). There is no "keep where
  it is" without a date.

### Disposal (iteration 3 onward)
- Dry run first. Quarantine before deletion, for a set period.
- Checks at the moment of acting: retain-until floor passed, no hold, hash unchanged, file not modified in the last few
  minutes, no sync temp file present (`.syncthing.*.tmp`, `*.part`, `.~lock.*`), optionally no process holds it open.
- A history table of retired files (path, hash, date, reason) stays after the file is gone.
- **Syncthing propagates deletes.** Roots that are synced need a flag; quarantine for them must live outside the synced
  folder or deletion must be handled on one device only. Surface `*.sync-conflict-*` files in review.
- Scheduled runs only ever scan. Quarantine and deletion are always manual, after review.

### Commands and scheduling
- Plain names: `scan`, `report`, `review`, `restore`, `revert`. No themed subcommands.
- A systemd user timer running `filo scan` is enough. No daemon. (`Persistent=true`, low CPU and I/O priority.)

## 3. Data model (iteration 1, proposed)

Carried over from the C# prototype. Adjust freely in the Go port, but keep the interval history and the partial unique
index.

```sql
CREATE TABLE meta  (key TEXT PRIMARY KEY, value TEXT NOT NULL);          -- schema_version, created_utc

CREATE TABLE scans (
    id INTEGER PRIMARY KEY, started_utc TEXT NOT NULL, finished_utc TEXT, root TEXT NOT NULL,
    files_seen INTEGER, new_count INTEGER, changed_count INTEGER, moved_count INTEGER,
    missing_count INTEGER, error_count INTEGER, elapsed_ms INTEGER);

CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,                 -- 'file' | 'symlink'
    size INTEGER NOT NULL, mtime_ticks INTEGER NOT NULL,
    hash TEXT,                          -- NULL for symlinks and unreadable files
    link_target TEXT, link_broken INTEGER NOT NULL DEFAULT 0,
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id));

CREATE TABLE paths (
    id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL REFERENCES files(id),
    path TEXT NOT NULL,
    valid_from_scan INTEGER NOT NULL REFERENCES scans(id),
    valid_to_scan INTEGER REFERENCES scans(id));       -- NULL = current location
CREATE UNIQUE INDEX ux_paths_current ON paths(path) WHERE valid_to_scan IS NULL;
CREATE INDEX ix_paths_file ON paths(file_id);
CREATE INDEX ix_files_hash ON files(hash);

CREATE TABLE events (
    id INTEGER PRIMARY KEY, scan_id INTEGER NOT NULL REFERENCES scans(id),
    kind TEXT NOT NULL,                 -- moved | missing | changed | possible_move | error
    file_id INTEGER, path TEXT, other_path TEXT, detail TEXT);
```

"Present" means a file has a path interval with `valid_to_scan IS NULL`; there is no status column. "New" means
`first_seen_scan` equals the scan. Later iterations add review columns (or a table), holds, and the retired-files history.

Location history gives snapshots without storing them: the tree at any scan is the set of intervals that cover it.
A revert moves each file back to the path it had then, with a dry run that reports and skips conflicts. It reverts
**locations only**; content recovery is the job of backups and quarantine.

## 4. Scan and reconcile

1. Take the lock. Open or create the database. Load the current known entries (current path to file row).
2. Walk the root. Record size and mtime from the directory listing. Note directories that failed to list.
3. Classify by path: new; same path with matching size and mtime (unchanged); same path with different size or mtime
   (candidate: rehash). Symlinks compare target and broken flag.
4. Hash only what needs it (new files, candidates, and known files with no hash).
5. Same path, same hash: only the mtime moved, so update it quietly. Same path, different hash: changed.
6. Known but not seen, and not under a failed directory: missing.
7. Pair missing and new files by hash (rules in section 2). Report same-name leftovers as possible moves.
8. Apply in one transaction: close paths of missing and moved files **first**, so a path freed by one rename can be
   reused by another in the same scan; then updates; then open new paths and insert new files; then events.
9. Print a summary: counts plus samples of moved, missing, changed, possible moves and errors.

`filo report` shows the last scan, totals, the largest files, duplicate groups with reclaimable space, broken symlinks
and event samples.

## 5. Iterations

1. **Scan, register, report** (read-only). Done when: on a generated test tree, every kind of change (rename, move,
   edit, delete, duplicate, symlink, unreadable directory) is reported exactly on rescan; a first scan of the real home
   directory completes and shows the size of the backlog; a second scan takes seconds to a couple of minutes.
2. **Review CSV export and import.** Backlog handled by bulk-setting initial review dates by folder, so the first
   queue is manageable.
3. **Quarantine and delete** with the safety checks above, history table, holds.
4. **Revert directory moves** from the path history.
5. **UI** in place of the CSV.

Long term: other properties (type, author, sender) where they can be derived automatically, tags, projects and tasks
linked to files, and a daily view of the files needed. All of it hangs off the stable file ID, so none of it needs the
register to change shape.

## 6. Open questions

- Hash: a non-cryptographic 128-bit hash (xxh3-128 or similar) is proposed. Pick the Go library.
- SQLite driver: a pure-Go driver (no CGO) is preferred to keep builds and the container simple. Confirm which one.
- Holds and asset links: modelled at project or asset level, not per file. Shape not yet decided.
- Where a retain-until floor comes from (frontmatter, Office or PDF properties, extended attributes, or entered during
  review). The register is the source of truth either way.
- Default review intervals (for example 30 days, 12 months, 7 years) as suggestions at review time.
- Exact CSV columns, and how a synced-folder flag is configured.
- Very large files: a cheap identity (size plus first and last megabyte) with a full hash on demand.
- UI technology.

## 7. Rejected or deferred

- **Tier directories** (bronze, silver, gold, platinum). A good workflow, but moving files breaks links (recent-file
  lists), scatters one subject across tiers, and ties lifetime to location. Kept as an idea: tiers become default
  review intervals.
- **Review date stored inside each file.** Fragile: many editors drop extended attributes on save, and some formats
  cannot carry properties. Losing it fails safe but causes churn, and it cannot hold floors reliably.
- **A daemon.** A scheduled scan does the job; a daemon adds state, file watching limits and failure modes. Revisit only
  for near-real-time change detection.
- **Device tracking in the database.** Unneeded while the database is per device and never synced.
- **Themed command and project names** (the earlier "dirge" idea, funeral-style subcommands). Plain names win.
- **Building search.** Existing indexers and document managers (Paperless-ngx for received documents, desktop
  indexers) already cover finding files. Filo owns lifecycle.
