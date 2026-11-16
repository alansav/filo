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
The duplicates CSV was designed on 2026-11-13/14 and taken ahead of that order (schema version 7); a gap found on
2026-11-14 (a second export shows decided groups blank) led to a redesign on 2026-11-16 (Duplicates: `filo delete
FILE.csv`, no stored decisions to delete, four kinds of keep), built on 2026-11-15/16 in five slices (schema version
8). The terminal output was designed on 2026-11-15 and built first (Terminal output). Last updated: 2026-11-16.

This file records settled decisions, the planned data model and the open questions. When a decision changes,
change it here in the same commit.

## 1. Problem and principles

Files go into storage and never leave. Notes, documents and received PDFs pile up, which makes backups bigger and
finding things harder, feeds a hoarding habit ("keep it in case someone asks"), and creates demand for the person
who can find any old file. Filo also helps with files under a retention rule: records that must be kept for a set
period (financial documents), and records that should be disposed of after one (personal data held for a fixed time).
It tracks each file's date and lists it when the date comes; it never deletes on its own, so it supports a retention
policy, it does not enforce one. Filo is a register and review workflow that makes disposal a normal, safe, routine
step.

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
- Go. The earlier C# prototype is a reference spec only. Go was chosen for two reasons: most features need few
  third-party packages (the SQLite driver is the only direct dependency), and the build is one self-contained binary
  that needs no C toolchain (the driver is pure Go) and is much smaller than a self-contained .NET build.
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
- When the database does not exist yet, Filo creates it with mode 0600, and SQLite gives its WAL and SHM files the
  same mode. An existing database keeps its mode: Filo never changes it.
- The database, its WAL/SHM/journal files, the lock file and the snapshot directory are never scanned, wherever they
  are.

### Register snapshots
Decided and implemented 2026-11-13. The register holds decisions (retentions, file dates) that exist nowhere else.
A snapshot is taken at the end of every run that changed the register, and the recent ones are kept (see Rotation),
so the register can be rolled back to an earlier state, by hand for now (see Restoring; a command may come later).
They are a short-term safety net against Filo's own mistakes, such as a bad update, not a backup: they sit next to
the database, on the same disk. Copying the register's snapshots to a separate physical location is the user's job.

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
- **Getting them off the machine** (to another machine or a backup) is part of the synced-roots work: for
  example a one-way sync of the snapshot directory to a machine that is backed up. A snapshot is a finished file that
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
- `filo roots add` scans the new root straight away; `--no-scan` only registers it (for example to run the first
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
  I/O error, or any other unexpected error: the flag only ever holds a file back) gets `files.unreadable_since_scan`,
  the first scan in a row that failed to read it; it stays set across later failing scans and is cleared by the first
  scan that hashes the file again. Its last good hash, size and mtime are kept, so its identity and history survive,
  but the hash is stale while the flag is set. A file that vanished or changed while it was read is not unreadable: it
  is retried as before and the flag is untouched. Losing read access changes the file's ctime, so the next scan notices
  it (see above). Files under a directory that cannot be listed are not flagged: Filo cannot tell what happened to them,
  so they are simply not covered by that scan and their "last verified" time stops advancing (see section 3).
  Decided 2026-11-11.

### Move detection
- Hash match: a missing file and a new file with the same hash are one file that moved (or was renamed). A copy of a
  file that is still present is a new file; copies are separate identities with their own rows.
- Empty files are registered like any other file (they can be given a retention), but are never paired as moves and
  never form a duplicate group: every empty file has the same hash, so the hash says nothing. Duplicates are never
  guessed: with several candidates, pair only when the file name is unique on both sides, otherwise report as new
  plus missing.
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
  then. On that date it becomes a deletion candidate, never deleted automatically: `filo delete` lists the candidates,
  and deleting each is the person's call (see Disposal). A recurring check on a file (an annual look at a seven-year
  record) is a task, a later iteration; this iteration tracks lifespan only. Hard legal retention is a later retention
  or `forever` (holds were dropped on 2026-11-12).
- **Status of a present file** (regular files and symlinks), the first that applies:
  1. *unreadable*: flagged unreadable and its date not set since the scan that flagged it; or flagged and past its
     date (its content cannot be checked);
  2. *possible move*: the new side of a possible move recorded after its date was last set. The event names the old
     file; the new one is the file whose path opened at the event's path in that scan;
  3. *needs a file_date* (schema 9, see Named retention policies): its policy is a period and it has no `file_date`;
  4. *undated*: no `keep_until` (a policy with no value included);
  5. *forever*: `keep_until` is `forever`;
  6. *kept*: `keep_until` after today (local date);
  7. *changed*: past its date, but a regular file whose hash is not the one the date was set on (or the date was set
     without a hash, or it has none now). A change before the date does not matter; at the date it needs a new one;
  8. *expired*: past its date and unchanged: a deletion candidate.
  *Unreadable*, *possible move*, *needs a file_date*, *undated* and *changed* need a decision; `filo report` counts
  them and shows the directories holding most of them.
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
  `file_date` (`7y`) replaced them. Dates only: a time (`2026-12-31 15:00`) is refused, not cut off.
- **`-o FILE` is required**: the CSV is never written to the terminal. FILE is created with mode 0600.
- **An existing FILE is replaced only after asking** (decided 2026-11-16, not built yet: until then an existing FILE
  is refused). Refusing made people delete the old CSV or invent `retention2.csv`. Filo asks `Replace
  retention.csv? [y/N]`; with no terminal it refuses. `-y` does not answer it: a timer or script never replaces a
  file silently. Only a regular file is replaced, never a directory, symlink or device. The new CSV is written to a
  temporary file next to it (mode 0600) and renamed over it, so a crash never leaves half a CSV. If the old file is
  a Filo export of the same kind with rows `filo update` has not applied, the question says so ("it has 12 rows not
  yet applied"). The same for every `-o` file Filo writes.
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
      a date written in the same row, so the same row means the same date whichever day it is applied. Stored as
      typed, apart from case and leading zeros: units are never converted (`7d` stays `7d`, `12m` stays `12m`);
    - `forever` (any case);
    - a date, `YYYY-MM-DD`: a fixed end date;
    - a policy name (see Named retention policies): the file takes that policy's value, whatever it is now or later
      becomes. `forever` is a reserved word, not a policy.
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
  a code review on 2026-11-12: `scan 20190101 id.pdf` would otherwise keep 2019-01-01 as its file_date.)
  Dates from photos' EXIF data: later (needs a library).
- **Register (schema version 5)**: `files.file_date` (YYYY-MM-DD, NULL = none), `files.retention` (the value as
  given: a period, `forever` or a date; NULL = none), and `files.keep_until`, now always worked out from those two
  (YYYY-MM-DD or `forever`). Statuses and `filo delete` keep using `keep_until` only. Migration: an existing
  `keep_until` becomes `retention` (its date, or `forever`) with the same `keep_until`. `export_rows` and `reviews`
  gain the same two columns, so an untouched cell is still recognised and the history shows what was typed.

### Named retention policies (designed and built 2026-11-16)
A retention schedule written once and named, so the same rule is typed as a word in the CSV and changed in one
place: if the law moves financial records from 7 to 10 years, `filo policies set bills 10y` changes every file using
`bills`. Settled with the person one topic at a time on 2026-11-16 (it had been a proposal in Open questions since
2026-11-13). Built on branch `retention-policies` in four slices, see section 5.

