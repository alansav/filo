# Filo: roadmap

**The future.** Of three documents, this is the future: `roadmap.md` (what Filo is not yet), `design.md` (the
present: what it is) and `history.md` (the past: how it got here).

Everything Filo does not do: the work planned, ideas, open questions, deferred improvements, and ideas rejected. What
Filo does today is in `design.md`; how it was built is in `history.md`. An item stays here while it is discussed and
designed. When work on it starts, its design moves into `design.md` (the first commit of its branch); when it is
complete, nothing of it is left here. Items keep their numbers, so gaps in the numbering are work already done, or ideas
rejected (see Rejected, at the end: 14 is deleting to the trash). Last updated: 2026-11-20.

## Direction

Agreed 2026-11-17. Filo's job is the lifecycle of files: knowing what there is, deciding how long each file stays,
and getting rid of it safely when its time comes. The register is how it does that, not the job itself.

- **Filo knows and decides; it deletes because deletion needs its checks** (the re-hash, the root check and the
  hard-link check at the moment of deleting). **Any other action on files is done by standard tools, from lists
  Filo writes** (a list for `tar`, a script of `mv` lines the person reads and runs). This keeps it one tool, in the
  Unix sense of programs that work together.
- **Filo stays outside the file system.** Making Filo a file system (every move seen as it happens, no scans) was
  considered and not taken: a bug in it could then lose data on every read and write, while today a broken Filo
  leaves the files untouched and the worst case is a wrong register, which the snapshots cover (principle 3, fail
  safe). It is also far more work.

### The goal: finding files by tags, not by directories
Agreed 2026-11-19/20 (the "virtual file system" of commit f3b092a; not a file system, see above). Received files
land in one simple, stable structure (`received/YYYY/MM/YYYY-MM-DD British Gas Bill for October 2026.pdf`, 34). The
person tags files in Filo (`sender:British Gas`, `type:bill`, `project:house sale`) and finds them by tag rather
than by a directory tree, from the command line or through a list piped to standard tools. A project is a tag, and
Filo can write a markdown note linking to its files. Tagging happens on any device: after a first pass that tags
everything, the routine is to drop a file in the inbox, tag it, and move it to `received/YYYY/MM/`. **Everything
about a file that is not in the file itself is shared between devices** (tags, retentions, file dates, rules,
policies, deletes by Filo) **for files in synced directories** (9a), the only files that are on several devices;
what each device observes (roots, inodes, scan history) stays its own. Tags and retentions are matched differently
on the other device: a retention by content hash (an edited file needs a new decision), a tag by hash and then path
(an edit keeps it; see 9c and 18a).

The pieces, in the order agreed: 37 showing and setting a file's details (`filo show`, `set`, `get`), 19 finding
files (`filo files`), 18a tags, 36 bash completion, then the sync items (9b device ID, 9a synced directories, 9c the
journal, which also carries tags), then 20 the Filo directory and 18b project notes and packages. 37 comes first
because working with a few files from the command line comes before finding and tagging many, and 36 comes after
tags so that its completion covers tag names from the start (reordered 2026-11-20).
Tags come before sync because the journal must start from the decisions that already exist in any case ("seeding": a
directory can be tagged before it is marked synced, a device can join late), so tags made earlier cost the journal
nothing extra; meanwhile tags are used on one device.

## Priority

Provisional, agreed 2026-11-17, with the first places reordered on 2026-11-20 for the goal above; the value and
complexity scores (1 to 10, Claude's estimates) are still to be settled with the person. Items 22 to 33, and
35, are not placed yet.

1. 37 showing and setting a file's details
2. 19 finding files
3. 18a tags
4. 36 bash completion
5. 9b device ID, 9a marking synced directories, 9c sharing decisions
6. 20 the Filo directory, 18b project notes and packages
7. 5a progress line, 5b `--needing-retention`
8. 13 removing empty directories
9. 21 script mode, 34 received documents (needs 21 and 9a)
10. 6 one decision for all copies
11. 16 undoing an update batch, 17 suggested rules
12. 15 where the space goes
13. 7a–7c verifying content, 8 backup awareness, 9d snapshots to the backup machine
14. 10 a summary for scheduled scans
15. Later a and c–e

## Items

Each item: what it is, then its value (V) and complexity (C), then any notes from earlier discussions. Notes are
starting points for the design, not decisions, unless they say so.

### 5a. Progress line in the report (V6, C2)
A line in `filo report` showing how far dating has got: files with a `file_date`, files with a retention, and per
directory what is left. The report's "Where most need one" list already covers part of the last.

### 5b. `filo export retention --needing-retention` (V6, C2)
Writes only the files the report counts as needing a retention, so each CSV is shorter and holds only work still to
do.

### 6. Duplicates: one decision for all copies (V5, C5)
Retention is about content, so dating one copy could offer the same retention to its undated copies (one
confirmation). Copies with different retentions are reported, not resolved.

### 7a. `filo scan --verify` (V5, C3)
A scan re-hashes only files whose size, mtime, ctime or inode changed, so bytes that rot in place go unnoticed until
backups have pruned the good copy. `--verify` re-hashes everything and reports content that changed with nothing
else changing. The register's hash is the reference, so it finds rot that happened after Filo first hashed a file
(an edit not yet scanned shows by its mtime, so it is told apart), not corruption from before.

### 7b. Checking sidecar hash files (V3, C3)
Sidecar files (`file.sha256`, `SHA256SUMS`) give a reference from outside, which finds corruption from before the
first hash. They can be checked against the register's SHA-256 at no extra read (MD5 or SHA-1 sidecars would need
another read); a mismatch (corruption, or a sidecar left from before an edit) is reported, never resolved. Perhaps a
separate `filo verify [DIR]`.

