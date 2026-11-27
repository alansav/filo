# Filo: design

**The present.** Of three documents, this is the present: `design.md` (what Filo is), `roadmap.md` (the future:
what it is not yet) and `history.md` (the past: how it got here).

Filo as built: schema version 11. Last updated: 2026-11-22.

This file records what Filo does and why, as built. Work not done yet, ideas, open questions and ideas rejected are
in `roadmap.md`, where designs are worked out; a design moves here, in its own commit, when work on it starts (see
`CLAUDE.md`, "After writing code").

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
- Build and install: `make build` builds a static `bin/filo` (`CGO_ENABLED=0`, which the pure-Go driver allows), so
  a binary built in the container runs on the host. `scripts/install.sh`, run on the host, copies it to
  `~/.local/bin/filo` (through a temporary name and a rename, so a running copy is not cut off). `make install` does
  both where Go is installed. No build version is stamped yet.
- Claude Code runs inside the container (`scripts/dev.sh claude`) with normal permission prompts. Its login and
  settings live in a named volume (`filo-claude`). Git commits and pushes happen on the host: the container has no git
  identity and no SSH keys.
- OS-specific behaviour (hidden-file rules, inode lookup, symlinks, path separators) lives behind small functions.
  Only Unix builds run: other platforms refuse to open the register (see Register).

### Register
- SQLite, one row per file identity, in **one database per device holding any number of roots**. Default location:
  `$XDG_DATA_HOME/filo/filo.db` (usually `~/.local/share/filo/filo.db`) on Linux and BSD,
  `~/Library/Application Support/filo/filo.db` on macOS, `%LocalAppData%\filo\filo.db` on Windows. `--db` (or
  `FILO_DB`) overrides it. Created automatically on first run; the default `filo` directory is the only directory
  Filo creates (mode 0700), and an explicit `--db` path must have an existing parent.
- **Never synced.** The default location is outside every root, so the register is never inside a synced directory and
  never scanned. That is also why there is no device column. The register stays per device.
- Driver: `modernc.org/sqlite` (pure Go, no CGO, plain `database/sql`), chosen 2026-11-11 at v1.62.0.
- `meta.schema_version` must equal the build's version. An older database is upgraded when it is opened, one version
  at a time, each step in its own transaction with the version bump (up to version 10); a newer version, or an older
  one with no migration, is refused, as is a SQLite file without Filo's `meta` keys. WAL mode is set only after those checks pass, so
  a foreign database is never modified.
- One writer at a time: an `flock` on `<db>.lock`, taken before the database is opened. The kernel drops it if Filo
  dies, so there are no stale locks. Non-unix platforms have no lock, so they refuse to open the register. Each
  scan is applied in a single transaction. The lock file is opened with `O_NOFOLLOW`, so a symlink planted in its
  place is refused, not followed.
- **The database path is checked** (security review, issue #15): the database's directory is resolved (symlinks
  followed) before the lock is taken, so one database reached through two symlinked directories has one lock. Then the
  database and its WAL, SHM and journal files, where they exist, must each be a regular file owned by the user, not a
  symlink: SQLite opens them by path and has no way to refuse a symlink, so one planted in their place would be
  followed. What is left is a hard link to the database under another name, which takes another lock.
- **The database's directory must be private** (issue #22): owned by the user, and not writable by the group or
  others, or the database is refused before the lock is taken and before anything is created in it (with how to fix
  it: `chmod go-w DIR`, or another directory). Whoever can write to the directory could swap the database or a
  companion file for a symlink in the instant between the check above and SQLite's open, or create a `-wal` file
  before SQLite does, and the driver cannot open them by descriptor. So `--db /tmp/f.db` is refused (`/tmp` is
  shared); a throwaway database goes in a private temporary directory (`--db "$(mktemp -d)/f.db"`). The default
  `filo` directory is created 0700. The directories above it are not checked.
- **The schema is not trusted** (2026-11-13): `PRAGMA trusted_schema = OFF` on every connection, so triggers and
  views stored in an altered or swapped database cannot call functions with side effects.
- When the database does not exist yet, Filo creates it with mode 0600, and SQLite gives its WAL and SHM files the
  same mode. An existing database keeps its mode: Filo never changes it.
- The database, its WAL/SHM/journal files, the lock file and the snapshot directory are never scanned, wherever they
  are.

### Register snapshots
Decided and implemented 2026-11-13. The register holds decisions (retentions, file dates) that exist nowhere else.
A snapshot is taken at the end of every run that changed the register, and the recent ones are kept (see Rotation),
so the register can be rolled back to an earlier state, by hand (see Restoring).
They are a short-term safety net against Filo's own mistakes, such as a bad update, not a backup: they sit next to
the database, on the same disk. Copying the register's snapshots to a separate physical location is the user's job.

- **What.** A consistent copy of the database, written with SQLite's `VACUUM INTO` into `<db>.snapshots/` next to
  it (`~/.local/share/filo/filo.db.snapshots/`). `VACUUM INTO` writes one self-contained, compacted file from a read
  transaction; copying `filo.db` instead could miss commits still in the WAL file or catch a page being written. It
  is a copy, not a compaction of the live database.
