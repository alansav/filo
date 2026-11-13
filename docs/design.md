# Filo: design

Status: iteration 1 (scan, register, report) is done: it passes its test-tree criterion, and on 2026-11-11 a real home
directory (5,582 files, about 3.7 GB) scanned completely and rescanned quickly, reporting only real changes. Iteration
2 was reshaped on 2026-11-12 from a review queue into one keep-until date per file (the retention CSV, `filo export
retention` and `filo update`), with root settings stored on the root; it passes its test-tree criterion, and setting
dates on the real home directory is still to be done. A C# prototype exists as a reference spec only; its schema and
reconcile rules were carried over. The second shape of the retention CSV (`file_date`, `retention`, schema version
5) is implemented. Iteration 3 (deletion) was designed on 2026-11-12 and implemented on 2026-11-13 (schema version
6); it passes its test-tree criterion and has not yet been run on real files. Register snapshots (rolling copies of
the database next to it) were added on 2026-11-13. The order of the next work was agreed the same day (section 5).
Last updated:
2026-11-13.

This file records settled decisions, the planned data model and the open questions. When a decision changes,
change it here in the same commit.

## 1. Problem and principles

Files go into storage and never leave. Notes, documents and received PDFs pile up, which makes backups bigger and
finding things harder, feeds a hoarding habit ("keep it in case someone asks"), and creates demand for the person
who can find any old file. Filo is a register and review workflow that makes disposal a normal, safe, routine step.

Principles. These are not negotiable:

1. **A keep-until date is not a trigger.** Reaching it makes a file a deletion candidate; nothing is ever deleted
   because a date passed. Deleting is a command the person runs (`filo delete`), after seeing the list and confirming
   it, or by passing `-y`. Filo never schedules deletion itself. (Until 2026-11-12 there were two dates, a review date
   and a retain-until floor; since every deletion goes through a person anyway, they became one.)
