# Filo: design

Status: iteration 1 (scan, register, report) is done: it passes its test-tree criterion, and on 2026-11-11 a real home
directory (5,582 files, about 3.7 GB) scanned completely and rescanned quickly, reporting only real changes. Iteration
2 was reshaped on 2026-11-12 from a review queue into one keep-until date per file (the lifespan CSV, `filo export
lifespan` and `filo update`), with root settings stored on the root; it passes its test-tree criterion, and setting
dates on the real home directory is still to be done. A C# prototype exists as a reference spec only; its schema and
reconcile rules were carried over. Last updated: 2026-11-12.

This file records settled decisions, the planned data model and the open questions. When a decision changes,
change it here in the same commit.

## 1. Problem and principles

Files go into storage and never leave. Notes, documents and received PDFs pile up, which makes backups bigger and
finding things harder, feeds a hoarding habit ("keep it in case someone asks"), and creates demand for the person
who can find any old file. Filo is a register and review workflow that makes disposal a normal, safe, routine step.

Principles. These are not negotiable:

1. **A keep-until date is a floor, not a trigger.** Reaching it makes a file a deletion candidate that the person
   confirms, one by one; nothing is ever deleted because a date passed. (Until 2026-11-12 there were two dates, a
   review date and a retain-until floor; since every deletion goes through a person anyway, they became one.)
2. **Holds override everything.** Legal, tax and dispute holds veto any disposal and are checked last, at the moment
   of disposal, not when the date was set.
3. **Disposal is staged and reversible:** review, then quarantine, then (much later) deletion.
4. **Fail safe.** A file that loses its metadata, or whose state is ambiguous, becomes "new, needs review", never
   "eligible for deletion".
5. **Record only what is known.** At creation that is at most a keep-until date and a link to an asset or project.
   Never invent one: a file without a date needs a decision.
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
- `meta.schema_version` must equal the build's version. An older database is upgraded when it is opened, one version
  at a time, each step in its own transaction with the version bump (1 to 2, 2 to 3, then 3 to 4); a newer version, or an older
  one with no migration, is refused, as is a SQLite file without Filo's `meta` keys. WAL mode is set only after those checks pass, so
  a foreign database is never modified.
- One writer at a time: an `flock` on `<db>.lock`, taken before the database is opened. The kernel drops it if Filo
  dies, so there are no stale locks. Non-unix platforms refuse to open until locking is implemented for them. Each
  scan is applied in a single transaction.
- Filo creates a new database file with mode 0600; SQLite gives the WAL and SHM files the same mode. An existing
  file's mode is never changed.
- The database, its WAL/SHM/journal files and the lock file are never scanned, wherever they are.

### Roots
- Roots are registered explicitly and deliberately (`filo roots add DIR`), and stored in the `roots` table as absolute
  paths with symlinks resolved. `filo roots` lists them; `filo roots remove` (later, with a dry run) removes a root's
  register rows, never files. Considered on 2026-11-12: letting `filo scan DIR` add DIR as a root when it is in none.
  Rejected, because a root carries settings that every later scan relies on, so setting one up should be a
  deliberate act; a scan of a directory under no root is refused with a hint to add it.
- **A root owns its scan settings** (decided 2026-11-12): whether hidden entries are scanned (`include_hidden`) and the
  extra names to ignore (`ignores`). They are given on `filo roots add DIR [--include-hidden] [--ignore NAME]...` and
  replaced as a whole by `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...`. Every scan of
  the root, whole or partial, uses them, so a timer's `filo scan` and a manual scan always cover the same files.
  (Until then they were flags on each scan, and a scan without `--include-hidden` quietly left hidden files
  unchecked.)
- `filo roots add` scans the new root straight away; `--no-scan` only registers it (for example to run the long first
  scan later). Ctrl-C during that scan keeps the root and writes nothing from the scan.
- **No aliases.** A new root is refused if it is the same directory as an existing root, by path or by identity
  (device and inode). This catches symlinks and bind mounts (`~` also mounted at `/mnt/home`). It cannot catch the same
  tree mounted back over the network (NFS or SMB to localhost), which has different device and inode numbers.
