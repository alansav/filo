# Filo

Filo keeps a register of your files: what they are, where they live, how that changes over time, and how long each one
should be kept. It is the start of a review-and-dispose workflow meant to stop files piling up forever ("keep it in
case someone asks").

**Filo manages files; it is not, and never will be, a backup tool.** `filo delete` deletes files permanently, so keep
backups of your files, and of Filo's register and its snapshots (see [Register snapshots](docs/design.md#register-snapshots)).

**Status: scan, lifespan, delete, duplicates and retention policies done.** You give every file a retention (how
long to keep it, often through a named policy such as `bills = 7y`), and
`filo delete` lists the files past theirs, asks, and deletes the ones you choose. For identical copies you decide
which to keep in a spreadsheet, and `filo delete` carries that out the same careful way. Apart from that, Filo is
**read-only on your files**: no other command modifies, moves, renames or deletes anything outside its own database.
See [Roadmap](#roadmap) for what comes next.

## What it does today

- **Registers files under one or more roots** (for example `~` and `/mnt/photos`) in a single SQLite database per
  device.
- **Tracks every file across scans**, comparing each scan with the register:
  - **new** files, and files whose content **changed**;
  - files that **moved** or were renamed (matched by SHA-256 content hash), keeping their identity and full location
    history;
  - files that are **missing**.

  Ambiguous cases are reported, never guessed. A file edited and moved at once shows as a **possible move**, and
  several identical copies are only paired when their names make it unambiguous.
- **Reports** totals per root, the files that still need a retention and where they are, the largest files,
  **duplicate groups with the space you could reclaim**, broken symlinks, unreadable files, and recent changes
  (including moved and missing files).
- **Tracks how long to keep each file.** Each file has a retention (`7y` from the file's date, `forever`, or a fixed
  date), which you set in a spreadsheet: export a CSV, fill in the retentions, update the register from it. When a
  date passes, the file is listed as a deletion candidate. A retention can be a named policy (`bills = 7y`), so
  changing the rule changes every file using it. Nothing is ever deleted because a date passed: `filo
  delete` shows you the list and asks.
- **Deletes what you confirm**, after reading each file again just before deleting it: a file that changed since its
  retention was set, moved, or can't be read is skipped, never deleted.
- **Clears out duplicates you decide on**: export the groups of identical copies, write `keep` or `delete` against
  each, and `filo delete` deletes the extra copies, never the last one. Copies kept on purpose (records, a `LICENSE`
  in every project, a backup folder) can be marked so they stop being reported. See [Duplicates](#duplicates).
- **Rescans quickly.** A file is only re-read when its size, modification time, change time (ctime) or inode changes.
  The change time is what catches a file whose permissions changed, or one rewritten with its old modification time
  put back. Windows has no change time, so there it isn't compared.

It is built to fail safe:

- **Partial scans are safe.** Scanning one folder never marks files elsewhere as missing.
- **Unreadable directories are harmless.** A directory that can't be read never makes its files "missing".
- **Unreadable files are flagged.** A file Filo can see but not read (for example permission denied) is reported as an
  error and flagged as unreadable since that scan, including a file you could read before. It keeps its record and its
  last known hash, and the flag clears the next time it can be read. `filo report` lists flagged files.
- **Unplugged disks are noticed.** A root on a disk that isn't mounted is refused, rather than looking as if every
  file was deleted. So is a mounted disk that turns out to be empty.
- **Files that come back are recognised.** If they return after going missing (a disk that was briefly unreadable,
  a file moved out and back), they are relinked to their old record.
- **Interrupting is safe.** Each scan is written in one transaction, so Ctrl-C leaves the register as it was.
- **A changed file needs a new decision.** If a file's content changed since its retention was set, it isn't a
  deletion candidate when the date comes: it needs a new decision instead.

## Install

Filo is pure Go (no C toolchain needed). It needs Go 1.27.3 or later and runs on Linux, macOS and the BSDs. Windows is
not supported yet: file locking and inode lookup aren't implemented there.

```sh
git clone https://github.com/alansav/filo.git
cd filo
go build -o bin/filo ./cmd/filo
```

Copy `bin/filo` somewhere on your `PATH`, or run it from the repository.

## Quick start

```sh
filo roots add ~          # register a directory as a root and scan it; the first scan hashes everything, so it is slow
filo scan                 # scan every root again: only what changed is rehashed
filo report               # totals, files needing a retention, largest files, duplicates, unreadable files, events
```

A scan prints a summary like this (paths are relative to the root named at the top; in a terminal the markers are
symbols and colours):

```
Scan   scan 2 . 15 files seen . 0.4 s . ~/FiloTest

  - 3 new
  - 2 changed
  - 2 moved
  - 2 missing
  [!] 1 possible move, not applied
  [refused] 2 could not be read: none marked missing

Events   up to 10 of each kind
    event
    changed         link-to-report   symlink: docs/report-2024.txt (now broken)
    changed         notes/todo.md   content
    moved           docs/contract.txt -> notes/contract-final.txt
    moved           photos/img2.bin -> photos/img2-renamed.bin
    missing         docs/receipt-tyre.txt
    missing         docs/report-2024.txt
    possible move   docs/report-2024.txt -> archive/report-2024.txt   same name, different content
    error           docs/private.txt   hash: open /home/you/FiloTest/docs/private.txt: permission denied
    error           locked   open /home/you/FiloTest/locked: permission denied

Next: filo report
```

A scan with nothing to report is two lines (`Scan …` and `[ok] no changes`); a directory on another filesystem
is listed but is not a change. `filo scan` with no directory scans
every root, one block each; a root that cannot be scanned (an unmounted disk) is reported in its place and the
others are still scanned.

The two errors are a file and a directory made unreadable. The files inside `locked` aren't reported missing,
because Filo couldn't look.

And `filo report` gives the overview, a section at a time (paths are relative to the root named at the top; an
empty section is one line ending in `none`, or `none yet` with what to do). Among other things:

```
Register   13 files · 313 KiB · ~/FiloTest

Duplicates                                 1 group · 3 copies · 195 KiB reclaimable
    img1-copy.bin · 97.7 KiB each · 3 copies · 195 KiB reclaimable
      photos/img1-copy.bin
      photos/img1-copy2.bin
      photos/img1.bin

Unreadable files                                                                1
    since
    scan 2   docs/private.txt

Recent events                                                                none
```

## Setting retentions

```sh
filo export retention ~/Documents -o docs.csv    # scan ~/Documents, then every file under it, with its retention
# open docs.csv in a spreadsheet, fill in retention, save it as CSV
filo update docs.csv                            # apply what you changed
filo delete                                     # list the files past their retention, ask, delete
```

The CSV has one row per file: `id, export, directory, name, size, modified, file_date, retention, keep_until`. Sort
and filter it however you like.

- **`retention`** is how long to keep the file:
  - a period counted from `file_date`: `7y`, `18m`, `6w` or `30d`;
  - `forever`: never a deletion candidate, and `filo report` counts how many files are kept forever;
  - a fixed date, `YYYY-MM-DD`;
  - or a policy name, such as `bills` (see [Retention policies](#retention-policies)).
- **`file_date`** is the date the file is about, the date a period counts from (a tax year's end, an invoice's
  date). If it's blank, the export fills it in from the file name when the name holds one clear date:
  `2023-03-14 invoice.pdf`, `IMG_20230714_1032.jpg`, or `2023-03 rent.pdf` (the month's last day, so nothing is kept
  for less time than meant). A lone year, a name with two dates, or `14-03-2023` (is that day or month first?) are
  left blank. A filled-in date you leave as it is only counts when you give that file a period; otherwise it is
  dropped. Check it before counting from it.
- **`keep_until`** is worked out by Filo, so you can sort by it. Leave it alone: a cell holding a different date is
  refused.

To set a whole folder at once, filter on `directory` and fill the retention down. Filtering on a blank `retention`
shows the files still to decide.

- Only `id`, `export`, `file_date` and `retention` are read back, so you can reorder columns or add your own. Rows
  can be sorted, filtered or deleted, but not added.
- Dates are `YYYY-MM-DD` and must be real (`2027-02-29` is refused). Anything else is refused rather than guessed.
  If your spreadsheet turns dates into `01/06/2027`, format those columns as text.
- Dates in the past are fine: a 2015 invoice with `7y` ended in 2022, so it's simply past its retention.
  `filo update` lists the files it left past their retention, so a typo like `7d` for `7y` shows straight away.
- A period with no `file_date` is refused for that row, since there's nothing to count from.
- Cells left as exported change nothing. Editing the same CSV again and running `filo update` again replaces what it
  set.
- A blank `retention` where one was set clears it, so the file needs a decision again. Filo lists those files and
  asks first; without a terminal it leaves them alone.
- A row is refused, with one line saying why, if the file changed since the export or its retention was changed
  after it (from a later export, or by `filo policies move`). The other rows are still applied. `--dry-run` checks everything and writes nothing.
- On a large system, export one directory at a time, so the spreadsheet stays fast.

Nothing is deleted because a date passed: `filo delete` lists the candidates and asks (see [Deleting](#deleting)).
A file kept until a date is past its retention **on** that date: kept until 2026-12-31, it is listed by `filo
delete` from 2026-12-31 on.

## Retention policies

A policy is a retention written once and named, so you type the name in the CSV instead of a period, and changing
the rule changes every file using it. If the law moves financial records from 7 to 10 years, that's one command:

```sh
filo policies add bills 7y        # a policy: bills = 7y
# in a retention CSV, type bills in the retention column of each bill, then filo update the CSV
filo policies set bills 10y       # every file on bills is now kept 10 years from its own file_date
filo policies                     # list them, with what needs attention
```

- **A policy's value** is a period (counted from each file's own `file_date`), a date (`project-x = 2030-12-31`:
  every file of it ends that day), or `forever`. Names are letters, digits, `-` and `_`, starting with a letter, in
  any case (`Bills` is `bills`); `forever` can't be a name.
- **Typing a new name in the CSV creates the policy**, with no value yet. `filo update` lists the new names and asks
  once (`Create 2 policies? [y/N]`), so a typo such as `bils` doesn't quietly become one; declined, those rows are
  left alone. Its files need a retention until the policy gets a value: `filo policies set bils 7y`.
- **Changing a policy** works out every file's `keep_until` again. Lengthening just does it. Shortening asks first
  if it would put files past their retention, and lists them. Setting a period asks first if some files have no
  `file_date` to count from; those then **need a file_date**, which `filo report`, `filo policies` and `filo
  policies show` point out. They are never deleted until they have one.
- **A typo or a duplicate**: `filo policies move bils bills` moves every file from one policy to another (into a new
  name it's a rename), then `filo policies remove bils`. A policy in use can't be removed. A CSV exported before the
  move never puts a file back on the old name: rows left as they were do nothing, and a row edited since is refused
  ("export again").
- **A policy whose date has passed** is flagged by `filo policies`, never removed automatically: set a new date (next
  year's, say) or remove it.
- A file has a policy or a retention of its own, never both; typing a period or a date over a policy name takes it
  off the policy. In a duplicates CSV, a copy on a policy with no value, or one that needs a `file_date`, can't be
  deleted until it has a date.

```
$ filo policies

Policies   3 · 2 need attention

  bills       7y           from each file's file_date   212 files
  bils        no value                                    3 files   ! needs a retention
  last-year   2025-12-31                                  0 files   ! passed: set a new date, or remove it
```

## Deleting

`filo delete [DIR]` lists the files past their retention under `DIR` (or every root), then asks:

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

- **The answers:** `a` deletes them all, `s` goes one by one, `l` lists every file (in a terminal the list shows 10
  per section) and asks again, and `n` deletes nothing. If the list was cut short, `a` first shows every file and
  asks once more, so you always see what will be deleted. The capital letter is the default: Enter, or an answer
  Filo doesn't know, never deletes anything. Without a terminal it lists everything and deletes nothing; `-y` deletes
  them all without asking.
- **One by one**, each file is shown with its full path, and you choose `d` delete, `r` give it a new retention, `s`
  skip, or `q` quit:

  ```
  [2/5]  past its retention · until 2025-06-30 · 3 KB
            ~/Documents/notes/meeting.txt
          d delete · r new retention · s skip · q quit  › d
          ✓ deleted
  ```
- **Just before deleting**, each file is read again. It is deleted only if it is still at its path as the same file
  and its content is still the content its retention was set on. Otherwise it is skipped and listed with the reason,
  and nothing about it changes.
- **Deletion is permanent:** there is no recycle bin, so keep backups. Folders the run emptied are removed, up to the
  root. A file you restore from a backup comes back as a new file, needing a retention.

In a terminal the output has colour and symbols (set `NO_COLOR`, or `TERM=dumb`, to turn colour off) and fits the
window: a path is never cut, but a long one moves to its own line. Piped into a file or another program, it is plain
text with the same words, one line per file.

## Duplicates

The report lists **duplicate groups**: the same content held by two or more copies (hard links to one file are one
copy). To deal with them, export the groups, decide in a spreadsheet, then carry the CSV out:

```sh
filo export duplicates ~/Documents -o dupes.csv   # every group with a copy under ~/Documents, with all its copies
# open dupes.csv, fill in the action column, save it as CSV
filo delete dupes.csv                             # check, list, ask, delete
```

**The rule: Filo decides which copy to keep, never whether the content goes.** Deciding duplicates never removes a
content or shortens how long it is kept; only a retention does.

Identical files exist for four reasons, and each has its own kind of keep:

| why the copies exist | example | what you do |
|---|---|---|
| a copy made by accident | a download saved twice | `keep` one copy, `delete` the rest |
| copies that are each a record | insurance terms received in 2020 and again in 2021, each with its own retention | `keep` on two or more: they are **kept as separate files** |
| a content that belongs everywhere | `LICENSE`, `__init__.py`, a template in each project | `always` on every row: the content is never a duplicate again |
| a deliberate copy of a folder | a backup, an old laptop's files | `filo duplicates exclude DIR`: nothing under it counts as a duplicate |

The `action` cell takes `delete`, `keep` or `always` (or `d`, `k`, `a`, any case). Each group is decided on its own:

| rows of a group | result |
|---|---|
| all blank | not decided: left alone, so you can work through a CSV and run it several times |
| some blank, some filled | refused: every copy needs a value |
| one `keep`, the rest `delete` | the `delete` copies are deleted |
| two or more `keep`, the rest `delete` or none | the `delete` copies are deleted; the copies kept are kept as separate files |
| every row `always` | the content is marked always; nothing is deleted |
| `always` mixed with anything else | refused: `always` is about the content, so it goes on every row |
| every row `delete` | refused: no copy would be left. To remove a content entirely, give it a past retention |

**Retentions across copies:** a copy can be deleted only if a copy kept lasts at least as long (the same or a later
`keep_until`, or `forever`). Terms from 2020 and 2021, each kept 7 years: keeping 2021 and deleting 2020 is fine;
the other way round is refused ("keep it instead, or keep both"). Filo never changes a retention to make a group
pass.

`filo delete dupes.csv` checks every decided group against the disk first, then lists what it will do:

```
Duplicates   dupes.csv · export 7 · 12 groups · ~/Documents

Will delete                                              9 copies in 6 groups · 1.2 GB
    contract.pdf · 8 MB each
      keep     a/contract.pdf          forever
      delete   b/contract.pdf
      delete   c/contract.pdf
               ↳ the symlink ~/Desktop/contract points here and would break
    … 5 more groups

Will record
  ◆ kept as separate files: terms.pdf (2 copies)
  ◆ content marked always (never a duplicate): LICENSE (3 copies)

Won't touch
  ✗ group 3   line 12   IMG_2041.jpg: every copy is delete: no copy would be left
  ! group 5   line 19   b/photo.jpg: changed since the export: export again
  · 3 groups not decided (blank)

Delete 9 copies and record 2 decisions? [y/s/l/N]
```

- **Strict about changes:** a group any of whose files changed or moved since the export is skipped whole, so a copy
  is deleted only if it is still exactly the content you decided on. A copy that is gone since (deleted by another
  run, or by you) is ignored and the rest of its group goes ahead. A file Filo can't check (a disk not mounted, a
  folder it can't read) is never taken as gone: its group is skipped.
- **Never the last copy:** just before deleting, Filo reads a copy kept again and checks it is still there with the
  same content, and is not the same file as the copy being deleted (a hard link, or a folder mounted twice). A copy
  with other hard links can be kept but not deleted, since deleting it frees nothing.
- **Run it again:** the CSV is your saved work, and nothing waits in the register between deciding and deleting.
  Edit it further and run it again: groups already done are reported as done, and only what is still as exported is
  acted on. A CSV of several exports, a sorted CSV, or one with groups removed are all fine; a row moved to another
  group, added or duplicated refuses the groups it touches ("export again").
- **The answers:** `y` does it all, `s` goes group by group (`d` do it, `s` skip, `q` quit), `l` lists every group,
  `n` does nothing. Without a terminal it only lists; `-y` does it all without asking.

`filo duplicates` lists what is not counted as duplicates, and `filo duplicates include PATH` undoes one: a
directory's exclusion, a content marked always, or a whole set of identical files kept as separate files.

```
$ filo duplicates exclude ~/Backup

Excluded   ~/Backup
  · 3,210 files under it no longer count as duplicates (still scanned, retentions unchanged)
  · 41 duplicate groups fewer: 256 left

Next: filo duplicates include /home/you/Backup to undo it
```

## Commands

| Command | What it does |
|---|---|
| `filo roots` | List roots: path, settings, mount point, file count, size, last scan. |
| `filo roots add DIR` | Register a directory as a root, with its settings, and scan it. |
| `filo roots set DIR` | Replace a root's settings. |
| `filo scan [DIR]` | Scan `DIR` (a directory inside a root: a partial scan), or every root if omitted. |
| `filo report [--top N]` | Show the overview; `--top` sets how many entries each list shows (default 10). |
| `filo export retention [DIR] -o FILE [--no-scan]` | Scan `DIR` (or every root), then write every file under it with its retention, as CSV. |
| `filo export duplicates [DIR] -o FILE [--no-scan]` | Scan every root, then write the duplicate groups with a copy under `DIR` as CSV, to decide which copies to keep. See [Duplicates](#duplicates). |
| `filo update FILE [--dry-run]` | Apply the retentions edited in an exported retention CSV. |
| `filo delete [DIR] [-y]` | List the files past their retention, ask, and delete the ones you choose. See [Deleting](#deleting). |
| `filo delete FILE [-y]` | Carry out a duplicates CSV: check it against the disk, list, ask, and delete the copies marked `delete`. |
| `filo duplicates` | List what is not counted as duplicates: excluded directories, contents marked always, identical files kept as separate files. |
| `filo duplicates exclude DIR` | Never count the files under `DIR` as duplicates (a backup folder). |
| `filo duplicates include PATH` | Undo one: a directory's exclusion, a file's content marked always, or its set of identical files kept as separate files. |
| `filo policies` | List the retention policies, with what needs attention. See [Retention policies](#retention-policies). |
| `filo policies add NAME [VALUE]` | Create a policy, with a period, a date or `forever`, or no value yet. |
| `filo policies set NAME VALUE [-y]` | Change a policy, and so every file using it; asks first if files would be put past their retention or left without a `file_date`. |
| `filo policies move OLD NEW [-y]` | Move every file of a policy to another (a rename, if NEW is new). |
| `filo policies remove NAME` | Delete a policy no file uses. |
| `filo policies show NAME` | A policy's value and its files, those needing a `file_date` first. |
| `filo help [COMMAND]` | Show usage. `-h` works on every command. |

Options for `filo roots add` and `filo roots set`, which every scan of the root then uses:

| Option | Meaning |
|---|---|
| `--include-hidden` | Also scan hidden files and directories (skipped by default). |
| `--skip-hidden` | Skip them again (`roots set` only). |
| `--ignore NAME` | Skip entries with this name, at any depth. Repeatable. `roots set` replaces the whole list. |
| `--no-scan` | Register the root without scanning it now (`roots add` only). |

Options for `filo scan`:

| Option | Meaning |
|---|---|
| `--workers N` | Files hashed in parallel, 1–64 (default 4; use 1 on a spinning disk). |
| `--allow-empty` | Record a scan that finds no files on a mounted disk (normally refused as "probably the wrong disk"). |

Every command accepts `--db PATH`. Options can come before or after `DIR`. A progress line is shown during long
scans when the output is a terminal. The only questions Filo asks are in a terminal, and saying no (or running
without a terminal, as a timer does) is always the safe choice.

Exit codes: `0` success (files that couldn't be read are reported but don't fail the scan; saying no at a question
is not a failure), `1` an error, a root that failed during `filo scan` of every root, a CSV row that `filo update`
refused, a file `filo delete` couldn't read or delete, or a group of a duplicates CSV it refused, `2` a usage
error.

## Roots

- **Explicit roots.** Nothing becomes a root by accident: `filo scan` of a path outside every root is refused, with a
  hint to add it.
- **A root has its own settings**: whether hidden files are scanned, and extra names to ignore. They are set when the
  root is added and changed with `filo roots set`, so a scheduled scan and one you run by hand always cover the same
  files.
- **Aliases are detected.** The same directory can't be added twice, whether by another path, through a symlink, or
  through a bind mount (for example `~` also mounted at `/mnt/home`). Filo compares device and inode numbers.
- **Nesting only across disks.** A root may sit inside another root only if it is on a different filesystem, such as
  a disk mounted at `~/mnt/disk`. A scan never crosses into another filesystem, so no file belongs to two roots.
- **Scans map to their root.** `filo scan ~/Pictures` is a partial scan of the root that contains it, with that root's
  settings.
- **A parent takes in its children.** Adding `~` when `~/Documents` is already a root (on the same filesystem) merges
  `~/Documents` into `~`, keeping its files' history and retentions. The settings of `~` then apply to all of it, and
  `roots add` says so if they differ.

## What a scan skips

- Hidden entries (names starting with `.`), unless the root includes them (`--include-hidden`).
- `node_modules`, `__pycache__`, `.filo`, and `bin`/`obj` next to a `.csproj` or `.fsproj` file.
- Anything the root ignores (`--ignore`).
- Other filesystems mounted inside the root (each can be its own root).
- Filo's own database files.
- FIFOs, sockets and devices.

Symlinks are recorded with their target and whether they're broken, but never followed.

## Where the data lives

One database per device holds every root:

| Platform | Default location |
|---|---|
| Linux, BSD | `$XDG_DATA_HOME/filo/filo.db`, usually `~/.local/share/filo/filo.db` |
| macOS | `~/Library/Application Support/filo/filo.db` |

Use `--db PATH` or the `FILO_DB` environment variable to choose another. The database is private to you (mode
`0600`), and only one Filo process can use it at a time.

**Don't sync the database between machines.** It describes one device's files. Keep it out of Syncthing or other
synced folders (the default location already is).

You can look inside it with `sqlite3`; hashes are the same as `sha256sum` prints.

A newer Filo upgrades an older database automatically the first time it opens it, including for `filo report`. After
that, older versions of Filo refuse to open it, so upgrade Filo on a device before running it there.

## Scheduling

A daily scan only ever reads. A systemd user timer is enough, and no daemon is needed. This example hasn't been
tested by the project yet:

```ini
# ~/.config/systemd/user/filo-scan.service
[Service]
Type=oneshot
ExecStart=%h/bin/filo scan
Nice=19
IOSchedulingClass=idle

# ~/.config/systemd/user/filo-scan.timer
[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
```

Enable it with `systemctl --user enable --now filo-scan.timer`.

## Roadmap

1. **Scan, register, report**, read-only. Done.
2. **Lifespan**: a retention for every file, set through a CSV. Done.
3. **Delete**: `filo delete` lists, asks, and deletes, with checks at the moment of deleting. Done.
4. **Clearer terminal output** for every command. Done.
5. **Duplicates**: decide in a CSV which copies to keep, carried out by `filo delete`; files kept as separate files,
   contents marked always and excluded directories. Done.
6. **Retention policies**: a named retention typed in the CSV, changed in one place. Done.
7. **Next**: folder rules (a default policy on a directory). The order is in [docs/design.md](docs/design.md),
   section 5.
8. Later: **a UI** in place of the CSV.

Principles that won't change:
- A retention is never a trigger: reaching its end makes a file a candidate you confirm.
- A file is deleted only if it is still exactly the content you decided on; one that can't be read is never deleted.
- Deletion is permanent, so keep backups: Filo has no recycle bin.
- Anything ambiguous is reported, never guessed.

The full design, settled decisions and open questions are in [docs/design.md](docs/design.md).

## Development

The repository includes a Podman dev container (`.devcontainer/Containerfile`), driven by `scripts/dev.sh`:

```sh
scripts/dev.sh run go build -o bin/filo ./cmd/filo   # build in the container
scripts/dev.sh run go test ./...                     # run the tests in the container
scripts/dev.sh run go vet ./...                      # vet
scripts/dev.sh shell                                 # or work inside it
```

The first call builds the image (Claude Code is installed only with `scripts/dev.sh build --claude`). The container
mounts only the repo, so `scripts/dev.sh run bin/filo ...` sees the repo and the container's own home, never your
files. To run Filo on your own files, build it static in the container and run it from the host. The SQLite driver
is pure Go, so `CGO_ENABLED=0` gives a binary with no library dependencies that runs on any x86-64 Linux, including
older CPUs (Go's default target is `GOAMD64=v1`). Without it, the binary links the container's C library and may not
start on an older host:

```sh
scripts/dev.sh run env CGO_ENABLED=0 go build -o bin/filo ./cmd/filo
bin/filo --db /tmp/f.db roots add ~/FiloTest           # on the host
```

Inside the container (or with Go installed locally):

```sh
go build -o bin/filo ./cmd/filo
go test ./...
go vet ./... && staticcheck ./...
```

`scripts/testtree.sh init DIR` builds a throwaway tree of test files, and `scripts/testtree.sh mutate DIR` applies a
known set of changes and prints what a rescan should report. The changes include making a directory and a file
unreadable, so run it as a normal user (root ignores permissions); `scripts/testtree.sh unlock DIR` makes them readable
again, which you need before you can delete the tree. When developing, always use a throwaway database and tree, never
your real home directory:

```sh
scripts/testtree.sh init /tmp/FiloTest
bin/filo --db /tmp/f.db roots add /tmp/FiloTest     # adds and scans
scripts/testtree.sh mutate /tmp/FiloTest
bin/filo --db /tmp/f.db scan
bin/filo --db /tmp/f.db report
scripts/testtree.sh unlock /tmp/FiloTest
bin/filo --db /tmp/f.db scan        # nothing new, changed or missing; the unreadable flag clears
bin/filo --db /tmp/f.db export retention /tmp/FiloTest/notes -o /tmp/l.csv
# fill in retention in /tmp/l.csv
bin/filo --db /tmp/f.db update /tmp/l.csv
bin/filo --db /tmp/f.db delete
rm -rf /tmp/FiloTest /tmp/f.db* /tmp/l.csv
```

Project conventions for contributors (and for Claude Code) are in [CLAUDE.md](CLAUDE.md).

## License

Apache License 2.0; see [LICENSE](LICENSE).
