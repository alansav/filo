# Filo: design

Status: iteration 1 (scan, register, report) is not yet implemented in Go. A C# prototype exists as a reference
spec only; its schema and reconcile rules are carried over below. Last updated: 2026-11-11.

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
- Claude Code runs inside the container (`scripts/dev.sh claude`) with normal permission prompts. Its login and
  settings live in a named volume (`filo-claude`). Git commits and pushes happen on the host: the container has no git
  identity and no SSH keys.
- OS-specific behaviour (hidden-file rules, inode lookup, symlinks, path separators) lives behind small functions so a
  Windows build is possible later. No platform is prioritised.

### Register
- SQLite, one row per file identity, in **one database per device holding any number of roots**. Default location:
  `$XDG_DATA_HOME/filo/filo.db` (usually `~/.local/share/filo/filo.db`) on Linux and BSD,
  `~/Library/Application Support/filo/filo.db` on macOS, `%LocalAppData%\filo\filo.db` on Windows. `--db` (or
  `FILO_DB`) overrides it. Created automatically on first run; the default `filo` directory is the only directory
  Filo creates (mode 0700), and an explicit `--db` path must have an existing parent.
- **Never synced.** The default location is outside every root, so the register is never inside a synced folder and
  never scanned. That is also why there is no device column. Revisit only if a shared register is ever wanted.
- Driver: `modernc.org/sqlite` (pure Go, no CGO, plain `database/sql`), chosen 2026-11-11 at v1.62.0.
- `meta.schema_version` must equal the build's version; there are no migrations yet, so any other version (older or
  newer) is refused, as is a SQLite file without Filo's `meta` keys. WAL mode is set only after those checks pass, so
  a foreign database is never modified.
- One writer at a time: an `flock` on `<db>.lock`, taken before the database is opened. The kernel drops it if Filo
  dies, so there are no stale locks. Non-unix platforms refuse to open until locking is implemented for them. Each
  scan is applied in a single transaction.
- Filo creates a new database file with mode 0600; SQLite gives the WAL and SHM files the same mode. An existing
  file's mode is never changed.
- The database, its WAL/SHM/journal files and the lock file are never scanned, wherever they are.

### Roots
- Roots are registered explicitly (`filo roots add PATH`) and stored in the `roots` table as absolute paths with
  symlinks resolved. `filo roots` lists them; `filo roots remove` (later, with a dry run) removes a root's register
  rows, never files.
- **No aliases.** A new root is refused if it is the same directory as an existing root, by path or by identity
  (device and inode). This catches symlinks and bind mounts (`~` also mounted at `/mnt/home`). It cannot catch the same
  tree mounted back over the network (NFS or SMB to localhost), which has different device and inode numbers.
- **Nesting only across filesystems.** A root may be inside another root (by path or identity) only if the two are on
  different filesystems, for example `~` and a disk mounted at `~/mnt/disk`. A walk never leaves its root's
  filesystem, so no file belongs to two roots.
- Each root records the **mount point it lives under** (`mount_path`): the deepest directory, the root itself or an
  ancestor, on a different device from its parent; NULL when the root is on the filesystem of `/`. A scan is refused
  as **unmounted** if that directory is now on its parent's device, or the root is no longer on it. So
  `/mnt/disk/photos` is protected when `/mnt/disk` is not mounted, even if an empty `photos` folder exists underneath.
- An existing root that cannot be reached when a new one is added (an unmounted disk) is checked by path only.
- Paths are stored relative to their root, with `/` separators. `filo scan [PATH]` maps PATH to the deepest root that
  contains it, by path or by identity (so `/mnt/home/docs` is a partial scan of `~` with scope `docs`). PATH must be a
  directory on that root's filesystem. A PATH under no root is refused with a hint to add it.
- Each scan records its root, its **scope** (a path relative to the root, empty for everything) and the hidden/ignore
  options it ran with.
- A file moved from one root to another shows as new plus missing; pairing across roots is a later improvement. File
  identity is not tied to a root, so that needs no schema change.
- Location history: each file's paths are stored as intervals (`valid_from_scan`, `valid_to_scan`, NULL = current),
  so the tree can be reconstructed at any scan and directory moves can be reverted.