- **What a policy is**: a name for a retention: a period (`bills = 7y`, counted from each file's own `file_date`), a
  date (`project-x = 2030-12-31`: every file of it ends that day), or `forever`. Names are letters, digits, `-` and
  `_`, starting with a letter, so a name is never mistaken for a period or a date; any case (`Bills` is `bills`,
  stored lowercase); `forever` is reserved. In the retention cell, `7y`, `2030-12-31` and `forever` keep their
  meaning and anything else that is a valid name is a policy. A file has either a policy or a retention of its own,
  never both.
- **Dates (unchanged, and to be said plainly in the README and help)**: a file kept until 2026-12-31 is a deletion
  candidate on 2026-12-31, since *kept* means the date is still to come; the same for a policy as for a date typed
  per file. Nothing is deleted on that day: the file is listed by the next `filo delete`, which asks.
- **Creating a policy** (the person's choice over creating them by command only: a policy created ahead may never be
  used): a name in the CSV that is not a policy yet creates one **with no value**. `filo update` lists the policies it
  is about to create, with how many rows name each, and asks once (`Create 2 policies? [y/N]`). Declined, or no
  terminal, the rows naming them are left alone and the summary says how many; `--dry-run` lists them. Its files
  need a retention until the policy gets a value. A policy made in error (`bils`) is fixed with `filo policies move
  bils bills`, then `remove bils`.
- **`filo policies`** (a new command name):
  - `filo policies` lists each policy with its value and how many files use it, and flags what needs attention: no
    value (`! needs a retention`), a date that has passed (`! passed · 0 files: set a new date, or remove it`), files
    without a `file_date` under a period (`! 3 without a file_date`). Nothing is ever cleared up automatically: a
    passed policy may be reused (set to next year's date).
  - `add NAME [VALUE]` creates one (refused if it exists, or the name is `forever` or not a valid name).
  - `set NAME VALUE` gives it a value or changes it (see Changing a policy).
  - `move OLD NEW` moves every file from OLD to NEW: into a new name it is a rename (the value goes with it), into an
    existing policy a merge (the files take NEW's value). It asks first, as `set` does.
  - `remove NAME` deletes the policy, refused while files use it ("12 files use bils: move them first"). It is
    `remove`, not `delete`: `filo delete` deletes files.
  - `show NAME` shows its value and every file using it (file_date, until, path), the files that need a `file_date`
    first; cut at 10 in a terminal with "… N more", all when piped.
- **Changing a policy** (`set`, and `move` into an existing one): each file's `keep_until` is worked out again.
  Longer or later: done, with a count line. Shorter or earlier: if any file ends up past its date, those are listed
  (the first ten, then a count) and it asks once (`Shorten bills for 212 files, putting 6 past their retention?
  [y/N]`); if none does, a count line and no question. To a period, files without a `file_date` are listed and it
  asks; on yes they get the status *needs a file_date*. `-y` answers yes; no terminal, or no answer, changes nothing.
  A change never touches what `filo delete` checks: a file is deleted only if its content is the content its
  retention was set on, whatever the date.
- **Needs a file_date**: a file whose policy is a period and which has no `file_date` has nothing to count from. It
  can only arise from a policy change (the CSV refuses such a row, and refuses clearing the `file_date` of a file
  whose policy is a period). In a duplicates CSV, such a copy marked `delete` refuses its group ("needs a file_date
  first"): its retention's end is not known, so no copy kept can be shown to outlast it. The same for a copy on a
  policy with no value yet ("its policy payslip has no value yet"). It is a status of its own (below *possible move*, before *undated*), so the fix is
  named: never a deletion candidate; counted in the report's decisions with the folders holding most of them (`! 3
  files need a file_date: their policy counts from it (bills, 7y)`); flagged by `filo policies`; listed first by
  `show`, with `Next: filo export retention DIR -o FILE, fill in file_date, then filo update FILE`.
- **The retention CSV**: the `retention` cell holds the policy name; `keep_until` shows the date worked out, as
  today. A policy whose value is a period, on a row with no `file_date`, refuses the row ("retention bills (7y) needs
  a file_date"); a policy with no value is accepted. An untouched cell is recognised by what was exported (`bils`),
  so an old CSV does nothing after a `move` or `set`.
- **Output** (mocked 2026-11-16, under the Terminal output rules):

```
$ filo policies

Policies   4 · 1 needs attention

  bills          7y           from each file's file_date   212 files
  payslips       forever                                    48 files
  project-x      2030-12-31                                 17 files
  bils           no value     ! needs a retention            3 files

Next: filo policies set bils VALUE, or filo policies move bils bills

$ filo policies show bills

Policy bills   7y from each file's file_date · 212 files · ~

    file_date    until        path
    2019-03-14   2026-03-14   Documents/bills/electric/2019-03.pdf
    … 211 more files

Next: filo policies set bills VALUE to change it for all of them

$ filo policies set bills 5y

Shorten bills   7y → 5y · 212 files

! 6 files would be past their retention:
       file_date    until
       2019-03-14   2024-03-14   Documents/bills/electric/2019-03.pdf
       …

Shorten bills for 212 files, putting 6 past their retention? [y/N]
```

  After yes: `Done   bills is 5y · 212 files updated · 6 past their retention`, `Next: filo delete`. Lengthening:
  `Done   bills is 10y · 212 files updated`. `filo update` naming new policies: a `New policies   2 · bils, payslip`
  block with a line per policy and its rows, "they have no value yet: their files need a retention until filo
  policies set NAME VALUE", then `Create 2 policies? [y/N]`. `move` asks `Move 3 files from bils to bills (7y)?
  [y/N]`; `add` is `Added   bills · 7y`; the report's Retention section ends with `! 4 policies, 1 needs
  attention (filo policies)` when there are policies (`·` when none needs attention), even with no files.
- **Register (schema version 9)**: `policies` (id, name lowercase unique, value: a period, a date, `forever` or NULL,
  added and changed times); `files.policy_id` (NULL for none; `files.retention` is NULL when it is set, and
  `keep_until` stays stored and worked out, so the statuses and `filo delete` read it as before); `policy_changes`
  (policy, old value, new value, when, how many files changed), since a file's `keep_until` can now change without
  the file or its row in a CSV being touched. `export_rows.retention` and `reviews.retention` keep what was typed
  (`bills`). The upgrade only creates the tables and the column.
- **The scenarios** (the test list):

  | scenario | outcome |
  |---|---|
  | A1 a CSV names a new policy, confirmed | created with no value; its files need a retention |
  | A2 declined, or no terminal | those rows left alone, counted in the summary |
  | A3 `Bills`, `7y`, `forever`, `2030-12-31`, `bills!` in the cell | the policy bills; a period; forever; a date; the row refused |
  | A4 `policies add` an existing name, or `forever` | refused |
  | B1 `bills` (7y) on a row with a file_date | keep_until is file_date plus 7 years |
  | B2 `bills` (7y) on a row without a file_date | the row refused (needs a file_date) |
  | B3 a date policy; a forever policy | the policy's date; never a candidate |
  | B4 a file moved from a policy to its own retention, and back | policy and retention swap |
  | B5 an old CSV with `bils` untouched after `move bils bills` | nothing happens |
  | C1 lengthen | no question; files updated; a policy_changes row |
  | C2 shorten, no file past its date | no question |
  | C3 shorten, some past: yes, no, no terminal, -y | listed and asked; changed only on yes or -y |
  | C4 set to a period, files without a file_date | listed and asked; they need a file_date |
  | D1 `move bils bills`, bills new or existing | a rename, or a merge with keep_until worked out again |
  | D2 `remove` a policy in use, or unused | refused, or removed |
  | E1 a policy with no value; a passed date with 0 files | flagged by `filo policies` |
  | E2 files that need a file_date | the report, `filo policies` and `show` say so; a file_date in the CSV fixes it |
  | E3 clearing the file_date of a file on a period policy | the row refused |
  | F1 a file on a date policy reaching its date | a candidate that day; `filo delete` checks its content as before |
  | G1 the upgrade from schema 8 | tables created, nothing else changes; the pre-upgrade snapshot kept |

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

### Duplicates (designed 2026-11-13/14; redesigned 2026-11-16, built 2026-11-15/16)
The report finds duplicate groups (297 groups, 605 files on the real home directory). The duplicates CSV lets the
person act on them: export the groups, decide per copy in a spreadsheet, then run `filo delete` on the CSV, which
checks every group against the disk, lists what it will and will not do, asks, and does it. It brushes against the
rule that a delete column in a spreadsheet is unsafe (a sort or a fill-down puts it on the wrong rows), hence the group
rules, the listing before the question and the check at the moment of deleting below.

**Built** (on branch `duplicates-redesign`, in five slices, see section 5). Slice 1, the register: schema 8, the
group definition below, the report's "not counted" line; the first design's `filo update` of a duplicates CSV, its
marks in `filo delete` and `--separate` are gone. Slice 2, the CSV: the `action` column, `keep` filled in
for records, and reading it into a plan per group (`duplicates.Plan`). Slice 3: `filo delete FILE.csv` carries a
duplicates CSV out. Slice 4: `filo duplicates`, `exclude` and `include`. Slice 5: the README and the help. See "The first design, and why it changed" at the end
of this section.

- **The principle** (2026-11-16). The duplicates process decides which copy to keep, never whether the content goes.
  A duplicates decision never removes a content or shortens its life; only a retention does.
- **No stored decisions to delete** (2026-11-16). There is no `filo update` for duplicates. The CSV is the person's
  saved work: they keep editing it and run `filo delete FILE.csv` when ready. Nothing waits in the register between
  deciding and deleting, so a second export never disagrees with a first, and an old CSV is safe to run: a group is
  acted on only if its files are still exactly as exported. The only duplicates state stored is what is meant to
  exist: excluded directories, contents *always*, and sets of *records* (below). Setting a retention stays a separate
  step (`filo update` on a retention CSV), since it never deletes: deletes happen later.
- **A group** is a content (hash) held by two or more *copies*: present regular files, size above zero, not deleted,
  not under an excluded directory, the content not *always*, and not all members of one set of records. Hard links
  (same inode in the same root; a root never leaves its filesystem) are one copy: two paths to one file are not a
  duplicate, and deleting one frees nothing. So a group needs two distinct copies, and the report's reclaimable space
  counts copies, not paths. A copy with more than one link can be kept but not deleted.
- **Why identical files exist, and the four kinds of keep** (2026-11-16):

  | reason | example | the decision is about | a new copy found later |
  |---|---|---|---|
  | a copy made by accident | a download saved twice | which copy to keep (`keep` one, `delete` the rest) | a new duplicate to decide |
  | copies that are each a record | insurance terms received in 2020 and again in 2021, each with its own retention | these copies (`keep` on two or more): *records* | a new duplicate to decide |
  | a content that belongs everywhere | `LICENSE`, `__init__.py`, a template in each project | the content (`always`) | not a duplicate |
  | a deliberate copy of a folder | a backup, an old laptop's files | a directory (`filo duplicates exclude`) | not a duplicate |

- **Excluded directories.** `filo duplicates` lists everything not counted as duplicates; `filo duplicates exclude DIR`
  makes files under DIR (its whole subtree) never count as duplicates, of each other or of files elsewhere;
  `filo duplicates include DIR` removes the exclusion. Directories are included by default, so `include` only removes
  an entry: if DIR is not excluded itself but a parent is, it says so ("~/Backup/Documents is not excluded itself;
  ~/Backup is"). `exclude` replaces the exclusions under DIR and says which ("excluded ~/Backup (replaces
  ~/Backup/Documents)"). DIR must be inside a root; exclusions are stored in the register. Retention is unaffected. A
  file with two copies in `~/Documents` and one in an excluded `~/Backup` is still a group, of the two. Filo does not
  try to spot such directories (any threshold would miss odd cases); `filo export duplicates` counts what is not
  counted, and while there is no excluded directory ends with "folders that are copies on purpose (backups) can be
  left out entirely: filo duplicates exclude DIR" (see Output, below; it first said "lists the exclusions" and
  "always ends with", changed on 2026-11-15: a list repeats `filo duplicates`, and a line on every run stops being
  read). Expected to be used rarely, mainly for backups.
- **`filo export duplicates [DIR] -o FILE`** writes every group with at least one copy under DIR, and all of that
  group's copies, also those outside DIR (the copy to keep may live elsewhere), so exports of different directories
  can overlap. One row per copy, groups numbered in order of their first path. Same CSV rules as the retention CSV
  (byte order mark, formula guard, `-o` required, mode 0600, an existing file replaced only after asking). Columns: `id, export, group, copies,
  directory, name, size, modified, file_date, retention, keep_until, action`. `copies` is the group's size, so a
  filtered view shows when copies are hidden. The retention columns are display only. `export_rows` records each
  row's hash, group and path. The `action` cell is blank, except `keep` on a copy that is one of a set of records (a
  new copy of records is exported with them: `delete` it, or `keep` it to join the set).
- **The `action` cell**: `delete` (`d`), `keep` (`k`) or `always` (`a`): the full word or its single letter, any case,
  spaces around it ignored. Anything else (`del`, `delete this`) refuses the group. Blank is no decision.
- **Group rules.** Each group is decided on its own; a group whose rows are all blank is not decided and is skipped,
  so a CSV can be worked through and run several times.

  | rows of a group | result |
  |---|---|
  | all blank | not decided: skipped |
  | some blank, some filled | refused: every copy needs a value |
  | one `keep`, the rest `delete` | the `delete` copies are deleted |
  | two or more `keep`, the rest `delete` or none | the `delete` copies are deleted; the kept copies are stored as a set of records |
  | every row `always` | the content is stored as always; nothing is deleted |
  | `always` with anything else | refused: `always` is about the content, so it goes on every row |
  | every row `delete` | refused: "no copy would be left; to remove this content entirely, give it a past retention" |

  To remove every copy of a content: `keep` one and `delete` the rest, run `filo delete`, then remove the last one by
  hand or with a retention. There is no `DELETE` word for every row: it adds risk, and a fill-down of it is as easy as
  of `delete`.
- **Retention across copies** (2026-11-14, kept on 2026-11-16): a copy can be deleted only if a kept copy lasts at
  least as long: the same or a later `keep_until`, or `forever` (a copy with no retention lasts no time, so it may go
  whatever is kept). A retention belongs to a copy (counted from its own `file_date`), so a content must exist until
  the latest `keep_until` of its copies. Insurance terms from 2020 and 2021, each `7y`: keeping 2020 and deleting
  2021 is refused ("keep the 2021 copy instead, or keep both"); keeping 2021 and deleting 2020 is allowed and shortens
  nothing, so such copies stay duplicates (hiding them would hide a safe saving). Filo never changes a retention to
  make a group pass. (Considered on 2026-11-16: allowing it with a warning; rejected, since it would let a decision
  cut a retention short.) It also means a kept copy that reaches its own date needs no rule: it outlasts every copy
  deleted, so by then every copy's retention has passed.
- **`filo delete FILE.csv [-y]`**, in order:
  1. Read the CSV: a CSV from the first design (a `delete` column) is refused ("this CSV is from an older export:
     export again"); a retention CSV is refused; each group's values and structure are checked. A row not in its
     export, or whose `group` is not the export's group for that file (its `group` edited, pasted from another
     export, added or duplicated), refuses both groups it touches; the other groups are still processed, and the
     output advises a new export. Sorting is fine: groups are found by each row's `group` cell, not by row order. A file in groups of two exports (overlapping
     exports, B3) with different actions refuses each of those groups ("give it one action"; agreed 2026-11-16), so
     the outcome never depends on which group runs first; the same action, or a blank group, is fine.
  2. Check every decided group against the disk, not the last scan. A file whose folder can be read and that is not
     in it is gone: its row is reported and ignored, and the rest of the group is decided as usual (also when the row
     was deleted from the CSV). A row deleted from the CSV whose file is still there: the group is skipped. A file
     filo cannot check (drive not mounted, folder unreadable) is never "gone": its group is skipped ("can't check …:
     is the drive mounted?"). Each copy must still be at its path and the content the export recorded (strict: a
     group any of whose files changed or moved since the export is skipped whole, even if its copies still match
     each other, so a file is deleted only if it is still exactly the content decided on). Then the retention rule.
     This check reads no content (built 2026-11-16): each copy is looked at (`lstat`) and compared with the
     register (the export's hash still the register's, the same inode, size and modification time, no other current
     path), and a copy to keep is opened to see it can be read; the content is re-hashed once, at the moment of
     deleting. A copy moved without a scan since is not at its path, so it reads as gone, as above; one whose move
     a scan saw skips its group ("moved since the export"). A copy to delete with another hard link skips its group.
  3. List, under the Terminal output rules: "Will delete" (per group, `keep` and `delete` lines, symlinks that would
     break), "Will record" (contents always, sets of records), "Won't touch" (each refused or skipped group with its
     line and reason; groups not decided as a count), and ask once: `Delete 9 copies and record 2 decisions?
     [y/s/l/N]`. `s` goes group by group (`d do it · s skip · q quit`); `-y` does all of it without asking; without a
     terminal nothing is done and one line says so.
  4. Carry it out a group at a time, each with the check at the moment of deleting (below); records and always are
     stored as their group is reached.
  5. The result: the "Done" summary, then anything skipped at the last moment, with its reason.

  Files gone by then: all of a group's copies gone, or the copies to delete gone and the kept one there, is reported
  (removed outside filo). One copy to delete gone and the kept one there: the others are deleted. The kept copy gone:
  the group is skipped, since no copy would be left. A group already carried out by an earlier run is reported as
  gone and skipped. An interrupted run keeps what it did.
- **Never the last copy: the check at the moment of deleting.** The register is not trusted for this; the disk is.
  Before a group's first deletion, Filo looks for a kept copy: a copy marked `keep` that is at its path as the same
  file, readable, and re-hashed to the same content, tried in order of the longest retention. A copy that is, on
  disk, the same file as a copy to delete never counts (same device and inode, looked at live: a folder bind-mounted
  into a second root shows the same file as two copies, and the register keeps no device numbers; found in the code
  review of 2026-11-14). The export merges such paths into one copy, and the device and inode are compared again at
  the last look before each unlink. If none passes, nothing in the group is deleted and the list says why for each
  copy tried. The retention rule is then checked against that copy; if it fails, the group is skipped. Each copy to
  delete then goes through the usual check (path, inode, re-hash against the content exported), and right before its
  unlink the kept copy is looked at again (same inode, size and modification time). A copy deleted earlier in the run
  never counts as kept. No scan is needed beforehand: a stale register means skips with "run filo scan", never a wrong
  deletion. The `deleted` event says which copy was kept, and a symlink in the register that points at a copy deleted
  is named in the list, since it would break.
- **`filo duplicates`: what is not counted, and undoing it.** It lists the excluded directories, the contents always
  (with their copies and a few paths) and the sets of records. `filo duplicates include PATH` undoes whichever applies
  ("count this as a duplicate again"): an excluded directory loses its exclusion; a copy of an always content makes
  the content a duplicate again ("LICENSE is a duplicate again (5 copies)"); a copy that is one of a set of records
  removes the whole set ("terms.pdf: the 2 records are duplicates again"); anything else says PATH is none of them.
  Records are stored per copy and its content, so a record that is edited leaves its set by itself.
- **Output** (agreed 2026-11-15, one topic at a time; under the Terminal output rules).
  - `filo duplicates` lists the whole register (no DIR): excluded directories with the files and size under each
    (so one far wider than meant shows), contents always as duplicate groups (`DupGroup`) with at most 3 paths and
    `… N more copies` (they are often everywhere), and sets of records with every copy and its keep-until date (a
    set is about those exact files). Nothing stored: `Not counted as duplicates   none` and a next step to
    `exclude`.
  - `filo duplicates exclude DIR`: `Excluded` with what it means, "still scanned, retentions unchanged" (unlike
    `roots set --ignore`), the exclusions under DIR it replaces, and the change in duplicate groups (`41 duplicate
    groups fewer: 256 left`, a sanity check worth computing the groups twice); `Next:` the `include` that undoes it.
    No question first: nothing is deleted and it is undone in one command. DIR may be a whole root; outside every
    root it is refused. Already covered (DIR or a parent excluded): `Already excluded`, nothing written.
  - `filo duplicates include PATH`: a directory is about exclusions, a file about always or records; a file that is
    both (a record under an excluded folder) has its own kind undone and a line saying the folder is still
    excluded, so one command never undoes two things. A set of records goes as a whole, and the heading names it.
    The change in groups is shown. Nothing to undo (`Not excluded itself`, with the parent that is; `Nothing to
    include`) is exit 0, nothing written. `Next:` (export to decide them) only after including a directory.
  - `filo export duplicates` (replacing the first design's words; the shape stays): the rules as `·` lines in the
    action words; a line when `keep` is filled in for a new copy of a set of records (F3), since the group otherwise
    looks half decided; the "not counted" count, as the report's line; the backups line only while nothing is
    excluded; `Next: edit the action column, then filo delete FILE`.

```
$ filo duplicates

Not counted as duplicates   1 excluded directory · 2 contents marked always · 1 set of identical files kept as separate files · ~

Excluded directories
  ~/Backup                                               3,210 files · 4.1 GB

Contents marked always
    LICENSE · 1 KB each · 5 copies
      projects/a/LICENSE
      projects/b/LICENSE
      projects/c/LICENSE
    … 2 more copies
    template.docx · 24 KB each · 3 copies
      work/2024/template.docx
      work/2025/template.docx
      work/template.docx

Kept as separate files
    terms.pdf · 2 MB each · 2 separate files
      insurance/2020/terms.pdf   until 2027-12-31
      insurance/2021/terms.pdf   until 2028-12-31

Next: filo duplicates include PATH to count one as a duplicate again
```

```
$ filo duplicates exclude ~/Backup

Excluded   ~/Backup
  · 3,210 files under it no longer count as duplicates (still scanned, retentions unchanged)
  · replaces 1 exclusion under it: Documents
  · 41 duplicate groups fewer: 256 left

Next: filo duplicates include /home/you/Backup to undo it

$ filo duplicates exclude ~/Backup/Photos

Already excluded   ~/Backup/Photos
  · through ~/Backup
```

```
$ filo duplicates include ~/Backup

Included again   ~/Backup
  · 3,210 files under it count as duplicates again
  · 41 duplicate groups more: 297

Next: filo export duplicates -o FILE to decide them

$ filo duplicates include ~/projects/a/LICENSE

Duplicate again   LICENSE · 1 KB each · 5 copies
  · no longer marked always: 1 duplicate group more

$ filo duplicates include ~/insurance/2020/terms.pdf

Duplicates again   terms.pdf · 2 MB each · 2 separate files
  · no longer kept as separate files: 1 duplicate group more

$ filo duplicates include ~/Backup/Documents

Not excluded itself   ~/Backup/Documents
  · ~/Backup is: filo duplicates include /home/you/Backup counts all of it again

$ filo duplicates include ~/notes.txt

Nothing to include   ~/notes.txt
  · not excluded, marked always or kept as a separate file: it already counts as a duplicate
```

```
$ filo export duplicates ~/Documents -o /tmp/d.csv

Duplicates export   export 13 · 12 groups · 31 copies · ~/Documents

  · in each group: keep for the copy to keep, delete for the rest; always on every row if the content belongs everywhere
  · keep on two or more copies: they are kept as separate files, each with its own retention
  · 1 group has keep filled in: a new copy of files you kept as separate files; delete it, or keep it as another separate file
  · 1 excluded directory, 2 contents marked always and 1 set of identical files kept as separate files, these are not counted as duplicates (use "filo duplicates" to view)

Next: edit the action column, then filo delete /tmp/d.csv
```

- **The report** leaves out excluded directories, contents always and sets of records from its duplicate groups and
  reclaimable space, with one line: "1 excluded directory, 2 contents marked always and 1 set of identical files
  kept as separate files, these are not counted as duplicates (use "filo duplicates" to view)". Only the kinds there
  are are named. The line "N copies marked for deletion" goes.
- **The words people see** (2026-11-16). This design calls them *records* and *always*; the output, help and README
  say "identical files kept as separate files" (a set's copies: "2 separate files") and "marked always". The report
  line was "not counted: … 1 set of records (filo duplicates)", which did not say counted as what, and "records" was
  never explained. The CSV's action words (`keep`, `delete`, `always`) and the code's names (`RecordSets`,
  `duplicate_records`) are unchanged.
- **The scenarios** (the test list): A a CSV run twice, B several CSVs, C editing the CSV, D files changing before the
  run, E inside one run, F new copies.

  | scenario | outcome |
  |---|---|
  | A1 a CSV run, edited further, run again | the groups done are gone (reported); the new decisions are carried out |
  | A2 a group changed between two runs of the same CSV | the second run does what the CSV now says, if the files are as exported |
  | B1 1.csv run after 2.csv | each run does what its CSV says, for the files still as exported |
  | B2 a CSV run after another run deleted some of its copies | their rows reported and ignored |
  | B3 exports of overlapping directories | fine: a group always comes with all its copies; one file given different actions in two exports refuses both groups |
  | C1 a row deleted from the CSV | the group skipped, unless that row's file is gone (B2) |
  | C2 a row moved to another group, added or duplicated | both groups refused; the rest processed; export again |
  | C3 partly filled, every row `delete`, `always` mixed, an unknown value | the group refused, with the reason |
  | C4 a copy deleted with a longer retention than every kept copy | the group refused: keep the longer copy |
  | C5 the spreadsheet sorted | fine |
  | D1 a copy edited after the export | the group skipped; export again to decide it |
  | D2 every copy edited the same way | skipped all the same |
  | D3 a copy moved or renamed | skipped; run `filo scan` and export again |
  | D4 a copy unreadable, or its drive not mounted | never counts as kept; the group skipped |
  | E1 A kept, B and C to delete: B gone | C deleted |
  | E2 A kept, B and C to delete: B and C gone | reported (removed outside filo) |
  | E3 A kept, B and C to delete: A gone | the group skipped: no copy would be left |
  | E4 the run interrupted | what was done stays done; the next run reports those groups as gone |
  | F1 a new copy of a content decided before (now one copy left) | a new group to decide |
  | F2 a new copy of an always content, or under an excluded directory | not a duplicate |
  | F3 a new copy of a set of records | exported with the records as `keep`, itself blank |

- **Register (schema version 8, planned)**: drops `duplicate_marks` and `duplicate_reviews` (and the review batches
  only they used); renames `separate_hashes` to `always_hashes` (existing separate contents become always); adds
  `duplicate_records` (file, hash, set) and `excluded_dirs` (root, path). `exports.kind` `separate` is no longer
  written. The upgrade drops the first design's marks and decision history (the real register's were all carried out
  already); each file's `deleted` events stay, as the history of what was deleted and which copy was kept. Marks not
  yet carried out are dropped too, and the run that upgrades says so once (agreed 2026-11-15): `filo: warning: the
  upgrade dropped 3 duplicate decisions not carried out (kept in the pre-upgrade snapshot); export duplicates again to
  decide them`, in the same line how many contents marked separate are now always. Dropping a mark never deletes
  anything, only forgets a decision, and the pre-upgrade snapshot keeps it; refusing to upgrade would need the old
  build to carry the marks out.
- **The first design, and why it changed** (2026-11-13/14, built; redesigned 2026-11-16). `filo update` recorded each
  group's Y/N decisions as marks in the register (two or more N marked the content separate, everywhere), and
  `filo delete` deleted the marked copies later, alongside retention candidates. A second export then showed decided
  groups blank, though the marks were stored. A fix that filled the cells in from the register (branch
  `fix-duplicates-includes-copy-decision`, kept at a4ffe5f) went through six code review rounds, each finding new
  cases, and was abandoned on 2026-11-15: the same decision lived in three places (the register, the CSV and the
  disk), each able to change on its own. Designing from the flow and its scenarios instead led to keeping no
  decisions to delete at all, and to splitting "separate" into records (these copies) and always (the content), since
  insurance terms kept as two records should not make every future copy of them acceptable.

### Commands and scheduling
- Plain names, easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete`, `duplicates` (added
  2026-11-16, see Duplicates; a `revert` was deferred). No themed subcommands. (`restore` was dropped on 2026-11-12:
  a file restored from a backup comes back as a new file.)
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
  name holding the same text. Since the terminal output components (2026-11-16, see Terminal output), the Renderer
  escapes every text it is given the same way (`view.Escape`) and then adds only its own colour codes, so it writes
  to stdout itself; since 2026-11-15 it is the only way to stdout, and stderr has one too, `errorLine` (each line
  escaped, `filo: ` before it), besides the progress line's own control codes. The first design's escaping writer
  (`safeText`) is retired, and a test (`TestOutputGoesThroughTheRenderer`) fails on any other write in the CLI (see
  Help and errors). Trailing alignment is dropped from a line, but a space that ends a file
  name is shown as `\x20` (2026-11-16, from the code review), so `report ` and `report` never read the same; and a
  relative path that starts with `~` (a folder named `~` inside a root) is shown as `./~/…`, so it never reads as a
  path in the home directory. The CSV export guards against spreadsheet formulas separately.
- **Prompts only in a terminal** (2026-11-12, replacing "no interactive prompts"). A command asks only where a person
  should look first: `filo update` before clearing dates, and `filo delete`. Declining, or having no
  terminal on standard input, is always the safe choice, so Filo still works the same from a script or a timer.
- `filo roots` lists roots (settings, mount point, files, size, last scan); `filo roots add DIR [--include-hidden]
  [--ignore NAME]... [--no-scan]` registers one and scans it (absorbing any roots inside it on the same filesystem,
  see Roots); `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...` replaces its settings; later
  `filo roots remove DIR`.
- `filo scan [DIR] [--workers N] [--allow-empty]` scans DIR, which must be inside a registered root (see Roots), with
  the root's settings; with no DIR, every root in turn, each in its own transaction. A root that fails (for example
  unmounted) is reported in its place on stdout (see Terminal output, Scan and roots) and the others are still
  scanned. `--workers` is 1–64, default 4.
- `filo report [--top N]`: see section 4.
- `filo export retention [DIR] -o FILE` and `filo update FILE [--dry-run]`: see Lifespan. The word after `export`
  names the CSV, so a later CSV is a new word, not a new command; `update` tells the kind from the CSV's header.
- `filo delete [DIR] [-y]`: lists the files past their keep-until date and deletes the ones chosen (see Disposal).
- `filo delete FILE.csv [-y]`: carries out a duplicates CSV (see Duplicates). An argument that is a directory is
  DIR; anything else is a CSV.
- `filo export duplicates [DIR] -o FILE`: see Duplicates.
- `filo duplicates`, `filo duplicates exclude DIR`, `filo duplicates include PATH`: what is
  not counted as duplicates (excluded directories, contents always, sets of records), and undoing it.
- `--db PATH` is accepted before or after any command; `FILO_DB` is the fallback, then the default location. Flags may
  come before or after PATH. Built on the standard library's `flag` package (no CLI framework).
- **A path that starts with `~` is refused** (2026-11-11): every DIR and FILE, `-o`, `--db` and `FILO_DB`.
  The shell expands `~` only when it is unquoted, so `"~/Documents"` would otherwise name a folder called `~` in the
  current directory (and `--db "~/f.db"` would create one). Filo does not expand it itself, because a name starting
  with `~` is legal. The one-line error suggests `$HOME/...` (or the full path for `~user`), and `./~...` for a name
  that really starts with `~`. Checked before the database is opened, so nothing is created.
- Output: see Terminal output (designed 2026-11-15; until it is built, output is plain text). Errors are one line,
  `filo: <message>`. Exit codes: 0 success (files that could not be read inside a completed scan are reported but do
  not fail it); 1 an error, or any root that failed in a scan of every root; 2 a usage error.
- A progress line (walking, hashing with files and bytes, writing) is drawn on stderr only when stderr is a terminal,
  so timer and cron logs stay clean. Ctrl-C or SIGTERM cancels a scan; nothing is written and the lock is released.
- A systemd user timer running `filo scan` is enough. No daemon. (`Persistent=true`, low CPU and I/O priority.)

### Terminal output (designed 2026-11-15)
The output worked but read badly, `filo update` and `filo delete` most: an absolute path on every line, refusals as
dense one-line messages on stderr (`filo: d.csv line 7: /long/path: group 3 of export 4: reason`), summaries in
whatever order the code reached them, no visual weight and no consistent next step. The terminal is treated as a UI:
output is built from components, so files, groups, problems and prompts look the same in every command. Designed one
topic at a time from mockups; the mockups below are the reference.

- **Rich in a terminal, plain when piped.** Colour and symbols (`✓` done, `✗` refused, `!` needs attention, `·`
  neutral, `◆` separate; green, red, yellow, dim) only when stdout is a terminal (one whose window size can be read,
  not just any character device) and `NO_COLOR` is unset and `TERM` is not `dumb`. Where the window size cannot be
  read that way (Windows), a terminal stays a terminal for asking and for cutting long lists, but gets no colour, since
  an older console shows colour codes as text. Piped,
  the words and their order are the same, with ASCII markers (`[ok]`, `[refused]`, `[!]`). ANSI codes are written
  by filo itself: no new dependency.
- **One shape for every command**: a heading line (what, how many, where), sections, a result line, then the next
  step ("Nothing has been deleted. Next: filo delete").
- **Paths.** With one root, the root is shown once in the heading and paths are relative to it. A DIR filter is named
  after it (`~/Documents, under video/`), but paths stay relative to the root, so a file reads the same in every
  listing. With several roots, a sub-heading per root. A duplicate group whose copies span roots shows full paths on
  its lines. The home directory shows as `~`. The last confirmation before a deletion (an item in select mode) shows
  the full path. A list gets a sub-heading per root only when its command says it is ordered by root (the delete
  list, broken symlinks), every row has a root, and some row is outside the heading's root; then every root gets one,
  the heading's too, so no row reads as part of the root above it. The component never guesses the order from the
  rows (it did, and a size-sorted list whose roots happened to sit together read as one ranking per root). Any other
  list (largest files, events) shows a row in the heading's root relative and any other in full. A file with hard
  links is listed once, with its links noted. A row with no path (a file with no current path)
  shows a label in brackets, never something that could read as a file name. A move shows both its paths, each as a
  name. In a duplicate group, a hard link is shown under its copy as the same file, never as another copy.
- **Streams and exit codes.** Refusals and skips are part of the report: stdout, in their section, so a log holds the
  whole story in order. stderr carries only errors that stop the command, one line `filo: <message>`. Exit codes are
  unchanged: 0 nothing went wrong (rows unchanged, groups left alone, declining a prompt, and a file `filo delete`
  skipped because it changed, all included), 1 an error, a CSV row or group refused, or a file `filo delete` could
  not read, delete, or tidy up after, 2 a usage error. Output that could not be written (a full disk, a closed pipe)
  makes a command exit 1 with the reason, so a scheduled `filo report > log` that fails is not taken for success. A
  finer split waits for scheduled scans.
- **Long lists**, three kinds:
  - *About to be deleted* (the `filo delete` overview): in a terminal 10 per section, then `… N more`, and the
    question gains `l` to list everything and ask again (`[a/s/l/N]`). If the list was cut, `a` first shows every
    file and asks once more ("Delete these 300 files? [y/N]"), so nothing is deleted that was not on screen just
    before the last question (2026-11-16, from the code review: CLAUDE.md's "anything destructive shows what it will
    do"). Piped, with `-y`, or with no terminal to ask in: always the full list, since the output is then the record
    (and with no question, `l` could not be asked for).
  - *Needs fixing* (refusals, skips): every item; more than 10 with the same reason collapse into one line with the
    count and the line range (a sorted CSV can refuse every row for one reason). Skips that come from no CSV line are
    always listed one by one. A refused duplicates group shows its number in a group column (and its export only
    when the CSV mixes exports), and the paths inside a reason are shown relative to the heading's root, like the
    columns.
  - *What a confirmation covers* (clears, contents always and sets of records, files now past their retention): the
    first 10 and "and N more", and the prompt offers `l` to list everything before answering (`[y/N/l]`).
- **Width.** A path is never cut, not even with `…`: every path printed is about to be acted on or names something to
  find. Short fixed columns (date, size, marker) come first and the path last; an entry whose path does not fit puts it
  on its own indented line, and the terminal wraps it if needed. Section totals sit at the right edge, one column short
  of it (some consoles wrap as soon as the last column is written). Piped, there is no width: one line per entry. The
  width is read with the standard library's `syscall` behind a small Linux-specific function; 80 columns when it cannot
  be read.
- **Prompts.** The question states the effect with the numbers ("Delete all 14"). The default is capitalised and
  always the safe choice (`[y/N]`, `[a/s/l/N]`); Enter takes it. A word or its first letter, any case. An
  unrecognised answer asks again with the choices (it used to mean no). End of input means the safe default; Ctrl-C
  stops as before. Select mode shows `[3/14]`, what the item is and its full path, the choices on one line, and one
  result line after each answer; `q` goes to the summary. Filo asks only when both the question and the answer are in
  a terminal (stdin and stdout): with stdout in a file (`filo delete > log`) the question could not be seen, so it is
  not asked (2026-11-16, from the code review). Otherwise nothing is recorded or deleted, and one line says so and how
  to go on.
- **Components** (package `internal/view`). One `Renderer` holds what every component needs: the escaping writer
  (see "Output cannot act on the terminal"), whether this is a terminal, colour, the width, the home directory and
  the roots. The components: `Heading`, `Section` (title, totals at the right edge, body), `Counts` (marker lines),
  `FileList` (rows of date or retention, size, path and note; grouped by root; wrapping; `… N more`), `DupGroup`
  (`contract.pdf · 8 MB each` with its keep and delete lines and symlink notes), `Problems` (line, file, reason and
  what to do; repeats collapsed), `Prompt`, `Item` (a select-mode item and its result) and `Next`. Commands build the
  data and hand it to components; they never format output themselves or decide plain or rich, relative or full
  paths. Each component writes whole lines, one write per line. Not called `ui`: the "UI in place of the CSV" is a
  different, later thing.
- **Scope: every command** (roots, scan, report, export, update, delete, help and errors), in this order: `delete`,
  `update`, `report`, `scan` and `roots`, then `export` and help.

The reference mockups (rich form; colour not shown):

```
$ filo update retention-notes.csv

Retention update   export 12 · 48 rows · ~/Documents

  ✓ 41 files given a retention
  · 5 rows unchanged
  ✗ 2 rows refused

Refused: fix these rows in the CSV and run filo update again
  line  7  invoices/2019-03.pdf       retention 7y needs a file_date
  line 31  notes/old draft.txt        changed since the export: export again

! 3 files are now past their retention. Check for a typo, such as 7d for 7y:
       retention   until
       7d          2023-01-08   receipts/2023-01-01 coffee.pdf
       2y          2021-06-30   tax/2019-06-30 statement.pdf
       2015-01-01  2015-01-01   old/letter.doc

Nothing has been deleted. Next: filo delete
```

```
$ filo delete dupes.csv

Duplicates   dupes.csv · export 7 · 12 groups · ~/Documents

Will delete                                              9 copies in 6 groups · 1.2 GB
    report.pdf · 4 MB each
      keep     Documents/report.pdf
      delete   Downloads/report.pdf
    contract.pdf · 8 MB each
      keep     a/contract.pdf          forever
      delete   b/contract.pdf
      delete   c/contract.pdf
               ↳ the symlink ~/Desktop/contract points here and would break
    … 4 more groups

Will record
  ◆ kept as separate files: terms.pdf (2 copies)
  ◆ content marked always (never a duplicate): LICENSE (3 copies)

Won't touch
  ✗ group 3   line 12  IMG_2041.jpg   every copy is delete: no copy would be left
  ✗ group 5   line 19  photo.jpg      b/photo.jpg changed since the export: export again
  · 3 groups not decided (blank)

Delete 9 copies and record 2 decisions? [y/s/l/N]
```

```
$ filo delete

Ready to delete   5 files · 820 MB · ~/Documents

Past their retention                                     5 files · 820 MB
    until        size
    2024-12-31   12 MB   tax/2017 return.pdf
    2025-06-30    3 KB   notes/meeting.txt
    2025-09-01  640 MB
      video/2015/summer holiday in the south of France with the whole family, day 3.mp4
    2025-11-01  160 MB   backups/old-phone.zip
    2025-11-14    8 KB   receipts/2018-11-14 taxi.pdf

Delete all 5, select one by one, list them all, or none? [a/s/l/N]
```

```
[2/6]  contract.pdf · 8 MB each                      (filo delete dupes.csv, s)
         keep     ~/Documents/a/contract.pdf          forever
         delete   ~/Documents/b/contract.pdf
         delete   ~/Documents/c/contract.pdf
       d do it · s skip · q quit  › d
       ✓ deleted 2 copies

[4/5]   past its retention · until 2024-12-31 · 12 MB  (filo delete, s)
          ~/Documents/tax/2017 return.pdf
        d delete · r new retention · s skip · q quit  › r
        retention (7y, forever or a date)  › 10y
        ✓ kept until 2027-12-31
```

```
Done   deleted 3 files · freed 812 MB · removed 1 empty folder

Skipped 1: nothing was changed
  tax/2017 return.pdf    changed since its retention was set: give it a new retention

  · 1 file given a new retention
  · 0 left for later
```

Piped or with `NO_COLOR`, the same text with ASCII markers:

```
Duplicates   dupes.csv . export 7 . 12 groups . ~/Documents
  [ok] 9 copies to delete in 6 groups
  [refused] 2 groups (listed below)
```

**Scan and roots** (agreed 2026-11-15, one topic at a time):

- **A scan** is a heading (scan number, files seen, how long, the root or DIR), then one count line per kind of
  change happened: new, changed, moved and missing are information (`·`; missing is usually a file the person
  deleted), a possible move needs attention (`!`), and files or directories that could not be read are `✗` (`2 could
  not be read: none marked missing`; it covers new files that could not be read and files that vanished during the
  scan, so it says no more than is true of all of them). Nothing found: `✓ no changes`, right under the heading, so a
  clean scan from a timer is two lines. Then the events (`Events   up to 10 of each kind`), as the report's event rows
  (same columns and words, relative to the root): the first 10 of each kind (`reconcile.SampleSize`; the first, not
  the latest), and a kind that had more gets one `… N more <kind>` line (no command lists all of a scan's events
  yet; see Open questions). Directories that failed to list are already error events. A directory on another
  filesystem is shown (`· 1 directory on another filesystem, not scanned`, event word `other filesystem`) but is not
  a change: every scan finds it. `Next: filo report` only when something changed, failed or needs attention.
- **`filo scan` of every root**: one block per root, each with its own heading, so it reads like a scan of that root
  alone. A root that fails is a block of its own on stdout, right under its heading, in order between the others:
  `✗ not scanned: the root looks unmounted (/mnt/disk is not mounted)` for an unmounted disk, the error's own text
  otherwise. The run then ends with one line on stderr, `filo: 1 root could not be scanned: /mnt/disk/photos`, so a
  timer that keeps only stderr (`filo scan >/dev/null`) still logs it (agreed 2026-11-15, from the second code
  review; an exception to "stderr only for errors that stop the command"). The exit code stays 1. With more than one
  root, a closing `Done   3 roots · 2 scanned · 1 failed`, and one `Next:` for the whole run (only if a root changed
  or failed), none per block.
- **The progress line** (stderr, only in a terminal): numbers first, the root or DIR last with `~`, `root 2 of 3`
  when scanning every root: `hashing · 1,204 / 52,310 files · 3.0 / 118 GiB · root 2 of 3 · ~/Pictures`. It is cut
  to stderr's width with `…`, the one place a path is cut: the line is transient and never acted on, and a line that
  wraps is not cleared by `\r\x1b[K`. The width is read again at each redraw (the window may be resized during a
  long scan), and is 80 columns where it cannot be read, so the line is always cut. It is drawn only on a terminal
  whose size can be read (not on another character device, as for prompts), and not with `TERM=dumb` or where the
  terminal cannot be checked (Windows): there its control codes would show as text. No colour, percentage or time
  left.
- **`filo roots`** and the report's Roots section are one renderer: per root its path, files, size and symlinks
  (left out when there are none); the heading as every heading's, the number of roots last (`21 files · 1.4 MB · 2
  roots`);
  its settings, always (`hidden files skipped · ignoring build, dist`, without the names always skipped); the mount
  point; the last scan as the report shows it. No live check of whether a root is reachable. No roots: `Roots   none
  yet` and `Next: filo roots add DIR`.
- **`filo roots add`**: `Root added` with the settings and mount point; the roots it took in in their own section,
  one line each, relative to the new root, `·` when its settings differed and `!` when the new settings skip it; for
  a skipped one, `Next:` gives the exact `filo roots set` command, built from the root's current settings, that would
  scan it again. Then the scan block, or with `--no-scan` `Next: filo scan PATH` (only when no `roots set` was
  suggested: one next step, and a scan follows that anyway). Ctrl-C during that first scan is
  one line on stderr: the root is added, nothing from the scan was written, and the `filo scan PATH` to run.
- **`filo roots set`**: `Settings changed` with `was` and `now` on two aligned lines, then what the change does:
  `!` with how many registered files and symlinks the new settings skip (neither checked nor marked missing; counted
  before the settings are written, so a failure changes nothing), `·` when the next scan will add files (hidden files
  as new files, each needing a retention); `Next: filo scan PATH`. Unchanged: `Settings unchanged` and the settings,
  no next step, and nothing written (so no snapshot rotates a useful one away).
- **Paths in a `Next:` line** are in full, never `~` (a quoted `~` is refused), and single-quoted when they hold
  anything but letters, digits and `/._-`, so the line can be pasted into a shell.

```
$ filo scan ~/FiloTest/docs

Scan   scan 12 · 15 files seen · 1.2 s · ~/FiloTest, under docs/

  · 3 new
  · 2 changed
  · 2 moved
  · 2 missing
  ! 1 possible move, not applied
  ✗ 2 could not be read: none marked missing

Events                                                       up to 10 of each kind
    event
    changed         notes/todo.md   content
    moved           docs/contract.txt -> notes/contract-final.txt
    possible move   docs/report-2024.txt -> archive/report-2024.txt   same name
    missing         old/letter.doc
    error           locked   open /home/you/FiloTest/locked: permission denied
    … 40 more missing

Next: filo report
```

```
$ filo scan

Scan   scan 13 · 15 files seen · 1.2 s · ~/FiloTest
  ✓ no changes

Scan   /mnt/disk/photos
  ✗ not scanned: the root looks unmounted (/mnt/disk is not mounted)

Done   2 roots · 1 scanned · 1 failed
Next: filo report
```

```
$ filo roots

Roots   21 files · 1.4 MB · 2 roots

  ~/FiloTest                                    15 files · 1.2 MB · 1 symlink
    hidden files skipped · ignoring build, dist
    last scan 3, 2026-11-15 10:02, whole root: 15 seen, new 0, changed 0, moved 0, missing 0, errors 0
  /mnt/disk/photos                              6 files · 200 KB
    hidden files included
    on the filesystem mounted at /mnt/disk
    never scanned
```

```
$ filo roots add ~

Root added   ~
  · hidden files skipped · ignoring build

Took in 2 roots, with their history and retentions
  ! .config   skipped by these settings: its files are neither checked nor marked missing
  · Documents   had other settings (hidden files included): these apply now
Next: filo roots set /home/you --include-hidden --ignore build, to scan .config again

Scan   scan 1 · 48,210 files seen · 3 min 12 s · ~
  …
```

```
$ filo roots set ~/FiloTest --ignore build --ignore cache

Settings changed   ~/FiloTest
  · was   hidden files included · ignoring build
  ✓ now   hidden files skipped · ignoring build, cache
  ! 1,204 registered files and symlinks are now skipped: neither checked nor marked missing
Next: filo scan /home/you/FiloTest
```

**Export** (agreed 2026-11-15, one topic at a time):

- **`filo export retention`**: a heading, `Retention export`, with the export number (the one `filo update`
  refers to), the number of files and the place (`~/Documents, under tax/`, as everywhere); a count line of files
  without a retention and with one (`·`); a `!` line when file_dates were filled in from file names, since a wrong
  guess moves every period counted from it; then `Next:` with the forms a retention takes and the `filo update`
  command. The CSV's path appears once, in that `Next:` line, in full and quoted when needed (see "Paths in a `Next:`
  line"), not also on a "written to" line. The full explanation stays in `filo help export`. Nothing to export:
  `Retention export   no files · <place>` and `Nothing exported.`
- **`filo export duplicates`** has the same shape: `Duplicates export` with the export number, groups, copies and the
  place; the Y/N rules as `·` lines (too long for the `Next:` line); `Next:` with the `filo update` command only.
  `--separate` is `Separate contents export` (contents and copies) with its one rule. Copies in the heading count
  every copy in the CSV, also those outside DIR (a group comes with all its copies). Built now, though the duplicates
  redesign changes its words (the `action` column, `filo delete FILE.csv`, no `--separate`), so that nothing is left
  printing outside the components and `safeText` can be retired; the redesign keeps the shape and rewords it.

```
$ filo export retention ~/Documents/tax -o /tmp/l.csv

Retention export   export 12 · 48 files · ~/Documents, under tax/

  · 41 without a retention, 7 with one
  ! 6 file_dates filled in from the file name: check them before counting from them

Next: fill in retention (7y, 18m, forever or a date), then filo update /tmp/l.csv
```

```
$ filo export duplicates ~/Documents/photos -o /tmp/d.csv

Duplicates export   export 13 · 12 groups · 31 copies · ~/Documents, under photos/

  · in each group you decide on: Y under delete for the copies to delete, N for those to keep
  · two or more N: identical copies meant to exist, no longer listed as duplicates

Next: filo update /tmp/d.csv
```

```
$ filo export duplicates --separate -o /tmp/s.csv

Separate contents export   export 14 · 3 contents · 7 copies · ~/Documents

  · blank a group's delete cells to make it a duplicate group again

Next: filo update /tmp/s.csv
```

```
$ filo export duplicates ~/Documents/photos -o /tmp/d.csv

Duplicates export   no groups · ~/Documents, under photos/
Nothing exported.
```

**Help and errors** (agreed 2026-11-15, one topic at a time):

- **Help** (`filo help`, `filo help COMMAND`, `-h`) goes through the Renderer: headings bold in a terminal, plain
  when piped. Its paragraphs are stored unwrapped and wrapped at word boundaries by a `Paragraph` component, to the
  terminal's width, or 80 columns when piped (so `filo help > file` reads well); the help was hand-wrapped at about
  115 columns, and one paragraph was a single 216-column line. Commands and flags keep two columns, a description
  that does not fit wrapping under its own column. The words stay as they are, regrouped under `Commands` and
  `Flags` with a usage line per command (the long report paragraph split into sentences); rewriting them is separate
  work. The overview ends with `Next: filo help COMMAND for a command's flags and rules`.
- **Errors and warnings** stay on stderr, plain (no colour, symbols or `~`: they end up in logs and get copied, and a
  full path is unambiguous there), one line each, `filo: <message>` and `filo: warning: <message>`. A usage error is
  one line too, pointing at the command's own help, `filo: roots add takes exactly one DIR (see filo help roots)`
  (it was two lines, the second "Run 'filo help' for usage."); exit code 2 as before. An error is always one line: a
  newline in it is escaped like any other, since a file name can hold one and must not forge a second `filo:` line
  (first designed as "split at newlines", changed on 2026-11-15 after the code review found that let a name do it).
  Filo's own errors of several parts (`errors.Join`, as the snapshot warnings) are split by the code that knows the
  parts, a line each. The wording of the errors is not reviewed here; a test runs the error cases and checks each is
  one line.
- **`safeText` retired.** Two ways out remain: stdout only through the Renderer (which escapes every text it is
  given), and stderr only through `errorLine`, which escapes the text with `view.Escape` and writes `filo: <text>`
  in one write; `rawStderr` is left only for the progress line, whose text `view.Fit` escapes. `safeText`,
  `writeLines` and `env.stdout` go. A guard test, like the store's `TestWritesGoThroughTheHelpers`, fails if anything
  else in `internal/cli` writes with `fmt.Fprint*`, `io.WriteString`, to `os.Stdout`/`os.Stderr`, or with `Write` or
  `WriteString` on the streams; the writers allowed are named by receiver and name (`env.errorLine`,
  `progress.draw`), so another function with the same name is not.

```
$ filo help

filo   keeps a register of your files, where they live, and how long to keep them

Commands
  roots                    list roots and their settings
  roots add DIR            register a directory as a root, then scan it
  roots set DIR            replace a root's settings
  scan [DIR]               scan every root, or DIR inside a root
  report [--top N]         totals, files needing a retention, largest files, duplicates, events
  export retention [DIR] -o FILE
                           write the files under DIR with their retention, as CSV
  …

Every command accepts --db PATH (default: $FILO_DB, then the user data directory).
Next: filo help COMMAND for a command's flags and rules
```

```
$ filo help scan

filo scan [DIR] [--workers N] [--allow-empty]

  Scans DIR, which must be inside a root (a partial scan), or every root if DIR is
  omitted, with each root's settings (see filo help roots).

Flags
  --workers N              files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)
  --allow-empty            record an empty scan of a root on a mounted filesystem
```

```
$ filo roots add
filo: roots add takes exactly one DIR (see filo help roots)
```

## 3. Data model (schema version 8)

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
    after_scan INTEGER REFERENCES scans(id),           -- the latest finished scan at export
    kind TEXT NOT NULL DEFAULT 'retention');           -- version 7: 'retention' or 'duplicates' ('separate',
                                                       -- the first design's, no longer written since version 8)
CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id), file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,                          -- the content shown; the update refuses the row if it has changed
    keep_until TEXT,                    -- version 4: the date exported
    file_date TEXT, retention TEXT,     -- version 5: as exported (file_date as filled in from the name); cells
                                        -- left as these are untouched
    dup_group INTEGER,                  -- version 7: duplicates exports: the group number in the CSV
    path_id INTEGER REFERENCES paths(id),              -- version 7: duplicates exports: the path shown
    PRIMARY KEY (export_id, file_id));
CREATE TABLE review_batches (           -- version 8 dropped the batches only the first design's duplicates used
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

-- version 8: duplicates (the redesign; version 7's duplicate_marks, duplicate_reviews and separate_hashes are
-- gone: no decisions to delete are stored, see Duplicates)
CREATE TABLE always_hashes (            -- contents that belong everywhere: never a duplicate group
    hash TEXT PRIMARY KEY, added_utc TEXT NOT NULL);    -- from version 7's separate_hashes, with its batch's time
CREATE INDEX ix_files_inode ON files(inode);                -- version 7: hard links
CREATE TABLE duplicate_records (        -- copies kept on purpose together; a file is one only while hash = files.hash
    file_id INTEGER PRIMARY KEY REFERENCES files(id), hash TEXT NOT NULL,
    set_id INTEGER NOT NULL, added_utc TEXT NOT NULL);
CREATE INDEX ix_duplicate_records_set ON duplicate_records(set_id);
CREATE TABLE excluded_dirs (            -- files under them (the whole subtree) never count as duplicates
    root_id INTEGER NOT NULL REFERENCES roots(id),
    path TEXT NOT NULL,                 -- relative to the root, '/'-separated; '' = the whole root; moved with
                                        -- the root when a new root takes it in
    added_utc TEXT NOT NULL, PRIMARY KEY (root_id, path));
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
`scan`); see Disposal. Version 7 (2026-11-14) adds `exports.kind` (existing exports are `retention`),
`export_rows.dup_group` and `path_id`, and the three duplicates tables; see Duplicates.

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
5. **Duplicates CSV** (taken ahead of the order below on 2026-11-14; see Duplicates). Done when: on a test tree, an
   export, edit, update and delete round trip deletes exactly the copies marked Y; refuses an all-Y group, a partly
   decided group, a sorted row, a moved or changed copy and a Y with a longer retention than every N; asks before
   recording anything refused or any separate content; marks a content separate and undoes it; and `filo delete`
   skips a group whose kept copy was deleted or changed after the update (`TestDuplicatesRoundTrip` in
   `internal/cli`, plus the store tests).

**Next, in order** (agreed 2026-11-13; revert, the earlier iteration 4, was deferred, see section 7; items 1 and 2
put first on 2026-11-15). Each is an open question below until it is designed.

1. Terminal output (designed 2026-11-15, see Terminal output): `internal/view` and its components, then the commands one
   at a time (`delete`, `update`, `report`, `scan` and `roots`, `export` and help). `internal/view` and `delete` done on
   2026-11-16 (component tests in `internal/view`, golden files `internal/cli/testdata/delete-*.txt`); `update`,
   retention and duplicates CSVs both, done on 2026-11-16 (golden files `testdata/update-*.txt`; every question in
   `delete` and `update` goes through one helper, `askChoice`). `report` done on 2026-11-16 (golden file
   `testdata/report.txt`: every section, an empty one as one line ending in "none" (Roots and Register snapshots say
   "none yet" with what to do; Duplicates says "no groups" when it still lists separate or marked contents); recent
   events as a list with scan and event columns). Done when each command's mocked scenarios have golden tests of the
   plain output, and read as designed by hand in a terminal, a 40-column terminal, piped through `cat` and with
   `NO_COLOR=1`. First, since the duplicates re-plan changes what `update` and `delete` print and can then use the
   components. `scan` done on 2026-11-15 (golden file `testdata/scan.txt`: every kind of change with a kind cut at
   ten, a clean directory scan, a disk mounted inside the root, every root with one unmounted; the progress line in
   `TestProgress`; reviewed 2026-11-15). `roots` done on 2026-11-15 (golden file `testdata/roots.txt`: no roots,
   add with --no-scan and with a mount point, taking in four roots with one now skipped, the list, set covering more,
   unchanged and skipping more; `TestRootsAddInterrupted` for Ctrl-C in the first scan; the report's Roots section
   now shows each root's settings, from the same renderer). `export` done on 2026-11-15 (golden file
   `testdata/export.txt`: a retention export with a file_date read from a name, nothing to export, a duplicates
   export with a path that needs quoting, `--separate` with nothing marked; see Terminal output, Export). Help and
   errors done on 2026-11-15 (golden files `testdata/help*.txt`, one per page, wrapped at 80 columns; usage errors
   one line with their help in `TestUsageAndHelp`; `TestErrorLine`; `TestOutputGoesThroughTheRenderer`). Nothing
   prints outside the components now, and `safeText` is retired: the terminal output is done.
2. Duplicates redesign (2026-11-16, see Duplicates). Done on 2026-11-16: `filo delete FILE.csv` carries out a duplicates CSV with no
   stored decisions to delete; the `action` column (`delete`, `keep`, `always`); records and always; `filo
   duplicates` with excluded directories; schema version 8. Done when every scenario in its table has a test, and the
   README explains the four kinds of keep and the rules. After item 1, so its output uses the components.
   Planned on 2026-11-15, in five slices, each with its scenario tests written first, from the table: (1) the
   register, schema 8 (the tables, the new group definition, the report's "Not counted" line; the first design's
   marks, `filo update` on a duplicates CSV, the marked copies in `filo delete` and `--separate` go in the same slice,
   since their tables go; F2, the upgrade); (2) the CSV (`action` column, `keep` filled in for records, reading the
   words, the group rules, the structure checks and refusing an older or a retention CSV; C2, C3, C5, F3); (3)
   `filo delete FILE.csv` (the check against the disk, the retention rule, the kept-copy check at the moment of
   deleting through `dispose`, recording records and always, the output as mocked; A, B, D, E, C1, C4); (4)
   `filo duplicates` list, `exclude` and `include` (mockup to agree first); (5) README, help and status. Between
   slices 1 and 3 duplicates cannot be deleted at all (accepted: the real-data run waits for the redesign anyway);
   slices 1 to 4 stay on one branch, so `main` never sits in that gap nor points at a `filo duplicates` not yet
   built (changed from 1 to 3 after the review of slice 1: its messages name `filo delete FILE` and `filo
   duplicates`, true only once those slices land). The branch's build is not run on the real register before slice
   3: the upgrade's warning is shown once, and its advice (export again to decide) can only be followed from then. Slice 1 done on 2026-11-15
   (`TestDuplicateGroups` for the group definition, F2 and F3; `TestMigrateFromV7` and `TestMigrateFromV7Quiet` for
   the upgrade; `TestNotCounted`, `TestAbsorbMovesExclusions`; `TestUpdateRefusesDuplicatesCSV`; and by hand: a
   register made by the release before, with a mark and a separate content, upgraded by this build, the warning
   once, nothing deleted). The tests of the first design's flow went with it; their scenarios return in slices 2
   and 3, the bind-mounted copy in the check at the moment of deleting among them. Reviewed the same day: a copy is a record if any of
   its paths is (hard links held as two file rows, and bind-mounted paths merged by the export, which then decides
   again which contents are groups), and "not counted" counts only what would otherwise be a group. Slice 2 done on
   2026-11-15: `duplicates.ReadCSV` refuses a CSV of the first design, a retention CSV and one without an action
   column; `duplicates.Plan` applies the group rules and the structure checks to the rows, against the groups the
   register recorded for each export (`store.ExportedGroups`), and notes each group's exported copies with no row in
   the CSV for slice 3's check against the disk (C1). Tests: `TestParseAction`, `TestPlanGroupRules` (every row of
   the rules table, C3), `TestPlanStructure` (C2, C5, C1), `TestWriteCSV` (F3), `TestReadCSVRefusesOtherCSVs`, and
   `TestDuplicatesCSVRoundTrip` from export to plan; the export's words as agreed (Duplicates, Output). A CSV may
   hold groups of several exports, each row checked against its own (agreed 2026-11-15); and it may be partial: groups
   deleted from it are not planned and blank ones are left alone, so a CSV can be worked through and run several
   times.
   Slice 2 reviewed on 2026-11-16: a row listed twice now refuses every group it is listed in and its exported
   group whichever row comes first (before, with the original first, its group was still decided: sorting changed
   the outcome); both orders are in `TestPlanStructure`. And a file given different actions in two exports in one CSV now refuses
   both groups (B3 in `TestPlanStructure`).
   Slice 3 done on 2026-11-16: `filo delete FILE.csv` (`internal/cli/dupdelete.go`; a regular file as the argument
   is a duplicates CSV, a directory is DIR as before), with `store.ExportedCopies` (each exported copy and its state
   in the register now), `store.RecordSet` and `store.AddAlways`, and `fsid.Links` for the hard-link rule. A run
   exits 1 when the CSV refuses a group (as `filo update` does for a refused row), 0 when groups are only skipped.
   Tests: `TestDuplicatesDeleteRunTwice` (A1, A2), `TestDuplicatesDeleteSeveralCSVs` (B1-B3),
   `TestDuplicatesDeleteEditedCSV` (C1 both ways, C4), `TestDuplicatesDeleteChangedFiles` (D1-D4, an unreadable
   copy kept and an unreadable folder), `TestDuplicatesDeleteInsideARun` (E1-E4), `TestDuplicatesDeleteSameFile`
   (a bind mount after the export, a hard link), `TestDuplicatesDeleteAsking`, and the golden files
   `testdata/delete-duplicates-{yes,again,select}.txt`; checked by hand on a throwaway tree, run twice. Reviewed
   the same day, all nine findings fixed (`TestDuplicatesDeleteReviewFindings`, and a hard link made during the
   check in `dispose`'s `TestDeleteDuplicate`): one group in two overlapping exports with the same actions is
   carried out once (the later says "the same as group N of export M"); a refused group exits 1 also when nothing is
   done; records asked for with one copy to keep left say "nothing to record"; the hard-link rule is checked again
   at the last look (`dispose.OnlyLink`; where links cannot be counted, nothing is deleted); an argument that is not
   a directory is a CSV, so a mistyped name fails as "open FILE"; readability and hard links are checked only for a
   group with something to delete, and one readable copy to keep is enough; groups are matched in one pass; the
   "moved since the export (now X)" path is the first by root and path. The help for `filo delete` still names DIR only (slice 5).
   Slice 4 done on 2026-11-16: `filo duplicates`, `exclude DIR` and `include PATH` (`internal/cli/dupcmd.go`,
   `internal/store/notcounted.go`), as in the mockups under Duplicates, Output. The list shows the contents always
   and the sets of records with two or more copies present, as the report's "not counted" line counts them (a set
   or a content with one copy left keeps nothing out, so it is not listed; it stays stored). `include` on a file
   that is both a record and always undoes the record and says the content is still always; a file not in the
   register is an error ("run filo scan"); a directory that is not excluded but has exclusions under it says so.
   Exclusions are matched by exact path prefix (never as LIKE patterns). Tests: `TestDuplicatesList`,
   `TestDuplicatesExclude`, `TestDuplicatesInclude`, `TestDuplicatesUsage`, and the golden file
   `testdata/duplicates.txt`. Reviewed the same day; nine of ten findings fixed (`TestDuplicatesReviewFindings`):
   `include` finds a PATH no longer on disk by its path alone (`store.Locate`), so the exclusion of a folder since
   deleted or unmounted can still be removed, and a record whose folder went still included; the list counts as the
   report does (a content always is listed as always only; a set with a new copy is a group again, so not listed,
   though `include` still removes it); `include` sees a file's copy as the list does (a hard link of a record is a
   record; copies counted the same way, excluded ones left out); `filo duplicates` has its help page
   (`testdata/help-duplicates.txt`); the "dir or under it" test is one SQL fragment (`atOrUnder`); `store.AbsPath`;
   "1 file … counts". Not changed: `exclude` and `include` count the groups before and after (the design's sanity
   check), and the list counts each exclusion on its own (they are few).
   Slice 5 done on 2026-11-16: the README's Duplicates section (the rule, the four kinds of keep, the group rules,
   retentions across copies, the list and its checks, `filo duplicates`), its status, commands, exit codes and
   roadmap; the help overview lists `delete FILE` and `duplicates`, `filo delete`'s page covers a duplicates CSV
   (golden files `help.txt`, `help-delete.txt`, `help-export.txt`). The redesign is done: every scenario in its
   table has a test (A-F: slices 1-3; the plan's C2, C3, C5 in `internal/duplicates`). Still to do: run it on the
   real register (back up first; the upgrade to schema 8 warns once), then merge the branch.
3. Named retention policies (designed 2026-11-16, see Lifespan, Named retention policies), on branch
   `retention-policies`, in four slices, each with its scenario tests written first: (1) the register and store,
   schema 9 (the tables, policies worked out into `keep_until`, the status *needs a file_date*, `policy_changes`;
   G1, B3, F1, E2's status); (2) the retention CSV (policy names in the cell, `filo update`'s create question, the
   refusals, the export; A1-A3, B1, B2, B4, B5, E3); (3) `filo policies` (list, add, set, move, remove, show and
   their questions; A4, C1-C4, D1, D2, E1, E2); (4) the report's line and count, the README (with the date rule),
   the help. Done when every scenario has a test.
   Slice 1 done on 2026-11-16 (`internal/store/policies.go`, schema 9): `PolicyName`, `AddPolicy`, `Policies`,
   `Policy`, `PreviewPolicy` and `SetPolicy`, `PreviewMove` and `MovePolicy`, `RemovePolicy`, `PolicyFiles`; a file
   on a policy reads with the policy's name as its retention (`FileItem.Policy`, `PolicyValue`), also in the
   duplicates CSV; the status *needs a file_date*; `SetRetention` (filo delete's prompt) takes a file off its policy.
   `RemovePolicy` gives the deleted and missing files still on the policy its value as a retention of their own, so a
   missing file that comes back keeps its keep_until. `files.policy_id` is not declared a foreign key (ALTER TABLE
   cannot drop such a column, which the upgrade tests need); the store keeps it pointing at a policy. Tests:
   `TestPolicyName`, `TestAddPolicy` (A4), `TestPolicyKeepUntil` (B3, F1, E2's status), `TestSetPolicy` (C1-C4 in the
   store), `TestMoveAndRemovePolicy` (D1, D2), `TestSetRetentionLeavesThePolicy` (B4 in the store),
   `TestMigrateFromV8` (G1); by hand, a register made by the `main` build upgraded by this one (schema 9, the
   pre-upgrade snapshot). Reviewed the same day; nine of ten findings fixed (`TestPolicyReviewFindings`,
   `TestDuplicatesDeleteNeedsFileDate`), the tenth (a policy file's name in the CSV is refused by `filo update`) is
   slice 2's: `filo update` setting a retention of its own takes a file off its policy too; `SetPolicy` and
   `MovePolicy` count present files (as the previews do) and the history counts the dates changed; removing a period
   policy gives a missing file with no file_date no retention, rather than a period with nothing to count from;
   policies are read inside the transaction that changes them; a `Filter.PolicyID` replaces loading every file; one
   SQL spelling of a file's retention (`retentionColumns`), so the duplicates code sees the policy's value; policy
   names are checked in ASCII before lowering (no Kelvin sign as k); the preview leaves the status out. **In the
   duplicates rules** (agreed 2026-11-16): a copy to delete that needs a file_date refuses its group ("needs a
   file_date first"), since its retention's end is not known.
   Slice 2 done on 2026-11-16: a policy name in the retention CSV (`retention.ParseRetention` returns it in lower
   case; `store.IsPolicyRef` tells it from a period, a date and `forever`); `store.UpdateRetentionWith` puts the file
   on the policy and works keep_until out from its value, lists the new policies (`NewPolicy`) and creates them only
   if `ConfirmPolicies` says yes (declined, or no terminal: those rows are left alone; a dry run lists them); a row
   naming a period policy with no file_date is refused, which also refuses clearing the file_date (E3); the stored
   retention is read as the policy's name, so an export of a policy file updates untouched (the review's tenth
   finding). `filo update` shows the "New policies" block and asks `Create 2 policies? [y/N]` before the clears.
   `filo delete`'s new-retention prompt takes a policy name too: an existing policy puts the file on it, an unknown
   one is refused there ("no such policy"), since that prompt does not create one; `KeepUntil` refuses a name, as a
   safety net. Every policy name is a valid cell now, so a lone `y` or `indefinitely` would be a new policy: the
   question catches them. Tests: `TestParseRetention`, `TestUpdateRetentionPolicies` (A1, A2, B1, B2, B4, B5, E3),
   `TestSetRetentionPolicy`, `TestUpdatePolicies` (golden `testdata/update-policies.txt`). Reviewed the same day,
   all ten findings fixed: a file_date suggested from the name is kept for a policy whose value is a period (the rule
   looked at the name, so such rows could never apply); `filo update` counts the files put on a policy with no value
   apart ("they need a retention until it has one"); filo delete's prompt says when a policy has no value yet, shows
   policy names in its refusal, and treats only "no such policy" and "needs a file_date" (`NeedsFileDateError`) as
   refusals, any other error ending the command; an update looks each policy up once (`policyCache`) and keeps its
   ID for the write; `policyJoin` with `retentionColumns` is the one spelling, used by `filo update`'s check too.
   **In the duplicates rules** (agreed 2026-11-16): a copy to delete on a policy with no value yet refuses its group
   too ("its policy payslip has no value yet"). Tests: new cases in `TestUpdatePolicies`, `TestDeletePromptPolicies`,
   `TestDuplicatesDeleteNeedsFileDate`.
   Slice 3 done on 2026-11-16: `filo policies` (`internal/cli/policies.go`), as mocked: the list (a `view.Table`:
   name, value, "from each file's file_date" for a period, files, and a `!` note: needs a retention, passed, N without
   a file_date; the heading counts those needing attention), `add`, `set` (a change with nothing to ask about is one
   `Done` line; shortening that puts files past their date, or a period leaving files without a file_date, shows them
   and asks; `-y`; no terminal changes nothing), `move` (asks for a rename too: `Rename bils to bilz (1 file)?`; a
   merge shows what it would do to dates and ends `Next: filo policies remove OLD`), `remove` (refused in one line
   while in use), `show` (files needing a file_date first, with the `Next:` to fix them). A passed date policy with
   files is flagged too ("its files are past their retention"). The help page `filo help policies`; the overview
   waits for slice 4. Tests: `TestPoliciesAdd` (A4), `TestPoliciesList` (E1, E2's flag), `TestPoliciesSet`
   (C1-C4), `TestPoliciesMoveRemove` (D1, D2), `TestPoliciesShow`, `TestPoliciesUsage`, golden
   `testdata/policies.txt` and `help-policies.txt`. Reviewed the same day: seven of eight findings fixed
   (`TestPoliciesReviewFindings`); the eighth (hard links counted twice) does not apply, since a hard link is a file
   row of its own. A move onto the same policy (`bills` to `BILLS`) is refused; a question after a cut list offers
   `l` (`askYesNo`, now also behind `filo update`'s two questions; `confirmChange` for the policies ones); a merge
   into a policy with no value lists the files that would lose their keep_until (`PolicyPreview.LoseDate`) and
   asks; one wording for a policy with no value ("no value"); the list works out today once.
   Slice 4 done on 2026-11-16: the report's Retention section names the policies whose files need a file_date (`! 1
   file needs a file_date: its policy counts from it (bills, 7y)`; the status reads "no file_date for their policy"
   in the needs-a-retention count) and ends with `N policies, M need attention (filo policies)` when there are any
   (`report.PolicyAttention`, shared with `filo policies`); the help overview lists the policies commands, `filo
   delete`'s page states the date rule, the export page names policy names; the README has a "Retention policies"
   section and the date rule. Test: `TestReportPolicies`; goldens `help.txt`, `help-delete.txt`, `help-export.txt`.
   Reviewed the same day (the whole branch), all nine findings fixed (`TestOldCSVAfterMove`,
   `TestReportPoliciesReviewFindings`): a `move` or rename records a history row per file (`reviews`, batch
   `policies`), so a row of a CSV exported before it and edited since is refused ("changed after export"), and an
   old CSV never puts a file back on the old name; the report's policies line shows with no files too, marked `!`
   when a policy needs attention, and names the policies of exactly the files it counts as needing a file_date; a
   `set` question names both what it puts past and what it leaves without a file_date. **Named retention policies are
   done**: every scenario in the table has a test. Next: merge; then folder rules (item 4).
4. Folder rules (default retention policies on directories).
5. Progress in the report, and `export retention --needing-retention`.
6. Duplicates: one decision for all copies (the duplicates review CSV was done first, see iteration 5).
7. `scan --verify`, with sidecar hash files.
8. Backup awareness.
9. Synced roots, sharing decisions between devices, and getting snapshots to the backup machine.
10. A summary for scheduled scans.
11. Replacing an existing `-o` file after asking (decided 2026-11-16, see Lifespan; small, can go earlier).

Later: the **UI** in place of the CSV, including viewing and editing the register in place, and notes, links and
tasks (see Open questions). All of it hangs off the stable file ID, so none of it needs the register to change
shape.

## 6. Open questions

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
- **Duplicates: one decision for all copies** (raised 2026-11-13). Retention is about content, so dating one copy
  could offer the same retention to its undated copies (one confirmation), and copies with different retentions are
  reported, not resolved. The duplicates CSV itself is designed and built (see Duplicates).
- **Duplicates: showing the decisions recorded** (raised 2026-11-14; resolved 2026-11-16 by the duplicates
  redesign: nothing waits in the register to be shown; see Duplicates, "The first design, and why it changed").
  Points the abandoned branch (`fix-duplicates-includes-copy-decision`, a4ffe5f) found that the redesign's build has
  to keep in mind: records are matched by hash, so a file restored to the content it was kept on would rejoin its
  set; a copy with hard links is one copy, named by its first path; `created_utc` strings do not sort as times
  (order by ids); the same content in two groups of one CSV (rows pasted from two exports) must not give
  contradictory actions.
- **Progress** (raised 2026-11-13). A report line for how far dating has got: files with a `file_date`, files with
  a retention, and per directory what is left. And `export retention --needing-retention`, writing only the files
  the report counts as needing a retention, so each CSV is shorter.
- **`filo scan --verify`** (raised 2026-11-13). A scan re-hashes only files whose size, mtime, ctime or inode
  changed, so bytes that rot in place go unnoticed until backups have pruned the good copy. `--verify` re-hashes
  everything and reports content that changed with nothing else changing. Sidecar hash files (`file.sha256`,
  `SHA256SUMS`) can be checked against the register's SHA-256 at no extra cost (MD5 or SHA-1 sidecars would need
  another read); a mismatch is reported (corruption, or a sidecar left from before an edit), never resolved.
  Discussed 2026-11-16: the two checks catch different things. The re-hash finds rot that happened after Filo first
  hashed a file (the register's hash is the reference, since a scan does not re-hash a file whose size, mtime, ctime
  and inode are unchanged; an edit not yet scanned shows by its mtime, so it is told apart). It cannot find
  corruption from before the first hash: that needs a reference from outside, a sidecar. So both stay, perhaps as
  `scan --verify` (re-hash) and a separate `filo verify [DIR]` (sidecars against the register, no extra read).
  Open: making references easy for files that are vital. Writing a sidecar next to each file was considered and is
  not proposed: it would be a new kind of write into the scanned trees, and each sidecar would arrive at the next
  scan as a new file needing a retention. Proposed instead: a "vital" flag on files and `filo export checksums -o
  FILE`, one standard `SHA256SUMS` file (checkable with `sha256sum -c`) written where the person chooses.
- **A summary for scheduled scans** (raised 2026-11-13; low priority). A short line ("4 files past retention, 2 need
  a decision") a timer can pass to a desktop notification, so candidates do not sit unnoticed. Filo still never
  deletes on its own.
- **All of a scan's events** (raised 2026-11-15, with the scan output). A scan shows the first 10 events of each
  kind and `… N more`; the report shows only the latest events overall, so the rest of a large scan's events (500
  missing after a folder was moved outside the root) cannot be listed. Perhaps `filo report --scan N`.
- **A root's last whole scan** (raised 2026-11-15). `filo roots` and the report show the last scan, which may have
  been partial (one folder), so a root can look recently checked when most of it was not. Showing the last
  whole-root scan as well needs one store query.
- **Paths inside notes and reasons** (found 2026-11-15, in the code review of the scan output). Showing the paths
  in a note (an event's detail, a symlink's target) relative to the heading's root was tried and taken back:
  shortening rewrites text, so an absolute symlink target read as a relative one, and in a list grouped by root a
  note was shortened against the wrong root. Notes show full paths. `Problems` and select-mode results (`delete`,
  `update`) still shorten their reasons; since the second review that day, only where a path starts (at the start of
  the text, or after a space or an opening bracket), so `/mnt/data/x.pdf` stays whole with the root `/data` (it
  used to read `/mntx.pdf`). A file name holding ` /data/` is still shortened. The fix is to pass paths in notes and
  reasons as structured parts (a root and a relative path) rather than rewriting text.
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
  - **A read-only query view first** (proposed 2026-11-16): `filo files [DIR] [--name GLOB] [--larger SIZE]
    [--smaller SIZE] [--modified-before DATE] [--modified-after DATE] [--status S] [--empty] [--sort
    size|modified|keep_until]`, cut at N rows in a terminal with "… N more", all rows when piped. It filters on what
    the register holds: size, mtime, `file_date`, `keep_until`, status, and first seen (`first_seen_scan`, the
    register's own date, which a copy or restore cannot reset). Not on creation time (many filesystems do not record
    it, and a copy, restore or sync resets it; see Lifespan, Columns) or access time (not stored; `relatime` and
    `noatime` mounts make it unreliable, and Filo reads with `O_NOATIME` so as not to disturb it).
  - **Empty files** are shown through it (`--empty`). They are registered and can be given a retention like any
    other file (see Move detection); until the query view exists, a count line in `filo report` would keep them
    visible.
- **Folder rules: a default retention policy on a directory** (second in line since 2026-11-13). Setting dates in the
  CSV is a one-off: a file added later arrives undated and needs a decision. A rule names a policy for a folder
  ("files under `personal/home/electric` get `bills`"). Proposed: a rule proposes and the person confirms in bulk
  (one answer per folder, not per file: "38 undated files under electric would get bills: 31 with a file_date, 7
  without"), for files already there and for new arrivals a scan reports, which keeps principle 4 and "never a
  trigger". About 60% of files have a `file_date` from their name; the rest need one typed before a period applies.
  Open: how rules on nested folders combine, whether a rule overrides a retention set by hand (it should not), and
  that a rule may never imply delete.
  - **Name patterns** (raised 2026-11-16). A rule is a folder plus an optional pattern on the file name, naming a
    policy: under `bank`, `*DailyStatement*` gets `daily-statements` and `*QuarterlyStatement*` gets `financial`.
    Proposed: case-insensitive globs on the base name ("contains" is `*X*`); a rule names a policy only, never a
    period or a date, so a change is made in one place (`policies add` first); the nearest folder's rules win, and a
    file matching two patterns of the same folder is reported, not guessed. Open: whether a rule without a pattern in
    the same folder is the fallback for files no pattern matched.
  - **Empty folders** (raised 2026-11-16). Removing folders that hold no files is part of this feature, as its own
    slice, since it is the only destructive part: it goes through `filo delete` and `internal/dispose` (for example
    `filo delete --empty-folders [DIR]`). Proposed: a fresh walk finds the folders with no files at any depth (the
    register does not store folders); the list is shown and Filo asks (or `-y`); each is removed bottom-up with the
    call that refuses a non-empty folder; never a root, a mount point, a folder that could not be read, or one under
    the ignore rules. Open: whether a folder holding only `.DS_Store`, `Thumbs.db` or `desktop.ini` counts as empty
    (today it does not); and that some programs need their empty folders (a maildir's `cur`, `new`, `tmp`;
    `.git/refs/tags`; project skeletons), which argues for listing them and letting the person choose, as
    `filo delete` does.
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
