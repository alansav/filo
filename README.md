# Filo

Filo keeps a register of your files: what they are, where they live, how that changes over time, and how long each one
should be kept. It is the start of a review-and-dispose workflow meant to stop files piling up forever ("keep it in
case someone asks").

**Status: iterations 1 to 3 done: scan, lifespan, delete.** You give every file a retention (how long to keep it), and
`filo delete` lists the files past theirs, asks, and deletes the ones you choose. Apart from that, Filo is
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
  date passes, the file is listed as a deletion candidate. Nothing is ever deleted because a date passed: `filo
  delete` shows you the list and asks.
- **Deletes what you confirm**, after reading each file again just before deleting it: a file that changed since its
  retention was set, moved, or can't be read is skipped, never deleted.
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
filo export retention ~/Documents -o docs.csv    # every file under ~/Documents, with its retention
# open docs.csv in a spreadsheet, fill in retention, save it as CSV
filo update docs.csv                            # apply what you changed
filo delete                                     # list the files past their retention, ask, delete
```

The CSV has one row per file: `id, export, directory, name, size, modified, file_date, retention, keep_until`. Sort
and filter it however you like.

- **`retention`** is how long to keep the file:
  - a period counted from `file_date`: `7y`, `18m`, `6w` or `30d`;
  - `forever`: never a deletion candidate, and `filo report` counts how many files are kept forever;
  - or a fixed date, `YYYY-MM-DD`.
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
- A row is refused, with one line saying why, if the file changed since the export or its retention was changed from
  a later export. The other rows are still applied. `--dry-run` checks everything and writes nothing.
- On a large system, export one directory at a time, so the spreadsheet stays fast.

Nothing is deleted because a date passed: `filo delete` lists the candidates and asks (see [Deleting](#deleting)).

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

## Commands

| Command | What it does |
|---|---|
| `filo roots` | List roots: path, settings, mount point, file count, size, last scan. |
| `filo roots add DIR` | Register a directory as a root, with its settings, and scan it. |
| `filo roots set DIR` | Replace a root's settings. |
| `filo scan [DIR]` | Scan `DIR` (a directory inside a root: a partial scan), or every root if omitted. |
| `filo report [--top N]` | Show the overview; `--top` sets how many entries each list shows (default 10). |
| `filo export retention [DIR] -o FILE` | Write every file under `DIR` (or every root) with its retention, as CSV. |
| `filo export duplicates [DIR] -o FILE` | Write the duplicate groups as CSV, to decide which copies to keep (being redesigned). |
| `filo update FILE [--dry-run]` | Apply the retentions (or duplicate decisions) edited in an exported CSV. |
| `filo delete [DIR] [-y]` | List the files past their retention (and duplicate copies marked for deletion), ask, and delete the ones you choose. See [Deleting](#deleting). |
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
refused, or a file `filo delete` couldn't read or delete, `2` a usage error.

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
4. **Next**: clearer terminal output for every command (started with `filo delete`), then a redesign of how
   duplicates are decided and deleted, then named retention policies. The order is in
   [docs/design.md](docs/design.md), section 5.
5. Later: **a UI** in place of the CSV.

Principles that won't change:
- A retention is never a trigger: reaching its end makes a file a candidate you confirm.
- A file is deleted only if it is still exactly the content you decided on; one that can't be read is never deleted.
- Deletion is permanent, so keep backups: Filo has no recycle bin.
- Anything ambiguous is reported, never guessed.

The full design, settled decisions and open questions are in [docs/design.md](docs/design.md).

## Development

The repository includes a Podman dev container (`.devcontainer/Containerfile`), driven by `scripts/dev.sh`:

```sh
scripts/dev.sh run go test ./...        # run the tests in the container
scripts/dev.sh shell                    # or work inside it
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