### Scan
- Skips hidden entries by default (`--include-hidden` to override) plus noise: `node_modules`, `__pycache__`, `.filo`,
  user-supplied `--ignore NAME`, and `bin`/`obj` beside a `.csproj` or `.fsproj`.
- **One filesystem.** A walk never descends into a directory on a different device from its root; such directories are
  reported once in the summary, not as errors. A directory already visited in the same walk (same device and inode: a
  bind mount of an ancestor, or a loop) is skipped and reported.
- Symlinks are recorded with their target and a broken flag (target missing, or a loop), never followed. Checking them
  is a later iteration.
- Only regular files and symlinks are recorded. FIFOs, sockets and devices are skipped silently. A file that vanishes
  between the listing and its lstat is simply not seen.
- **A file can only be marked missing if this scan would have visited it.** Entries outside the scan's scope, entries
  excluded by this scan's hidden or ignore rules, entries under a directory that failed to list (even partly), on
  another filesystem or revisited, and entries whose lstat failed are left untouched. The walker answers this from what
  it actually did: a path is covered only if every directory from the scope down to its parent was listed, or one of
  them has gone (or is no longer a directory) inside a listed parent that did not skip it.
  This is what makes a scan of just `~/Pictures` safe after a scan of the whole home directory, and a scan without
  `--include-hidden` safe after one with it.
- **Empty-walk guard, only for roots on a mounted filesystem** (`mount_path` set): if the walk finds no files but the
  register knows files this scan covers, abort and change nothing (the wrong or an empty disk is probably mounted).
  `--allow-empty` records it anyway. For other roots an empty walk is recorded: if the files are gone, they are gone.
- Scans are read-only on user files and safe to run while files are changing. A file caught mid-write appears
  correctly on the next scan.
- Size, mtime, **ctime** and **inode** decide whether a file is rehashed; only new or changed files are hashed. A
  different ctime means the file's metadata changed (permissions, owner, a rewrite that restored its mtime), so it is
  rehashed even when size and mtime match; ctime cannot be set by the user. Where the platform gives no change time
  (Windows), 0 is recorded and ctime is not compared, as with inodes. A different inode
  at the same path means a different file (moved onto the path, or saved by writing a new file and renaming it over
  the old one), even when size and mtime match. Only the inode is stored, never the device number, which can change
  across reboots and remounts; a root never leaves its filesystem anyway. Filesystems whose inode numbers are not
  stable across remounts (FAT/exFAT, some FUSE and network filesystems) make every file a candidate after a remount:
  still correct (the hashes match, so the updates are quiet) but as slow as a first scan. Hashing is parallel with a
  configurable thread count (default 4; use 1 on a spinning disk).
- **Hash: SHA-256** from Go's standard library, stored as lowercase hex (the same text `sha256sum` prints) and
  recorded in `meta.hash_algorithm`; a database with another algorithm is refused. Chosen 2026-11-11 over xxh3-128:
  no dependency, about 2.1 GB/s per core with SHA instructions (measured), so hashing is limited by the disk either
  way, and hashes can be checked by hand.
- Every file is hashed in full. A file is opened without following symlinks (`O_NOFOLLOW`), without blocking on a
  FIFO (`O_NONBLOCK`) and, on Linux, without updating its access time (`O_NOATIME` when Filo owns the file). Only
  regular files are hashed. If size or mtime changes while the file is read, or fewer bytes are read than its size,
  the hash is discarded and the file is retried on the next scan. The stored size and mtime are the ones the file had
  while it was hashed.
- **Unreadable files are flagged, not dropped.** A file that is listed but cannot be opened or read (permission denied,
  I/O error, or any other unexpected error: the flag only ever holds a file back) gets `files.unreadable_since_scan`, the first scan in a row that failed to read it; it stays set across
  later failing scans and is cleared by the first scan that hashes the file again. Its last good hash, size and mtime
  are kept, so its identity and history survive, but the hash is stale while the flag is set. A file that vanished or
  changed while it was read is not unreadable: it is retried as before and the flag is untouched. Losing read access
  changes the file's ctime, so the next scan notices it (see above). Files under a directory that cannot be listed are
  not flagged: Filo cannot tell what happened to them, so they are simply not covered by that scan and their "last
  verified" time stops advancing (see section 3). Decided 2026-11-11.