2. **Deletion is permanent; backups make it recoverable** (decided 2026-11-12, replacing "disposal is staged and
   reversible": review, quarantine, then deletion). Filo deletes files outright, without a quarantine or the recycle
   bin. What stays: the person sees and confirms the list, and each file is re-read and re-hashed at the moment of
   deleting and deleted only if it is still exactly what its retention was set on. Holds were dropped the same day: a
   file that must be kept gets a later retention or `forever`.
3. **Fail safe.** A file that loses its metadata, or whose state is ambiguous, becomes "new, needs a decision", never
   "eligible for deletion". A file that cannot be read is never deleted.
4. **Record only what is known.** At creation that is at most a keep-until date and a link to an asset or project.
   Never invent one: a file without a date needs a decision.
5. **Nothing destructive before its iteration.** A destructive command shows what it will do and asks first, or needs
   `-y`.
6. **Never guess.** Duplicates and ambiguous moves are reported, not resolved.

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
  never scanned. That is also why there is no device column. Sharing retention decisions between devices is an open
  question; the register itself stays per device (see Open questions).
- Driver: `modernc.org/sqlite` (pure Go, no CGO, plain `database/sql`), chosen 2026-11-11 at v1.62.0.
- `meta.schema_version` must equal the build's version. An older database is upgraded when it is opened, one version
  at a time, each step in its own transaction with the version bump (1 to 2, 2 to 3, then 3 to 4); a newer version, or an older
  one with no migration, is refused, as is a SQLite file without Filo's `meta` keys. WAL mode is set only after those checks pass, so
  a foreign database is never modified.
- One writer at a time: an `flock` on `<db>.lock`, taken before the database is opened. The kernel drops it if Filo
  dies, so there are no stale locks. Non-unix platforms refuse to open until locking is implemented for them. Each
  scan is applied in a single transaction. The lock file is opened with `O_NOFOLLOW`, so a symlink planted in its
  place (with `--db` in a shared directory such as `/tmp`) is refused, not followed.
- **The schema is not trusted** (2026-11-13): `PRAGMA trusted_schema = OFF` on every connection, so triggers and
  views stored in an altered or swapped database cannot call functions with side effects.
- Filo creates a new database file with mode 0600; SQLite gives the WAL and SHM files the same mode. An existing
  file's mode is never changed.
- The database, its WAL/SHM/journal files, the lock file and the snapshot directory are never scanned, wherever they
  are.

### Register snapshots
Decided and implemented 2026-11-13. After a run of weekend dating the register holds thousands of decisions that
exist nowhere else, and on the laptop it is outside every backup (Vorta runs on the desktop).

- **What.** A consistent copy of the database, written with SQLite's `VACUUM INTO` into `<db>.snapshots/` next to
  it (`~/.local/share/filo/filo.db.snapshots/`). `VACUUM INTO` writes one self-contained, compacted file from a read
  transaction; copying `filo.db` instead could miss commits still in the WAL file or catch a page being written. It
  is a copy, not a compaction of the live database.
- **When.** At the end of every run that committed a change to the register, in `Store.Close`, while the lock is
  held; so no list of writing commands is kept. Every write in the store goes through two helpers (`commit` and
  `exec`) that note the change only once it is committed (a test guards that rule), plus creating or upgrading the
  database. A run that only read takes none, and nor does one whose write was rolled back (a failed or interrupted
  scan; `total_changes()`, used at first, also counted those). An interrupted run that committed something takes
  one.
- **Before a schema upgrade**, a copy of the old database is kept as `filo-vN-before-upgrade-<time>Z.db`, outside
  the rotation, one per old version: an upgrade that keeps failing (a timer opening an old database every hour)
  does not pile up copies. If that copy cannot be written, the upgrade is refused.
- **Files.** `filo-YYYYMMDDTHHMMSS.mmmZ.db` (UTC); the time in the name, not the file's mtime, drives the rotation,
  so a copy or sync cannot disturb it. The temporary file is created empty with mode 0600 before `VACUUM INTO`
  fills it (otherwise SQLite creates it with the umask's mode and the register is readable while it is written),
  flushed to disk (`VACUUM INTO` does not), renamed into place, never over an existing file, and the directory is
  flushed after the rename; so a power cut cannot leave a torn newest snapshot once older ones are pruned. Leftover
  temporary files of either kind are removed by the next snapshot. Directory 0700 (an existing one is made private),
  files 0600: the register names every file. A snapshot directory that is a symlink, not a directory, or owned by
  another user is refused (hardening of 2026-11-13).
- **Rotation** (reworked 2026-11-13 from a first draft that kept only the newest three of the day, which was fragile
  around midnight). Snapshots are a short-term safety net; long-term copies are the backups of them. Kept: every
  snapshot from the last 24 hours (rolling, so working past midnight loses nothing); the newest three whatever their
  age (so a write after a month away keeps the state just before it); and, up to 31 days old, the newest of each of
  the seven most recent days with older snapshots, then the newest of each ISO week. Nothing else older than 31 days.
  Periods count only days that have snapshots. Pruning runs only after a new snapshot was written, and removes only
  files whose names are rotated snapshots; pre-upgrade copies and anything else in the directory are left alone. A
  snapshot dated more than a minute in the future (the clock was ahead) is kept but takes none of the newest places,
  and the report flags it.
- **Failure** never undoes the run's work: one line on stderr, `filo: warning: register snapshot failed: …`.
  `filo report` ends with a Register snapshots section (count, directory, latest), and warns when there are none
  though the register has changed, when the latest is older than the register's latest write, or when the
  directory cannot be read. The latest write is recorded in `meta.last_write_utc` by every run that writes, just
  before its snapshot and with the same time, so any change counts (a root's settings too). That is how a failing
  snapshot is noticed when scans run from a timer.
- **Restoring** is by hand: with no filo running, move `filo.db`, `filo.db-wal` and `filo.db-shm` aside and copy a
  snapshot to `filo.db`. A command can come later if wanted.
- **Getting them off the machine** (to the desktop, where Vorta backs them up) is part of the synced-roots work: for
  example a Syncthing share of the snapshot directory, laptop to desktop only. A snapshot is a finished file that
  nothing opens for writing, so unlike the live database it is safe to sync.

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
retention CSV and `filo update` write only the register and the CSV file the user names; they never touch the files.

- **Forever** (decided 2026-11-12): the retention (and so `keep_until`) may be the word `forever` instead of a date, for files kept
  indefinitely. It is stored as that word, never as a far-off date such as 9999-12-31 (which would look like a
  chosen date in the CSV, sort among real dates, and need special-casing everywhere), and checked explicitly, not by
  comparing it with dates. A `forever` file is never a deletion candidate, however old it is or however often it
  changes; `filo report` counts them so the pile stays visible, and typing a date over it later is how it ends. One
  word only, in any case (`Forever`): no aliases such as `indefinitely` or `never`.
- **One date per file: `keep_until`**, worked out from the file's retention (see second shape). Keep the file until
  then. On that date it becomes a deletion candidate, never
  deleted automatically: `filo delete` lists the candidates, and deleting each is the person's call (see Disposal). A
  recurring check on a file (an annual look at a seven-year record) is a task, a later iteration; this iteration
  tracks lifespan only. Hard legal retention is a later retention or `forever` (holds were dropped on 2026-11-12).
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
- **The CSV has one purpose**: take the files into a spreadsheet, filter, sort, edit the retention, update back. So it
  holds **every present file** under DIR (or every root), with its current date, not a queue: dated files are
  reviewed too, and filtering on a blank `retention` gives the undated ones. Missing and moved files are not in it;
  they are in `filo report`. On a large system, export one directory at a time so a spreadsheet can open it.
- **Columns**, in order: `id` (file row ID), `export` (export number), `directory` (absolute), `name`, `size`
  (bytes), `modified` (local time; a cue to when the file was last worked on), `file_date`, `retention`, `keep_until`
  (see Retention CSV, second shape). The name is a column of its own because file names often carry dates. Left out on purpose: a reason (one purpose only), an action (a
  delete column in a spreadsheet is unsafe: a one-column sort or a fill-down puts it on the wrong rows), and creation
  or first-seen dates (not reliable: many filesystems do not record a birth time, and a copy, restore or sync resets
  it). `filo update` finds columns by header name and reads only `id`, `export`, `file_date` and `retention` (and
  checks `keep_until` was left alone), so a spreadsheet may reorder columns or add its own. Rows are keyed on (export, id). Each export's rows, hashes and dates are stored in
  the register (`export_rows`).
- **Encoding.** UTF-8 with a byte order mark (so spreadsheets detect UTF-8), standard quoting. File names are written
  as they are, emoji and all; bytes that are not valid UTF-8 are shown as U+FFFD, which is harmless because the update
  never reads the name. A display cell that starts with `= + - @`, tab or CR gets a leading `'` so a spreadsheet does
  not run it as a formula. The update accepts a byte order mark, CRLF, `;` or tab separators, and empty rows.
- **Dates**: `YYYY-MM-DD` only, and a real date (`2027-02-29` is refused). Anything else, such as `06/07/2027`, is
  refused, never guessed; the message suggests formatting the column as text, since spreadsheets may rewrite dates in
  the locale's format. Past dates are accepted (see second shape). Periods counted from today (`+1y`) were dropped
  from the CSV on 2026-11-12: the same cell meant different dates on different days. A period counted from the row's
  `file_date` (`7y`) replaced them.
- **`-o FILE` is required**: the CSV is never written to the terminal. FILE is created with mode 0600 and never
  overwritten.
- **What the update does with each row**:
  - `file_date` and `retention` as exported: nothing, whatever happened since. Without a `file_date` column, the
    `file_date` exported is kept;
  - the values already stored: nothing;
  - a new `file_date` or `retention`: replaces the stored ones and works `keep_until` out again. Editing the same CSV
    again and updating again replaces them again (the old "already imported" rule, which silently ignored a
    re-edited row, is gone);
  - a blank `retention` where one is stored: clears it, so the file is undated again. Clearing a retention does not
    clear `file_date`; blanking the `file_date` cell as well is a separate change and clears it too. Before
    clearing, the update lists those files (the first ten, then a count) and asks once,
    `Clear the retention of N files? [y/n]`. Anything but `y` or `yes`, or no terminal on standard input, leaves those
    rows alone, and the summary says how many and why. `--dry-run` counts them without asking.
- **The update refuses a row**, with one line naming the CSV line and the file, and still applies the others, when:
  a value does not parse, or is typed past the last column; the export is not in the register, or the file is not in
  that export (a row added in the spreadsheet); the file is no longer present; the file's hash differs from the
  export's (changed since the export); its date was changed from a later export since (an old CSV never overwrites a
  newer decision); a retention period has no `file_date`; `keep_until` was changed; the same (export, id) appears
  more than once (every copy is refused). A deleted row changes nothing. The applied rows go in one transaction;
  `--dry-run` checks everything and writes nothing. The exit code is 1 if any row was refused. A CSV without a
  `retention` header is refused as a whole: with a pointer to `filo export retention` if it is a review CSV or a
  retention CSV from before schema version 5, or, if it has a `file_date` column (a current CSV whose `retention`
  column was deleted or renamed), saying the `retention` column is missing.
- **Bulk dates**: set in the spreadsheet (filter on `directory`, fill down). `filo review set-date` was dropped on
  2026-11-12, at least for now.
- The hash at export is stored; it is **re-checked at action time** (`filo delete`) against `files.reviewed_hash`.

### Retention CSV, second shape (decided and implemented 2026-11-12)
Retention is usually counted from a document's own date ("keep tax records for seven years"), not from today or from
when the file reached the disk. So the CSV gains a date to count from and a retention that can be a period. This
replaced the single `keep_until` input of the first shape; the rest of the Lifespan section (one purpose, every present file,
encoding, refusals, the clear prompt, "since" measured in scans) stays.

- **Columns**, in order: `id, export, directory, name, size, modified, file_date, retention, keep_until`.
  - `file_date`: the date the file is about or was issued, the date a retention period counts from. Typed by the
    person, or filled in by the export from the file name (below). Not the filesystem's creation date, which is not
    reliable. Kept in the register even when no retention uses it.
  - `retention`, one cell for the decision, whose value says what kind it is:
    - a period, `Ny`, `Nm`, `Nw` or `Nd` (years, months, weeks, days; any case, stored without leading zeros, so
      `07Y` is `7y`): keep until `file_date` plus that period. A
      month step that lands past a month's end stops on its last day. Unlike the dropped `+1y`, a period counts from
      a date written in the same row, so the same row means the same date whichever day it is applied;
    - `forever` (any case);
    - a date, `YYYY-MM-DD`: a fixed end date.
  - `keep_until`: **display only**. The export fills in the end date Filo worked out (or `forever`), so the
    spreadsheet can sort and filter by it. The update only checks it was left alone, comparing it as a date (so
    surrounding spaces or `FOREVER` do not count as a change): a row whose `keep_until` holds another date is refused,
    "keep_until is worked out; change retention instead", and one holding a value that is not a date at all (a
    spreadsheet rewrote it as `3/14/2030`) is refused with the same "format the column as text" hint as the other
    date columns.
  - The update reads `id`, `export`, `file_date` and `retention`.
- **Past dates are accepted** (changed 2026-11-12; the update used to refuse a date not after today), whether typed
  or worked out from a period: a 2015 invoice with `7y` ends in 2022, which means it is already past its retention.
  The file simply becomes a deletion candidate. Since that also lets a typo through (`7d` for `7y`, 2016 for 2026),
  the update's summary **lists** the files it left past their retention (the first ten, then a count), with their
  retention and end date, and suggests checking for a typo; nothing is deleted until `filo delete` is run.
- **A period without a `file_date`** cannot be worked out: that row is refused with one line ("retention 7y needs a
  file_date") and nothing is set for that file. The other rows still apply.
- **Changing `file_date`** on a file whose retention is a period works its `keep_until` out again; a fixed date or
  `forever` does not depend on it.
- **A blank `retention`** where one is stored clears it, behind the prompt described above.
- **Dates read from file names**, to fill in `file_date` on export where it is blank. Accepted, with non-digit
  characters (or the start or end of the name) on both sides:
  - `YYYY-MM-DD`, also with `_` or `.` as the separator;
  - `YYYYMMDD`, as in camera names (`IMG_20230714_1032.jpg`);
  - `YYYY-MM` (also `_`, `.`): the last day of that month, so a file is never kept for less time than meant.

  The result must be a real date between 1900-01-01 and 30 days after today (one day for the whole export, even if
  it runs past midnight); anything else is not a date
  (`1245-06`, `2099-01-01`). A lone year (`2019 report.pdf`) is not read: it may be a model number or part of a title.
  A name with two different dates (`2023-01-01 to 2023-12-31 statement.pdf`) gets none: the person types it. A
  filled-in `file_date` is a suggestion shown in the spreadsheet. Left untouched, it is stored only when the row's
  retention is a period, which is when it is used and the person has had reason to look at it; with `forever`, a
  fixed date or no retention it is dropped, so a later period can never count from a guess nobody checked. (Found in
  a code review on 2026-11-12: `scan 20190101 id.pdf` would otherwise keep 2019-01-01 as its file_date.) Dates from photos' EXIF data: later (needs a library).
- **Register (schema version 5)**: `files.file_date` (YYYY-MM-DD, NULL = none), `files.retention` (the value as
  given: a period, `forever` or a date; NULL = none), and `files.keep_until`, now always worked out from those two
  (YYYY-MM-DD or `forever`). Statuses and `filo delete` keep using `keep_until` only. Migration: an existing
  `keep_until` becomes `retention` (its date, or `forever`) with the same `keep_until`. `export_rows` and `reviews`
  gain the same two columns, so an untouched cell is still recognised and the history shows what was typed.

### Disposal (iteration 3)
Decided 2026-11-12, implemented 2026-11-13 (package `internal/dispose`, the only code that deletes the user's
files). Replaces the earlier plan of a quarantine, holds and several checks at the moment of acting.

- **Deletion is permanent.** No quarantine folder and no recycle bin: the bin can fill up with large files, and is
  missing on network disks; backups are what make a deletion recoverable. So the list the person confirms, and the
  check below, are what keep it safe.
- **`filo delete [DIR] [-y]`** lists the candidates (expired files) under DIR, or under every root, with their end date,
  size and path, and their total size. All of them are listed, however many: that list is what is being confirmed;
  `filo delete DIR` narrows it. In a terminal it then asks `Delete [a]ll, [s]elect or [n]one?`:
  - *a* deletes them all, each through the check below. No second confirmation: the list was it;
  - *s* goes file by file: `[d]elete, [r]etention…, [s]kip, [q]uit?`. *r* asks for a new retention (a date, a
    period counted from `file_date`, or `forever`, as in the CSV), saves it at once and drops the file from the list;
    a period on a file without a `file_date` is refused and asked again. *s* leaves it a candidate. *q* stops: what
    was deleted stays deleted, the rest is untouched;
  - *n*, anything else, or end of input: nothing.
  Without a terminal it only lists. **`-y`** deletes them all without asking, in a terminal or not, so deletion can
  be run from a script: that is the person's choice, never Filo's.
- **The check at the moment of deleting** (one check; decided 2026-11-12). Each file is re-read and re-hashed just
  before it is deleted, after a quick size and modification-time comparison that skips a changed file without reading
  it. It is deleted only if:
  - it is still at its recorded path, as the same file (same inode);
  - it can be read: otherwise it is not deleted, a warning says why, and it is flagged unreadable as a scan would;
  - its content hash is the one its retention was set on (`files.reviewed_hash`); for a symlink, its target is the
    one recorded. Otherwise it is skipped ("changed since its retention was set"), its new hash is recorded, and it
    needs a new retention.

  A file that can be read but not deleted (its folder is not writable) is skipped and reported, and stays a
  candidate. Dropped as checks: recently modified (the re-hash covers it; reading or opening a file does not change
  its modification time, but saving does), sync and lock companion files (`.~lock.*#`, `~$*`, `.syncthing.*.tmp`,
  `*.part`), and whether a process has the file open (platform-specific, and deleting an open file does not break the
  app: a later save brings the file back, as a new file that needs a decision).
- **Recording.** Each deletion is written to the register straight after the file is removed, so Ctrl-C part-way
  leaves an accurate record. The file's row stays, marked deleted (`files.deleted_utc`), with its last path, hash,
  size and retention: those rows are the history of what Filo deleted, so there is no separate table. Its current
  path is closed and a `deleted` event recorded.
- **Restored files are new rows.** Deleted rows are never relinked by a scan's pairing (unlike a missing file that
  returns), so a file restored from a backup comes back as a new file with no retention, needing a decision; an old
  retention can never carry over to it. A scan that finds a new file with the same content as a deleted one says so
  ("same content as a file deleted on 2027-03-14"), as a hint only; the rows stay separate. So there is no
  `filo restore`.
- **Empty folders.** A folder is removed only if this run deleted its last entry: never a folder that was already
  empty, never one Filo deleted nothing from, never a root. Before removing it, Filo lists it on disk and removes it
  only if it has no entries at all (hidden files included), then removes it with the call that refuses a non-empty
  folder, so anything that appeared in between keeps it. Then it works up: a parent emptied by that removal is
  checked and removed the same way, stopping at the root. The summary says how many folders were removed.
- **Summary and exit code.** "Deleted N files (size). Skipped M:" and one line per skipped file and why. Exit code 0
  when everything went as decided (a changed file is an expected outcome), 1 if a file could not be read or deleted.
- **`filo report`** adds "Deleted by filo: N files, size freed (last run DATE: n files, size)", from the deleted
  rows.
- **Syncthing propagates deletes**: a delete on one device removes the file on every device that syncs the folder.
  A flag for synced roots is left for later (see Open questions). Surface `*.sync-conflict-*` files later too.
- **Decided while building it (2026-11-13):**
  - *A delete run is recorded as a scan of kind `delete`.* A closed path and an event both need a scan, so the run
    adds one `scans` row (`kind = 'delete'`, whole root) in each root where it first records something; deleted
    paths close at it, and its `deleted`, `changed` and `error` events belong to it. It is finished when the run
    ends, however it ends. The last scan `filo report` shows per root counts only `kind = 'scan'`; the latest scan
    an export records includes delete runs, which only orders events. The report's "last run" is the delete scans
    sharing the latest start time (recorded to the nanosecond, so runs do not collide).
  - *What a skipped file leaves behind.* Size or modification time differs (the quick check): nothing is
    recorded; the next scan records the change. Same size and time but another hash: the new content is recorded
    with a `changed` event, as a scan would. Cannot be read: flagged unreadable with an `error` event, and exit 1.
    Gone, replaced (another inode), no longer the same kind, or a symlink pointing elsewhere: nothing is recorded;
    the message says to run `filo scan`. Cannot be deleted: nothing is recorded, it stays a candidate, and exit 1.
    A last `lstat` just before the removal checks inode, size and modification time against what was read.
  - *Only inside its root* (hardening of 2026-11-13). Before anything else, the recorded path must be a clean
    relative path below a clean, absolute root (no `..`), and every folder from the root down must still be a real
    folder, each checked with `lstat`: a symlink there means the tree changed since the scan (skipped, "run filo
    scan"). Folders above the root are not checked: a symlink there (a home directory moved to another disk) moves
    the whole root, which scans handle. The folder check is repeated at the last look before the unlink, since
    hashing a large file leaves time for a folder to be swapped. A path outside its root can only come from an
    altered register (`../../.ssh/id_ed25519` with that file's hash): it is refused with exit 1. The hash check alone
    does not stop that, since whoever can edit the register can usually read the file.
  - *Removal* is `unlink` for a file or symlink (it refuses a directory) and `rmdir` for a folder (it refuses a
    non-empty folder and a file). The fallback on other platforms checks first and is untested.
  - *A file deleted by filo is never "missing"*: pairing (returned files) skips deleted rows. A new file with a
    deleted file's content gets a `same_as_deleted` event, shown in the scan summary and the report.
  - *`[r]etention…`* keeps the file's `file_date`, works `keep_until` out again, and is recorded as a one-row batch
    with command `delete`. A retention that is still past says so; the file still leaves this run's list.
  - *Prompts* are read in the background, so Ctrl-C ends a wait for an answer; nothing is deleted after it.
- Scheduled runs only ever scan; Filo never schedules a deletion.

### Commands and scheduling
- Plain names, easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete` (a `revert` was
  deferred). No themed subcommands. (`restore` was dropped on 2026-11-12: a file restored from a backup comes back
  as a new file.)
  (Reshaped 2026-11-12: `review` and its subcommands `set-date`, `export` and
  `import` were replaced by `export lifespan` and `update`; "update" says that only existing files change, which
  "import" did not. `export lifespan` was renamed `export retention` the same day: the CSV's word names what is
  set in it, and the lifespan CSV became the retention CSV (package `internal/retention`).)
- **Keep flags and options to a minimum** (2026-11-11). Prefer a safe default and clear output over a flag; prefer
  doing the obvious thing and saying what was done over asking.
- **Output cannot act on the terminal** (2026-11-13). File names can hold any byte but `/` and NUL and come from
  anywhere (attachments, files synced from a phone, an altered register). Everything printed goes through one
  writer that shows control characters, escape sequences, invalid UTF-8 and Unicode bidi controls as visible
  escapes (`\x1b`, `\u202e`), so a name cannot erase lines of the list a person confirms before a deletion, set the
  window title, write the clipboard, or read in another order than it is. Filo writes each line with one call, so a
  newline passes only as the last byte of a write: one inside a write can only come from a name and is shown as
  `\n`, so a name cannot add invented lines to that list (found in the code review of 2026-11-13; the first version
  let newlines through). Tabs are shown as `\t` and a backslash as `\\`, so an escape cannot be confused with a
  name holding the same text. Filo's own multi-line text (help) is written a line at a time; only the progress
  line's own control codes bypass the writer. The CSV export guards against spreadsheet formulas separately.
- **Prompts only in a terminal** (2026-11-12, replacing "no interactive prompts"). A command asks only where a person
  should look first: `filo update` before clearing dates, and `filo delete`. Declining, or having no
  terminal on standard input, is always the safe choice, so Filo still works the same from a script or a timer.
- `filo roots` lists roots (settings, mount point, files, size, last scan); `filo roots add DIR [--include-hidden]
  [--ignore NAME]... [--no-scan]` registers one and scans it (absorbing any roots inside it on the same filesystem,
  see Roots); `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...` replaces its settings; later
  `filo roots remove DIR`.
- `filo scan [DIR] [--workers N] [--allow-empty]` scans DIR, which must be inside a registered root (see Roots), with
  the root's settings; with no DIR, every root in turn, each in its own transaction. A root that fails (for example
  unmounted) is reported and the others are still scanned. `--workers` is 1–64, default 4.
- `filo report [--top N]`: see section 4.
- `filo export retention [DIR] -o FILE` and `filo update FILE [--dry-run]`: see Lifespan. The word after `export`
  names the CSV, so a later CSV is a new word, not a new command; `update` tells the kind from the CSV's header.
- `filo delete [DIR] [-y]`: lists the files past their keep-until date and deletes the ones chosen (see Disposal).
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

## 3. Data model (schema version 6)

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
    kind TEXT NOT NULL DEFAULT 'scan',  -- version 6: 'scan', or 'delete' for a delete run's record in a root
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
    -- the current lifespan (versions 4 and 5); the history is in reviews
    keep_until TEXT,                    -- worked out: YYYY-MM-DD or 'forever'; NULL = undated, needs a decision
    reviewed_utc TEXT,                  -- when the retention was last set by an update; NULL = never
    reviewed_after_scan INTEGER REFERENCES scans(id), -- the latest scan that update's export had seen
    reviewed_hash TEXT,                 -- the content the retention was set on
    file_date TEXT,                     -- version 5: YYYY-MM-DD a retention period counts from; NULL = none
    retention TEXT);                    -- version 5: as given: a period (7y), 'forever' or YYYY-MM-DD; NULL = none

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
    kind TEXT NOT NULL,                 -- moved | missing | changed | possible_move | error | other_fs |
                                        -- deleted | same_as_deleted (version 6)
    file_id INTEGER, path TEXT, other_path TEXT, detail TEXT);

-- versions 3 to 5: exports and the history of retentions
CREATE TABLE exports (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    path TEXT,                          -- the CSV written (NULL only for exports to standard output before version 4)
    after_scan INTEGER REFERENCES scans(id));          -- the latest finished scan at export
CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id), file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,                          -- the content shown; the update refuses the row if it has changed
    keep_until TEXT,                    -- version 4: the date exported
    file_date TEXT, retention TEXT,     -- version 5: as exported (file_date as filled in from the name); cells
                                        -- left as these are untouched
    PRIMARY KEY (export_id, file_id));
CREATE TABLE review_batches (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    command TEXT NOT NULL,              -- 'update', or 'delete' for a retention typed at filo delete's prompt
                                        -- ('set-date' in databases from before version 4)
    detail TEXT);                       -- the CSV read
CREATE TABLE reviews (                  -- append-only: every keep_until ever set or cleared
    id INTEGER PRIMARY KEY, batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    file_id INTEGER NOT NULL REFERENCES files(id), export_id INTEGER REFERENCES exports(id),
    keep_until TEXT,                    -- NULL = cleared
    hash TEXT,
    file_date TEXT, retention TEXT);    -- version 5
CREATE INDEX ix_reviews_file ON reviews(file_id);
```

"Present" means a file has a path interval with `valid_to_scan IS NULL`; there is no status column. "New" means
`first_seen_scan` equals the scan. A file's "last verified" time is derived, not stored: it is the latest completed scan
whose scope covers its path. Disposal does not look at it: it refuses files flagged unreadable and re-checks the
file itself at the moment of deleting (decided 2026-11-13; an earlier note here also refused files no recent scan
covers, which the re-hash makes unnecessary). `ctime_ns` and `unreadable_since_scan` are schema version 2,
the first migration (two `ALTER TABLE ... ADD COLUMN`s; existing rows start with ctime 0, so the first scan after the
upgrade rehashes nothing extra and records each file's ctime quietly). Version 3 adds the review columns on `files`
(all NULL for existing rows, so every file starts undated) and the four review tables. Version 4 (2026-11-12) merges
`review_date` and `retain_until` into `keep_until`, taking the later of the two so nothing is kept for less time; a
recorded `delete` becomes `keep_until` = the day it was decided, so the file is a deletion candidate; the same in
`reviews`; `export_rows` loses `reason` and gains `keep_until`; `import` batches are renamed `update`; roots gain
their settings (defaults for existing roots: hidden skipped, no extra ignores). Columns are dropped with `ALTER TABLE
... DROP COLUMN`. Version 5 (2026-11-12) adds `file_date` and `retention` to `files`, `export_rows` and
`reviews`; each existing `keep_until` becomes the retention it was (its date, or `forever`). Version 6 (2026-11-13)
adds `files.deleted_utc` (the deleted rows are the history of deleted files) and `scans.kind` (existing scans are
`scan`); see Disposal.

Location history gives snapshots without storing them: the tree at any scan is the set of intervals that cover it.
A revert moves each file back to the path it had then, with a dry run that reports and skips conflicts. It reverts
**locations only**; content recovery is the job of backups.

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
2. **Lifespan: one keep-until date per file**, set through the retention CSV (reshaped 2026-11-12 from a review
   queue). Done when: on a test tree, an export, edit and update round trip records exactly the dates set, refuses
   with one clear line each a changed file, an impossible or ambiguous date, a stale export and an added row; a
   cleared date asks first; a file past its date is listed by `filo delete`, and once edited needs a new date instead
   (`TestRetentionRoundTrip` in `internal/cli`, plus the store and retention package tests). The second shape of the
   CSV (`file_date`, `retention`, display-only `keep_until`, dates from file names, past dates allowed; schema
   version 5) is done, and the round trip covers periods, `file_date` from names, the display column and clears.
   Still to do: on the real home directory, every file gets a retention or `forever`, a directory at a time (filter
   on `directory`, fill down), through a few directory-sized exports.
3. **Delete.** Done 2026-11-13 on a test tree; not yet run on real files. `filo delete [DIR] [-y]` deletes the candidates after the prompt, with the re-hash check, deleted rows,
   the "freed" line in the report and empty folders removed (see Disposal). Done when: on a test tree, a confirmed
   run deletes exactly the unchanged candidates; a file changed, unreadable, replaced or moved after its retention was
   set is skipped with one clear line each; folders emptied by the run are removed up to the root but not an empty
   folder that was already there or one holding a hidden file; a restored file comes back as a new row with the hint;
   `-y` and no-terminal behave as designed. Checked by `TestDeleteRoundTrip` and `TestDeleteInterruptedAtPrompt` in
   `internal/cli` and the `internal/dispose` tests, and by hand on `scripts/testtree.sh` with a changed and an
   unreadable candidate.
4. **Register snapshots.** Done 2026-11-13 (see Register snapshots): `TestKeep` and the snapshot tests in
   `internal/store`, `TestSnapshots` in `internal/cli`, and by hand with a throwaway database.

**Next, in order** (agreed 2026-11-13; revert, the earlier iteration 4, was deferred, see section 7). Each is an open
question below until it is designed.

1. Named retention policies.
2. Folder rules (default retention policies on directories).
3. Progress in the report, and `export retention --needing-retention`.
4. Duplicates: one decision for all copies, and a duplicates review CSV.
5. `scan --verify`, with sidecar hash files.
6. Backup awareness.
7. Synced roots, sharing decisions between devices, and getting snapshots to the backup machine.
8. A summary for scheduled scans.

Later: the **UI** in place of the CSV, including viewing and editing the register in place, and notes, links and
tasks (see Open questions). All of it hangs off the stable file ID, so none of it needs the register to change
shape.

## 6. Open questions

- **Named retention policies** (raised 2026-11-13; next). A retention schedule written once, `bills = 7y`,
  `payslips = forever`, and the policy name typed in the CSV (and later in folder rules) in place of a period. If the
  law changes ("financial records: 10 years"), changing the policy changes every file using it. Proposed shape: a
  name that is not a policy yet creates one with no period, and its files stay "needing a retention" until it has
  one (fail safe); `filo update` lists the policies it created and asks once, so a typo (`bils`) does not quietly
  make a new one; `forever` becomes a built-in policy; periods still count from `file_date`; fixed dates per file
  stay possible. Changing a policy works out `keep_until` again for its files; lengthening is harmless, shortening
  lists the files it makes past their date and asks. A command lists policies, flagging those without a period.
  Worth having before the real home directory is dated, though setting `file_date`s does not depend on it.
- **Notes, links and tasks** (raised 2026-11-13; merges asset links, the long-term "projects and tasks linked to
  files", and notes on files). Notes on a file; links to an asset or project (modelled at that level, not per
  file); tasks, some with files and some without; possibly a productivity system built on the register. That is a
  change of scope from "Filo owns lifecycle" and needs its own discussion first. The stable file ID means nothing
  built now blocks it. (Holds were dropped on 2026-11-12: a file that must be kept gets a later retention or
  `forever`.)
- How synced roots are marked, and what Filo does differently for them (Syncthing propagates deletes; surface
  `*.sync-conflict-*` files). Belongs with sharing decisions between devices and getting snapshots to the backup
  machine (Register snapshots).
- **Sharing retention decisions between devices** (raised 2026-11-13). A synced folder (Syncthing) is on several
  devices, each with its own register; a retention set on one should be known on the others. Tested on 2026-11-13:
  a file dated on the laptop, then moved and renamed on the desktop, kept its retention on the laptop's next scan,
  because the move was paired by hash. Sharing the database file itself is rejected: Syncthing syncs `filo.db` and
  its WAL and SHM files separately and not as transactions, so a copy can be stale or corrupt; the `flock` is local,
  so writes on two devices between syncs give a `sync-conflict` copy and one side is silently lost; and most of the
  register is per device (root paths, inodes, ctimes, mount points, unreadable flags, scan history), so another
  device would rehash everything and, with sync lag, record moves back and forth. A separate database for only the
  synced roots has the same problems. Proposed instead: each device keeps its own register and they exchange
  **decisions keyed on content hash** (`file_date`, `retention`, when and on which device it was set). Each device
  writes only its own file in a synced folder (for example `decisions-<device-id>.csv`, so Syncthing never makes a
  conflict copy) and reads the others'; a file with a matching hash and no decision takes the shared one, recording
  where it came from; two devices disagreeing on a hash is reported, not resolved; an edited file has a new hash and
  needs a date again. It also helps with deletes: another device could tell "deleted by Filo on device X after the
  hash check" from an unexplained missing file (see the synced roots question above). No-code alternative: register
  a synced folder on one device only and set retention there; the content is the same everywhere and deletes
  propagate. Small step worth taking early: a random device ID in `meta`, made when the database is created, and the
  device recorded with each decision, so no decision has an unknown origin later. The exchange itself can wait until
  after iteration 3.
- **Backup awareness** (raised 2026-11-13). Principle 2 says backups make deletion recoverable; Filo cannot check
  it. Being in a folder Vorta backs up is a weak signal (the backup may not have run, may have failed, or may predate
  the current content). The strong one is "this content is in archive X from date Y": match Borg archive listings
  (path, size, mtime, perhaps a content hash; Borg's listing options to be checked against its documentation)
  against the register, giving each file a "last seen in a backup" date. It may only ever add warnings to
  `filo delete` ("in no backup"), never make deleting easier. Open: reading Borg directly or through Vorta's
  settings, the cost of listing a large archive, the repository passphrase, and that Borg runs on another machine.
- **Duplicates** (raised 2026-11-13; the report finds 297 groups, 605 files on the real home directory). Retention is
  about content, so dating one copy could offer the same retention to its undated copies (one confirmation), and
  copies with different retentions are reported, not resolved. Also an `export duplicates` CSV for review: mark the
  copies to delete, update, and Filo refuses a group with no copy kept or a copy that no longer exists. This brushes
  against the rule that a delete column in a spreadsheet is unsafe (a sort or fill-down moves it), so each row
  carries its group, path and hash, the marked copies are listed and confirmed, and each deletion goes through
  `filo delete`'s check.
- **Progress** (raised 2026-11-13). A report line for how far dating has got: files with a `file_date`, files with
  a retention, and per directory what is left. And `export retention --needing-retention`, writing only the files
  the report counts as needing a retention, so each CSV is shorter.
- **`filo scan --verify`** (raised 2026-11-13). A scan re-hashes only files whose size, mtime, ctime or inode
  changed, so bytes that rot in place go unnoticed until backups have pruned the good copy. `--verify` re-hashes
  everything and reports content that changed with nothing else changing. Sidecar hash files (`file.sha256`,
  `SHA256SUMS`) can be checked against the register's SHA-256 at no extra cost (MD5 or SHA-1 sidecars would need
  another read); a mismatch is reported (corruption, or a sidecar left from before an edit), never resolved.
- **A summary for scheduled scans** (raised 2026-11-13; low priority). A short line ("4 files past retention, 2 need
  a decision") a timer can pass to a desktop notification, so candidates do not sit unnoticed. Filo still never
  deletes on its own.
- Confirming a possible move, and undoing an update batch.
- **Telling a terminal from a character device** (found 2026-11-13). The prompt check treats standard input as a
  terminal if it is any character device, so `filo delete </dev/null` asks, reads end of input and deletes nothing
  (safe, but it should print the no-terminal message instead). A real check needs `golang.org/x/term` or
  `golang.org/x/sys` (already an indirect dependency) used directly; not added without asking.
- **A version command** (raised 2026-11-12): `filo version` showing Filo's own version (no build version is stamped
  yet) and the database in use: its path, schema version and when it was created. Today the schema version can
  only be read with `sqlite3 DB "SELECT key, value FROM meta"`. Showing the same in `filo report` was considered as
  a simpler alternative.
- **Viewing and editing the register in place** (raised 2026-11-12). Setting or clearing `keep_until` on a few files
  through a whole export, edit and update round is cumbersome. Wanted: query and view what the register holds (files,
  dates, history) and set or clear a date on a few files directly. Open: a command (for example
  `filo keep PATH... RETENTION`, taking the same retentions as the CSV), part of the UI, or both. Periods always count
  from `file_date`; a period counted from today (`+1y`) was closed on 2026-11-13.
- **Folder rules: a default retention policy on a directory** (second in line since 2026-11-13). Setting dates in the
  CSV is a one-off: a file added later arrives undated and needs a decision. A rule names a policy for a folder
  ("files under `personal/home/electric` get `bills`"). Proposed: a rule proposes and the person confirms in bulk
  (one answer per folder, not per file: "38 undated files under electric would get bills: 31 with a file_date, 7
  without"), for files already there and for new arrivals a scan reports, which keeps principle 4 and "never a
  trigger". About 60% of files have a `file_date` from their name; the rest need one typed before a period applies.
  Open: how rules on nested folders combine, whether a rule overrides a retention set by hand (it should not), and
  that a rule may never imply delete.
- UI technology.

## 7. Rejected or deferred

- **Accepted risks** (security review of 2026-11-13). Anyone who can edit the register already runs as the user and
  could delete files directly, so an altered register matters through indirect routes (a restored snapshot, a
  database in a synced folder, a `--db` from elsewhere). Accepted: a register whose *root* path was altered to
  another folder is not detected (the paths below it are checked, and the confirmation list shows absolute paths);
  `filo delete -y` deletes without a person reading the list, which is the point of `-y`; a file swapped in the
  microseconds between the last check and the unlink is deleted (anyone able to do that can delete it directly);
  SQLite parses a database before Filo checks it is one of its own (kept small by staying on a current driver);
  ownership of the snapshot directory is not checked on non-Unix platforms. From the code review of the snapshots:
  Ctrl-C cannot interrupt a snapshot being written (milliseconds for a register of a few MB), and every run that
  writes copies the whole register, timer scans that found nothing included (fine at a few MB; skipping a snapshot
  when a scan changed nothing is the first improvement if registers grow large). From the code review of
  2026-11-13: a snapshot is renamed into place after a check that the name is free, and a program not taking Filo's
  lock could create that name in between and be overwritten (the directory is private to the user; an atomic
  no-replace rename needs `golang.org/x/sys`); and the folders on a deleted file's path are checked by name, not
  held open, so a folder swapped in the microseconds after the last look is followed (the thorough fix, opening each
  folder from the root and unlinking relative to it, is for later).

- **Quarantine, the recycle bin, holds and `filo restore`** (rejected 2026-11-12). Deletion is permanent and backups
  make it recoverable; the confirmed list and the re-hash check keep it safe. The bin fills up with large files and is
  missing on network disks. A hold is a later retention or `forever`. A restored file is a new file.
- **More checks at the moment of deleting** (rejected 2026-11-12): recently modified, lock and sync companion files,
  open in a process. See Disposal.
- **Relinking a restored file to its deleted record** (rejected 2026-11-12): it would carry the old, passed retention
  onto a file the person just chose to bring back. It comes back as a new row, with a hint.
- **Filling `retention` from a filesystem creation date** (rejected 2026-11-12): many filesystems do not record one,
  and a copy, restore or sync resets it. `file_date` is typed, or read from the file name.

- **Revert directory moves** (the earlier iteration 4; deferred 2026-11-13). A zip of the files taken before a
  reorganisation covers it, and a revert only works if a scan ran before the moves anyway. The path history that
  would drive it is kept regardless.
- **Reading a retention from the file itself** (frontmatter, Office or PDF properties, extended attributes;
  rejected 2026-11-13). A date stored in the file can be changed under the register, which then has to decide which
  one wins; the register stays the one source of truth.

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
- **Device tracking in the database.** Unneeded while the database is per device and never synced. A device ID for
  shared decisions is an open question (see Open questions).
- **One database per root** (the first Go design, with the database in `<root>/.filo`). Several roots on one device
  meant several registers to manage; one database with a `roots` table replaced it on 2026-11-11.
- **Themed command and project names** (the earlier "dirge" idea, funeral-style subcommands). Plain names win.
- **Building search.** Existing indexers and document managers (Paperless-ngx for received documents, desktop
  indexers) already cover finding files. Filo owns lifecycle.