- **Nesting only across filesystems.** A root may be inside another root (by path or identity) only if the two are on
  different filesystems, for example `~` and a disk mounted at `~/mnt/disk`. A walk never leaves its root's
  filesystem, so no file belongs to two roots.
- **A new root absorbs the roots inside it** (decided 2026-11-11, automatically: no flag, no prompt). Adding `~` when
  `~/Documents` and `~/Pictures` are roots on the same filesystem merges them into `~` in the same transaction: their
  paths get the folder as a prefix (`a.txt` becomes `Documents/a.txt`), their scans become scans of `~` scoped to
  that folder, their event paths are prefixed (except a returned file's `other_path`, which is relative to the root
  named in the event's detail), and the old root rows are deleted. Files keep their IDs, so history and keep-until
  dates are kept, and the first scan of `~` finds those files unchanged. Nothing on disk changes. The new root's
  settings apply to everything it absorbs. `roots add` says which roots it absorbed, notes any whose settings differed,
  and any that the new root's settings skip (a hidden or ignored folder: their files are then neither verified nor
  marked missing until `filo roots set` includes them). Refused instead: absorbing a
  root that cannot be checked now (not reachable or its disk not mounted), since Filo cannot tell whether it is on the
  same filesystem; and the reverse, a directory inside an existing root on the same filesystem, which is already
  covered (the message suggests `filo scan` on it). Roots inside the new one on other filesystems stay roots.
- Each root records the **mount point it lives under** (`mount_path`): the deepest directory, the root itself or an
  ancestor, on a different device from its parent; NULL when the root is on the filesystem of `/`. A scan is refused
  as **unmounted** if that directory is now on its parent's device, or the root is no longer on it. So
  `/mnt/disk/photos` is protected when `/mnt/disk` is not mounted, even if an empty `photos` folder exists underneath.
- An existing root that cannot be reached when a new one is added (an unmounted disk) is checked by path only.
- Paths are stored relative to their root, with `/` separators. `filo scan [DIR]` maps DIR to the deepest root that
  contains it, by path or by identity (so `/mnt/home/docs` is a partial scan of `~` with scope `docs`). DIR must be a
  directory on that root's filesystem. A DIR under no root is refused with a hint to add it.