### Move detection
- Hash match: a missing file and a new file with the same hash are one file that moved (or was renamed). A copy of a
  file that is still present is a new file; copies are separate identities with their own rows.
- Empty files are never matched. Duplicates are never guessed: with several candidates, pair only when the file name is
  unique on both sides, otherwise report as new plus missing.
- Same name, different content (edited then moved?) is reported as a **possible move** and never applied.
- Pairing only happens inside a scan's scope. A file moved in from outside the scope appears as new, and its old path is
  marked missing by the next scan that covers it, so the history link is lost. (Possible improvement: stat the old path
  of same-hash files outside the scope.)
- **Path reuse.** A file whose content changed at a known path may be another file moved onto it: if its new content
  pairs with a file gone in this scan, that file moved there, and the file that was at the path is displaced (it moved
  too if its old content pairs with a new file, otherwise it is missing). If the new content pairs with nothing, it is
  a plain edit. So `a→b, c→a` is two moves, while copying `a` to `a.bak` and then editing `a` stays an edit plus a new
  file.
- **Returned files.** Files that went missing in earlier scans (any root, never empty files) are paired last, against
  new files nothing else claimed, under the same rules. A match is recorded as a move from the file's last path
  ("returned"), so its identity and history survive a disk that was briefly unreadable or a file moved out and back.
  A file moved from one root to another is linked the same way when the old root is scanned first; scanned the other
  way round it is new plus missing.
- **Known limits.** Two files that swap contents show as two changes, not two moves. A file rewritten in place (same
  inode) that keeps both its size and its mtime is noticed only through its ctime, so not on a platform without one
  (Windows). Symlinks have no hash, so a moved symlink is missing
  plus new. Hard links are separate files in Filo (one per path) that happen to share an inode.
- Pairing moves by inode instead of hash (to avoid hashing moved files) is a later optimisation; the inode is only
  used to decide what to rehash.

### Review (iteration 2)
- Export a CSV, review it in a spreadsheet, import it back. A UI replaces the CSV later.
- The CSV holds the **queue only**: files with no review date, files past their review date, possible moves, and
  present files flagged unreadable that have not been reviewed since they were flagged. Not the whole database. Each
  row says why it is queued; an unreadable row also says since when (the scan that flagged it, and its time).
- **Unreadable files** (decided 2026-11-11). A file flagged unreadable joins the queue even if its review date has
  not come, because its stored hash is stale and nothing about it can be checked. Reviewing it while it is still flagged
  takes it out of the queue until its review date; if it is read again and later becomes unreadable again, it rejoins.
  "Reviewed since flagged" compares the review's time with the finish time of the scan that set the flag. The import
  accepts only a review date for a file still flagged at import time: a delete action is refused for that row (one
  clear line naming the file; the other rows are still applied), because its content cannot be checked at action
  time. Disposal refuses flagged files anyway (see Disposal).
- Rows are keyed on the database row ID. The import touches only the decision columns, never path or hash.
- Separate `review_date` and `action` columns. ISO dates, UTF-8, proper quoting.
- The hash at export is stored; it is **re-checked at action time**. A file edited since review goes back to the queue.
- Every reviewed file ends with one of two outcomes: a new review date, or an action (delete). There is no "keep where
  it is" without a date.

### Disposal (iteration 3 onward)
- Dry run first. Quarantine before deletion, for a set period.
- Checks at the moment of acting: retain-until floor passed, no hold, not flagged unreadable, hash unchanged, file not modified in the last few
  minutes, no sync temp file present (`.syncthing.*.tmp`, `*.part`, `.~lock.*`), optionally no process holds it open.
- A history table of retired files (path, hash, date, reason) stays after the file is gone.
- **Syncthing propagates deletes.** Roots that are synced need a flag; quarantine for them must live outside the synced
  folder or deletion must be handled on one device only. Surface `*.sync-conflict-*` files in review.
- Scheduled runs only ever scan. Quarantine and deletion are always manual, after review.

### Commands and scheduling
- Plain names: `scan`, `report`, `review`, `restore`, `revert`, `roots`. No themed subcommands.
- `filo roots` lists roots (mount point, files, size, last scan); `filo roots add PATH` registers one; later
  `filo roots remove PATH`.