- **When.** At the end of every run that committed a change to the register, in `Store.Close`, while the lock is
  held; so no list of writing commands is kept. A scan that found nothing (no new, changed, moved or missing file,
  no error, no rule that set a policy) is not a change (2026-11-21, with `filo find`): its record is committed, but
  it takes no snapshot and leaves `meta.last_write_utc` alone. Snapshots protect decisions, and such a scan alters
  none, nor any file's record; a restored snapshot lacking a few of those scan records loses nothing the next scan
  does not write again. So an hourly timer, or a `find` (which scans first), does not leave a copy each time. An error
  found again (a directory still unreadable) records an event, so it counts as a change. Every write in the store goes through two helpers (`commit` and
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
- **Rotation**. Snapshots are a short-term safety net; long-term copies are the backups of them. Kept: every
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
  snapshot to `filo.db`.

### Roots
- Roots are registered explicitly and deliberately (`filo roots add DIR`), and stored in the `roots` table as absolute
  paths with symlinks resolved. `filo roots` lists them; there is no command to remove one. Considered on 2026-11-12:
  letting `filo scan DIR` add DIR as a root when it is in none. Rejected, because a root carries settings that every
  later scan relies on, so setting one up should be a deliberate act; a scan of a directory under no root is refused
  with a hint to add it.
- **A root owns its scan settings** (decided 2026-11-12): whether hidden entries are scanned (`include_hidden`) and the
  extra names to ignore (`ignores`). They are given on `filo roots add DIR [--include-hidden] [--ignore NAME]...` and
  replaced as a whole by `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...`. Every scan of
  the root, whole or partial, uses them, so a timer's `filo scan` and a manual scan always cover the same files.
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
  paths get the directory as a prefix (`a.txt` becomes `Documents/a.txt`), their scans become scans of `~` scoped to
  that directory, their event paths are prefixed (except a returned file's `other_path`, which is relative to the root
  named in the event's detail), and the old root rows are deleted. Files keep their IDs, so history and keep-until
  dates are kept, and the first scan of `~` finds those files unchanged. Nothing on disk changes. The new root's
  settings apply to everything it absorbs. `roots add` says which roots it absorbed, notes any whose settings differed,
  and any that the new root's settings skip (a hidden or ignored directory: their files are then neither verified nor
  marked missing until `filo roots set` includes them). Refused instead: absorbing a
  root that cannot be checked now (not reachable or its disk not mounted), since Filo cannot tell whether it is on the
  same filesystem; and the reverse, a directory inside an existing root on the same filesystem, which is already
  covered (the message suggests `filo scan` on it). Roots inside the new one on other filesystems stay roots.
- Each root records the **mount point it lives under** (`mount_path`): the deepest directory, the root itself or an
  ancestor, on a different device from its parent; NULL when the root is on the filesystem of `/`. A scan is refused
  as **unmounted** if that directory is now on its parent's device, or the root is no longer on it.
  So `/mnt/disk/photos` is protected when `/mnt/disk` is not mounted, even if an empty `photos` directory exists
  underneath.
- An existing root that cannot be reached when a new one is added (an unmounted disk) is checked by path only.
- Paths are stored relative to their root, with `/` separators. `filo scan [DIR]` maps DIR to the deepest root that
  contains it, by path or by identity (so `/mnt/home/docs` is a partial scan of `~` with scope `docs`). DIR must be a
  directory on that root's filesystem. A DIR under no root is refused with a hint to add it.
- Each scan records its root, its **scope** (a path relative to the root, empty for everything) and the hidden/ignore
  settings it ran with (the root's at the time).
- A file moved from one root to another is linked as a returned file when the old root is scanned first; scanned the
  other way round it is new plus missing (see Move detection). File identity is not tied to a root.
- Location history: each file's paths are stored as intervals (`valid_from_scan`, `valid_to_scan`, NULL = current),
  so the tree at any scan is recorded.

### Scan
- Skips hidden entries unless the root includes them, plus noise: `node_modules`, `__pycache__`, `.filo`, the root's
  ignore names, and `bin`/`obj` beside a `.csproj` or `.fsproj`.
- **A `.filo-delete-*` directory is reported, never walked** (issue #21), whatever the root's settings: it is the
  private directory `filo delete` moves a file into to delete it (see Disposal), left behind only if the run was
  interrupted or the file could not be put back. It is an error in the scan summary and the report ("left behind by
  filo delete …"); the file in it is not recorded (it is not the user's file at its path any more, and it would come
  back as a new file), and nothing under it is covered. Filo never moves it back or deletes it: the person does.
- **Never by a path that can be redirected** (security review, issue #14). A walk opens the root, then each
  directory through the one above it (`os.Root`, `openat` with `O_NOFOLLOW`), and lists, `lstat`s and reads symlinks
  through that open directory, so a directory swapped for a symlink while the walk runs cannot lead it outside the
  root. A directory is walked only if the one opened is the directory its `lstat` saw (same device and inode); one
  swapped since, or gone by the time it is opened, is skipped with a problem ("changed while it was being scanned"),
  so nothing under it is covered and none of its files is marked missing; one that cannot be opened (permission
  denied) is a failed listing, the root or scope directory included. A scope is opened the same way, so a symlink on
  it is refused. Only a symlink's broken flag is looked up by path, following the link.
- **One filesystem.** A walk never descends into a directory on a different device from its root; such directories are
  reported once in the summary, not as errors. A directory already visited in the same walk (same device and inode: a
  bind mount of an ancestor, or a loop) is skipped and reported.
- Symlinks are recorded with their target and a broken flag (target missing, or a loop), never followed. `filo report`
  lists the broken ones.
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
  regular files are hashed. A scan opens each file's directory through its root (`os.Root`; each worker keeps the
  last one open, since files come in walk order) and the file in it by name, only if it is the regular file `lstat`
  sees there (same device and inode), so a directory swapped for a symlink since the walk cannot lead it to a file
  outside the root, and a symlink swapped in for the file is not followed (issue #14). A directory swapped for a
  symlink to another directory inside the root is followed: the file read there is inside the root, recorded under
  the path walked; the next scan corrects it. If size or mtime changes while the file is read, or fewer bytes are read than its size,
  the hash is discarded and the file is retried on the next scan. The stored size and mtime are the ones the file had
  while it was hashed.
- **Unreadable files are flagged, not dropped.** A file that is listed but cannot be opened or read (permission denied,
  I/O error, or any other unexpected error: the flag only ever holds a file back) gets `files.unreadable_since_scan`,
  the first scan in a row that failed to read it; it stays set across later failing scans and is cleared by the first
  scan that hashes the file again. Its last good hash, size and mtime are kept, so its identity and history survive,
  but the hash is stale while the flag is set. A file that vanished or changed while it was read is not unreadable: it
  is retried as before and the flag is untouched. Losing read access changes the file's ctime, so the next scan notices
  it (see above). Files under a directory that cannot be listed are not flagged: Filo cannot tell what happened to them,
  so they are simply not covered by that scan.
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
  marked missing by the next scan that covers it, so the history link is lost.
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
- The inode is only used to decide what to rehash, never to pair moves.

### Lifespan
The retention CSV and `filo update` write only the register and the CSV file the user names; they never touch the files.

- **Forever** (decided 2026-11-12): the retention (and so `keep_until`) may be the word `forever` instead of a date, for files kept
  indefinitely. It is stored as that word, never as a far-off date such as 9999-12-31 (which would look like a
  chosen date in the CSV, sort among real dates, and need special-casing everywhere), and checked explicitly, not by
  comparing it with dates. A `forever` file is never a deletion candidate, however old it is or however often it
  changes; `filo report` counts them so the pile stays visible, and typing a date over it later is how it ends. One
  word only, in any case (`Forever`): no aliases such as `indefinitely` or `never`.
- **One date per file: `keep_until`**, worked out from the file's retention (see Retention CSV columns). Keep the file until
  then. On that date it becomes a deletion candidate, never deleted automatically: `filo delete` lists the candidates,
  and deleting each is the person's call (see Disposal). Filo tracks lifespan only. Hard legal retention is a later
  retention or `forever`; there are no holds.
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
- **A scan first**: every export scans before it reads the
  register, so the CSV holds the files as they are now, not as of the last scan, and a file saved since is in it.
  `--no-scan` skips it (nothing changed, or the scan is slow); `--workers N` is passed to the scan, as for `filo scan`,
  and refused with `--no-scan` (so is `roots add --no-scan --workers N`, since 2026-11-16). What it scans:

  | Export | Scans first |
  |---|---|
  | `export retention DIR` | DIR only (a scan of part of a root is safe, see Scan) |
  | `export retention`, no DIR | every root |
  | `export duplicates [DIR]` | every root: a group's copies can be anywhere |

  It is the ordinary scan, with its block on stdout before the export's (see Terminal output, Scan and roots), so the
  export's `after_scan` is that scan. An export therefore writes the register: it takes the lock and leaves a
  snapshot. A root that fails to scan is reported in its place, as `filo scan` does, and the export stops without
  writing the CSV, so a wrong or missing disk never produces a CSV that looks complete; `--no-scan` exports from the
  register as it is. Ctrl-C during the scan writes nothing, neither scan nor CSV. A directory that cannot be read
  leaves its files as they were (see Scan) and the export goes on. With `--no-scan` the export block says so with a
  `·` line naming when the last scan of the place finished.
  - **No "is a scan needed?" check** (considered 2026-11-16, rejected): the incremental scan is that check. Its cost
    is hashing, and it hashes only files whose size, mtime, ctime or inode changed; with nothing changed it walks and
    lstats, and hashes nothing. Every cheaper test misses something: a directory's mtime changes when a file in it is
    added, removed or renamed, but not when a file is edited in place; "scanned a few minutes ago" is a guess that
    leaves out a file saved since, unnoticed; watching the disk needs a process running all the time.
- **Columns**, in order: `id` (file row ID), `export` (export number), `directory` (absolute), `name`, `size`
  (bytes), `modified` (local time; a cue to when the file was last worked on), `file_date`, `retention`, `keep_until`
  (see Retention CSV columns). The name is a column of its own because file names often carry dates. Left out on purpose: a reason (one purpose only), an action (a
  delete column in a spreadsheet is unsafe: a one-column sort or a fill-down puts it on the wrong rows), and creation
  or first-seen dates (not reliable: many filesystems do not record a birth time, and a copy, restore or sync resets
  it). `filo update` finds columns by header name and reads only `id`, `export`, `file_date` and `retention` (and
  checks `keep_until` was left alone), so a spreadsheet may reorder columns or add its own. Rows are keyed on (export, id). Each export's rows, hashes and dates are stored in
  the register (`export_rows`).
- **Encoding.** UTF-8 with a byte order mark (so spreadsheets detect UTF-8), standard quoting. File names are written
  as they are, emoji and all; bytes that are not valid UTF-8 are shown as U+FFFD, which is harmless because the update
  never reads the name. A display cell that starts with `= + - @`, tab or CR gets a leading `'` so a spreadsheet does
  not run it as a formula. The update accepts a byte order mark, CRLF, `;` or tab separators, and empty rows.
- **Limits on a CSV read back** (security review, issue #18), for the retention and duplicates CSVs alike, all in
  package `sheet`: at most 256 MB (no more than that is ever read), 1,048,576 rows (header and empty rows included)
  and 16,384 fields in a row, the most a spreadsheet keeps, and 1 MB in one row. A CSV over any of them is refused
  whole, before a row is read, saying which limit and on which line; the limits follow the quotes, so a separator or
  line end inside a quoted cell does not count. `filo update` and `filo delete FILE.csv` read the file once (issue
  #16): a file whose size is over the limit is refused before it is read.
- **Dates**: `YYYY-MM-DD` only, and a real date (`2027-02-29` is refused). Anything else, such as `06/07/2027`, is
  refused, never guessed; the message suggests formatting the column as text, since spreadsheets may rewrite dates in
  the locale's format. Past dates are accepted (see Retention CSV columns). Periods counted from today (`+1y`) were dropped
  from the CSV on 2026-11-12: the same cell meant different dates on different days. A period counted from the row's
  `file_date` (`7y`) replaced them. Dates only: a time (`2026-12-31 15:00`) is refused, not cut off.
- **`-o FILE` is required**: the CSV is never written to the terminal. FILE is created with mode 0600.
- **An existing FILE is replaced only after asking**. Refusing would make people delete the old CSV or invent
  `retention2.csv`. Filo asks `Replace retention.csv? [y/N]`; with no terminal it refuses. `-y` does not answer it: a
  timer or script never replaces a file silently. Only a regular file is replaced, never a directory, symlink or device.
  The new CSV is written to a temporary file next to it (mode 0600) and renamed over it, so a crash never leaves half a
  CSV. The question is asked first, before the scan, so a no costs nothing (`Nothing exported.`); if the file is no
  longer the one asked about when the CSV is ready (replaced, edited, or no longer a regular file), the export stops and
  the file is left as it is. Two exports of the same register cannot race for one file: each holds the register's lock
  from start to end. What is left is two exports of different registers (`--db`) writing one file, or another program
  writing it in the instant between Filo's last check and the rename; then that write is lost, or, if it lands just
  before the last check, the export is recorded with no CSV (an export number is skipped). The same for every `-o` file
  Filo writes. Checking whether a spreadsheet has the file open (LibreOffice's `.~lock.NAME#`) was considered and left
  out (2026-11-16): an edge case, and a stale marker would block exports.
  - **No "rows not yet applied" in the question** (considered 2026-11-16, dropped the same day): whether to replace
    is the person's call, and a clean slate is a fair reason. Warning about unapplied rows steers people to a second
    file and then to merging two CSVs by hand, which causes the problems the warning meant to prevent; and `filo
    update` already refuses a row edited after a later export of its file. For a duplicates CSV it could not even be
    counted: nothing records which rows `filo delete` carried out.
- **What the update does with each row**:
  - `file_date` and `retention` as exported: nothing, whatever happened since. Without a `file_date` column, the
    `file_date` exported is kept;
  - the values already stored: nothing;
  - a new `file_date` or `retention`: replaces the stored ones and works `keep_until` out again. Editing the same CSV
    again and updating again replaces them again;
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
- **Bulk dates**: set in the spreadsheet (filter on `directory`, fill down). There is no command for them.
- The hash at export is stored; it is **re-checked at action time** (`filo delete`) against `files.reviewed_hash`.

### Retention CSV columns
Retention is usually counted from a document's own date ("keep tax records for seven years"), not from today or from
when the file reached the disk. So the CSV has a date to count from and a retention that can be a period.

- **Columns**, in order: `id, export, directory, name, size, modified, file_date, retention, keep_until`.
  - `file_date`: the date the file is about or was issued, the date a retention period counts from. Typed by the
    person, or suggested by the export from the file name, marked `!` (below). Not the filesystem's creation date, which is not
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
- **Past dates are accepted**, whether typed
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
  A name with two different dates (`2023-01-01 to 2023-12-31 statement.pdf`) gets none: the person types it.
- **A date read from a file name is marked** (decided 2026-11-24): it is written `!2023-07-14`, so the rows to check
  can be found (filter or search on `!`), and it counts only once the `!` is removed. A spreadsheet treats the
  marked cell as text, so marked dates also sort apart. `!` and not `?`, which is a wildcard in Excel's find and
  replace, so removing every `!` in the column accepts them all in one deliberate step. Format the `file_date`
  column as text first: a spreadsheet may read the cell left after the replace as a date and save it in the
  locale's format (`14/03/2023`), which the update refuses (LibreOffice Calc keeps `YYYY-MM-DD`, checked
  2026-11-24; Excel not checked). On update:

  | `file_date` cell | Result |
  |---|---|
  | marked, as exported, and a retention that is not a period | nothing is stored for the date |
  | marked, as exported, and a period (or a policy whose value is one) | row refused: "file_date !2023-07-14 was read from the file name: remove the ! to accept it" |
  | marked, but not as exported | row refused with the same hint |
  | the `!` removed | an ordinary date, as if typed |

  So a guess nobody looked at never reaches the register, not even when a period is filled down a whole directory.
  (Before 2026-11-24 the date was written unmarked and stored, untouched, when the row's retention was a period; a
  CSV from then still works that way. Found in a code review on 2026-11-12: `scan 20190101 id.pdf` would otherwise
  keep 2019-01-01 as its file_date.)
- **Register (schema version 5)**: `files.file_date` (YYYY-MM-DD, NULL = none), `files.retention` (the value as
  given: a period, `forever` or a date; NULL = none), and `files.keep_until`, now always worked out from those two
  (YYYY-MM-DD or `forever`). Statuses and `filo delete` keep using `keep_until` only. Migration: an existing
  `keep_until` becomes `retention` (its date, or `forever`) with the same `keep_until`. `export_rows` and `reviews`
  gain the same two columns, so an untouched cell is still recognised and the history shows what was typed.

### Named retention policies
A retention schedule written once and named, so the same rule is typed as a word in the CSV and changed in one
place: if the law moves financial records from 7 to 10 years, `filo policies set bills 10y` changes every file using
`bills`.

- **What a policy is**: a name for a retention: a period (`bills = 7y`, counted from each file's own `file_date`), a
  date (`project-x = 2030-12-31`: every file of it ends that day), or `forever`. Names are letters, digits, `-` and
  `_`, starting with a letter, so a name is never mistaken for a period or a date; any case (`Bills` is `bills`,
  stored lowercase); `forever` is reserved. In the retention cell, `7y`, `2030-12-31` and `forever` keep their
  meaning and anything else that is a valid name is a policy. A file has either a policy or a retention of its own,
  never both.
- **Dates** (said plainly in the README and help): a file kept until 2026-12-31 is a deletion
  candidate on 2026-12-31, since *kept* means the date is still to come; the same for a policy as for a date typed
  per file. Nothing is deleted on that day: the file is listed by the next `filo delete`, which asks.
- **Creating a policy** (the person's choice over creating them by command only: a policy created ahead may never be
  used): a name in the CSV that is not a policy yet creates one **with no value**. `filo update` lists the policies it
  is about to create, with how many rows name each, and asks once (`Create 2 policies? [y/N]`). Declined, or no
  terminal, the rows naming them are left alone and the summary says how many; `--dry-run` lists them. Its files
  need a retention until the policy gets a value. A policy made in error (`bils`) is fixed with `filo policies move
  bils bills`, then `remove bils`.
- **`filo policies`**:
  - `filo policies` lists each policy with its value and how many files use it, and flags what needs attention: no
    value (`! needs a retention`), a date that has passed (`! passed: set a new date, or remove it`), files
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
  named: never a deletion candidate; counted in the report's decisions with the directories holding most of them (`! 3
  files need a file_date: their policy counts from it (bills, 7y)`); flagged by `filo policies`; listed first by
  `show`, with `Next: filo export retention DIR -o FILE, fill in file_date, then filo update FILE`.
- **The retention CSV**: the `retention` cell holds the policy name; `keep_until` shows the date worked out, as
  today. A policy whose value is a period, on a row with no `file_date`, refuses the row ("retention bills (7y) needs
  a file_date"); a policy with no value is accepted. An untouched cell is recognised by what was exported (`bils`),
  so an old CSV does nothing after a `move` or `set`.
- **Output** (under the Terminal output rules; from `internal/cli/testdata/policies.txt`, plain form):

```
$ filo policies

Policies   3 . 2 need attention

  bills       7y           from each file's file_date   2 files
  bils        no value                                   1 file   [!] needs a retention
  last-year   2025-12-31                                0 files   [!] passed: set a new date, or remove it

Next: filo policies show bils, then filo policies set bils VALUE or filo policies move bils NAME

$ filo policies show bills

Policy bills   7y from each file's file_date . 2 files . <root>

    file_date    until
    2015-06-30   2022-06-30   bills/2015-06.pdf
    2022-01-31   2029-01-31   bills/2022-01.pdf

Next: filo policies set bills VALUE to change it for all of them

$ filo policies set bills 3y

Shorten bills   7y -> 3y . 2 files . <root>

[!] 1 file would be past its retention:
    file_date    until
    2022-01-31   2025-01-31   bills/2022-01.pdf

Shorten bills for 2 files, putting 1 past its retention? [y/N]
Done   bills is 3y . 2 files updated . 1 past its retention

Nothing has been deleted. Next: filo delete
```

  Lengthening: `Done   bills is 10y · 2 files updated`. `filo update` naming new policies: a `New policies   2 ·
  bils, payslip` block with a line per policy and its rows, "they have no value yet: their files need a retention
  until filo policies set NAME VALUE", then `Create 2 policies? [y/N]` (`testdata/update-policies.txt`). `move` asks
  `Move 1 file from bils to bills (3y)? [y/N]` and ends `Next: filo policies remove bils, now it has no files`; `add`
  is `Added   bills · 7y`; the report's Retention section ends with `! 4 policies, 1 needs
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

### Directory rules
Setting retention in the CSV is a one-off: a file added later arrives undated and needs a decision. A rule names a
policy for the files under a directory, or only those whose name matches a pattern: "files under
`personal/home/electric` get `bills`", "under `bank`, `*DailyStatement*` gets `daily-statements`". A rule is the person's own
decision, written down ahead of time, so a file dated by it records what is known (principle 4). A rule only sets a
retention: the file becomes a candidate at its date and is deleted only through `filo delete`, as before (principle
1).

- **Applied automatically** (the person's choice over confirming each time): every scan, also a scheduled one and the
  scan before an export, applies the rules in its own transaction, so the run's snapshot covers it, and says what it set
  (see Output). No `filo rules` command asks either (decided 2026-11-16, replacing a question on `add`): whether 0 or
  1000 files dated is what the person meant, Filo cannot know, so it says how many and does it; `--dry-run` shows what
  it would do first. Dating sets a retention only: nothing is deleted without `filo delete`, which asks, and the run's
  snapshot holds the register as it was.
- **Which files a rule dates**: a file with no retention at all (no date, no `forever`, no policy, not even a policy
  with no value), and each file only once. The file records the rule that dated it; after that no rule touches it
  again, even if its retention is later cleared in the CSV (the rule never undoes what the person did) or the rule is
  removed (the file keeps its policy). Dating records the content it was set on, stamped with the scan, as `filo
  update` does, so `filo delete` checks it the same way.

  | the file, under a rule | what the rule does |
  |---|---|
  | new, no retention | gives it the policy |
  | no retention and no `file_date`, the policy a period | gives it the policy; its status is *needs a file_date*, which names the fix |
  | a retention already (a date, `forever`, a policy, also one with no value) | nothing: the person's choice wins |
  | moved in from elsewhere with its retention | nothing: a move keeps the retention |
  | *unreadable*, or the new side of a *possible move* | nothing until resolved: no content to record, and a move may bring its own retention |
  | under a directory the latest scan covering it could not read | nothing: what is there now is not known |
  | a hard link (Filo keeps each as a file of its own, see Move detection) | the rule of its own directory, as any file; its other links follow theirs (decided 2026-11-16) |
  | dated by the rule, then its retention cleared in the CSV | stays undated |
  | dated by the rule, then the rule removed or changed | keeps its policy |

- **Matching**: a pattern is a glob on the file's name (not its path), in any case: `*` is any run of characters, `?`
  one character, "contains" is `*X*`. No pattern is `*`, everywhere, and a run of wildcards is kept in one form, its `?`
  first and then one `*` (`**` is `*`, `*?` is `?*`), since two forms of it would tie on every name. A rule covers the
  files at any depth below its directory. For a file, Filo looks at the file's own directory, then each parent up to the
  root: **the first directory with a rule that matches decides**, so the result can be read off the path. Within a
  directory, rules are tried **longest pattern first, counting every character but `*`**: a `?` is one character, so it
  counts (`*` counts 0, `?*` 1, `??*` 2; decided 2026-11-17), and `*` alone is always tried last (the fallback for files
  no other pattern matched); a pattern `a` counts 1. Two matching patterns with the same count are reported, not
  guessed, and the file stays undated. A directory's patterns are unique, in any case (`*statement*` is `*Statement*`).
  Longer is not always more specific (`Daily*`, 5, loses to `*Statement*`, 9, on `DailyStatement.pdf`); the fix is the
  longer pattern meant (`*DailyStatement*`), and `filo rules add --dry-run` shows the files it would date. A pattern is
  refused, each with its own one-line reason, if it has a `/` or a `\` (a pattern is a name, not a path; a `\` is
  usually a Windows path or a shell escape, and a name with one is still matched by `?`), a NUL, is not valid UTF-8, or
  is longer than 255 bytes. Nothing else is checked: a pattern no file can be named (`..`) simply matches nothing, and a
  pattern is never joined into a path.

  With rules on `bank` (`*DailyStatement*` 14 → `daily-statements`, `*Statement*` 9 → `statements`, `*2024*` 4 →
  `tax-2024`, `*bank*` 4 → `banking`, `*` 0 → `financial`) and on `bank/2024` (`*` → `tax-2024`):

  | file | result |
  |---|---|
  | `bank/DailyStatement-03.pdf` | `daily-statements`: the longest match |
  | `bank/QuarterlyStatement-Q1.pdf` | `statements` |
  | `bank/bank-2024.pdf` | not applied, reported: `*2024*` and `*bank*` both count 4 |
  | `bank/letter.pdf`, `bank/old/letter.pdf` | `financial`: `*`, from `bank` for both |
  | `bank/2024/DailyStatement-01.pdf` | `tax-2024`: the nearest directory with a matching rule wins |

- **Changing a rule affects new files only**: files it already set a policy on keep it, and `set` says so ("38 files
  keep the policy it set: the change is for new files"). Moving them is a step of its own: `filo policies move` for
  every file of a policy, the CSV filtered on the directory for some.
- **Policies and rules**: `filo policies remove` is refused while a rule names the policy ("2 rules use bills:
  filo rules lists them"), as it is while files use it. `filo policies move` takes the rules with the files: a rename
  keeps them, and a merge points them at the policy moved to.
- **`filo rules`**. DIR is read as in every command (relative to the current directory, `~`
  expanded) and must be a root or inside one. PATTERN may be left out, meaning `*`. A pattern with wildcards or spaces
  is quoted (`'*Statement*'` or `"*Statement*"`), since the shell expands an unquoted one; the wrong number of arguments
  is refused, and every result line shows the pattern as received, so a file name the shell put in its place shows (it
  would usually date no files). `add`, `move` and `copy` take `--dry-run`: they show what they would date (the files,
  cut at ten in a terminal) and change nothing. No command asks, so there is no `-y`.
  - `filo rules [DIR]` lists the rules at DIR and below it, or every rule: by root, by directory (a parent before the
    directories inside it), and within a directory **in the order they are tried**, each with its policy and how many
    files it has dated. It flags a rule whose directory is not on disk (`! bank: no such directory`; the register
    stores no directories, so an empty directory is still one) or cannot be checked (its root not reachable), and the
    files that two rules match equally.
  - `add DIR [PATTERN] POLICY` adds the rule and dates the files it decides now, and says how many (and how many need
    a `file_date`, and any file it ties on). A POLICY that does not exist is created with no value, as the CSV does,
    and the result says so. Refused if the directory already has that pattern.
  - `set DIR [PATTERN] POLICY` changes the policy of the rule with exactly that pattern (compared as text, in any
    case, never matched as a glob, so `*Statement*` never picks `*DailyStatement*`); a new POLICY is created as for
    `add`. A pattern is never changed: remove the rule and add a new one.
  - `remove DIR [PATTERN]` removes the rule with exactly that pattern.
  - `move OLD NEW` and `copy OLD NEW` put the rules at OLD and at the directories below it in the same places under NEW
    (`bank/2024` goes to `banking/2024`); `move` removes them from OLD, `copy` keeps them (`copy bank/2024 bank/2025`
    for a new year). They date the undated files under NEW they now decide, as `add` does. Refused, with nothing merged,
    if NEW or any directory below it already has rules (they are listed: remove them first, or add the rules by hand; a
    move up, `bank/2024` to `bank`, does not count the rules it moves, a copy does); if NEW is OLD or inside it; if OLD
    has no rules at or below it. NEW may be in another root, whose rules they then are. Files dated by a moved rule keep
    their policy and still count as dated by it.
  - No `show`: `--dry-run` and the list cover it.
- **Directories that change**: a rule stores its root and its directory relative to the root, as files do.
  - A directory renamed or removed on disk: its files are moves or missing, as before; the rule stays and is flagged,
    never followed (pairing a directory that went with one that came is a guess, principle 6). `filo rules move`
    fixes it.
  - A directory that cannot be read, or a root not mounted: nothing is applied there; the scan already reports it.
    `add`, `move` and `copy` go by the latest scan covering each directory, and refuse a root that cannot be reached.
  - A root added above an existing one (Roots): the old root's rules get the same prefix as its files, in the same
    transaction.
- **Output** (under the Terminal output rules; from `internal/cli/testdata/rules.txt`, plain form). The list: one root
  in the heading, or a sub-heading per root; each rule with its policy's value, so a policy with no value shows; the
  flags on the right.

```
$ filo rules add <root>/bank *Statement* statements --dry-run
Dry run   bank . *Statement* -> statements (7y) . policy would be set on 3 files . 3 need a file_date . <root>

    file_date   until
                needs a file_date   bank/2024/DailyStatement-01.pdf
                needs a file_date   bank/DailyStatement-2019-03.pdf
                needs a file_date   bank/QuarterlyStatement-Q1.pdf

Dry run: nothing was changed.

$ filo rules add <root>/bank *Statement* statements
Done   bank . *Statement* -> statements (7y) . policy set on 3 files . 3 need a file_date . <root>

Next: filo export retention <root>/bank -o FILE, fill in file_date, then filo update FILE

$ filo rules add <root>/bank *bank* banking
Done   bank . *bank* -> banking . policy set on 1 file . <root>
  - policy banking was created with no value: its files need a retention until it has one

Next: filo policies set banking VALUE

$ filo rules
Rules   5 . 3 directories . 4 need attention . <root>

  bank
    *Statement*   statements   7y         3 files
    *2024*        tax          no value   0 files   [!] needs a retention
    *bank*        banking      no value    1 file   [!] needs a retention
  bank/2024
    *             tax          no value   0 files   [!] needs a retention
  scans/old                                         [!] no such directory
    *             statements   7y         0 files

[!] Not applied   1 file matches 2 rules of bank equally (*2024*, *bank*)
    bank/new-2024-bank.pdf

Next: filo rules add DIR [PATTERN] POLICY, filo rules remove DIR [PATTERN]

$ filo rules set <root>/bank *statement* financial
Done   bank . *Statement* -> financial (was statements)
  - 3 files keep the policy it set: the change is for new files
  - policy financial was created with no value: its files need a retention until it has one

Next: filo policies set financial VALUE

$ filo rules remove <root>/bank *bank*
Removed   bank . *bank* -> banking
  - 1 file keeps the policy it set
```

  Refusals are one line each: `bank: no rule *Statment* there (filo rules lists them)`, `bank: already has the rule
  *Statement* (filo rules set changes its policy)`, a root that is not mounted (the store's "is not mounted (root …);
  mount it and retry"), a DIR inside no root, and for the wrong number of arguments the usage, with "quote a pattern
  with * or spaces". The `--dry-run` list is cut at ten in a terminal ("… N more"), all of it when piped, as elsewhere.
  `move` and `copy` say what they did the same way: `Done   moved 2 rules from bank to banking · policy set on 1 file`
  (`testdata/rules-move.txt`).

  **The words** (decided 2026-11-16): a rule does not date a file, it sets the file's policy, and the date follows
  from the policy. Output says "policy set on 212 files", "given a policy by rules", "not given a policy"; never
  "dated by a rule". **A scan** (every scan: `filo scan`, the scan before an export, `roots add`) applies the rules to
  every file in its scope with no retention, not only the new ones, so a file is given its policy once its directory
  can be read again or a tie is fixed. Its summary counts what the rules did and did not do; a section lists each
  rule that set a policy, with the directory, pattern and policy lined up; every tie is listed, every scan, with its
  files and the rules that match them equally, as `filo rules` lists them. A scan the rules did nothing in shows
  none of it (from `internal/cli/testdata/scan-rules.txt`, plain form):

```
$ filo scan (four new files)
Scan #2   11 files seen . <took> . <root>

  - 4 new
  - 3 given a policy by rules
  [!] 1 not given a policy: two rules match it equally

Policies set by rules
    directory                pattern            policy
    bank                     *DailyStatement*   statements   2 files   2 need a file_date
    personal/home/electric   *                  bills         1 file

[!] Not applied   1 file matches 2 rules of bank equally (*2024*, *bank*)
    bank/new-2024-bank.pdf

Next: filo report
```

  **`filo report`**, in its Retention section: how many present files have a policy set by rules, the files no rule
  could give one (each with the rules that tie on it, after "Where most need one"), and `filo rules add DIR POLICY`
  in the next step (the lines `TestReportRules` checks):

```
Retention   …
  - 4 with a policy set by directory rules (filo rules)
  …
  [!] 1 not given a policy: two rules match it equally
    bank/new-2024-bank.pdf   rules of bank: *2024*, *bank*
    Next: filo export retention [DIR] -o FILE, then filo update FILE, or filo rules add DIR POLICY
```
- **Register (schema version 10)**: `rules` (id, root, directory relative to the root, empty for the root itself,
  pattern as typed, its length (column `letters`: every character but `*`; a `?` counted from 2026-11-17), policy, when
  added; the root, directory and lowercased pattern unique); `files.rule_id` (the rule that dated the file, NULL once
  that rule is removed) and `files.rule_dated_utc` (when; set once, and while it is set no rule dates the file again).
  The upgrade only creates the table and the columns.
- **The scenarios** (the test list):

  | scenario | outcome |
  |---|---|
  | A1 `add` with undated files under DIR; with `--dry-run`; in a terminal or not | added and dated, the count said; nothing changed, the files listed; the same either way |
  | A2 `add` naming a policy that does not exist | created with no value, and said so |
  | A3 `add` a pattern the directory has (`*statement*` after `*Statement*`) | refused |
  | A4 the wrong number of arguments; DIR outside every root | refused |
  | B1 a scan finds a new undated file under a rule | dated; the scan says so |
  | B2 a file with a retention, a policy with no value, or moved in with its retention | untouched |
  | B3 an unreadable file; the new side of a possible move | untouched until resolved |
  | B4 a period policy, the file without a file_date | dated; needs a file_date |
  | B5 dated by a rule, then its retention cleared in the CSV; then the rule removed | stays undated; keeps its policy |
  | C1 two matching patterns of different counts; `*` with another match | the longer one; `*` last |
  | C2 two matching patterns of the same count | not applied, reported by the scan and `filo rules` |
  | C3 a rule in a parent directory; a nearer directory with a matching rule | applies at any depth; the nearer one wins |
  | C4 two hard links in directories with different rules | each dated by its own directory's rule |
  | D1 `set`: files already dated | keep their policy; a line says so |
  | D2 `set` or `remove` with a pattern that is not stored (`*Statement` for `*Statement*`) | refused |
  | D3 `filo policies remove` of a policy a rule names; `filo policies move` (a rename, a merge) | refused; the rules follow |
  | E1 `move` after a rename, with a rule in a subdirectory | both moved; the flag gone |
  | E2 `copy` to a new directory with undated files | dated, the count said; OLD keeps its rules |
  | E3 NEW or a directory below it has rules; NEW inside OLD; OLD without rules | refused, nothing changed |
  | F1 a rule's directory gone from disk | flagged by `filo rules`, never followed |
  | F2 an unreadable directory under a rule | nothing applied there |
  | F3 a root added above a root with rules | the rules get the prefix |
  | G1 the upgrade from schema 9 | table and columns created, nothing else changes |

### Disposal
Package `internal/dispose`, the only code that changes the user's files: deleting them from `filo delete`, and
renaming them in place from `filo update` (see Tags, "Renaming a file by its `name` cell").

- **Deletion is permanent.** No quarantine and no recycle bin: the bin can fill up with large files, and is
  missing on network disks; backups are what make a deletion recoverable. (The private directory a file passes
  through as it is deleted, below, exists only for that instant.) So the list the person confirms, and the
  check below, are what keep it safe.
- **`filo delete [DIR] [-y]`** lists the candidates (expired files) under DIR, or under every root, with their end date,
  size and path, and their total size; `filo delete DIR` narrows it. In a terminal the list is cut at 10 (see Terminal
  output, Long lists), and it asks `Delete all N, select one by one, list them all, or none? [a/s/l/N]`:
  - *a* deletes them all, each through the check below. If the list was cut, it first shows every file and asks once
    more (`Delete these N files? [y/N]`), so nothing is deleted that was not on screen;
  - *s* goes file by file: `d delete · r new retention · s skip · q quit`. *r* asks for a new retention (a date, a
    period counted from `file_date`, `forever`, or a policy, as in the CSV), saves it at once and drops the file from
    the list; a period on a file without a `file_date` is refused and asked again. *s* leaves it a candidate. *q* stops:
    what was deleted stays deleted, the rest is untouched;
  - *l* lists every file and asks again;
  - *n*, Enter or end of input: nothing (an unrecognised answer asks again).
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

  A file that can be read but not deleted (its directory is not writable) is skipped and reported, and stays a
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
- **Empty directories.** A directory is removed only if this run deleted its last entry: never a directory that was
  already empty, never one Filo deleted nothing from, never a root. Before removing it, Filo lists it on disk and
  removes it only if it has no entries at all (hidden files included), then removes it with the call that refuses a
  non-empty directory, so anything that appeared in between keeps it. Then it works up: a parent emptied by that removal
  is checked and removed the same way, stopping at the root. The summary says how many directories were removed.
- **Summary and exit code.** "Deleted N files (size). Skipped M:" and one line per skipped file and why. Exit code 0
  when everything went as decided (a changed file is an expected outcome), 1 if a file could not be read or deleted.
- **`filo report`** adds "deleted by filo: N files, SIZE freed (last run DATE: n files, SIZE)" to its Retention
  section, from the deleted rows.
- **Syncthing propagates deletes**: a delete on one device removes the file on every device that shares the Syncthing
  folder. Filo treats synced roots like any other.
- **Details:**
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
  - *Only inside its root*. Before anything else, the recorded path must be a clean
    relative path below a clean, absolute root (no `..`), and every directory from the root down must still be a real
    directory, each checked with `lstat`: a symlink there means the tree changed since the scan (skipped, "run filo
    scan"). Directories above the root are not checked: a symlink there (a home directory moved to another disk) moves
    the whole root, which scans handle. The directory check is repeated at the last look before the unlink, since
    hashing a large file leaves time for a directory to be swapped. A path outside its root can only come from an
    altered register (`../../.ssh/id_ed25519` with that file's hash): it is refused with exit 1. The hash check alone
    does not stop that, since whoever can edit the register can usually read the file.
  - *Through the directory checked, never by path* (security review, issue #13). The directory holding the file is
    opened once, from the root down, each directory through the one above it (`os.Root`, `openat` with
    `O_NOFOLLOW`), each a real directory and the very one its `lstat` saw (same device and inode). The file's `lstat`,
    its symlink target, its hash, the last look and the removal all go through that open directory, so a directory
    swapped for a symlink cannot send any of them elsewhere. The last look also checks, by path, that every directory
    from the root down is still the very one opened, so a directory moved out of the root while the file was hashed
    (with a new one made in its place) stops the deletion. A copy kept for a duplicate is checked the same way, and
    pruning reaches each empty directory the same way, with the same check before the `rmdir`. What is left is a move
    in the instant between the last look and the removal: the file then removed is still the one checked, in the
    directory that was checked, wherever that has just been moved to. Only someone who can write to those directories
    can do that, and they could remove the file themselves.
  - *Through a private directory* (issue #21; Linux). After the last look, another file could be put at the name (an
    editor or Syncthing saving a new version by renaming it into place) and be deleted unseen. So the file is not
    unlinked at its name: a new directory `.filo-delete-<random>` is made next to it (mode 0700), opened, and checked
    to be that very directory (not a symlink, the one `lstat` sees at its name, empty), owned by the user and not
    writable by anyone else (only write access lets anyone replace an entry in it); the file is moved into it
    (`renameat`, through the open directories), looked at there (the same file on disk, kind, size and modification
    time as at the last look), then unlinked, and the empty private directory removed. If what was moved is not the
    file checked, it is not deleted: it is moved back to its name with `renameat2(RENAME_NOREPLACE)`, which never
    replaces anything, and the file is skipped ("changed as it was being deleted, so it was put back"). Putting it
    back restores exactly what the other program made; leaving it hidden would let Syncthing delete it everywhere. A
    file checked that then cannot be unlinked there (an append-only file) is put back the same way, and the run says
    it could not be deleted (exit 1). If its name is taken again by then, or `renameat2` is refused, it stays in the
    private directory and the run says where (exit 1). Such a directory, or one left by a run that was killed in
    between, is reported by every scan and never touched by Filo (see Scan). A directory at the private directory's
    name that is not empty, or not the one opened, is never used and left alone: the file stays, and the run fails
    with exit 1.
    *Where no private directory can be had,* the file is unlinked at its name after the last look, as before #21, so
    the instant between the two remains there: when the directory cannot be made for a full disk, a used-up quota or
    a read-only filesystem (deleting must still free space on a full disk), and when the directory made comes out
    owned by someone else or writable by others (filesystems without Unix modes: FAT, exFAT, NTFS, network shares;
    the directory is then removed). The same happens if, in the instant after it is made, another user puts an empty
    directory of their own in its place; they could delete the file directly anyway. Other platforms always unlink at
    the name after the last look (untested).
  - *Removal* is `unlinkat` relative to the open directory: for a file or symlink without flags (it refuses a
    directory), and for a directory with `AT_REMOVEDIR` (it refuses a non-empty directory and a file). On Unix
    systems other than Linux, whose Go `syscall` package has no `unlinkat`, the removal goes through the open
    directory's `Remove` after an `lstat` through it (a swap between the two stays inside that directory); that and
    the fallback on other platforms are untested.
  - *A file deleted by filo is never "missing"*: pairing (returned files) skips deleted rows. A new file with a
    deleted file's content gets a `same_as_deleted` event, shown in the scan summary and the report.
  - *`[r]etention…`* keeps the file's `file_date`, works `keep_until` out again, and is recorded as a one-row batch
    with command `delete`. A retention that is still past says so; the file still leaves this run's list.
  - *Prompts* are read in the background, so Ctrl-C ends a wait for an answer; nothing is deleted after it.
- Scheduled runs only ever scan; Filo never schedules a deletion.

### Duplicates
The report finds duplicate groups. The duplicates CSV lets the
person act on them: export the groups, decide per copy in a spreadsheet, then run `filo delete` on the CSV, which
checks every group against the disk, lists what it will and will not do, asks, and does it. It brushes against the
rule that a delete column in a spreadsheet is unsafe (a sort or a fill-down puts it on the wrong rows), hence the group
rules, the listing before the question and the check at the moment of deleting below.

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
  | a deliberate copy of a directory | a backup, an old laptop's files | a directory (`filo duplicates exclude`) | not a duplicate |

- **Excluded directories.** `filo duplicates` lists everything not counted as duplicates; `filo duplicates exclude DIR`
  makes files under DIR (its whole subtree) never count as duplicates, of each other or of files elsewhere;
  `filo duplicates include DIR` removes the exclusion. Directories are included by default, so `include` only removes
  an entry: if DIR is not excluded itself but a parent is, it says so ("~/Backup/Documents is not excluded itself;
  ~/Backup is"). `exclude` replaces the exclusions under DIR and says which ("excluded ~/Backup (replaces
  ~/Backup/Documents)"). DIR must be inside a root; exclusions are stored in the register. Retention is unaffected. A
  file with two copies in `~/Documents` and one in an excluded `~/Backup` is still a group, of the two. Filo does not
  try to spot such directories (any threshold would miss odd cases); `filo export duplicates` counts what is not
  counted, and while there is no excluded directory ends with "directories that are copies on purpose (backups) can be
  left out entirely: filo duplicates exclude DIR" (see Output, below). Expected to be used rarely, mainly for backups.
- **`filo export duplicates [DIR] -o FILE [--no-scan] [--workers N]`** writes every group with at least one copy under DIR, and all of that
  group's copies, also those outside DIR (the copy to keep may live elsewhere), so exports of different directories
  can overlap. One row per copy, groups numbered in order of their first path. Same CSV rules as the retention CSV
  (byte order mark, formula guard, `-o` required, mode 0600, an existing file replaced only after asking). Columns: `id, export, group, copies,
  directory, name, size, modified, file_date, retention, keep_until, action`. `copies` is the group's size, so a
  filtered view shows when copies are hidden. The retention columns are display only. `export_rows` records each
  row's hash, group and path. **A scan first**, of every root, not only DIR, since a group's copies can be anywhere;
  `--no-scan` skips it (see Lifespan, "A scan first"). The `action` cell is blank, except `keep` on a copy that is one of a set of records (a
  new copy of records is exported with them: `delete` it, or `keep` it to join the set).
- **Why the export reads the register, not the disk alone** (considered 2026-11-16: an export that only walks DIR
  and hashes it, with no root or scan, so the CSV is never stale and no setup is needed; rejected, and the scan first
  above fixes staleness instead):
  - The check in `filo delete FILE.csv` compares each copy with what the export recorded (`export_rows`' hash, and
    the register's inode, size and modification time). That is how it knows a file is still exactly the content
    decided on without reading every file again. An export with no register would have to keep that state
    somewhere else: a second register.
  - Files are deleted only inside a root (`internal/dispose` checks it). A no-root export would break that rule or
    need a second, weaker safety model.
  - Excluded directories, contents always, sets of records and the retention rule across copies all come from the
    register. A plain walk of a directory would bring back groups the person has already decided.
  - Scans only hash new or changed files. An export that walks the disk alone would hash every file every time,
    which is slow on a home directory.
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
  2. Check every decided group against the disk, not the last scan. A file whose directory can be read and that is not
     in it is gone: its row is reported and ignored, and the rest of the group is decided as usual (also when the row
     was deleted from the CSV). A row deleted from the CSV whose file is still there: the group is skipped.
     A file filo cannot check (drive not mounted, directory unreadable) is never "gone": its group is skipped
     ("can't check …: is the drive mounted?"). Each copy must still be at its path and the content the export recorded
     (strict: a group any of whose files changed or moved since the export is skipped whole, even if its copies still
     match each other, so a file is deleted only if it is still exactly the content decided on). Then the retention
     rule. This check reads no content (built 2026-11-16): each copy is looked at (`lstat`) and compared with the
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
  disk, the same file as a copy to delete never counts (same device and inode, looked at live: a directory bind-mounted
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
- **Output** (under the Terminal output rules; the examples are from `internal/cli/testdata/duplicates.txt` and
  `export.txt`, plain form).
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
    both (a record under an excluded directory) has its own kind undone and a line saying the directory is still
    excluded, so one command never undoes two things. A set of records goes as a whole, and the heading names it.
    The change in groups is shown. Nothing to undo (`Not excluded itself`, with the parent that is; `Nothing to
    include`) is exit 0, nothing written. `Next:` (export to decide them) only after including a directory.
  - `filo export duplicates`: the rules as `·` lines in the action words; a line when `keep` is filled in for a new copy
    of a set of records (F3), since the group otherwise looks half decided; the "not counted" count, as the report's
    line; the backups line only while nothing is excluded; `Next: edit the action column, then filo delete FILE`.

```
$ filo duplicates

Not counted as duplicates   1 excluded directory . 1 content marked always . 1 set of identical files kept as separate files . <root>

Excluded directories
  <root>/Backup   3 files . 16 B

Contents marked always
    LICENSE . 3 B each . 5 copies
      projects/a/LICENSE
      projects/b/LICENSE
      projects/c/LICENSE
    ... 2 more copies

Kept as separate files
    terms.pdf . 5 B each . 2 separate files
      2020/terms.pdf   until 2027-12-31
      2021/terms.pdf   until 2028-12-31

Next: filo duplicates include PATH to count one as a duplicate again
```

```
$ filo duplicates exclude <root>/Backup

Excluded   <root>/Backup
  - 3 files under it no longer count as duplicates (still scanned, retentions unchanged)
  - replaces 1 exclusion under it: Documents
  - duplicate groups unchanged: 1

Next: filo duplicates include <root>/Backup to undo it

$ filo duplicates exclude <root>/Backup/Documents

Already excluded   <root>/Backup/Documents
  - through <root>/Backup
```

```
$ filo duplicates include <root>/Backup/Documents

Not excluded itself   <root>/Backup/Documents
  - <root>/Backup is: filo duplicates include <root>/Backup counts all of it again

$ filo duplicates include <root>/Backup

Included again   <root>/Backup
  - 3 files under it count as duplicates again
  - 2 duplicate groups more: 3

Next: filo export duplicates -o FILE to decide them

$ filo duplicates include <root>/projects/a/LICENSE

Duplicate again   LICENSE . 3 B each . 5 copies
  - no longer marked always: 1 duplicate group more: 4

$ filo duplicates include <root>/2020/terms.pdf

Duplicates again   terms.pdf . 5 B each . 2 separate files
  - no longer kept as separate files: 1 duplicate group more: 5

$ filo duplicates include <root>/Documents/notes.txt

Nothing to include   <root>/Documents/notes.txt
  - not excluded, marked always or kept as a separate file: it already counts as a duplicate
```

```
$ filo export duplicates
Scan #4   4 files seen . under 0.1 s . <root>
  [ok] no changes

Export #2 (duplicates)   1 group . 2 copies . <root>

  - in each group: keep for the copy to keep, delete for the rest; always on every row if the content belongs everywhere
  - keep on two or more copies: they are kept as separate files, each with its own retention
  - directories that are copies on purpose (backups) can be left out entirely: filo duplicates exclude DIR

Next: edit the action column, then filo delete '<work>/my d.csv'
```

- **The report** leaves out excluded directories, contents always and sets of records from its duplicate groups and
  reclaimable space, with one line: "1 excluded directory, 2 contents marked always and 1 set of identical files
  kept as separate files, these are not counted as duplicates (use "filo duplicates" to view)". Only the kinds there
  are are named.
- **The words people see** (2026-11-16). This design calls them *records* and *always*; the output, help and README say
  "identical files kept as separate files" (a set's copies: "2 separate files") and "marked always", since "records"
  alone is never explained. The CSV's action words (`keep`, `delete`, `always`) and the code's names (`RecordSets`,
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

- **Register (schema version 8)**: drops `duplicate_marks` and `duplicate_reviews` (and the review batches
  only they used); renames `separate_hashes` to `always_hashes` (existing separate contents become always); adds
  `duplicate_records` (file, hash, set) and `excluded_dirs` (root, path). `exports.kind` `separate` is no longer
  written. The upgrade drops the first design's marks and decision history (the real register's were all carried out
  already); each file's `deleted` events stay, as the history of what was deleted and which copy was kept. Marks not
  yet carried out are dropped too, and the run that upgrades says so once (agreed 2026-11-15): `filo: warning: the
  upgrade dropped 3 duplicate decisions not carried out (kept in the pre-upgrade snapshot); export duplicates again to
  decide them`, in the same line how many contents marked separate are now always. Dropping a mark never deletes
  anything, only forgets a decision, and the pre-upgrade snapshot keeps it; refusing to upgrade would need the old
  build to carry the marks out.

### Showing and setting a file's details
`filo show`, `filo set` and `filo get` (roadmap item 37). The notes first say how each command was built where it
differs from, or adds to, the design agreed on 2026-11-20 that follows them. The README's "One file at a time" and
`filo help show`, `set` and `get` describe it for the person.

**`filo show`, as built** (`internal/cli/show.go`, `internal/show`, `store.ShowFile` and `ShowDir`; the output is in
`internal/cli/testdata/show.txt` and `show-dir.txt`). A directory gone from the disk is shown from
the files the register still holds under it. Under `-r`, a directory holding no file of its own but one below it
gets a block of its own ("no files"), as `ls -R` lists it; a rule set below the shown directory is a line under its
block ("rule * -> bills (7y)"). Sizes are in binary units (`184 KiB`), as everywhere. A path that cannot be shown is
an error line naming it and why (not in the register yet: run filo scan; outside every root; its root's settings
skip it); the other paths are still shown, and the exit status is 1, as for `get`. Read-only: no snapshot.

**`filo set`, as built** (`store.SetFiles`, `internal/cli/set.go`; `internal/cli/testdata/set.txt`): a policy name that is not a policy yet creates it with no
value, without asking (the person's decision, 2026-11-21; the CSV asks first), and says so: "policy tax was created
with no value: its files need a retention until it has one", then `Next: filo policies set tax VALUE`; a dry run says
it would be. The usage line is `filo set FILE... [flags]`, the flags listed under it. A directory or a path
not in the register is refused with the other files, all or nothing. Each change is a review of a batch of command
`set` on the file's current content, so `filo show` says "by filo set". `filo show` ends with
`Next: filo set FILE --retention R or --file-date DATE (filo help set for more)` (for a run of directories only,
`Next: filo show FILE for one file, filo set FILE... to change files (filo help set for more)`).

**`filo set --duplicates`, as built** (`SetValues.Duplicates`; `internal/cli/testdata/set-duplicates.txt`), in the same
transaction as the other flags. A decision is the content's as a
whole, read as the duplicate groups read it (hard links one copy; a copy is a record if any of its paths is), whichever
copies are named: it was `always`, or records if any copy is one, or counted. `always` replaces the content's sets of
records (asked, "was: a separate record"); `records` leaves the content exactly one set, the copies named (any other
set of it goes, so no set of one is left behind), and the content no longer always; `counted` undoes both, so it is
counted as a duplicate again. Refused, all
or nothing: a symlink, an empty file, a file never read or unreadable, and for `records` a lone copy or a file under
an excluded directory (not counted as a copy). The heading's fact is the decision ("always", "records", "counted as
duplicates"), with how many contents and copies. `filo show`'s Next line names `--duplicates always|records`.

**`filo get`, as built** (`internal/cli/get.go`, `show.Fields` and `File.Value`; `view.Line`, `Pairs`, `Record`). The values are for scripts: as stored ("" for none), `status` by its register name (`kept`,
`needs_file_date` …, or `missing`, `deleted`), `duplicates` the content's decision, `size` in bytes, `modified` in RFC
3339 local time, `first_seen` a local date. Nothing is read from the disk. The layout follows what was asked (how
many files and fields), not what was found, so a script sees one format when a path fails. Piped, a value or name is
escaped as everywhere (a tab in a name shows as `\t`, a trailing space as `\x20`, so the tabs between fields are
always Filo's). `duplicates` is empty for a file with no content to decide on (a symlink, an empty file, one never
read), and records when any copy of the content is one; `modified` is empty when not known. **No `-0`** (decided
2026-11-21): `get`'s rows carry values as well as the path, so they are no list of names for `xargs -0`, and a raw
name would have been the one exception to escaping. Everything `get` prints is escaped; NUL-separated names belong
to `filo find -0` (see Finding files), whose output is the list of files.

Agreed with the person on 2026-11-20. Everything Filo holds about a file, from the command line, and every decision
it shows can be changed by a command: quicker than an export, edit and update round for a few files (the CSV stays
the tool for many). Replaces "Later b, setting dates in place".
- **`filo show PATH...`**, not a bare `filo FILE`: a file named like a command (`scan`, `delete`) would be guessed
  at. Paths are looked up in the register, not on the disk: a missing file works, and a file deleted by Filo is
  found by its last path. This lookup (a typed path to a root and a register path) is shared later by 36 and 18a.
- **Observed and decided.** What Filo observed is shown and never edited, since a scan corrects it: paths now and
  before (moves), root, status, size, modified time, hash, first seen, the copies with the same content. What the
  person decided is all editable by command: `file_date`, the retention (a date, a period, `forever` or a policy),
  the duplicate decisions (always, records) and, with 18a, tags. Shown but not edited: `keep_until` (worked out),
  the review record (history), and "policy set by rule X" (changed through `filo rules`; setting a retention by
  hand replaces it, as the CSV does).
- **A directory** shows a summary: its root and whether it is reachable; files and size by status; retention by
  state (dated, needs a date, candidates, `forever`) and by policy; the rules that apply and any ties; whether it is
  excluded from duplicates and how many files have copies; later, tags. Only its own files unless `-r` or
  `--recursive` is given, with one line per subdirectory and its count. A directory's decisions are its rules and
  its exclusion, which have their commands already; `set` does not take a directory (a rule also covers files that
  arrive later; two ways of doing it would blur which one set a file's retention).
- **`filo set FILE... [--file-date DATE | --no-file-date] [--retention R | --no-retention] [--duplicates
  always|records|counted]`.** Several flags at once, in any order. A contradiction (`--file-date` with
  `--no-file-date`) or no flag at all is refused before anything is checked. All or nothing: if any file is
  refused, nothing is written and every refusal is listed. The retention CSV's rules apply unchanged: a period needs
  a `file_date` (stored, or given in the same command), so `--no-file-date` on a file whose retention is a period is
  refused; a new `file_date` works a period out again; past dates are accepted and listed with the hint to check for
  a typo; clearing asks first (or `-y`); no date is read from the file name (the CSV only suggests one where the
  person sees it). Recorded as a review batch, like an update. `--duplicates` is about the content, not the one
  file, and the output says so: `always` covers every copy ("applies to 3 copies"), `records` needs two or more
  files with the same content, all named, and `counted` undoes either, as `filo duplicates include FILE` does. There
  are no separate `filo duplicates always` or `records` commands (one way to do each thing).
- **`filo get FILE... --retention --file-date --keep-until ...`**: the read-only pair of `set`, for scripts and
  quick checks, with the same flag names plus read-only fields (`--keep-until`, `--hash`, `--size`). One flag on one
  file prints the bare value. The layout for several files and fields (file names are untrusted when piped) is
  settled with the mockups.
- **`filo show FILE`, the agreed output** (2026-11-20). Each file starts with a title line: the name,
  then a summary (status, size, directory); several files are shown in full one after another, each title with a
  bold `[n/total]` in front (as `filo delete` numbers its files; no counter for one file), and the sections indented
  under it, so only titles start at the left edge. Order: File, Retention, Copies. One `Next:` line at the end.

  ```
  [1/3] 2022-01 British Gas.pdf   kept until 2029-01-31 . 184 KB . ~/Documents/bills
    File
      path        ~/Documents/bills/2022-01 British Gas.pdf
      root        ~/Documents
      size        184 KB
      modified    2022-02-03 09:14   unchanged since the last scan (2026-11-20)
      sha256      3f9a1c0e8b2d47f6a9e0c5d1b7a3f2e8c4d6b0a9e1f3c5d7b9a2e4f6c8d0e21e
      first seen  2026-11-12 (scan #3)
      moved from  ~/Documents/inbox/british-gas.pdf   2026-11-12 to 2026-11-13   ... 2 more: --history
    Retention
      status      kept
      retention   bills (7y), a policy set by rule bills/*
      file_date   2022-01-31
      keep_until  2029-01-31   in 2 years 2 months
      reviewed    2026-11-14, by filo update, on this content
    Copies   1 other file with the same content . counted as a duplicate
      ~/Backup/bills/2022-01.pdf   until 2029-01-31

  [2/3] 2025-03 Water.pdf   [!] needs a retention . 61 KB . ~/Documents/bills
    ...
      status      [!] undated: no retention

  [3/3] 2015-06 Council Tax.pdf   deleted by filo on 2026-11-16 . 90 KB . ~/Documents/bills
    ...

  Next: filo set FILE --retention R, --file-date DATE or --duplicates always|records (filo help set for more)
  ```
  - **The disk check** is on the `modified` line: an `lstat` compared with the register's size, modified time and
    inode, reading no content ("unchanged since the last scan (DATE)", or "[!] changed on disk since the last scan:
    run filo scan"); a file not there says "[!] not on disk" on the `path` line. **`--verify`** re-hashes the file
    and says on the `sha256` line "content matches the register (read now)" or "[!] differs from the register"
    (the one-file form of 7a; show still records nothing). Sidecar hash files are 7b's, which adds a line here.
  - The full SHA-256, as `sha256sum` prints it. `keep_until` with the time left: years and months, days under a
    month, "today", and "[!] … ago" once past. `file_date` only when one is stored (no date read from the name).
  - **Earlier paths:** the latest, with the dates Filo saw the file there, then "… N more: --history"; `--history`
    lists every path newest first, each with its from and to dates.
  - A missing file and a file deleted by Filo are shown from the register, with their status in the title. A path
    the register does not know is an error: "not in the register yet: run filo scan", or "outside every root".
- **`filo show DIR`, the agreed output** (2026-11-20). The same shape: a title line with a summary,
  sections indented under it, one `Next:` line. No retention or copies counts: the contents are listed instead.

  ```
  bills   directory . 3 directories . 14 files . 2.1 MB . ~/Documents
    Directory
      path        ~/Documents/bills
      root        ~/Documents   reachable . last scan 2026-11-20
      files       12 present . 1 missing . 1 unreadable . 4 deleted by filo
    Rules   in the order tried
      ~/Documents/bills   *Council*   council (3y)
      ~/Documents/bills   *           bills (7y)
      ~/Documents         *.pdf       documents (10y)
    Directories   3
      2023/     40 files . 1.9 MB   [!] 1 needs a decision
      2024/     38 files . 1.7 MB
      scans/    12 files .  30 MB   [!] 12 need a decision
    Files   14
      2019-04 Council Tax.pdf      88 KB   [!] expired 2026-11-01
      2022-01 British Gas.pdf     184 KB   kept until 2029-01-31 . has copies
      2024-12 Gas.pdf              90 KB   [!] missing since 2026-11-18
      2025-03 Water.pdf            61 KB   [!] undated
      locked.pdf                   12 KB   [!] unreadable
      terms.pdf                     5 KB   forever . a separate record
      ...

  Next: filo show FILE for one file, filo set FILE... to change files (filo help set for more)
  ```
  - **Rules:** every rule that decides files here, in the order tried, with the directory it is set on (a parent's
    rules included).
  - **Directories** first, as `ls` lists them, with a trailing `/`: each with its whole subtree's file count and
    size, and how many need a decision.
  - **Files:** all of them (no cut at 10), sorted by name: the name, the size, then the status with its date
    ("kept until", "expired", "missing since"), then the duplicate state ("has copies", "a separate record",
    "copies on purpose"). Files deleted by Filo are counted on the Directory line, not listed.
  - **With `-r`:** the title says "and below" and the counts cover the whole subtree; the Directories and Files
    sections become a block per directory, as `ls -R` (the same directories-then-files order at each level), each
    under a header with the directory's path from the shown one, its file count and how many need a decision. The
    Rules section lists the shown directory's rules as without `-r`; a rule set on a directory below it is listed
    under that directory's block header (agreed 2026-11-20):

    ```
    bills and below   directory . 3 directories . 9 files . 34 MB . ~/Documents
      ...
      bills/   3 files
        2019-04 Council Tax.pdf      88 KB   [!] expired 2026-11-01
        2022-01 British Gas.pdf     184 KB   kept until 2029-01-31 . has copies
        2025-03 Water.pdf            61 KB   [!] undated
      bills/2023/   2 files
        2023-01 Gas.pdf              95 KB   kept until 2030-01-31
        2023-02 Gas.pdf              91 KB   [!] undated
      bills/scans/   2 files . [!] 2 need a decision
        0001.pdf                     15 MB   [!] undated
        0002.pdf                     15 MB   [!] undated
    ```
- **`filo set`, the agreed output** (2026-11-20). It asks **only when it would replace a value
  already stored** (a different `file_date` or retention, a clear, or a content's existing duplicates decision),
  showing was and now for each field given, with `until` to show the effect on `keep_until`; giving a value to a
  file that has none is not asked. `-y` answers yes; no terminal and no `-y` changes nothing. All or nothing: no
  changes no file, not even those that had no value. `l` lists every file with its was and now values, as `filo
  update` does. `--dry-run` prints the same title and tables and ends "Dry run: nothing was changed.", without
  asking. Replacing a rule's policy is noted on the row ("set by rule *.pdf").

  ```
  Set   file_date 2024-03-31 . retention 7y . 3 files . ~/Documents/receipts

  [!] 1 file has values that would be replaced:
      file_date                  retention                until
      was          now           was          now         was          now
      2024-03-02   2024-03-31    bills (7y)   7y          2031-03-02   2031-03-31   lunch.pdf   (set by rule *.pdf)

    - 1 file gets them for the first time: coffee.pdf
    - 1 file already has them: taxi.pdf

  Replace the values of 1 file and set 3? [y/N/l]
  Done   file_date 2024-03-31 . retention 7y . 2 files changed . 1 unchanged . ~/Documents/receipts
  ```

  Nothing replaced, so no question:

  ```
  Done   retention bills (7y) . 1 file . ~/Documents/bills
      retention        until
      was     now      was    now
      none    bills    none   2032-03-31   2025-03 Water.pdf
  ```

  A refusal writes nothing and lists every refused file:

  ```
  [refused] Nothing was changed: 1 of 3 files was refused
    [refused] coffee.pdf   retention 7y needs a file_date to count from: add --file-date DATE

  Next: filo set FILE --file-date DATE --retention 7y for that file, or leave it out
  ```

  A file left past its retention is listed after `Done` with the update's typo hint ("Check for a typo, such as 7d
  for 7y"), every one of them (the update, from a CSV, shows the first ten in a terminal; `filo set` is a command
  naming its files, so it shows every file it affected), then "Nothing has been deleted. Next: filo delete".
  Clearing (`--no-retention`) asks in the same shape, adding "they will need a retention again". `--duplicates`
  names the content and every copy it covers:

  ```
  Done   always . 1 content . applies to 3 copies
      ~/Projects/a/LICENSE   (named)
      ~/Projects/b/LICENSE
      ~/Projects/c/LICENSE
    - these copies no longer count as duplicates; filo set FILE --duplicates counted undoes it
  ```
- **`filo get`, the agreed output** (2026-11-20). Read-only, for scripts and quick checks. Fields are
  `set`'s plus the read-only ones (`--file-date`, `--retention`, `--keep-until`, `--status`, `--duplicates`, `--size`,
  `--modified`, `--sha256`, `--root`, `--first-seen`), printed in the order given; no `--all` (that is `show`). A
  field with no value prints as empty, never "none" (a policy may be named `none`; a script tests for empty).
  - One file, one field: the bare value, terminal or piped (`filo get tax.pdf --retention` prints `7y`).
  - One file, several fields: name and value lines (`retention    7y`); piped, a tab between them.
  - Several files: one row per file, values in the order given and the path last; in a terminal with a header
    and `~`, piped with no header, tab-separated, the path absolute, last. (A `-0` was planned here and dropped when
    built: see `filo get`, as built, above.)

    ```
    retention    keep_until
    7y           2031-03-31   ~/Documents/receipts/coffee.pdf
                 2030-02-28   ~/Documents/receipts/lunch.pdf
    forever      forever      ~/Documents/receipts/taxi.pdf
    ```
  - A path the register does not know: the error line on stderr, the other files still printed, exit status 1.

### Finding files
`filo find` (roadmap item 19). Agreed with the person on 2026-11-21. Finds files in the register by name, to read
in a terminal or to hand to standard tools (`xargs`, `tar`) through a pipe. It covers what the register holds, not
file contents (see `roadmap.md`, Rejected, "Building search"); tags add `--tag` and `--no-tag` (see Tags).

- **`filo find [DIR] [--name PATTERN] [-0] [--no-scan] [--workers N]`.** Conditions combine with AND only; an OR is
  two runs. With no `--name`, every file under DIR.
- **`--name`** matches the file's base name, never its directory (that is DIR's job), ignoring case (Unicode case
  folding, so `bill`, `Bill` and `BILL` match alike), and `*`, matching any run of characters, is the only wildcard:
  `?` and `[` occur in real names (`Q1 [draft].xlsx`) and match themselves, as they do in a tag search. A pattern
  with no `*` matches the whole name exactly.

  | `--name` | finds | does not find |
  |---|---|---|
  | `*gas*` | `2026-10-03 British Gas Bill.pdf` | `gas/notes.txt` (the directory) |
  | `*.PDF` | `bill.pdf`, `Bill.Pdf` | `bill.pdf.bak` |
  | `Q1 [draft]*` | `Q1 [draft] budget.xlsx` | |
  | `report?.pdf` | only `report?.pdf` | `report1.pdf` |
- **DIR** is one directory, looked up in the register as `filo show` looks a path up (so a directory gone from the
  disk is no error; it just holds no file present now), and searched with everything below it: finding is
  recursive by nature, so there is no `-r`. With no DIR, every root: the point is finding the file wherever it is,
  from wherever the person is, not the current directory as `find(1)` does. One DIR only: a search of two places is
  two runs, as an OR is. A DIR outside every root, a file given as DIR ("is a file: use filo show"), or a symlink
  ("give the directory it points to") is a usage error. A path not on the disk that the register never held a file
  under is an error, as for `filo show` (a review finding, 2026-11-21): a mistyped DIR must not read as a search that
  found nothing.
- **A scan first**, as every export does (see Lifespan, "A scan first"), for the same reason: a file saved since the
  last scan is found, and the incremental scan is the "is a scan needed?" check. It scans DIR when one is given
  (its root, when DIR is gone from the disk, so the files it held are found missing), otherwise every root. `--no-scan` skips it and lists from the register as it is; `--workers N` is passed to the
  scan and refused with `--no-scan`. In a terminal the scan's block comes first, as before an export (two lines when
  nothing changed). Piped, the scan prints nothing on stdout, so the list stays a list of paths. A root that fails
  to scan stops the command with nothing listed and exit status 1, as an export stops, so a missing disk never
  gives a list that looks complete: in a terminal its block says why, piped one `filo:` line on stderr does.
  With `--no-scan` in a terminal, one `·` line names when the last scan of the place finished, as an export's does.
  Since a scan that found nothing takes no snapshot (see Register snapshots), a `find` leaves none unless its scan
  found something.
- **What is listed**: the regular files the last scan saw at their current paths, each once. Empty files are
  listed, and so are files that could not be read (noted `could not be read`: they are there, and only their hash
  is unknown). A possible move is listed at its new path. A file with hard links is one file: listed once, under one
  of its paths, with its links noted (piped, that one path). Links are the paths in its root with the same inode,
  size and modified time, counted over the whole root, not only DIR; the inode alone is not enough, since one kept
  current in an unreadable directory may have been reused by another file (a review finding, 2026-11-21). Not listed: missing files, files deleted by Filo, and
  symlinks (the file a symlink points to is listed if it is inside a root; broken symlinks are in `filo report`).
- **In a terminal**: a heading (how many files, their size, the search, where), then every match, never cut (the
  person's decision, 2026-11-21: a search shows all it found), through the file list component, ordered by root,
  then by path; with several roots, a sub-heading per root. Columns: modified date and size, then the path. Nothing
  found: the heading and a Next line about the name.

  ```
  Found   3 files . 412 KiB . named *gas* . ~/Documents

      modified        size
      2025-01-14    48 KiB   house/gas safety certificate.pdf
      2026-09-02   180 KiB   received/2026/09/2026-09-02 British Gas Bill for September 2026.pdf
      2026-10-03   184 KiB   received/2026/10/2026-10-03 British Gas Bill for October 2026.pdf
  ```
- **Piped**: one absolute path per line, escaped as all output is (see Commands and scheduling, "Output cannot act
  on the terminal"), since a piped list often ends in a terminal (`| less`). Almost every name is unchanged by it.
  A space that ends a name stays a space (not `\x20`, as elsewhere): the line is for a tool, which needs the bytes.
  When escaping changed a name, the list is still written and then one stderr line says so,
  `filo: 1 name was escaped and will not match on disk; use -0`, with exit status 1, so a script does not skip a
  file unnoticed. Nothing found: no output.
- **`-0`**: the absolute paths as they are, byte for byte, each followed by a NUL, which no path can hold, for
  `xargs -0` and `tar --null -T -`. It is **the one output not escaped**: a list of names for a tool must be the
  names. It is refused when stdout is a terminal (a usage error, "-0 writes raw names for a pipe, such as xargs
  -0"), so raw names never go to the screen directly; `filo find -0 | cat` still can (see Accepted risks).
- **Exit status**: 0 when it searched, matches or none (a script tests for empty output; exit 1 means something
  went wrong, as everywhere in Filo); 1 on an error, a root that failed to scan, or a piped name changed by
  escaping; 2 on a usage error.
- **Read-only on the files**, like every command but `filo delete`; it writes the register only through its scan.

### Tags
Roadmap item 18a, designed with the person on 2026-11-19/20 (see `roadmap.md`, Direction, the goal: finding files by
tags, not by directories); the details below were settled when work started, on 2026-11-22. Tags describe files,
so files can be found by tag; a project is a tag (`project:house sale`).

- **One mechanism.** Tags and projects are one thing. **A tag is not a retention:** a file has one retention (a
  date, a period or a policy) and any number of tags, and a tag never carries a retention. A project note (18b) is
  not a tag either: notes and tags are separate (2026-11-22).
- **A tag is `key:value`, or a bare value**, and the key is first-class: files are found by key, by key and value,
  or by a key they lack (`--no-tag sender:`, the tagging to-do list). A file can have several values for one key
  (two projects). Keys are created on the fly, never declared; `filo tags` lists the tags, and a tag exists only
  while some file not deleted by Filo has it. A typo is fixed by renaming the tag (`filo tags rename`), or by hand:
  list its files, add the right tag, remove the wrong one.
- **Spelling.** One rule: the first colon separates the key when what is before it is a valid key; otherwise the
  whole tag is a bare value.
  - A key is lower case whatever is typed, has no spaces, holds only letters, digits, `-` and `_`, and at least one
    letter. A value may hold spaces: trimmed at the ends, a run of spaces (or tabs) is one space. It keeps the case
    first typed, and matches ignoring case (Unicode case folding, as `--name` does).
  - **The spelling first typed is the tag's**, for every file: once a file has `sender:British Gas`, tagging
    another with `sender:british gas` gives it `sender:British Gas`, and `filo tag` says so. `filo tags rename`
    changes it, the case alone included.
  - A tag cannot hold `;` (the CSV's separator) or `*` (the one wildcard in a search, as in `filo find --name`),
    nor a control character; `filo tag` refuses them and says why. `?` and `[` are ordinary characters.
  - A tag needs a value: `filo tag FILE sender:` is refused ("give a value"), since `key:` alone is only a search,
    or, for `untag`, every value of the key.

  | typed | key | value |
  |---|---|---|
  | `Sender: British  Gas ` | `sender` | `British Gas` |
  | `project:trip: Paris 2027` | `project` | `trip: Paris 2027` |
  | `10:30 call` | (bare) | `10:30 call` |
  | `re: invoice` | `re` | `invoice` (odd but logical) |
  | `urgent?` | (bare) | `urgent?` |
  | `sender:` | refused: give a value | |
  | `type:bill;receipt` | refused: `;` | |
- **`filo tag FILE... TAG...`** and **`filo untag FILE... TAG...`**. The files come first: the arguments that are
  on the disk are FILEs, and the first that is not starts the TAGs, so a tag that is also the name of something in
  the current directory is read as a FILE (run from another directory, or give the tag after one that is not a
  path). A path after a tag is a usage error ("give the files first, then the tags"). Each FILE is a regular file in
  a root, looked up as `filo set` looks one up (a directory, a symlink or a file at a path it has moved from is
  refused, with why). **All or nothing**, as `filo set`: if any file is refused, nothing changes, and the refusals are
  listed. Neither asks: they change no file, and a tag removed is put back with `filo tag`. `untag` takes `key:` for
  every value of the key, and refuses `*`. A file that has the tags already (or, for `untag`, lacks them) is counted
  and left alone.

  ```
  Tagged   2 files · sender:British Gas · type:bill · ~/Documents

      received/2026/09/2026-09-02 British Gas Bill.pdf   sender:British Gas; type:bill
      received/2026/10/2026-10-03 British Gas Bill.pdf   sender:British Gas; type:bill
    · sender:british gas is written sender:British Gas, as first typed (filo tags rename changes it)
  ```

  Each file is listed with all its tags afterwards, and the heading counts the files left alone (`1 had them
  already`). With nothing to change, one line: `Unchanged   type:bill · 2 files have this tag already` (`do not have
  this tag`, `have none of these tags` for `untag`).
- **A file Filo has not seen yet** (dropped in the inbox, tagged at once): `filo tag` registers only that file, not
  its directory, for speed: a scan of that one file, recorded as a scan whose scope is the file, so the next scan
  treats it as known. The same inode as a file the register has at another path now: refused, "looks moved from X;
  run filo scan" (never guess; a scan pairs it). Outside every root, or where the root's settings skip it (hidden,
  ignored): refused, saying why. Otherwise it is registered as a new file (hashed; its copies' tags taken, below;
  directory rules apply, as in any scan), and the output says `· registered 1 new file`; it counts among the files
  tagged even when its copies gave it the tags already. Each file is registered in its own transaction before the
  tags are written: should the run fail after that (interrupted), the file stays registered, untagged, as a scan
  would have left it. One file given by two paths (through a symlinked directory) is one file. `untag` registers
  nothing: a file not in the register has no tags to remove.
- **Tags follow the file**, as the register tracks it, not its path or content: kept through a move or rename (the
  inbox routine), an edit (unlike retention, which asks again), and while missing. A file deleted by Filo keeps its
  rows in the register but drops out of every listing, count and search. **A copy keeps the tags:** a new file
  with the same content as files present (any root, not empty, not deleted by Filo) takes the union of their tags,
  once, when first seen; after that each copy's tags are its own. The scan says so only when those copies had
  different tags (`· 1 new file took the tags of copies that differed: check with filo show`, and the file
  among the events, `took tags`, with the tags it took), since a copy
  that matches its original needs no look. Deleted files pass on nothing (a restored file is new, as for retention).
  A file saved by writing a new file and renaming it into place (new inode, new content) stays a reported possible
  move, never paired; its event says the old file had tags (`same name, different content; the old file had tags`).
- **`filo tags`**: every tag with how many files have it (files not deleted by Filo: present or missing), bare tags
  first, then by key and value ignoring case; the heading counts the tags, the files tagged and the present files
  with no tag.

  ```
  Tags   4 · 5 files tagged · 12 untagged

    urgent                1 file
    project:house sale   2 files
    sender:British Gas   3 files
    type:bill            3 files
  ```

  Laid out as `filo policies` is. No tag at all: `Tags   none · 12 untagged`, and `Next: filo tag FILE... TAG...
  tags files`.
- **`filo tags rename OLD NEW`**, across every file that has OLD, which fixes a typo in the key or the value, or
  changes only the case (`british gas` to `British Gas`). OLD matches ignoring case, never with `*`. `OLD` and `NEW`
  may both be `key:`, renaming the key of every value it has (the same key twice is a usage error). A file that has
  NEW already keeps one: the two merge, and the output counts them (`2 files had sender:British Gas already`; for a
  key, `had the same value under sender: already`). NEW is spelled as typed on every file that has it, so a file
  that had it in another spelling is respelled, and counted apart (`2 other files respelled`). OLD that no file has is an error ("no file
  has the tag X (filo tags lists them)"). It does not ask, as `filo policies rename` does not.

  ```
  Done   renamed sender:british gas to sender:British Gas · 3 files
  ```

  A rename that changes nothing (`urgent` to `urgent`) says `Unchanged   urgent is spelled so already`.
- **Finding by tag**: `filo find --tag TAG` and `--no-tag TAG`, each repeatable; every condition must hold (AND,
  with `--name` too). A search is `key:value`, where `*` in the value matches any run of characters; `key:` (any
  value of the key; `--no-tag sender:` lists the files with no sender); or a bare value, which finds bare tags only
  (`*` allowed). Values match ignoring case; the key is lower-cased as when tagging. The heading names the search
  (`tagged sender:British Gas`, `not tagged type:`). In a terminal, a file's tags follow its path, and Filo's own
  notes come after them in brackets, so no tag reads as one (`could not be read`); piped and with `-0`, the list is
  paths only, as before. A file with hard links is one file to `find` (see Finding files), and the register keeps
  each link as a file of its own, each with its own tags: `find` takes the tags of all its links together, so a file
  tagged through one link is tagged. `filo show FILE` has a Tags part: how many, then one tag a line (or `none`), so
  a tag named `none` cannot be mistaken; a file deleted by Filo shows the tags it had, `as when it was deleted`.
- **The tags CSV**: `filo export tags [DIR] -o FILE [--no-scan] [--workers N]`, a scan first as every export, and
  read back by `filo update FILE [--dry-run] [-y]`, which tells it from a retention or duplicates CSV by its header.
  Columns: `id, export, directory, name, size, modified, tags`; every present file under DIR (every root with no
  DIR). The `tags` cell holds the file's tags, separated by `; `, in the order `filo tags` lists them. **The cell
  replaces the file's tags:** a tag not in it is removed, so an untouched cell changes nothing and an emptied one
  removes all. Cells are read with the spelling rules, so `type:Bill` on a file tagged `type:bill` changes nothing
  (the first spelling stays: `filo tags rename` changes it), and a tag typed twice is one. A cell left as exported
  is no decision, whatever happened to the file since, and a cell with the tags the file has already changes nothing
  (a CSV applied twice). A row whose file's tags changed since the export (by `tag`, `untag` or `tags rename`) to
  others than the cell's is refused, as a changed file is for retention, and so are a row with a tag that breaks the
  spelling rules, a file deleted by Filo since, and a file listed twice (even under two exports); the other rows still
  apply. A cell that starts with a character a spreadsheet would read as a formula (`=`, `+`, `-`, `@`), or with an
  apostrophe, is written after an apostrophe, which the update drops. An update that removes tags lists those rows
  and asks first (`Remove them? [y/N/l]`), as clearing a retention does, since an emptied column (a spreadsheet that
  lost it on a save) would remove every tag at once. `-y` answers yes, for a script (the person's decision,
  2026-11-22); it answers only this question, so `filo update -y` of a retention CSV is a usage error (clearing a
  retention has no `-y`). No, or no terminal and no `-y`, leaves those rows as they were; the other rows are
  recorded either way. Ctrl-C at the question records nothing.
- **The combined CSV** (decided 2026-11-27): `filo export [DIR] -o FILE [--no-scan] [--workers N]`, with no word
  after `export`, writes every column a person edits in one CSV, so a tag seen while dating files, or a date seen
  while tagging, is set in the same pass. It is the retention export's CSV with the tags CSV's column after it:
  `id, export, directory, name, size, modified, file_date, retention, keep_until, tags`. Everything else is as for
  those two: a scan of DIR (or every root) first, every present file, the `!` mark on a `file_date` read from the
  name, the formula guard, `-o` asked about if it exists. The export is recorded as kind `files`; each row keeps the
  hash, `file_date`, `retention`, `keep_until` and the tags cell as exported (no schema change: `exports.kind` is
  text). A first argument `retention`, `tags` or `duplicates` is still that export (until roadmap 40), so a directory
  with one of those names is given with `./` before it (`./tags`).
  - **Read back by `filo update FILE [--dry-run] [-y]`**, which takes a CSV with both a `retention` and a `tags`
    column, whose rows name `files` exports, as a combined CSV (a retention CSV of an older export with a `tags`
    column of the person's own stays a retention CSV). Each row is checked with the rules of both: the retention
    half as a retention CSV's row (file_date, retention, the `!` mark, keep_until left alone, the file unchanged
    since the export, a newer decision not overwritten), the tags half as a tags CSV's row. A symlink's row (the
    retention CSV has them, the tags CSV does not) has an empty `tags` cell, and tags typed in it are refused, as
    `filo tag` refuses a symlink ("give the file it points to"). **A row refused by
    either half is refused whole**, with each reason, so a file never ends up half updated; the other rows still
    apply. A row from another kind of export is refused. A CSV whose rows name `files` exports but which has lost
    its `retention` or its `tags` column (deleted or renamed in the spreadsheet) is refused as a whole, naming the
    column, since a lost `tags` column would otherwise read as every tag removed.
  - **One question, before anything is written.** If any row clears a stored retention, removes a tag, or names a
    policy that does not exist yet, the update first lists them (the first ones of each, `l` lists all) and asks
    once, `Apply the update? Nothing is recorded otherwise. [y/N/l]`. Yes records every row that was not refused,
    in one transaction. **No, end of input, Ctrl-C, or no terminal: nothing is recorded**, not even the rows that
    needed no question, and one line says why and how to go on; so an emptied column never half applies. With no
    terminal (and no `-y`) the exit code is 1, so a script or timer can tell nothing was done (the person's
    decision, 2026-11-27); a no typed at the question is the person's choice, exit 0. `-y`
    answers yes, for a script (the person's decision, 2026-11-27: the same `-y` for clearing, removing and new
    policies). An update with none of these asks nothing. `--dry-run` shows the same lists, asks nothing and
    writes nothing.
  - The output: `Update   FILE . export #N . R rows . <root>`, then the counts (files given a retention, files put
    on a policy with no value yet, retentions cleared, files whose tags changed, rows unchanged, rows refused, and
    the policies created), the files whose tags changed, the refused rows, and the files left past their retention
    with the hint to check for a typo, as the two updates show them.
  - The retention and tags CSVs keep their own rules (their questions keep the other rows, and `-y` is refused for
    a retention CSV) until roadmap 40 removes them.
- **Renaming a file by its `name` cell** (decided 2026-11-27, roadmap 39; schema version 13). In the combined CSV, a
  `name` cell changed from what was exported asks for the file to be renamed in its own directory; no second column.
  This is part of the export and bulk update, not a clone of `mv`: Filo has no command that moves or copies files. It
  is the second of the two ways Filo changes the person's files, beside `filo delete`, and is done by
  `internal/dispose` with the same machinery (inside its root, through the opened directories, never by path).
  - **A change is the cell compared with the cell as exported** (`export_rows.name`), so the formula guard
    (`'=odd.txt`) left alone is no change. As in the tags cell, a name that starts with `'` itself is written after
    another `'` in the combined CSV, so on a change one `'` before `=`, `+`, `-`, `@`, tab, CR or `'` is dropped from
    the new cell and the name is always read back as typed. A row of an export made before schema 13 has no exported name: a changed cell is refused,
    "export again". The `directory` cell stays display only: moving is not a rename.
  - **The new name must be one plain name the next scan sees as the same file in the same place.** A changed cell is
    refused, with one line saying why, when:

    | `name` cell, edited | Result |
    |---|---|
    | `Britsh Gas bill.pdf` → `British Gas bill.pdf`; `bill.pdf` → `Bill.pdf` | renamed |
    | `notes.txt ` → `notes.txt` (a space at the start or end added or removed) | renamed, noted in the summary |
    | `.bashrc.bak` → `.bashrc.old` (hidden to hidden, in a root that includes hidden files) | renamed |
    | a name holding `/`, or empty, `.` or `..` | refused: one name in the same directory |
    | `bill.pdf` → `.bill.pdf`, or `.notes` → `notes` | refused: a rename never hides or unhides a file |
    | a name starting `.filo-delete-` | refused: Filo's own |
    | a name the root ignores (`--ignore`) | refused: the next scan would lose it |
    | a control character (tab, newline, escape) in it | refused |
    | more than 255 bytes | refused (the filesystem's limit) |
    | an old name that is not valid UTF-8 (exported with U+FFFD), its cell changed | refused: its real name cannot be written back |
    | two rows giving one directory the same new name | both refused |
    | a new name another row's file has now (a chain or a swap, `a`→`b` and `b`→`c`) | both refused: "rename one at a time" |
    | a new name a registered file in that directory has | refused |

  - **The check at the rename** is that the file at the old name is still the very file the register knows, at the
    name exported: through the opened directory, the same kind, inode, size and modification time as the last scan
    recorded (for a symlink, its own). No re-hash: a rename keeps the content, and renaming a large file should not
    read it; a changed file's typo is still a typo. A file whose register path changed since the export (moved,
    renamed) is refused, "moved since export #N; export again"; one no longer present is refused. A file edited,
    replaced or gone on disk since the last scan is skipped, "changed since the last scan; run filo scan and export
    again". The rename is `renameat2(RENAME_NOREPLACE)` through the open directory, so a name taken on disk (by a file
    not registered yet) is never replaced: skipped, "a file named X already exists". On a filesystem that ignores
    case, a case-only rename can be seen as taken: skipped, "this filesystem ignores case; rename it by hand" (not
    tested). On other platforms than Linux, which have no no-replace rename, renames are refused with one line
    rather than risk replacing a file (untested). A directory Filo cannot write, or a root not mounted: skipped with
    the reason. A hard link is renamed alone (its other names stay), as `mv` does; a symlink itself, never its target.
    In a Syncthing folder it is an ordinary rename, which Syncthing passes on.
  - **In the update.** Renames are checked, against the register and on disk, with the other rows before the one
    question, and listed in its summary, so every rename asks (or needs `-y`); a fill-down on `name` shows there. After
    yes, the register's changes (retentions, tags, policies) are recorded first, in one transaction, as without
    renames; then each rename is checked again and done, and recorded straight after, so Ctrl-C between them leaves
    an accurate record (it stops after the current one). A rename skipped then leaves its row's retention and tags
    as recorded: they belong to the file, whatever its name. The exit code is 1 if any rename was refused or skipped.
  - **Recording**, as a delete is: each root with a rename gets one `scans` row of kind `rename`; the old path
    closes at it and the new one opens on the same file row, so retention, tags and history stay with the file; a
    `moved` event says "renamed by filo update". The next scan finds the file at its new path, re-hashes it once
    (a rename changes its ctime) and records no change.
  - **Output.** The summary's renames part lists old and new names side by side under each directory, the first
    ones (`l` lists all), with a note on a rename that changes only spaces at the start or end, or the extension
    (`2023 return.pdf` → `2023 return.pd`); neither note refuses anything. In that list a name that starts or ends
    with a space is shown in double quotes (`"notes.txt "`), a `"` in a quoted name as `\"`; other lists keep
    `\x20`. After the run: `N files renamed`, and the renames skipped, each with its reason, under "Skipped: run
    filo scan, then export again". A dry run says "would be renamed".
- **Register (schema version 11)**: a `tags` table on file ids (the key, the value as spelled and its folded form),
  and `tag_changes`, the history of every tag added or removed and by which command, so each change can be written
  to the journal later (9c) without rework. `export_rows` gain `tags`, the cell as exported.

### Tag index pages
Roadmap item 38, designed with the person on 2026-11-22. Filo writes HTML pages that list files by tag, to browse
and tap instead of typing searches: a read-only view of the register written out as static pages, far cheaper than
a UI. An index is declared once on a directory (one per role, say) and kept up to date by Filo, so its links are
relative: on any device that syncs the directory, the pages and the files are side by side (a relative link tested
on the person's phone opens the file).

- **`filo indexes add [DIR] [--name NAME]`** declares an index on DIR (the current directory when not given) and
  writes its pages: `DIR/NAME.html`, and the other pages and `style.css` in `DIR/.NAME/`. NAME is `index` unless
  given; it holds letters, digits, `-` and `_`, and starts with a letter or digit. Refused, with why, and nothing
  created: DIR not a directory in a root, or skipped by the root's settings (hidden, ignored), or `NAME.html` or
  `.NAME` there already (anything at those paths, Filo's or not).

  ```
  Index added   bills/index.html · 214 files · 9 tags · 38 pages · ~/Documents
    · its pages are in bills/.index/; scans skip both
    · 11 files have no tags: they are listed on the page "no tags"

  Next: open ~/Documents/bills/index.html in a browser
  ```

  ```
  filo: bills/index.html exists already: give another name with --name
  ```
- **`filo indexes`** lists them, by root and then path, with their files, tags and when their pages were last
  written; one whose last writing failed is flagged with why.

  ```
  Indexes   2 · 1 needs attention · ~/Documents

    bills/index.html    214 files   9 tags   written 2026-11-22 14:03
    records/tags.html    88 files   4 tags   written 2026-11-21 09:12   ! not up to date
      ! .tags/sender.html was changed since Filo wrote it: not replaced

  Next: move records/.tags/sender.html aside, then filo indexes refresh ~/Documents/records
  ```
- **`filo indexes remove [DIR] [--name NAME] [-y]`** stops keeping an index and removes its files, after listing
  them and asking (`-y` answers; no terminal and no `-y`: refused, as `filo delete` is). A file that is not Filo's
  (below) is left, and so is the directory holding it: the output says so and the exit code is 1, since the person
  needs to look at it. The index is removed from the register either way. When the index's root cannot be reached
  (a drive not mounted, lost or formatted; the person's decision, 2026-11-22), its files cannot be removed: `remove`
  says so, and that should they come back they are no longer Filo's and the next scan registers them, and asks
  `Remove the index anyway, leaving its files? [y/N]` (`-y` answers). An index whose directory is gone from a root
  that is there is removed with nothing more to say: its pages went with it.

  ```
  Remove index   records/tags.html · 12 files · ~/Documents

      records/tags.html
      records/.tags/       11 files: its pages and style.css
    ! records/.tags/notes.txt is not Filo's: it stays, and so does records/.tags/

  Remove the index and its 12 files? [y/N] y
  Done   removed 12 files · records/.tags/ kept, with the 1 file that is not Filo's
  ```
- **`filo indexes refresh [DIR] [--name NAME]`** (no DIR: every index) renders the pages again even when the data
  is unchanged, and writes those that differ, for example after a page Filo refused to replace was moved aside.
  `remove` and `refresh` given a DIR with more than one index and no `--name` are refused, listing the names.

  ```
  Refreshed   2 indexes · ~/Documents
    · bills/index.html    up to date
    ✓ records/tags.html   3 pages written · 1 removed
  ```
- **Scans skip them.** The two paths of each index (`NAME.html` and `.NAME/`) are among the register's own paths,
  which no scan records, whatever the root's hidden setting; not through the root's ignore names, which match a
  name anywhere in the root and would hide every real `index.html` in it. `filo tag` refuses them as it refuses the
  database.
- **Filo's own files.** Filo keeps the hash of every file it writes in an index, and replaces or removes one only
  while it is still exactly what Filo wrote; a file at a path Filo has not written is never replaced. Anything else
  (a page edited by hand, a file dropped into `.NAME/`, a symlink) is left alone and reported. This is the one place
  outside its data directory, `--db` and `-o` files where Filo writes, besides `filo delete`: the files of a tag
  index, created only where nothing exists, replaced or removed only while still what Filo wrote, and a whole index
  removed only by `filo indexes remove`. `internal/tagindex` is the only code that writes or removes them. They are
  Filo's files, not the person's: `internal/dispose` stays the only code that deletes the person's files.
- **When the pages are written.** At the end of every run that committed a change to the register (as the snapshot
  is, so no list of commands is kept), before the snapshot, and by `add` and `refresh`. A run that only read writes
  none. Data first (the person's idea): Filo builds each index's data, hashes it (with the version of the pages'
  layout) and does nothing when that hash is the one stored; otherwise it renders the pages, writes only those whose
  content changed (each to a temporary file in its directory, renamed into place), and removes the pages no longer
  needed, and the directories it made that are left empty. It says nothing when that works. When it fails (a page
  not Filo's, a directory that cannot be reached or written), one line on stderr, `filo: the change is made, but
  ~/Documents/bills/index.html was not brought up to date: bills/index.html was changed since Filo wrote it: not
  replaced (filo indexes lists it)`, and exit 1; the command's own change stands, and the next run that changes what
  the index shows tries again. Only a fully written index records its new data hash, and `filo indexes` shows the
  problem until then.
- **Rendering.** Go's `html/template`, with the templates and stylesheet embedded in the binary; no dependency and
  no JavaScript. Not XSLT: Go has none (a cgo binding would be a dependency and break the static build, and
  `xsltproc` would have to be installed on every device), and `html/template` escapes each value for where it lands
  (text, attribute, link), as untrusted file names and tags need. Links are relative, each part of the path
  URL-encoded (`%20` for a space, so a `#` or `?` in a name cannot break one). The output is deterministic (sorted,
  no times), so syncing it makes no conflict copies.
- **Pages.** Each has a trail at the top (`bills › sender › British Gas`).
  - `NAME.html`: each key with its number of values and files; "other tags", the bare tags; and in a section of
    its own, "11 files have no tags", linking to `.NAME/_untagged.html`.
  - `.NAME/<key>/index.html`: the key's values, each with its number of files; and in a section of its own, "34
    files have no sender tag", linking to `.NAME/<key>/_none.html`. The bare tags have `.NAME/_/index.html`: a key
    has a letter, so none is named `_`. A key with a letter outside `a-z` (`café:`) has a directory named by a slug,
    a dot and eight hex digits of the key (`caf.1a2b3c4d`), since two forms of one letter are one name on some file
    systems; no key holds a dot.
  - `.NAME/<key>/<slug>-<hash>.html`: one value's files. The slug holds only `a-z`, `0-9` and `-` (at most 40), the
    hash is eight hex digits of the key and the folded value. The slug alone would make different tags one name
    (`British Gas` and `British-Gas`, values with no Latin letters, values too long for a file name); with the hash
    a tag's page has the same name on every run and device, and keeps it when only its case is renamed, so
    bookmarks keep working. Two tags of one key with the same name (never seen; the hash makes it unlikely) fail the
    writing, never overwrite each other.
  - A tag can be spelled almost any way (`sender:no sender` is one), so the "no …" pages are told apart from tags
    by their place and wording, and by names no tag page has (`_` first; a slug never starts with one).
  - Each file row: its name (a link that opens the file), its directory, its date, and its other tags, each a link
    to its tag's page. No size, retention, keep-until or duplicates: the pages are for finding files.
  - The files: regular files in the register, present now, under DIR at any depth, each path its own row (a hard
    link too); newest first by `file_date`, else modification time, then path. A link is as fresh as the last scan.
    Indexes nested in one another are independent.
- **Look.** One local stylesheet, `.NAME/style.css`: system fonts and nothing remote (it works offline), a viewport
  meta tag, one column, rows as cards on a narrow screen, tap targets of at least 44px, tags as chips, dark mode by
  `prefers-color-scheme`.
- **Register (schema version 12)**: `tag_indexes` (root, directory, name, the data hash of the last full write,
  when, and the last problem) and `tag_index_files` (each file and directory Filo wrote in an index, with the file's
  hash).

### Commands and scheduling
- Plain names, easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete`, `duplicates` (added
  2026-11-16, see Duplicates), `policies` and `rules` (both 2026-11-16, see Lifespan), `show` (2026-11-21, see Showing and setting a file's
  details), `find` (2026-11-21, see Finding files), `tag`, `untag` and `tags` (2026-11-22, see Tags), `indexes`
  (2026-11-22, see Tag index pages). No
  themed subcommands. There is no `restore`: a file restored from a backup comes back as a new file. "Update" says
  that only existing files change; `filo review` only says it was replaced by `export retention` and `update`.
- **Keep flags and options to a minimum** (2026-11-11). Prefer a safe default and clear output over a flag; prefer
  doing the obvious thing and saying what was done over asking.
- **Output cannot act on the terminal** (2026-11-13). File names can hold any byte but `/` and NUL and come from
  anywhere (attachments, files synced from a phone, an altered register). Everything printed goes through one writer
  that shows control characters, escape sequences, invalid UTF-8 and Unicode bidi controls as visible escapes (`\x1b`,
  `\u202e`), so a name cannot erase lines of the list a person confirms before a deletion, set the window title, write
  the clipboard, or read in another order than it is. Filo writes each line with one call, so a newline passes only as
  the last byte of a write: one inside a write can only come from a name and is shown as `\n`, so a name cannot add
  invented lines to that list. Tabs are shown as `\t` and a backslash as `\\`, so an escape cannot be confused with a
  name holding the same text. The Renderer (see Terminal output) escapes every text it is given this way (`view.Escape`)
  and then adds only its own colour codes; it is the only way to stdout, and stderr has one too, `errorLine` (each line
  escaped, `filo: ` before it), besides the progress line's own control codes. The one exception is `filo find -0`,
  a NUL-separated list of names for a tool, written as they are by the Renderer's raw path writer and refused when
  stdout is a terminal (see Finding files). A test
  (`TestOutputGoesThroughTheRenderer`) fails on any other write in the CLI (see Help and errors). Trailing alignment is
  dropped from a line, but a space that ends a file name is shown as `\x20` (2026-11-16, from the code review), so
  `report ` and `report` never read the same; and a relative path that starts with `~` (a directory named `~` inside a
  root) is shown as `./~/…`, so it never reads as a path in the home directory. The CSV export guards against
  spreadsheet formulas separately.
- **Prompts only in a terminal** (2026-11-12, replacing "no interactive prompts"). A command asks only where a person
  should look first: `filo update` before clearing dates, removing tags, creating policies or renaming files, and
  `filo delete`. Declining, or having no
  terminal on standard input, is always the safe choice, so Filo still works the same from a script or a timer.
- `filo roots` lists roots (settings, mount point, files, size, last scan); `filo roots add DIR [--include-hidden]
  [--ignore NAME]... [--no-scan]` registers one and scans it (absorbing any roots inside it on the same filesystem,
  see Roots); `filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...` replaces its settings.
- `filo scan [DIR] [--workers N] [--allow-empty]` scans DIR, which must be inside a registered root (see Roots), with
  the root's settings; with no DIR, every root in turn, each in its own transaction. A root that fails (for example
  unmounted) is reported in its place on stdout (see Terminal output, Scan and roots) and the others are still
  scanned. `--workers` is 1–64, default 4.
- `filo report [--top N]`: see section 4.
- `filo export [DIR] -o FILE [--no-scan] [--workers N]` and `filo update FILE [--dry-run] [-y]`: the combined CSV,
  see Tags.
- `filo export retention [DIR] -o FILE [--no-scan] [--workers N]` and `filo update FILE [--dry-run]`: see Lifespan. Every export
  scans first unless `--no-scan` is given (see Lifespan, "A scan first"), with `--workers` as for `filo scan`
  (refused with `--no-scan`). The word after `export`
  names the CSV, so a later CSV is a new word, not a new command, and no word is the combined CSV (2026-11-27);
  `update` tells the kind from the CSV's header (and, for a CSV with both `retention` and `tags`, from its exports).
- `filo find [DIR] [--name PATTERN] [--tag TAG]... [--no-tag TAG]... [-0] [--no-scan] [--workers N]`: see Finding
  files and Tags.
- `filo tag FILE... TAG...`, `filo untag FILE... TAG...`, `filo tags`, `filo tags rename OLD NEW`, and `filo export
  tags [DIR] -o FILE [--no-scan] [--workers N]`, read back by `filo update`: see Tags.
- `filo indexes`, `filo indexes add [DIR] [--name NAME]`, `filo indexes remove [DIR] [--name NAME] [-y]` and `filo
  indexes refresh [DIR] [--name NAME]`: see Tag index pages.
- `filo delete [DIR] [-y]`: lists the files past their keep-until date and deletes the ones chosen (see Disposal).
- `filo delete FILE.csv [-y]`: carries out a duplicates CSV (see Duplicates). An argument that is a directory is
  DIR; anything else is a CSV.
- `filo export duplicates [DIR] -o FILE`: see Duplicates.
- `filo duplicates`, `filo duplicates exclude DIR`, `filo duplicates include PATH`: what is
  not counted as duplicates (excluded directories, contents always, sets of records), and undoing it.
- `--db PATH` is accepted before or after any command; `FILO_DB` is the fallback, then the default location. Flags may
  come before or after PATH. Built on the standard library's `flag` package (no CLI framework).
- **A path that starts with `~` is refused** (2026-11-11): every DIR and FILE, `-o`, `--db` and `FILO_DB`.
  The shell expands `~` only when it is unquoted, so `"~/Documents"` would otherwise name a directory called `~` in the
  current directory (and `--db "~/f.db"` would create one). Filo does not expand it itself, because a name starting
  with `~` is legal. The one-line error suggests `$HOME/...` (or the full path for `~user`), and `./~...` for a name
  that really starts with `~`. Checked before the database is opened, so nothing is created.
- Output: see Terminal output. Errors are one line,
  `filo: <message>`. Exit codes: 0 success (files that could not be read inside a completed scan are reported but do
  not fail it); 1 an error, or any root that failed in a scan of every root; 2 a usage error.
- A progress line (walking, hashing with files and bytes, writing) is drawn on stderr only when stderr is a terminal,
  so timer and cron logs stay clean. Ctrl-C or SIGTERM cancels a scan; nothing is written and the lock is released.
- Scheduling is left to the system: a systemd user timer running `filo scan` is enough (the README has an example,
  not yet tested). No daemon.

### Terminal output
The terminal is treated as a UI: output is built from components, so files, groups, problems and prompts look the
same in every command. The golden files in `internal/cli/testdata` are the reference: the tests compare each
command's output with them. The examples here are excerpts of them.

- **Rich in a terminal, plain when piped.** Colour and symbols (`✓` done, `✗` refused, `!` needs attention, `·` neutral,
  `◆` kept on purpose; green, red, yellow, dim, magenta) only when stdout is a terminal (one whose window size can be
  read, not just any character device) and `NO_COLOR` is unset and `TERM` is not `dumb`. Where the window size cannot be
  read that way (Windows), a terminal stays a terminal for asking and for cutting long lists, but gets no colour, since
  an older console shows colour codes as text. Piped, the words and their order are the same, with ASCII markers
  (`[ok]`, `[refused]`, `[!]`, `-`, `[kept]`) and `.` for the `·` between facts. ANSI codes are written by filo itself:
  no new dependency.
- **"Directory", never "folder"** (decided 2026-11-16): in output, help, docs and comments, matching `DIR` and the
  `directory` CSV column. The one exception is Syncthing's own term, written "a Syncthing folder".
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
  makes a command exit 1 with the reason, so a scheduled `filo report > log` that fails is not taken for success.
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
    columns: a reason's text is shortened only where a path starts (at the start of the text, or after a space or an
    opening bracket), so `/mnt/data/x.pdf` stays whole with the root `/data`. It still rewrites text: a file name
    holding ` /data/` is shortened too. Notes (an event's detail, a symlink's target) show full paths: shortening them would rewrite text, and an absolute symlink target would read as a
    relative one.
  - *What a confirmation covers* (clears, contents always and sets of records, files now past their retention): the
    first 10 and "and N more", and the prompt offers `l` to list everything before answering (`[y/N/l]`).
- **Width.** A path is never cut, not even with `…`: every path printed is about to be acted on or names something to
  find. Short fixed columns (date, size, marker) come first and the path last; an entry whose path does not fit puts it
  on its own indented line, and the terminal wraps it if needed. Section totals sit at the right edge, one column short
  of it (some consoles wrap as soon as the last column is written). Piped, there is no width: one line per entry. The
  width is read with the standard library's `syscall` behind a small Unix-specific function; 80 columns when it cannot
  be read.
- **Prompts.** The question states the effect with the numbers ("Delete all 14"). The default is capitalised and
  always the safe choice (`[y/N]`, `[a/s/l/N]`); Enter takes it. A word or its first letter, any case. An
  unrecognised answer asks again with the choices. End of input means the safe default; Ctrl-C
  stops as before. Select mode shows `[3/14]`, what the item is and its full path, the choices on one line, and one
  result line after each answer; `q` goes to the summary. Filo asks only when both the question and the answer are in
  a terminal (stdin and stdout): with stdout in a file (`filo delete > log`) the question could not be seen, so it is
  not asked (2026-11-16, from the code review). Otherwise nothing is recorded or deleted, and one line says so and how
  to go on. Standard input counts as a terminal when it is a character device, so `filo delete </dev/null` asks, reads
  end of input and deletes nothing (a real terminal check needs a dependency).
- **Components** (package `internal/view`). One `Renderer` holds what every component needs: the escaping writer
  (see "Output cannot act on the terminal"), whether this is a terminal, colour, the width, the home directory and
  the roots. The components are its methods: `Heading` and `Outcome` (the title line), `Section` (title, totals at
  the right edge), `Count` (a marker line), `Files` (a `FileList`: rows of date or retention, size, path and note;
  grouped by root; wrapping; `… N more`), `Table`, `Groups` and `HeadedTable`, `Dup` and `Dups` (a `DupGroup`:
  `contract.pdf · 8 MB each` with its keep and delete lines and symlink notes), `Problems` (line, file, reason and
  what to do; repeats collapsed), `Ask` and `Choose` (prompts), `Item` and `ItemPath` (a select-mode item and its
  result), `Paragraph` and `Terms` (help), `PathLines` and `RawPaths` (`filo find`'s lists for a pipe), and `Next`. Commands build the
  data and hand it to components; they never format output themselves or decide plain or rich, relative or full
  paths. Each component writes whole lines, one write per line.
- **Scope: every command** (roots, scan, report, export, update, delete, help and errors).

Examples, from the golden files (plain form, as when piped; `<root>` is the test's root directory; what the person
typed at a prompt is not echoed):

```
Retention update   r.csv . export #1 . 4 rows . <root>

  [ok] 2 files given a retention
  [refused] 2 rows refused

Refused: fix these rows in the CSV and run filo update again
  [refused] line 2    retention "2027-02-29" is not a retention: use a period such as 7y, 18m, 6w or 30d (counted from file_date), forever, a date YYYY-MM-DD, or a policy name (letters, digits, - and _) (a spreadsheet may have rewritten it; format the column as text)
  [refused] line 3    notes/old draft.txt: retention 7y needs a file_date to count from

[!] 1 file is now past its retention. Check for a typo, such as 7d for 7y:
    retention   until
    7d          2023-01-08   receipts/2023-01-01 coffee.pdf

Nothing has been deleted. Next: filo delete
```

```
Duplicates   d.csv . export #1 . 5 groups . <root>

Will delete   2 copies in 1 group . 16 B
    contract.pdf . 8 B each
      keep     a/contract.pdf
      delete   b/contract.pdf
               -> the symlink <root>/link points here and would break
      delete   c/contract.pdf

Will record
  [kept] kept as separate files: terms.pdf (2 copies)
  [kept] content marked always (never a duplicate): LICENSE (3 copies)

Won't touch
  [refused] group 5   line 12   report.pdf: every copy is delete: no copy would be left; to remove this content entirely, give it a past retention
  - 1 group not decided (blank)
```

```
Ready to delete   3 files . 20 B . <root>

Past their retention                                             3 files . 20 B
    until        size
    2025-06-30    5 B   notes/meeting.txt
    2024-12-31   10 B   tax/2017 return.pdf
    2025-09-01    5 B
      video/2015/summer holiday in the south of France with the whole family, day 3.mp4

Delete all 3, select one by one, list them all, or none? [a/s/l/N]
```

```
[1/3]  past its retention . until 2025-06-30 . 5 B
          <root>/notes/meeting.txt
        d delete . r new retention . s skip . q quit  >         [ok] deleted

[2/3]  past its retention . until 2024-12-31 . 10 B
          <root>/tax/2017 return.pdf
        d delete . r new retention . s skip . q quit  >         new retention: forever, YYYY-MM-DD or a policy (a period needs a file_date, which this file has not got); blank to go back  >         [ok] kept until 2030-01-01

[3/3]  past its retention . until 2025-09-01 . 5 B
          <root>/video/2015/summer holiday in the south of France with the whole family, day 3.mp4
        d delete . r new retention . s skip . q quit  >
```

```
Done   deleted 1 file . freed 5 B . removed 1 empty directory

  - 1 file given a new retention
  - 1 file left for later
```


**Scan and roots**:

- **A scan** (examples from `internal/cli/testdata/scan.txt`, plain form) is a heading (scan number, files seen, how
  long, the root or DIR), then one count line per kind of change happened: new, changed, moved and missing are
  information (`·`; missing is usually a file the person deleted), a possible move needs attention (`!`), and files or
  directories that could not be read are `✗` (`2 could not be read: none marked missing`; it covers new files that could
  not be read and files that vanished during the scan, so it says no more than is true of all of them). Nothing found:
  `✓ no changes`, right under the heading, so a clean scan from a timer is two lines. Then the events (`Events   up to
  10 of each kind`), as the report's event rows (same columns and words, relative to the root): the first 10 of each
  kind (`reconcile.SampleSize`; the first, not the latest), and a kind that had more gets one `… N more <kind>` line (no
  command lists all of a scan's events). Directories that failed to list are already error events. A directory on
  another filesystem is shown (`· 1 directory on another filesystem, not scanned`, event word `other filesystem`) but is
  not a change: every scan finds it. `Next: filo report` only when something changed, failed or needs attention. What
  the directory rules did, and the files two rules tie on, are shown as under Directory rules.
- **`filo scan` of every root**: one block per root, each with its own heading, so it reads like a scan of that root
  alone. A root that fails is a block of its own on stdout, right under its heading, in order between the others:
  `✗ not scanned: the root looks unmounted (/mnt/disk is not mounted)` for an unmounted disk, the error's own text
  otherwise. The run then ends with one line on stderr, `filo: 1 root could not be scanned: /mnt/disk/photos`, so a
  timer that keeps only stderr (`filo scan >/dev/null`) still logs it (an exception to "stderr only for errors that
  stop the command"). The exit code stays 1. With more than one
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
- **`filo roots`** (examples from `internal/cli/testdata/roots.txt`, plain form) and the report's Roots section are one
  renderer: per root its path, files, size and symlinks (left out when there are none); the heading as every heading's,
  the number of roots last (`21 files · 1.4 MB · 2 roots`); its settings, always (`hidden files skipped · ignoring
  build, dist`, without the names always skipped); the mount point; the last scan as the report shows it. No live check
  of whether a root is reachable. No roots: `Roots   none yet` and `Next: filo roots add DIR`.
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
$ filo scan
Scan #2   4 files seen . <took> . <root>

  - 2 new
  - 1 changed
  - 1 moved
  - 13 missing
  [!] 1 possible move, not applied

Events   up to 10 of each kind
    event
    changed         notes/todo.md   content
    moved           docs/contract.txt -> notes/contract-final.txt
    missing         docs/report.txt
    missing         old/f01.txt
    missing         old/f02.txt
    missing         old/f03.txt
    missing         old/f04.txt
    missing         old/f05.txt
    missing         old/f06.txt
    missing         old/f07.txt
    missing         old/f08.txt
    missing         old/f09.txt
    possible move   docs/report.txt -> archive/report.txt   same name, different content
    ... 3 more missing

Next: filo report
```

```
$ filo scan docs
Scan #3   0 files seen . <took> . <root>, under docs/
  [ok] no changes

$ filo scan (one root unmounted)
Scan   <work>/disk
  [refused] not scanned: the root looks unmounted (<work>/disk is not mounted)

Scan #5   4 files seen . <took> . <root>
  [ok] no changes

Done   2 roots . 1 scanned . 1 failed
Next: filo report
```

```
$ filo roots
Roots   5 files . 17 B . 2 roots

  <work>/disk   0 files . 0 B
    hidden files skipped
    on the filesystem mounted at <work>/disk
    never scanned
  <root>   5 files . 17 B
    hidden files skipped . ignoring build
    last scan #4, <time>, whole root: 4 seen, new 2, changed 0, moved 0, missing 0, errors 0
```

```
$ filo roots add home --ignore build
Root added   <root>
  - hidden files skipped . ignoring build

Took in 4 roots, with their history and retentions
  [!] .config   skipped by these settings: its files are neither checked nor marked missing
  - Documents   had other settings (hidden files skipped): these apply now
  - Pictures   had other settings (hidden files skipped . ignoring raw): these apply now
  - my docs   had other settings (hidden files included): these apply now
Next: filo roots set <root> --include-hidden --ignore build, to scan .config again

Scan #4   4 files seen . <took> . <root>
  - 2 new

Next: filo report
```

```
$ filo roots set home --ignore build --ignore Pictures (skips more)
Settings changed   <root>
  - was   hidden files included . ignoring build
  [ok] now   hidden files skipped . ignoring build, Pictures
  [!] 3 registered files and symlinks are now skipped: neither checked nor marked missing
Next: filo scan <root>
```

**Export** (examples from `internal/cli/testdata/export.txt`, plain form):

- **The scan first**: the scan block exactly as `filo scan` prints it, `Next: filo report` included
  when the scan found something, then the export's block. The export runs whatever the scan found (missing and moved
  files are usually genuine); the person sees it all and decides whether to look. The one place a run shows two
  `Next:` lines: hiding the scan's would hide where its findings are listed.
  With `--no-scan`, no scan block, and a `·` line in the export block: `not scanned first: as of the last scan,
  <when>`.
- **`filo export retention`**: a heading, `Export #18 (retention)`: the export number marked as an ID by `#` (the
  one in the CSV's `export` column and `filo update`'s heading; one sequence for every kind of export), then the
  number of files and the place (`~/Documents, under tax/`, as everywhere); a count line of files
  without a retention and with one (`·`); a `!` line when file_dates were read from file names, saying they are
  marked `!` and count only once it is removed; then `Next:` with the forms a retention takes and the `filo update`
  command. The CSV's path appears once, in that `Next:` line, in full and quoted when needed (see "Paths in a `Next:`
  line"), not also on a "written to" line. The full explanation stays in `filo help export`. Nothing to export:
  `Export (retention)   no files · <place>` (no number is used) and `Nothing exported.` A scan's heading names its
  number the same way, `Scan #2`; an update's heading names the export it reads as a fact, `export #1`. Every
  message that names an export or a scan writes its number the same way (`changed since export #3`, `first seen
  2026-11-12 (scan #3)`, `last scan #4`), so one number never shows in two forms; only the CSV's `export` column
  holds the bare number.
- **`filo export duplicates`** has the same shape: `Export #2 (duplicates)`, groups, copies and the
  place; the rules of the `action` column as `·` lines (too long for the `Next:` line); `Next:` with the `filo delete`
  command (see Duplicates, Output). Copies in the heading count every copy in the CSV, also those outside DIR (a group
  comes with all its copies).

```
$ filo export retention tax
Scan #2   2 files seen . under 0.1 s . <root>, under tax/
  [ok] no changes

Export #1 (retention)   2 files . <root>, under tax/

  - 2 without a retention, 0 with one
  [!] 1 file_date read from the file name, marked !: remove the ! to accept it

Next: fill in retention (7y, 18m, forever or a date), then filo update <work>/l.csv

$ filo export retention empty
Scan #3   0 files seen . under 0.1 s . <root>, under empty/
  [ok] no changes

Export (retention)   no files . <root>, under empty/
Nothing exported.
```



```
$ filo export duplicates photos
Export (duplicates)   no groups . <root>, under photos/
Nothing exported.
```

**Help and errors**:

- **Help** (`filo help`, `filo help COMMAND`, `-h`; golden files `testdata/help*.txt`, one per page) goes through the
  Renderer: headings bold in a terminal, plain when piped. Its paragraphs are stored unwrapped and wrapped at word
  boundaries by a `Paragraph` component, to the terminal's width, or 80 columns when piped (so `filo help > file` reads
  well). Commands and flags keep two columns, a description that does not fit wrapping under its own column. The words
  stay as they are, regrouped under `Commands` and `Flags` with a usage line per command (the long report paragraph
  split into sentences). The overview ends with `Next: filo help COMMAND for a command's flags and rules`.
- **Errors and warnings** stay on stderr, plain (no colour, symbols or `~`: they end up in logs and get copied, and a
  full path is unambiguous there), one line each, `filo: <message>` and `filo: warning: <message>`. A usage error is
  one line too, pointing at the command's own help, `filo: roots add takes exactly one DIR (see filo help roots)`;
  exit code 2. An error is always one line: a
  newline in it is escaped like any other, since a file name can hold one and must not forge a second `filo:` line.
  Filo's own errors of several parts (`errors.Join`, as the snapshot warnings) are split by the code that knows the
  parts, a line each. A test runs the error cases and checks each is one line.
- **Two ways out.** stdout only through the Renderer (which escapes every text it is given), and stderr only through
  `errorLine`, which escapes the text with `view.Escape` and writes `filo: <text>` in one write; `rawStderr` is left
  only for the progress line, whose text `view.Fit` escapes. A guard test, like the store's
  `TestWritesGoThroughTheHelpers`, fails if anything else in `internal/cli` writes with `fmt.Fprint*`, `io.WriteString`,
  to `os.Stdout`/`os.Stderr`, or with `Write` or `WriteString` on the streams; the writers allowed are named by receiver
  and name (`env.errorLine`, `progress.draw`), so another function with the same name is not.

```
$ filo help

filo   keeps a register of your files, where they live, and how long to keep them

Commands
  roots                    list roots and their settings
  roots add DIR [flags]    register a directory as a root, then scan it
  roots set DIR [flags]    replace a root's settings
  scan [DIR]               scan every root, or DIR inside a root
  report [--top N]         totals, files needing a retention, largest files,
                           duplicates, events
  …

Every command accepts --db PATH (default: $FILO_DB, then the user data
directory).
Next: filo help COMMAND for a command's flags and rules
```

```
$ filo help scan

filo scan [DIR] [--workers N] [--allow-empty]

  Scans DIR, which must be inside a root (a partial scan), or every root if DIR
  is omitted, with each root's settings (see filo help roots).

Flags
  --workers N              files hashed in parallel, 1-64 (default 4; use 1 on a
                           spinning disk)
  --allow-empty            record an empty scan of a root on a mounted
                           filesystem
```

```
$ filo roots add
filo: roots add takes exactly one DIR (see filo help roots)
```

## 3. Data model (schema version 13)

The register as `internal/store/schema.go` creates it (`schemaSQL`), with the meaning of each column. Keep the
interval history and the partial unique index.

```sql
CREATE TABLE meta  (key TEXT PRIMARY KEY, value TEXT NOT NULL);
    -- schema_version, created_utc, hash_algorithm (sha256: a database with another is refused), last_write_utc

CREATE TABLE roots (
    id INTEGER PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,          -- absolute, symlinks resolved
    mount_path TEXT,                    -- the mount point the root lives under; NULL = the filesystem of /
    added_utc TEXT NOT NULL,
    include_hidden INTEGER NOT NULL DEFAULT 0,          -- the root's scan settings
    ignores TEXT NOT NULL DEFAULT '[]');                -- JSON array of names skipped at any depth

CREATE TABLE scans (
    id INTEGER PRIMARY KEY, started_utc TEXT NOT NULL, finished_utc TEXT,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    kind TEXT NOT NULL DEFAULT 'scan',  -- 'scan'; 'delete' for a delete run's record in a root, 'rename' for an
                                        -- update's renames in a root (schema 13)
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
    -- the current lifespan; the history is in reviews (and policy_changes)
    file_date TEXT,                     -- YYYY-MM-DD a retention period counts from; NULL = none
    retention TEXT,                     -- as given: a period (7y), 'forever' or YYYY-MM-DD; NULL = none or a policy
    keep_until TEXT,                    -- worked out: YYYY-MM-DD or 'forever'; NULL = undated, needs a decision
    reviewed_utc TEXT,                  -- when the retention was last set; NULL = never
    reviewed_after_scan INTEGER REFERENCES scans(id), -- the latest scan the setting saw (an update: its export's)
    reviewed_hash TEXT,                 -- the content the retention was set on
    deleted_utc TEXT,                   -- when filo deleted it; its path is closed. NULL = not deleted by filo
    policy_id INTEGER,                  -- its policy (policies.id; retention is then NULL); NULL = none.
                                        -- Not a declared foreign key: the store keeps it pointing at a policy
    rule_id INTEGER,                    -- the rule that set its policy (rules.id); NULL = none, the rule removed,
                                        -- or its retention since set by hand
    rule_dated_utc TEXT);               -- when a rule set its policy; once set, no rule does it again

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
                                        -- deleted | same_as_deleted | copy_tags
    file_id INTEGER, path TEXT, other_path TEXT, detail TEXT);

-- exports and the history of retentions
CREATE TABLE exports (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    path TEXT,                          -- the CSV written
    after_scan INTEGER REFERENCES scans(id),           -- the latest finished scan at export
    kind TEXT NOT NULL DEFAULT 'retention');           -- 'retention' or 'duplicates'
CREATE TABLE export_rows (
    export_id INTEGER NOT NULL REFERENCES exports(id), file_id INTEGER NOT NULL REFERENCES files(id),
    hash TEXT,                          -- the content shown; the update refuses the row if it has changed
    keep_until TEXT,                    -- the date exported
    file_date TEXT, retention TEXT,     -- as exported (file_date as read from the name, marked !; retention a
                                        -- policy's name for a file on one); cells left as these are untouched
    dup_group INTEGER,                  -- duplicates exports: the group number in the CSV
    path_id INTEGER REFERENCES paths(id),              -- duplicates exports: the path shown
    tags TEXT,                          -- tags exports: the cell as exported
    name TEXT,                          -- combined exports: the name cell as exported (schema 13)
    PRIMARY KEY (export_id, file_id));
CREATE TABLE review_batches (
    id INTEGER PRIMARY KEY, created_utc TEXT NOT NULL,
    command TEXT NOT NULL,              -- 'update'; 'delete' for a retention typed at filo delete's prompt;
                                        -- 'policies' (a move or rename); 'rules add', 'rules move', 'rules copy'
                                        -- 'scan' (policies set by rules) and 'set' (filo set)
    detail TEXT);                       -- the CSV read, or what the command did
CREATE TABLE reviews (                  -- append-only: every retention ever set or cleared
    id INTEGER PRIMARY KEY, batch_id INTEGER NOT NULL REFERENCES review_batches(id),
    file_id INTEGER NOT NULL REFERENCES files(id), export_id INTEGER REFERENCES exports(id),
    keep_until TEXT,                    -- NULL = cleared
    hash TEXT,
    file_date TEXT, retention TEXT);
CREATE INDEX ix_reviews_file ON reviews(file_id);

-- duplicates (see Duplicates)
CREATE TABLE always_hashes (            -- contents that belong everywhere: never a duplicate group
    hash TEXT PRIMARY KEY, added_utc TEXT NOT NULL);
CREATE INDEX ix_files_inode ON files(inode);            -- hard links
CREATE TABLE duplicate_records (        -- copies kept on purpose together; a file is one only while hash = files.hash
    file_id INTEGER PRIMARY KEY REFERENCES files(id), hash TEXT NOT NULL,
    set_id INTEGER NOT NULL, added_utc TEXT NOT NULL);
CREATE INDEX ix_duplicate_records_set ON duplicate_records(set_id);
CREATE TABLE excluded_dirs (            -- files under them (the whole subtree) never count as duplicates
    root_id INTEGER NOT NULL REFERENCES roots(id),
    path TEXT NOT NULL,                 -- relative to the root, '/'-separated; '' = the whole root; moved with
                                        -- the root when a new root takes it in
    added_utc TEXT NOT NULL, PRIMARY KEY (root_id, path));

-- named retention policies (see Lifespan, Named retention policies)
CREATE TABLE policies (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,          -- lower case: letters, digits, - and _, starting with a letter
    value TEXT,                         -- a period (7y), a date (YYYY-MM-DD) or 'forever'; NULL = none yet
    added_utc TEXT NOT NULL, changed_utc TEXT NOT NULL);
CREATE INDEX ix_files_policy ON files(policy_id);
CREATE TABLE policy_changes (           -- the history of policies
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,                 -- 'add', 'set', 'rename', 'move' or 'remove'
    policy TEXT NOT NULL,               -- its name at the time
    old_value TEXT, new_value TEXT,     -- for 'rename' and 'move', new_value is the policy moved to
    files INTEGER NOT NULL DEFAULT 0,   -- how many files the change touched
    changed_utc TEXT NOT NULL);

-- directory rules (see Lifespan, Directory rules)
CREATE TABLE rules (
    id INTEGER PRIMARY KEY,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    dir TEXT NOT NULL,                  -- relative to the root, '/'-separated; '' = the root itself
    pattern TEXT NOT NULL,              -- as typed; '*' for none
    pattern_key TEXT NOT NULL,          -- the pattern in lower case
    letters INTEGER NOT NULL,           -- the pattern's length, every character but * (for reading by hand;
                                        -- matching works it out again)
    policy_id INTEGER NOT NULL REFERENCES policies(id),
    added_utc TEXT NOT NULL,
    UNIQUE (root_id, dir, pattern_key));
CREATE INDEX ix_files_rule ON files(rule_id);

-- tags (see Tags)
CREATE TABLE tags (                     -- a file has a tag while it has the row; rows of a file filo deleted stay
    file_id INTEGER NOT NULL REFERENCES files(id),
    key TEXT NOT NULL,                  -- lower case; '' for a bare tag
    value TEXT NOT NULL,                -- as spelled: the tag's spelling, the same on every file
    folded TEXT NOT NULL,               -- the value case-folded, what it is matched on
    added_utc TEXT NOT NULL,
    PRIMARY KEY (file_id, key, folded));
CREATE INDEX ix_tags_tag ON tags(key, folded);
CREATE TABLE tag_changes (              -- append-only: every tag added or removed, for the journal (roadmap 9c)
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,                 -- 'add' or 'remove'
    file_id INTEGER NOT NULL REFERENCES files(id),
    key TEXT NOT NULL, value TEXT NOT NULL,
    command TEXT NOT NULL,              -- 'tag', 'untag', 'rename', 'update', or 'scan' (taken from copies)
    changed_utc TEXT NOT NULL);
CREATE INDEX ix_tag_changes_file ON tag_changes(file_id);

-- tag index pages (see Tag index pages)
CREATE TABLE tag_indexes (
    id INTEGER PRIMARY KEY,
    root_id INTEGER NOT NULL REFERENCES roots(id),
    dir TEXT NOT NULL,                  -- relative to the root; '' = the root itself
    name TEXT NOT NULL,                 -- NAME.html and .NAME/
    added_utc TEXT NOT NULL,
    data_hash TEXT,                     -- the data of the last full write; NULL = never written in full
    written_utc TEXT,                   -- when its pages were last brought up to date
    problem TEXT,                       -- why the last writing failed; NULL = it did not
    UNIQUE (root_id, dir, name));
CREATE TABLE tag_index_files (          -- every file and directory Filo wrote in an index
    index_id INTEGER NOT NULL REFERENCES tag_indexes(id),
    path TEXT NOT NULL,                 -- relative to the index's directory: 'index.html', '.index/_/index.html'
    hash TEXT,                          -- the file's SHA-256 as written; NULL = a directory
    PRIMARY KEY (index_id, path));
```

"Present" means a file has a path interval with `valid_to_scan IS NULL`; there is no status column. "New" means
`first_seen_scan` equals the scan. Disposal does not ask when a scan last covered a file: it refuses files flagged
unreadable and re-checks the file itself at the moment of deleting (decided 2026-11-13; an earlier note here also
refused files no recent scan covers, which the re-hash makes unnecessary). Each schema version and its upgrade is in
`internal/store/schema.go` (`migrations`); `history.md` tells what each changed.

Location history gives snapshots without storing them: the tree at any scan is the set of intervals that cover it.

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
   still left. Files deleted by filo are never paired; a new file with a deleted file's content gets a
   `same_as_deleted` event. Report same-name leftovers as possible moves.
8. Apply in one transaction: close paths of missing and moved files **first**, so a path freed by one rename can be
   reused by another in the same scan; then updates; then open new paths and insert new files; then events; then, in
   the same transaction, the directory rules give a policy to the files in the scope with no retention (see Directory
   rules).
9. Print a summary: counts plus samples of moved, missing, changed, possible moves and errors, what the rules did, and
   the directories on another filesystem (see Terminal output, Scan and roots).

`filo report` shows, under a heading with the register's totals: each root (as `filo roots` shows it); Retention
(the files needing a retention by status and the directories, to depth 2, holding most of them; the files with a
policy set by rules and the files two rules tie on; how many are kept until a later date, kept forever and past their
retention; the "deleted by filo" line; the policies line); the largest files; the duplicate groups with reclaimable
space and the "not counted as duplicates" line; broken symlinks; unreadable files (path and the scan they became
unreadable in); the most recent events; and Register snapshots. `--top N` sets the length of each list (default 10).
Paths are relative to the root, as everywhere (see Terminal output, Paths). A **duplicate group** is defined under
Duplicates ("A group"): copies, not paths, so reclaimable space is size × (copies − 1) per group.

## 5. Accepted risks

Limits of Filo as built, accepted on purpose (security review of 2026-11-13). Anyone who can edit the register already
runs as the user and could delete files directly, so an altered register matters through indirect routes (a restored
snapshot, a database in a synced directory, a `--db` from elsewhere). Accepted: a register whose *root* path was altered
to another directory is not detected (the paths below it are checked, and the confirmation list shows absolute paths);
`filo delete -y` deletes without a person reading the list, which is the point of `-y`; on platforms other than Linux,
and on Linux where no private directory can be had (see Disposal), a file swapped in the microseconds between the last
check and the unlink is deleted (anyone able to do that can delete
it directly); SQLite
parses a database before Filo checks it is one of its own (kept small by staying on a current driver); ownership of the
snapshot directory, and of the database's directory, is not checked on non-Unix platforms; and the directories above
the database's directory are not checked (one that others can write to lets them move the database's directory aside
and put another in its place). From the code review of the snapshots: Ctrl-C cannot interrupt
a snapshot being written (milliseconds for a register of a few MB), and every run that writes copies the whole register
(fine at a few MB; a scan that found nothing takes none since 2026-11-21). From the code review of 2026-11-13: a snapshot is renamed
into place after a check that the name is free, and a program not taking Filo's lock could create that name in between
and be overwritten (the directory is private to the user; an atomic no-replace rename needs `golang.org/x/sys`); and the
directories on a deleted file's path are checked by name, not held open, so a directory swapped in the microseconds
after the last look is followed. `filo find -0` writes file names unescaped (see Finding files): it is refused when
stdout is a terminal, but a pipe that ends in one (`filo find -0 | cat`) shows the raw names, as `find -print0 | cat`
does; a list of names for `xargs -0` has to be the names.