- Each scan records its root, its **scope** (a path relative to the root, empty for everything) and the hidden/ignore
  settings it ran with (the root's at the time).
- A file moved from one root to another shows as new plus missing; pairing across roots is a later improvement. File
  identity is not tied to a root, so that needs no schema change.
- Location history: each file's paths are stored as intervals (`valid_from_scan`, `valid_to_scan`, NULL = current),
  so the tree can be reconstructed at any scan and directory moves can be reverted.

### Scan
- Skips hidden entries unless the root includes them, plus noise: `node_modules`, `__pycache__`, `.filo`, the root's
  ignore names, and `bin`/`obj` beside a `.csproj` or `.fsproj`.
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
  This is what makes a scan of just `~/Pictures` safe after a scan of the whole home directory, and a scan after
  `filo roots set` stops including hidden files safe after one that included them.
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

### Lifespan (iteration 2)
Decided 2026-11-12, replacing the review queue, review dates, actions and retain-until floors of 2026-11-11. The
lifespan CSV and `filo update` write only the register and the CSV file the user names; they never touch the files.

- **Forever** (decided 2026-11-12): `keep_until` may be the word `forever` instead of a date, for files kept
  indefinitely. It is stored as that word, never as a far-off date such as 9999-12-31 (which would look like a
  chosen date in the CSV, sort among real dates, and need special-casing everywhere), and checked explicitly, not by
  comparing it with dates. A `forever` file is never a deletion candidate, however old it is or however often it
  changes; `filo report` counts them so the pile stays visible, and typing a date over it later is how it ends. One
  word only, in any case (`Forever`): no aliases such as `indefinitely` or `never`. No schema change: the column is
  text already.
- **One date per file: `keep_until`.** Keep the file until then. On that date it becomes a deletion candidate, never
  deleted automatically: `filo delete` lists the candidates, and deleting each is the person's call (iteration 3). A
  recurring check on a file (an annual look at a seven-year record) is a task, a later iteration; this iteration
  tracks lifespan only. Hard legal retention may become a hold with an end date (see Open questions).
- **Status of a present file** (regular files and symlinks), the first that applies:
  1. *unreadable*: flagged unreadable and its date not set since the scan that flagged it; or flagged and past its
     date (its content cannot be checked);
  2. *possible move*: the new side of a possible move recorded after its date was last set. The event names the old
     file; the new one is the file whose path opened at the event's path in that scan;
  3. *undated*: no `keep_until`;
  4. *forever*: `keep_until` is `forever`;
  5. *kept*: `keep_until` after today (local date);
  6. *changed*: past its date, but a regular file whose hash is not the one the date was set on (or the date was set
     without a hash, or it has none now). A change before the date does not matter; at the date it needs a new one;
  7. *expired*: past its date and unchanged: a deletion candidate.
  *Unreadable*, *possible move*, *undated* and *changed* need a decision; `filo report` counts them and shows the directories
  holding most of them.
- **"Since" is measured in scans**, not clock times. Each export records the latest finished scan
  (`exports.after_scan`); a date set from it is stamped with that scan (`files.reviewed_after_scan`), the content it
  was exported with (`reviewed_hash`) and when (`reviewed_utc`). A file flagged unreadable, or recorded as a possible
  move, in a scan after the export keeps that status after the update.
- **The CSV has one purpose**: take the files into a spreadsheet, filter, sort, edit `keep_until`, update back. So it
  holds **every present file** under DIR (or every root), with its current date, not a queue: dated files are
  reviewed too, and filtering on a blank `keep_until` gives the undated ones. Missing and moved files are not in it;
  they are in `filo report`. On a large system, export one directory at a time so a spreadsheet can open it.
- **Columns**, in order: `id` (file row ID), `export` (export number), `directory` (absolute), `name`, `size`
  (bytes), `modified` (local time; a cue to when the file was last worked on), `keep_until`. The name is a column of
  its own because file names often carry dates. Left out on purpose: a reason (one purpose only), an action (a
  delete column in a spreadsheet is unsafe: a one-column sort or a fill-down puts it on the wrong rows), and creation
  or first-seen dates (not reliable: many filesystems do not record a birth time, and a copy, restore or sync resets
  it). `filo update` finds columns by header name and reads only `id`, `export` and `keep_until`, so a spreadsheet may
  reorder columns or add its own. Rows are keyed on (export, id). Each export's rows, hashes and dates are stored in
  the register (`export_rows`).
- **Encoding.** UTF-8 with a byte order mark (so spreadsheets detect UTF-8), standard quoting. File names are written
  as they are, emoji and all; bytes that are not valid UTF-8 are shown as U+FFFD, which is harmless because the update
  never reads the name. A display cell that starts with `= + - @`, tab or CR gets a leading `'` so a spreadsheet does
  not run it as a formula. The update accepts a byte order mark, CRLF, `;` or tab separators, and empty rows.
- **Dates**: `YYYY-MM-DD` only, and a real date (`2027-02-29` is refused), or `forever`. Anything else, such as
  `06/07/2027` or `+1y`, is refused, never guessed; the message suggests formatting the column as text, since spreadsheets may
  rewrite dates in the locale's format. A new date must be after today. Durations (`+1y`) were dropped from the CSV on
  2026-11-12: a duration is relative to the day of the update, so the same cell meant different dates on different
  days. Whether commands take durations is open.
- **`-o FILE` is required**: the CSV is never written to the terminal. FILE is created with mode 0600 and never
  overwritten.
- **What the update does with each row**:
  - the cell as exported (the date it was exported with, or blank): nothing, whatever happened since;
  - the date already stored: nothing;
  - a new date: replaces the stored one. Editing the same CSV again and updating again replaces the date again (the
    old "already imported" rule, which silently ignored a re-edited row, is gone);
  - blank where a date is stored: clears it, so the file is undated again. Before clearing, the update lists those
    files (the first ten, then a count) and asks once, `Clear the keep_until date of N files? [y/n]`. Anything but
    `y` or `yes`, or no terminal on standard input, leaves those rows alone, and the summary says how many and why.
    `--dry-run` counts them without asking.
- **The update refuses a row**, with one line naming the CSV line and the file, and still applies the others, when:
  a value does not parse, or is typed past the last column; the export is not in the register, or the file is not in
  that export (a row added in the spreadsheet); the file is no longer present; the file's hash differs from the
  export's (changed since the export); its date was changed from a later export since (an old CSV never overwrites a
  newer decision); the date is not after today; the same (export, id) appears more than once (every copy is
  refused). A deleted row changes nothing. The applied rows go in one transaction; `--dry-run` checks everything and
  writes nothing. The exit code is 1 if any row was refused. A CSV without a `keep_until` header is refused as a
  whole, with a pointer to `filo export lifespan` if it is an old review CSV.