### 7c. Vital files and `filo export checksums -o FILE` (V4, C4)
Making references easy for files that are vital: a "vital" flag on files and one standard `SHA256SUMS` file
(checkable with `sha256sum -c`) written where the person chooses. Writing a sidecar next to each file was considered
and is not proposed: it would be a new kind of write into the scanned trees, and each sidecar would arrive at the
next scan as a new file needing a retention.

### 8. Backup awareness (V6, C8)
Principle 2 says backups make deletion recoverable; Filo cannot check it. Being in a directory Vorta backs up is a
weak signal (the backup may not have run, may have failed, or may predate the current content). The strong one is
"this content is in archive X from date Y": match Borg archive listings (path, size, mtime, perhaps a content hash;
Borg's listing options to be checked against its documentation) against the register, giving each file a "last seen
in a backup" date. It may only ever add warnings to `filo delete` ("in no backup"), never make deleting easier.
Open: reading Borg directly or through Vorta's settings, the cost of listing a large archive, the repository
passphrase, and that Borg runs on another machine.

### 9a. Marking synced directories (V5, C3)
The person marks a directory as the top of a synced directory: a directory that a sync service shares between
devices (for example `filo roots set DIR --synced`, or a mark on a directory inside a root). The person marks it,
so it works with any sync service and does not break if the service changes; Filo reads nothing from the
service's own configuration (agreed 2026-11-17), and the docs say "synced directory", not a product's name. The
mark tells Filo where the journal lives and what its paths are relative to (9c), so no name shared between
devices is needed (changed 2026-11-19: the shared name was meant for decision logs kept in a separate directory,
which now live in the synced directory itself). Open: whether a synced directory is always a root or can be a
directory inside one (a root `~` holding `~/Sync`), and whether one can contain another. What Filo then does
differently: a sync service propagates deletes (see `design.md`, Disposal), and a list of name patterns for
conflict copies, set by the person because every service names them differently, could be surfaced.

### 9b. A device ID (V3, C1)
A random device ID in `meta`, made when the database is created, and recorded with each decision, so no decision
ever has an unknown origin. Small, and cheaper the earlier it is done; needed by 9c, where it names the device's
journal. Only devices that run Filo have one: a phone that only syncs files has none. A reinstalled device gets a
new ID and imports its former journal like any other. **To decide first:** Rejected (below, "Device tracking in
the database") and `design.md` (Register, "no device column") rule it out while the register is never synced;
sharing decisions (9c) is the reason to revisit that, while the register itself stays per device.

### 9c. Sharing decisions between devices (V9, C8)
A synced directory is on several devices, each with its own register; a retention set on one should be known on
the others (for example a rule added on the laptop is applied on the desktop). Tested on 2026-11-13: a file dated
on the laptop, then moved and renamed on the desktop, kept its retention on the laptop's next scan, because the
move was paired by hash.

Sharing the database file itself is rejected: a sync service copies `filo.db` and its WAL and SHM files
separately and not as transactions, so a copy can be stale or corrupt; the `flock` is local, so writes on two
devices between syncs give a conflict copy and one side is silently lost; and most of the register is per device
(root paths, inodes, ctimes, mount points, unreadable flags, scan history), so another device would rehash
everything and, with sync lag, record moves back and forth. A separate database for only the synced roots has the
same problems.

Proposed instead (revised 2026-11-19, replacing logs in the Filo directory): each device keeps its own register,
and they exchange **decisions** through one journal per device and synced directory,
`<synced directory>/.filo/journal.<device-id>.jsonl` (9a, 9b). Two synced directories on two devices make four
journals. A device writes only its own journals and reads all of them, so the sync never makes a conflict copy.
The scan already skips `.filo`.
- **Lines.** JSON Lines, one decision per line, each with a format version `v`, the `device`, a per-device
  sequence number `seq`, the time, and the key of what it decides. The decisions, with paths relative to the
  synced directory:
  - directory rules and the policies they use;
  - retentions and `file_date`s, keyed on content hash, so a file moved on one device still matches;
  - tags (18a), with the file's relative path and hash, matched on hash first and path second; a tag whose file
    cannot be found is reported, never guessed;
  - deletes by Filo, so another device can tell "deleted by Filo on the laptop after the hash check" from an
    unexplained missing file.
- **Importing.** Idempotent: each device records the highest `seq` applied per journal and skips anything at or
  below it. Journals are found by exact name; any other file in `.filo/` is reported, never acted on. A journal
  may arrive half written (not every service renames a finished file into place), so an incomplete last line is
  ignored until it is complete. Decisions apply on the next scan. Two devices disagreeing (the same policy name
  with different values, one hash with two retentions) is reported, never resolved. An edited file has a new hash
  and needs a date again.
- **Versions.** A journal or line with a format version newer than the build knows is skipped whole and reported,
  applying nothing, as a newer database is refused. An unknown kind of decision in a known version is counted and
  reported.
- **Unknown devices.** Filo cannot know how many devices share a directory, or when another joins. A new device
  reads every journal in the directory, and no journal is trimmed on the grounds that every device has seen it.
  Compaction, later: a device drops its own lines that a later line of its own replaces, keeping the other lines'
  `seq`; the rewrite is a temp file renamed into place; delete records may expire after a long window. Not needed
  soon: journals hold decisions only (about 150 bytes a line, so 100,000 decisions is about 15 MB) and a rule is
  one line, not one per file. Reserve `seq` and the key in the format now.
- **Silent failure.** `filo report` lists the journals it can see (device, last change) and flags a device whose
  journal never appeared, so a sync rule that skips hidden directories (a `.*` pattern, say) shows up whatever the
  service.

Needs 9a and 9b. Writing `.filo/` inside a synced directory is one more exception to the hard rule on where Filo
writes (20 has the other). No-code alternative meanwhile: register the synced directory on one device only and set
retention there; the content is the same everywhere and deletes propagate.

### 9d. Snapshots to the backup machine (V4, C3)
Getting the register snapshots off the machine, so a lost disk does not take the register with it. A snapshot is a
finished file that nothing opens for writing, so unlike the live database it is safe to sync, for example by a
one-way sync of the snapshot directory to a machine that is backed up. With the Filo directory (20), each device's
snapshots could go to `snapshots/<device-id>/` there.

### 10. A summary for scheduled scans (V3, C2)
A short line ("4 files past retention, 2 need a decision") a timer can pass to a desktop notification, so candidates
do not sit unnoticed. Filo still never deletes on its own. Perhaps with it, a finer split of exit codes.

### 13. Removing empty directories (V5, C4)
Removing directories that hold no files and were already there (`filo delete` already removes the directories its own
run empties, see `design.md`, Disposal). Destructive, so through `filo delete` and `internal/dispose`, for example
`filo delete --empty-directories [DIR]`. Proposed: a fresh walk finds the directories with no files at any depth (the
register does not store directories); the list is shown and Filo asks (or `-y`); each is removed bottom-up with the
call that refuses a non-empty directory; never a root, a mount point, a directory that could not be read, or one
under the ignore rules. Open: whether a directory holding only `.DS_Store`, `Thumbs.db` or `desktop.ini` counts as
empty (today it does not); and that some programs need their empty directories (a maildir's `cur`, `new`, `tmp`;
`.git/refs/tags`; project skeletons), which argues for listing them and letting the person choose, as `filo delete`
does.

### 15. Where the space goes (V5, C3)
A report section, or `filo files --sort size`, showing the largest directories and files and how much of it is past
retention or in duplicates, so the person knows which directory to date first.

### 16. Undoing an update batch (V5, C6)
Undo a retention CSV applied by mistake. Every batch is already recorded (`review_batches`, `reviews`), so most of
what it needs is there.

### 17. Suggested rules (V5, C5)
Point out directories where most files already share one policy and offer the `filo rules add` that would cover the
rest. A suggestion only, never applied on its own.

### 18a. Tags (V7, C6)
Designed with the person on 2026-11-19/20 (see Direction, the goal). Tags describe files, so files can be found by
tag; a project is a tag.
- **One mechanism.** Tags and projects are one thing: a project is a tag (`project:house sale`), which 18b can give
  a markdown note. **A tag is not a retention:** a file has one retention (a date, a period or a policy) and any
  number of tags, and a tag never carries a retention. (Possible later, not agreed: tags as an input to rules that
  set a policy.)
- **A tag is `key:value`, or a bare value**, and the key is first-class: files can be found by key, by key and
  value, or by a key they lack (`--no-tag sender:`, the tagging to-do list). A file can have several values for one
  key (two projects). Keys are created on the fly, never declared; `filo tags` lists them, and a tag exists only
  while some file has it. A typo is fixed by renaming the tag, or by hand: list its files, add the right tag, remove
  the wrong one. (To check with 18b: a project tag with a note whose files all lose it.)
- **Spelling.** Values may hold spaces (trimmed at the ends, runs collapsed), keep the case first typed, and match
  ignoring case (`sender:british gas` finds `British Gas`). Keys are lowercase whatever is typed, have no spaces
  (letters, digits, `-`, `_`) and at least one letter. The first colon separates the key when what is before it is
  a valid key, so `project:trip: Paris 2027` is key `project`, and `10:30 call` is a bare tag; `re: invoice` is key
  `re`, odd but logical. A tag cannot hold `;` (the CSV's separator) or `*` (the one wildcard in a search, 19), so
  `filo tag` refuses both and says why; `?` and `[` are ordinary characters (`urgent?`, `Q1 [draft]`). A tag needs a
  value: `filo tag FILE sender:` is refused ("give a value"), since `key:` alone is only a search (agreed
  2026-11-20).
- **Commands** (the first version): `filo tag FILE... TAG...` and `filo untag FILE... TAG...`; `filo tags`, tags with
  their file counts; `filo tags rename OLD NEW`, across every file, which fixes a typo in the key or the value, and
  may also change only the case (`british gas` to `British Gas`); `filo export tags [DIR] -o FILE` and `filo update
  FILE` (a separate CSV from retention, told apart by its header). Finding files: `filo files --tag` and `--no-tag`
  (19).
- **The tags CSV** has a `tags` cell per file, tags separated by `;`, filled at export with the file's current tags.
  The cell replaces the file's tags: a tag not in it is removed, so an untouched cell changes nothing and an emptied
  one removes all. A file whose tags changed since the export (tagged by command meanwhile) is refused for that row,
  as a changed file is today.
- **A file Filo has not seen yet** (dropped in the inbox, tagged at once): `filo tag` registers only that file, not
  its directory, for speed. Its path already known: just tagged. The same inode as a known file at another path:
  refused, "looks moved from X; run filo scan" (never guess; a scan pairs it). Otherwise it is registered as a new
  file (hashed; copies' tags taken, below), and the next scan treats it as known. Outside every root, or where the
  root's settings skip it (hidden, ignored): refused, saying why.
- **Tags follow the file**, as the register tracks it, not its path or content: kept through a move or rename (the
  inbox routine), an edit (unlike retention, which asks again), and while missing; a file deleted by Filo drops out of
  listings. **A copy keeps the tags:** a new file with the same content as files present takes the union of their
  tags, once, when first seen, and the scan says so when those copies had different tags; after that each copy's tags
  are its own. Deleted files pass on nothing (a restored file is new, as for retention). A file saved by writing a
  new file and renaming it into place (new inode, new content) stays a reported possible move, never paired; the
  report says the old file had tags.
- **Slices** (one PR): the register (a schema version, a tags table on file ids) and the spelling rules; `filo tag`
  and `untag` with single-file registration and copies' tags in scans; `filo tags` and rename; `filo files --tag`;
  `filo export tags` and `filo update`; README and help. Each output mocked and agreed before its slice. Tags are
  stored so each change can be written to the journal later (9c) without rework.
- **Later:** rules that tag by name pattern (`*British Gas*` under `received` gives `sender:British Gas`), once tags
  have been used by hand. In the journal (9c) a tag goes with its file's relative path and hash, and another device
  matches it on the hash first and the path second (an edited file keeps its tags, unlike its retention); a tag
  whose file cannot be found there is reported, never guessed onto another file (proposed 2026-11-19, with 9c).

### 18b. Project notes and packages (V5, C5)
Pull a project's files together. By the direction above, Filo writes the list and a standard tool does the copying
(`filo files --tag "project:trip" -0 | tar --null -T - -czf trip.tgz`), or Filo writes a copy script (21). Filo itself
never copies files into the scanned trees: the copies would come back as new files and new duplicate groups.
Project notes (proposed 2026-11-19): Filo could also write one markdown note per project, with a link to each
file, into the Filo directory (20), never into a scanned tree, where each regeneration would show as a changed
file needing a decision. The output is deterministic (sorted, no timestamps, written only when it changed) so two
devices never make conflict copies. A relative link works on a device only if the note and its targets are in the
same synced directory. It is Filo's first write beside the register, so it comes after the delete work.

### 19. Finding files (V8, C2)
`filo files [DIR] [--name GLOB] [-0]`: find files in the register by name; read-only. The first slice of the query
view (Later a), and what tags (18a) build on: they add `--tag TAG` and `--no-tag TAG` (agreed 2026-11-19).
- Conditions combine with AND only; an OR is two runs. `key:` means any value of that key (only in a search: a tag
  always has a value), `*` in a value matches any run of characters (`sender:British*`; the only wildcard, and a tag
  cannot contain it, so an exact search is always possible; `?` and `[` match themselves), and `--no-tag sender:`
  lists the files with no sender.
- In a terminal: each file with its path (and, with 18a, its tags), cut at 10 with `… N more`. Piped: one absolute
  path per line, or NUL-separated with `-0`, so `tar`, `xargs` and the like work with any file name.
- Only files present now: not deleted, not missing.
- It covers what the register holds (name, status, tags), not file contents; see Rejected, "Building search".

### 20. The Filo directory (V6, C4)
One directory per device, chosen by the person (for example `~/Sync/Filo`), shared across devices by any sync service,
which Filo is allowed to write into. It carries snapshots (9d) and project notes (18b). The decision journals (9c) moved
out on 2026-11-19: they live in `.filo/` of each synced directory (9a). Filo never scans it, even inside a root. Each
device writes only its own files there. Needs a change to the hard rule that Filo writes nothing outside its data
directory, `--db` and `-o` files: one more place, chosen by the person.

### 21. Script mode (V4, C7)
For the cautious: instead of acting, Filo writes a script that does it, for the person to read and run (`filo delete
--script -o FILE`; later empty directories, a revert (27a), a project package (18b)). A script is an `-o` file, so
it fits the hard rules. The checks at the moment of deleting have to travel with the script (each line re-checks the
hash with standard tools before `rm`); file names are untrusted, so quoting them safely is the hard part; and Filo
records what it planned, so the next scan records a file gone with its hash matching as deleted by the script.

### 22. `filo version` (V4, C2)
Filo's own version (no build version is stamped yet) and the database in use: its path, schema version and when it
was created. Today the schema version can only be read with `sqlite3 DB "SELECT key, value FROM meta"`. Showing the
same in `filo report` was considered as a simpler alternative.

### 23. All of a scan's events (V3, C2)
A scan shows the first 10 events of each kind and `… N more`; the report shows only the latest events overall, so
the rest of a large scan's events (500 missing after a directory was moved outside the root) cannot be listed.
Perhaps `filo report --scan N`.

### 24. A root's last whole scan (V3, C1)
`filo roots` and the report show the last scan, which may have been partial (one directory), so a root can look
recently checked when most of it was not. Showing the last whole-root scan as well needs one store query.

### 25. Paths inside notes and reasons as structured parts (V2, C4)
`Problems` and select-mode results (`delete`, `update`) shorten the paths in their reasons where a path starts (at
the start of the text, or after a space or an opening bracket), so a file name holding ` /data/` is still shortened
against the root `/data`. The fix is to pass paths in notes and reasons as structured parts (a root and a relative
path) rather than rewriting text. Shortening notes (an event's detail, a symlink's target) was tried and taken back
on 2026-11-15: an absolute symlink target read as a relative one, and in a list grouped by root a note was shortened
against the wrong root.

### 26. Telling a terminal from a character device (V2, C1)
The prompt check treats standard input as a terminal if it is any character device, so `filo delete </dev/null`
asks, reads end of input and deletes nothing (safe, but it should print the no-terminal message instead). A real
check needs `golang.org/x/term` or `golang.org/x/sys` (already an indirect dependency) used directly: a new direct
dependency, which needs the person's agreement.

### 27a. `filo revert`: undo a rearrangement (V5, C7)
A person rearranges files and directories, then after hours or days prefers the old layout: Filo moves things back
to where they were at an earlier scan. Locations only; content recovery is the job of backups. The data is kept
already: paths are stored as intervals of scans, so the tree at any scan is known. Deferred on 2026-11-13 (it was the
earlier iteration 4: a zip of the files taken before a reorganisation covers it, and a revert only works if a scan
ran before the moves, which a timer gives). By the direction above, Filo would write a script of `mv` lines (21)
rather than move files itself. It can only undo moves Filo paired; conflicts (the old path taken, the file gone or
unreadable, another filesystem) are reported and skipped, never resolved.

### 27b. Confirming a possible move (V3, C3)
A scan reports "same name, different content" as a possible move and never pairs it. Confirming it would join the
two, so the file keeps its history; its content changed, so it still needs a new date.

### 28. Setting up scheduled scans (V5, C2)
`design.md` (Commands and scheduling) leaves scheduling to a systemd user timer; the README has an example, not yet
tested. Filo could print the unit files ready to install (`Persistent=true`, low CPU and I/O priority); by the
direction above it does not install them itself.
Several items lean on regular scans: 10, 27a, 9c.

### 29. Links in exports (V5, C3)
`filo export --links`: a column whose cell opens the file (and, for duplicates, its directory) from the spreadsheet,
so copies can be checked with a click. Mainly for duplicates. Whether a spreadsheet makes such a cell clickable is to
be checked in Calc first; the column is display-only, and file names inside it are untrusted.

### 34. Received documents: inbox and archive (V5, C3)
A convention for documents that arrive (bills, letters, PDFs), from a design discussion on 2026-11-19; nothing is
built.
- **Inbox.** One directory where everything received lands. Each file is renamed `YYYY-MM-DD sender description.ext`
  (for example `2026-11-19 British Gas Bill for October 2026.pdf`) and filed. The name already feeds Filo:
  `file_date` is read from the date in a name, and directory rules match name globs (`*British Gas*` under
  `received` gives `bills`) at any depth, so no delimiter and no parsing of the sender is needed. A delimiter
  between sender and description would only help a person, or a later search.
- **Archive.** `received/YYYY/MM/`, the directory chosen from the date in the name when the file is filed and never
  changed afterwards, so paths stay stable (links from notes, recent-file lists, traffic between devices). A name
  with no date is not filed and stays in the inbox.
- **Filing is not done by Filo.** By the direction above Filo writes a list of `mv` lines for the person to read
  and run (21), or a small separate script does it. A name that already exists in the target gets a numeric
  suffix, in the script.
- **Inbox ageing.** A report line for files in the inbox longer than N days, from `first_seen_scan`, so the inbox
  does not become a new pile. Needs a root setting that marks a directory as an inbox.
- Open: whether to offer a suggested name from the date alone (never the sender).

### 35. Forgetting deleted files (V6, C4)
Raised 2026-11-19, while designing tags. A file Filo deletes keeps its register row forever (`design.md`,
Disposal: "those rows are the history of what Filo deleted"): its name, path, hash, size and retention, its path
history, events, review and export rows, and from 18a its tags. Snapshots hold copies for up to 31 days, and the
copies kept before a schema upgrade are never rotated. So a bill deleted because its seven years were up is still
named in the register, which outlives the retention that deleted it.
- What the history is used for: the report's "deleted by filo" counts and sizes, the "same content as a file
  deleted on …" hint, and (9c) telling another device that a missing file was deleted by Filo. None of these needs
  names or paths for long.
- Idea: after a grace period (long enough for every device to sync the delete, for example 90 days), forget a
  deleted file entirely (its rows, paths, events, tags, review and export rows), keeping only counts and sizes for
  the report.
- Open: the period, and whether it is a setting; what happens to snapshots and the pre-upgrade copies; whether the
  register's `freelist` needs a `VACUUM` so forgotten names are really gone from the file; how it fits 9c's delete
  records, which other devices read.

### 36. Bash completion (V5, C4)
Agreed with the person on 2026-11-20. Tab completes Filo's commands, flags and arguments in bash.
- **Loading it.** `filo completion bash` prints the script and writes nothing. The README shows two ways: `source
  <(filo completion bash)` in `.bashrc` (always current, run at each shell start), or the output saved once to
  `~/.local/share/bash-completion/completions/filo`, which the `bash-completion` package loads on the first Tab
  (rewritten after an upgrade that changes commands). Filo never edits `.bashrc` itself (hard rules).
- **Static:** commands, subcommands and flags. Commands are a `switch` in `internal/cli` and each command's flags are
  known only inside it, so the list is written down once or the flags move into a table; a test checks that every
  command and flag the CLI accepts is completed, so the two cannot drift apart.
- **Dynamic:** a hidden `filo __complete WORDS...` the script calls on Tab. Strictly read-only: no snapshot, never
  creates a database, prints nothing on failure, and honours a `--db` typed on the line. It completes file paths
  from the register merged with the disk (so missing files, and files deleted by Filo, complete; the typed word,
  relative or starting with `~`, is turned into a root and a path in it, as 37 does), policy names, rule patterns,
  and tag names (18a comes first).
- Bash only at first (zsh can load bash completions). No framework: Cobra would be a new dependency and a rewrite of
  every command's parsing.

### 37. Showing and setting a file's details (V7, C5)
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
- **Slices** (one PR), each output mocked and agreed before its slice: `show FILE`, with the path lookup; `show DIR`
  and `-r`; `set` for file date and retention; `set --duplicates`; `get`; README and help.
- **`filo show FILE`, the agreed output** (2026-11-20, for slice 1). Each file starts with a title line: the name,
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
      first seen  2026-11-12 (scan 3)
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
- **`filo show DIR`, the agreed output** (2026-11-20, for slice 2). The same shape: a title line with a summary,
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
- **`filo set`, the agreed output** (2026-11-20, for slices 3 and 4). It asks **only when it would replace a value
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
  for 7y"), then "Nothing has been deleted. Next: filo delete". Clearing (`--no-retention`) asks in the same shape,
  adding "they will need a retention again". `--duplicates` names the content and every copy it covers:

  ```
  Done   always . 1 content . applies to 3 copies
      ~/Projects/a/LICENSE   (named)
      ~/Projects/b/LICENSE
      ~/Projects/c/LICENSE
    - these copies no longer count as duplicates; filo set FILE --duplicates counted undoes it
  ```
- **`filo get`, the agreed output** (2026-11-20, for slice 5). Read-only, for scripts and quick checks. Fields are
  `set`'s plus the read-only ones (`--file-date`, `--retention`, `--keep-until`, `--status`, `--duplicates`, `--size`,
  `--modified`, `--sha256`, `--root`, `--first-seen`), printed in the order given; no `--all` (that is `show`). A
  field with no value prints as empty, never "none" (a policy may be named `none`; a script tests for empty).
  - One file, one field: the bare value, terminal or piped (`filo get tax.pdf --retention` prints `7y`).
  - One file, several fields: name and value lines (`retention    7y`); piped, a tab between them.
  - Several files: one row per file, values in the order given and the path last; in a terminal with a header
    and `~`, piped with no header, tab-separated, the path absolute. The path is last because a file name may hold
    a tab and a value cannot, so splitting on the first N tabs keeps the whole path; `-0` ends each row with NUL
    instead of a newline, as in 19.

    ```
    retention    keep_until
    7y           2031-03-31   ~/Documents/receipts/coffee.pdf
                 2030-02-28   ~/Documents/receipts/lunch.pdf
    forever      forever      ~/Documents/receipts/taxi.pdf
    ```
  - A path the register does not know: the error line on stderr, the other files still printed, exit status 1.

### Docs

Improvements to the three documents themselves, suggested on 2026-11-17 after the split.

#### 30. A test that keeps design.md's output examples honest (V6, C3)
The examples in `design.md` are excerpts of the golden files in `internal/cli/testdata`; they drift again as soon
as output changes and the doc is not updated. A test could check that every example block in `design.md` still
appears in a golden file, so a change to the output that is not carried into the doc fails the build. A Go test
reading a document is a small coupling; in return "design.md matches the app" becomes a check, not a habit.

#### 31. Splitting design.md by feature (V4, C3)
`design.md` is about 1,800 lines. One file per feature (`design/duplicates.md`, `design/rules.md`, …) would make each
branch's design commit, and each read before a change, smaller. The cost: the code comments that cite "design.md,
Duplicates" and the like are updated to the new paths.

#### 32. history.md append-only (V3, C1)
New entries go at the end, dated, and old ones are never rewritten, so the past cannot drift the way `design.md`
did. A rule for `CLAUDE.md`, no code.

#### 33. Short roadmap items until one is picked (V3, C1)
An item holds its value and complexity and a few lines of what and why; detailed design notes are written only
once it is chosen, so the roadmap stays a list that can be scanned and reordered. Some items above already hold
more (carried over from the old open questions); they are trimmed when reviewed. A rule for `CLAUDE.md`, no code.

### Later a. `filo files`, the query view (V6, C3)
Viewing the register: `filo files [DIR] [--name GLOB] [--larger SIZE] [--smaller SIZE] [--modified-before DATE]
[--modified-after DATE] [--status S] [--empty] [--sort size|modified|keep_until]`, cut at N rows in a terminal with
"… N more", all rows when piped. It filters on what the register holds: size, mtime, `file_date`, `keep_until`,
status, and first seen (`first_seen_scan`, the register's own date, which a copy or restore cannot reset). Not on
creation time (many filesystems do not record it, and a copy, restore or sync resets it) or access time (not stored;
`relatime` and `noatime` mounts make it unreliable, and Filo reads with `O_NOATIME` so as not to disturb it). Empty
files are shown through it (`--empty`); until then a count line in `filo report` would keep them visible. `--name`
is item 19.

### Later c. A UI in place of the CSV (V8, C10)
Including viewing and editing the register in place. Everything hangs off the stable file ID, so none of it needs the
register to change shape. Open: the technology.

### Later d. Notes and tasks (V5, C9)
Notes on a file; tasks, some with files and some without; possibly a productivity system built on the register (a
recurring check on a file, such as an annual look at a seven-year record, is a task). A change of scope from "Filo owns
lifecycle" that needs its own discussion first; by the direction above it may be a separate tool reading the register.
Linking files to a project is a tag (18a). (Holds were dropped on 2026-11-12: a file that must be kept gets a later
retention or `forever`.)

### Later e. A watcher (V3, C8)
Seeing changes as they happen (`inotify`/`fanotify`) instead of on the next scan. Rejected (below, "A daemon")
rejects a daemon unless near-real-time change detection is needed; a scheduled scan does the job, and inotify needs
one watch per directory and drops events when its queue fills.

## Deferred improvements

Smaller improvements to what exists, noted while it was built. Not prioritised.

- **Register:** a restore command for snapshots (restoring is by hand today); skipping a snapshot when a scan changed
  nothing, if registers grow large (every run that writes copies the whole register); an atomic no-replace rename
  for snapshots (needs `golang.org/x/sys`), so a program not taking Filo's lock cannot slip a file in under the
  snapshot's name.
- **Roots:** `filo roots remove DIR`, with a dry run: removes a root's register rows, never files, and its directory
  rules with it (its confirmation says how many).
- **Moves:** pairing across roots whichever root is scanned first (today a file moved to another root is linked only
  when the old root is scanned first); statting the old path of same-hash files outside a scan's scope, so a file
  moved in from outside it keeps its history; pairing moves by inode instead of hash, to avoid hashing moved files.
- **Symlinks:** checking them beyond the broken flag.
- **Platforms:** a Windows build (the OS-specific code sits behind small functions; locking is the missing piece:
  non-Unix builds refuse to open the register).
- **Dates:** reading `file_date` from photos' EXIF data (needs a library).
- **Deleting:** opening each directory from the root and unlinking relative to it, so a directory swapped in the
  microseconds after the last look is not followed (today the directories on the path are checked by name).
- **Hashing:** partial hashing of very large files (size plus first and last megabyte, full hash on demand), if first
  scans of large media are too slow; it would need a "partial" marker per hash and a full hash before any disposal.
- **"Last verified":** a file's last verified time, derived as the latest completed scan whose scope covers its
  path, if something needs it.
- **Help and errors:** rewriting the help's words (only their layout was redone), and reviewing the wording of the
  errors.
- **Tiers as review intervals:** tier directories (bronze, silver, gold, platinum) were rejected (see Rejected,
  below); kept as an idea: tiers become default review intervals.

## Rejected

Ideas considered and not taken, with the reasons, so they are not raised again without them. An item above that
contradicts one of them is a discussion when the roadmap is reviewed.

- **Quarantine, the recycle bin, holds and `filo restore`** (rejected 2026-11-12). Deletion is permanent and backups
  make it recoverable; the confirmed list and the re-hash check keep it safe. The bin fills up with large files and is
  missing on network disks. A hold is a later retention or `forever`. A restored file is a new file. Considered
  again on 2026-11-17 (deleting to the trash) and rejected again: a vital file should already be in a backup; a file in
  the trash is not deleted, so its retention is not really carried out; and a file restored from the trash, or moved
  out of it to somewhere else, comes back to Filo as a new file with no history.
- **More checks at the moment of deleting** (rejected 2026-11-12): recently modified, lock and sync companion files,
  open in a process. See Disposal.
- **Relinking a restored file to its deleted record** (rejected 2026-11-12): it would carry the old, passed retention
  onto a file the person just chose to bring back. It comes back as a new row, with a hint.
- **Filling `retention` from a filesystem creation date** (rejected 2026-11-12): many filesystems do not record one,
  and a copy, restore or sync resets it. `file_date` is typed, or read from the file name.

- **Reading a retention from the file itself** (frontmatter, Office or PDF properties, extended attributes;
  rejected 2026-11-13). A date stored in the file can be changed under the register, which then has to decide which
  one wins; the register stays the one source of truth.

- **Tier directories** (bronze, silver, gold, platinum). A good workflow, but moving files breaks links (recent-file
  lists), scatters one subject across tiers, and ties lifetime to location.
- **Review date stored inside each file.** Fragile: many editors drop extended attributes on save, and some formats
  cannot carry properties. Losing it fails safe but causes churn, and it cannot hold floors reliably.
- **A daemon.** A scheduled scan does the job; a daemon adds state, file watching limits and failure modes. Revisit only
  for near-real-time change detection.
- **Device tracking in the database.** Unneeded while the database is per device and never synced.
- **One database per root** (the first Go design, with the database in `<root>/.filo`). Several roots on one device
  meant several registers to manage; one database with a `roots` table replaced it on 2026-11-11.
- **Themed command and project names** (the earlier "dirge" idea, funeral-style subcommands). Plain names win.
- **Building search: Filo does not index file contents** (narrowed 2026-11-20). Existing indexers and document
  managers (Paperless-ngx for received documents, desktop indexers) already search inside files. Finding files by
  name and tag over what the register holds (19, 18a) is a lookup, not search, and is the goal (see Direction).