- `filo scan [PATH] [--include-hidden] [--ignore NAME]... [--workers N] [--allow-empty]` scans PATH, which must be
  inside a registered root (see Roots); with no PATH, every root in turn, each in its own transaction. A root that
  fails (for example unmounted) is reported and the others are still scanned. `--workers` is 1–64, default 4.
- `filo report [--top N]`: see section 4.
- `--db PATH` is accepted before or after any command; `FILO_DB` is the fallback, then the default location. Flags may
  come before or after PATH. Built on the standard library's `flag` package (no CLI framework).
- Output is plain text. Errors are one line, `filo: <message>`. Exit codes: 0 success (files that could not be read
  inside a completed scan are reported but do not fail it); 1 an error, or any root that failed in a scan of every
  root; 2 a usage error.
- A progress line (walking, hashing with files and bytes, writing) is drawn on stderr only when stderr is a terminal,
  so timer and cron logs stay clean. Ctrl-C or SIGTERM cancels a scan; nothing is written and the lock is released.
- A systemd user timer running `filo scan` is enough. No daemon. (`Persistent=true`, low CPU and I/O priority.)

## 3. Data model (iteration 1, schema version 2)

Carried over from the C# prototype. Adjust freely in the Go port, but keep the interval history and the partial unique
index.

```sql
CREATE TABLE meta  (key TEXT PRIMARY KEY, value TEXT NOT NULL);          -- schema_version, created_utc

CREATE TABLE roots (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,          -- absolute, symlinks resolved
    mount_path TEXT,                    -- the mount point the root lives under; NULL = the filesystem of /
    added_utc TEXT NOT NULL);

CREATE TABLE scans (
    id INTEGER PRIMARY KEY, started_utc TEXT NOT NULL, finished_utc TEXT,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    scope TEXT NOT NULL DEFAULT '',     -- relative to root; '' = the whole root
    include_hidden INTEGER NOT NULL DEFAULT 0, ignores TEXT,
    files_seen INTEGER, new_count INTEGER, changed_count INTEGER, moved_count INTEGER,
    missing_count INTEGER, error_count INTEGER, elapsed_ms INTEGER);

CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,                 -- 'file' | 'symlink'
    size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,   -- Unix nanoseconds
    ctime_ns INTEGER NOT NULL DEFAULT 0,                -- status change time; 0 = not known
    hash TEXT,                          -- NULL for symlinks and files never read; stale while unreadable_since_scan is set
    unreadable_since_scan INTEGER REFERENCES scans(id), -- NULL = read fine the last time it was hashed
    inode INTEGER,                      -- NULL for symlinks, or where the platform gives none
    link_target TEXT, link_broken INTEGER NOT NULL DEFAULT 0,
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id));

CREATE TABLE paths (
    id INTEGER PRIMARY KEY, file_id INTEGER NOT NULL REFERENCES files(id),
    root_id INTEGER NOT NULL REFERENCES roots(id),
    path TEXT NOT NULL,                 -- relative to the root, '/' separators
    valid_from_scan INTEGER NOT NULL REFERENCES scans(id),
    valid_to_scan INTEGER REFERENCES scans(id));       -- NULL = current location
CREATE UNIQUE INDEX ux_paths_current ON paths(root_id, path) WHERE valid_to_scan IS NULL;
CREATE INDEX ix_paths_file ON paths(file_id);
CREATE INDEX ix_files_hash ON files(hash);

CREATE TABLE events (
    id INTEGER PRIMARY KEY, scan_id INTEGER NOT NULL REFERENCES scans(id),
    kind TEXT NOT NULL,                 -- moved | missing | changed | possible_move | error | other_fs
    file_id INTEGER, path TEXT, other_path TEXT, detail TEXT);
```