- **Bulk dates**: set in the spreadsheet (filter on `directory`, fill down). `filo review set-date` was dropped on
  2026-11-12, at least for now.
- The hash at export is stored; it is **re-checked at action time** (iteration 3) against `files.reviewed_hash`.

### Disposal (iteration 3 onward)
- Dry run first. Quarantine before deletion, for a set period.
- `filo delete` lists the candidates (expired files). In iteration 3, run in a terminal, it then asks
  `Delete [a]ll, [s]elect or [n]one?`; *select* goes file by file (`[d]elete, [k]eep until…, [s]kip, [q]uit`). Without
  a terminal it only lists. In iteration 2 it only lists.
- Checks at the moment of acting: keep-until passed, no hold, not flagged unreadable, hash unchanged, file not modified in the last few
  minutes, no sync temp file present (`.syncthing.*.tmp`, `*.part`, `.~lock.*`), optionally no process holds it open.
- A history table of retired files (path, hash, date, reason) stays after the file is gone.
- **Syncthing propagates deletes.** Roots that are synced need a flag; quarantine for them must live outside the synced
  folder or deletion must be handled on one device only. Surface `*.sync-conflict-*` files in review.
- Scheduled runs only ever scan. Quarantine and deletion are always manual, after review.

### Commands and scheduling
- Plain names, easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete`, later `restore` and
  `revert`. No themed subcommands. (Reshaped 2026-11-12: `review` and its subcommands `set-date`, `export` and
  `import` were replaced by `export lifespan` and `update`; "update" says that only existing files change, which
  "import" did not.)
- **Keep flags and options to a minimum** (2026-11-11). Prefer a safe default and clear output over a flag; prefer
  doing the obvious thing and saying what was done over asking.
- **Prompts only in a terminal** (2026-11-12, replacing "no interactive prompts"). A command asks only where a person
  should look first: `filo update` before clearing dates, and in iteration 3 `filo delete`. Declining, or having no
  terminal on standard input, is always the safe choice, so Filo still works the same from a script or a timer.
- `filo roots` lists roots (settings, mount point, files, size, last scan); `filo roots add DIR [--include-hidden]
  [--ignore NAME]... [--no-scan]` registers one and scans it (absorbing any roots inside it on the same filesystem,
  see Roots); `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...` replaces its settings; later
  `filo roots remove DIR`.
- `filo scan [DIR] [--workers N] [--allow-empty]` scans DIR, which must be inside a registered root (see Roots), with
  the root's settings; with no DIR, every root in turn, each in its own transaction. A root that fails (for example
  unmounted) is reported and the others are still scanned. `--workers` is 1–64, default 4.
- `filo report [--top N]`: see section 4.
- `filo export lifespan [DIR] -o FILE` and `filo update FILE [--dry-run]`: see Lifespan. The word after `export`
  names the CSV, so a later CSV is a new word, not a new command; `update` tells the kind from the CSV's header.
- `filo delete`: lists the files past their keep-until date (see Disposal).
- `--db PATH` is accepted before or after any command; `FILO_DB` is the fallback, then the default location. Flags may
  come before or after PATH. Built on the standard library's `flag` package (no CLI framework).
- **A path that starts with `~` is refused** (2026-11-11): every DIR and FILE, `-o`, `--db` and `FILO_DB`.
  The shell expands `~` only when it is unquoted, so `"~/Documents"` would otherwise name a folder called `~` in the
  current directory (and `--db "~/f.db"` would create one). Filo does not expand it itself, because a name starting
  with `~` is legal. The one-line error suggests `$HOME/...` (or the full path for `~user`), and `./~...` for a name
  that really starts with `~`. Checked before the database is opened, so nothing is created.
- Output is plain text. Errors are one line, `filo: <message>`. Exit codes: 0 success (files that could not be read
  inside a completed scan are reported but do not fail it); 1 an error, or any root that failed in a scan of every
  root; 2 a usage error.
- A progress line (walking, hashing with files and bytes, writing) is drawn on stderr only when stderr is a terminal,
  so timer and cron logs stay clean. Ctrl-C or SIGTERM cancels a scan; nothing is written and the lock is released.
- A systemd user timer running `filo scan` is enough. No daemon. (`Persistent=true`, low CPU and I/O priority.)

## 3. Data model (schema version 4)

Carried over from the C# prototype. Adjust freely in the Go port, but keep the interval history and the partial unique
index.

```sql
CREATE TABLE meta  (key TEXT PRIMARY KEY, value TEXT NOT NULL);          -- schema_version, created_utc

CREATE TABLE roots (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,          -- absolute, symlinks resolved
    mount_path TEXT,                    -- the mount point the root lives under; NULL = the filesystem of /
    added_utc TEXT NOT NULL,
    include_hidden INTEGER NOT NULL DEFAULT 0,          -- version 4: the root's scan settings
    ignores TEXT NOT NULL DEFAULT '[]');                -- JSON array of names skipped at any depth

CREATE TABLE scans (
    id INTEGER PRIMARY KEY, started_utc TEXT NOT NULL, finished_utc TEXT,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    scope TEXT NOT NULL DEFAULT '',     -- relative to root; '' = the whole root
    include_hidden INTEGER NOT NULL DEFAULT 0, ignores TEXT,  -- the root's settings when it ran
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
    first_seen_scan INTEGER NOT NULL REFERENCES scans(id),
    -- the current lifespan (version 4); the history is in reviews
    keep_until TEXT,                    -- YYYY-MM-DD or 'forever'; NULL = undated, needs a decision
    reviewed_utc TEXT,                  -- when keep_until was last set by an update; NULL = never
    reviewed_after_scan INTEGER REFERENCES scans(id), -- the latest scan that update's export had seen
    reviewed_hash TEXT);                -- the content the date was set on

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

-- versions 3 and 4: exports and the history of keep_until dates
CREATE TABLE exports (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    path TEXT,                          -- the CSV written (NULL only for exports to standard output before version 4)
    after_scan INTEGER REFERENCES scans(id));          -- the latest finished scan at export
CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id), file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,                          -- the content shown; the update refuses the row if it has changed
    keep_until TEXT,                    -- version 4: the date exported; a cell left as this is untouched
    PRIMARY KEY (export_id, file_id));
CREATE TABLE review_batches (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    command TEXT NOT NULL,              -- 'update' ('set-date' in databases from before version 4)
    detail TEXT);                       -- the CSV read
CREATE TABLE reviews (                  -- append-only: every keep_until ever set or cleared
    id INTEGER PRIMARY KEY, batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    file_id INTEGER NOT NULL REFERENCES files(id), export_id INTEGER REFERENCES exports(id),
    keep_until TEXT,                    -- NULL = cleared
    hash TEXT);