"Present" means a file has a path interval with `valid_to_scan IS NULL`; there is no status column. "New" means
`first_seen_scan` equals the scan. A file's "last verified" time is derived, not stored: it is the latest completed scan
whose scope covers its path. Disposal (iteration 3) refuses files that no recent scan covers or that are flagged
unreadable, and re-checks the file itself at action time. `ctime_ns` and `unreadable_since_scan` are schema version 2,
the first migration (two `ALTER TABLE ... ADD COLUMN`s; existing rows start with ctime 0, so the first scan after the
upgrade rehashes nothing extra and records each file's ctime quietly). Later iterations add review columns (or a table), holds, and the retired-files history.

Location history gives snapshots without storing them: the tree at any scan is the set of intervals that cover it.
A revert moves each file back to the path it had then, with a dry run that reports and skips conflicts. It reverts
**locations only**; content recovery is the job of backups and quarantine.

## 4. Scan and reconcile

1. Take the lock. Open or create the database. Map the target to its root and scope, and check the root is mounted.
   Load the root's current known entries (current path to file row).
2. Walk the scope. Record size and mtime from the directory listing. Note directories that failed to list. Apply the
   empty-walk guard.
3. Classify by path: new; same path with matching size, mtime, ctime and inode, a hash on record and no unreadable
   flag (unchanged, nothing written; a recorded ctime of 0 is filled in quietly); same path with a different size,
   mtime, ctime or inode, no hash, or the unreadable flag set (candidate: rehash). Symlinks compare target and broken flag. A
   path whose kind changed (file and symlink) counts as the old one gone plus a new one.
4. Hash only what needs it (new files and candidates). A candidate that cannot be hashed (unreadable, or changed while
   it was read) is left untouched and reported as an error, so the next scan tries again; if it could not be opened or
   read, its `unreadable_since_scan` is set unless already set. A new file that cannot be hashed is recorded without a
   hash, and flagged unreadable if it could not be opened or read. A successful hash clears the flag.
5. Same path, same hash: only the mtime moved, so update it quietly. A first hash for a file that never had one is
   also quiet. Same path, different hash: changed, unless step 7 finds another file moved onto the path.
6. Known but not seen, and **covered by this walk** (see Scan): missing.
7. Pair by hash (rules above), in three passes: files gone in this scan against new files and changed files' new
   content; files displaced by that against the new files left; files gone in earlier scans against the new files
   still left. Report same-name leftovers as possible moves.
8. Apply in one transaction: close paths of missing and moved files **first**, so a path freed by one rename can be
   reused by another in the same scan; then updates; then open new paths and insert new files; then events.
9. Print a summary: counts plus samples of moved, missing, changed, possible moves and errors.

`filo report` shows each root's totals (present files, size, symlinks) and last scan, the largest files, duplicate
groups with reclaimable space, broken symlinks, unreadable files (path and the scan they became unreadable in) and the
most recent events (`--top N` per list, default 10). A
**duplicate group** is present regular files (size > 0, with a hash) sharing a hash, across all roots; each copy keeps
its own identity. Reclaimable space is size × (copies − 1) per group. Paths are shown absolute.

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

- Holds and asset links: modelled at project or asset level, not per file. Shape not yet decided.
- Where a retain-until floor comes from (frontmatter, Office or PDF properties, extended attributes, or entered during
  review). The register is the source of truth either way.
- Default review intervals (for example 30 days, 12 months, 7 years) as suggestions at review time.
- Exact CSV columns, and how a synced-folder flag is configured.
- UI technology.

## 7. Rejected or deferred

- **Partial hashing of very large files** (size plus first and last megabyte, full hash on demand). Deferred
  2026-11-11: every file is hashed in full, and only rehashed when its size or mtime changes, so the cost falls on
  the first scan. Revisit if first scans of large media are too slow; it would need a "partial" marker per hash and a
  full hash before any disposal.
- **Tier directories** (bronze, silver, gold, platinum). A good workflow, but moving files breaks links (recent-file
  lists), scatters one subject across tiers, and ties lifetime to location. Kept as an idea: tiers become default
  review intervals.
- **Review date stored inside each file.** Fragile: many editors drop extended attributes on save, and some formats
  cannot carry properties. Losing it fails safe but causes churn, and it cannot hold floors reliably.
- **A daemon.** A scheduled scan does the job; a daemon adds state, file watching limits and failure modes. Revisit only
  for near-real-time change detection.
- **Device tracking in the database.** Unneeded while the database is per device and never synced.
- **One database per root** (the first Go design, with the database in `<root>/.filo`). Several roots on one device
  meant several registers to manage; one database with a `roots` table replaced it on 2026-11-11.
- **Themed command and project names** (the earlier "dirge" idea, funeral-style subcommands). Plain names win.
- **Building search.** Existing indexers and document managers (Paperless-ngx for received documents, desktop
  indexers) already cover finding files. Filo owns lifecycle.