CREATE INDEX ix_reviews_file ON reviews(file_id);
```

"Present" means a file has a path interval with `valid_to_scan IS NULL`; there is no status column. "New" means
`first_seen_scan` equals the scan. A file's "last verified" time is derived, not stored: it is the latest completed scan
whose scope covers its path. Disposal (iteration 3) refuses files that no recent scan covers or that are flagged
unreadable, and re-checks the file itself at action time. `ctime_ns` and `unreadable_since_scan` are schema version 2,
the first migration (two `ALTER TABLE ... ADD COLUMN`s; existing rows start with ctime 0, so the first scan after the
upgrade rehashes nothing extra and records each file's ctime quietly). Version 3 adds the review columns on `files`
(all NULL for existing rows, so every file starts undated) and the four review tables. Version 4 (2026-11-12) merges
`review_date` and `retain_until` into `keep_until`, taking the later of the two so nothing is kept for less time; a
recorded `delete` becomes `keep_until` = the day it was decided, so the file is a deletion candidate; the same in
`reviews`; `export_rows` loses `reason` and gains `keep_until`; `import` batches are renamed `update`; roots gain
their settings (defaults for existing roots: hidden skipped, no extra ignores). Columns are dropped with `ALTER TABLE
... DROP COLUMN`. Later iterations add holds and the retired-files history.

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

`filo report` shows each root's totals (present files, size, symlinks) and last scan, the files needing a keep-until
date by status and the directories (two levels below the root) holding most of them, how many are kept and how many
are past their date, the largest files, duplicate
groups with reclaimable space, broken symlinks, unreadable files (path and the scan they became unreadable in) and the
most recent events (`--top N` per list, default 10). A
**duplicate group** is present regular files (size > 0, with a hash) sharing a hash, across all roots; each copy keeps
its own identity. Reclaimable space is size × (copies − 1) per group. Paths are shown absolute.

## 5. Iterations

1. **Scan, register, report** (read-only). Done 2026-11-11. Done when: on a generated test tree, every kind of change (rename, move,
   edit, delete, duplicate, symlink, unreadable directory, unreadable file) is reported exactly on rescan
   (`scripts/testtree.sh`, checked by the walk and reconcile tests); a first scan of the real home
   directory completes and shows the size of the backlog; a second scan takes seconds to a couple of minutes.
2. **Lifespan: one keep-until date per file**, set through the lifespan CSV (reshaped 2026-11-12 from a review
   queue). Done when: on a test tree, an export, edit and update round trip records exactly the dates set, refuses
   with one clear line each a changed file, an impossible or ambiguous date, a stale export and an added row; a
   cleared date asks first; a file past its date is listed by `filo delete`, and once edited needs a new date instead
   (`TestLifespanRoundTrip` in `internal/cli`, plus the store and lifespan package tests); on the real home directory,
   every file gets a date or `forever`, a directory at a time (filter on `directory`, fill down), through a few
   directory-sized exports.
3. **Quarantine and delete** with the safety checks above, history table, holds.
4. **Revert directory moves** from the path history.
5. **UI** in place of the CSV, including viewing and editing the register in place (see Open questions).

Long term: other properties (type, author, sender) where they can be derived automatically, tags, projects and tasks
linked to files, and a daily view of the files needed. All of it hangs off the stable file ID, so none of it needs the
register to change shape.

## 6. Open questions

- Holds and asset links: modelled at project or asset level, not per file. Shape not yet decided. A hold with an end
  date may be how hard retention ("keep for seven years, by law") is expressed, now that there is no separate floor.
- Reading a keep-until date from the file itself (frontmatter, Office or PDF properties, extended attributes).
  For now it is set in the CSV; the register is the source of truth either way.
- Whether commands that take a date (the `[k]eep until…` choice in `filo delete`, a future bulk command) accept
  durations such as `+1y`. The CSV takes dates only.
- How a synced-folder flag is configured (needed for disposal, iteration 3).
- Confirming a possible move, and undoing an update batch.
- **Viewing and editing the register in place** (raised 2026-11-12). Setting or clearing `keep_until` on a few files
  through a whole export, edit and update round is cumbersome. Wanted: query and view what the register holds (files,
  dates, history) and set or clear a date on a few files directly. Open: a command (for example
  `filo keep PATH... DATE`, which reopens whether commands take durations such as `+1y`), part of the UI (iteration
  5), or both.
- **Folder rules for new files.** Setting dates in the CSV is a one-off: it dates the files under a folder that exist
  now, and a file added later arrives undated and needs a decision. A stored rule ("new files under FOLDER get a review
  date N from first seen") applied by each scan would save rerunning it, but it relaxes principle 4's default that a
  new file needs review. Open: whether to have rules at all, whether a rule-dated file still gets one look first,
  how rules on nested folders combine, and whether a rule may ever imply delete (it should not).
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
