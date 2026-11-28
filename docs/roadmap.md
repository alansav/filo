# Filo: roadmap

**The future.** Of three documents, this is the future: `roadmap.md` (what Filo is not yet), `design.md` (the
present: what it is) and `history.md` (the past: how it got here).

Everything Filo does not do: the work planned, ideas, open questions, deferred improvements, and ideas rejected. What
Filo does today is in `design.md`; how it was built is in `history.md`. An item stays here while it is discussed and
designed. When work on it starts, its design moves into `design.md` (the first commit of its branch); when it is
complete, nothing of it is left here. Items keep their numbers, so gaps in the numbering are work already done, or ideas
rejected (see Rejected, at the end: 14 is deleting to the trash). Last updated: 2026-11-28.

## Direction

Agreed 2026-11-17. Filo's job is the lifecycle of files: knowing what there is, deciding how long each file stays,
and getting rid of it safely when its time comes. The register is how it does that, not the job itself.

- **Filo knows and decides; it deletes because deletion needs its checks** (the re-hash, the root check and the
  hard-link check at the moment of deleting). **Any other action on files is done by standard tools, from lists
  Filo writes** (a list for `tar`, a script of `mv` lines the person reads and runs). This keeps it one tool, in the
  Unix sense of programs that work together. Clarified 2026-11-27: this rules out clones of file commands (`filo
  mv`, `filo cp`), not changes made through the export and bulk update, such as renaming a file by editing its
  `name` cell (built, see `design.md`).
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
policies, deletes by Filo) **for files in synced directories** (9), the only files that are on several devices;
what each device observes (roots, inodes, scan history) stays its own. Tags and retentions are matched differently
on the other device: a retention by content hash (an edited file needs a new decision), a tag by hash and then path
(an edit keeps it; see 9, and `design.md`, Tags).

The pieces, in the order agreed: finding files (`filo find`, item 19, built 2026-11-21), 18a tags (built
2026-11-22), 38 the tag index
pages (built 2026-11-22), 36 bash completion (built 2026-11-28), then sharing decisions between devices (9, which
merged the former 9a, 9b and 9c on 2026-11-28), then 20 the Filo directory and 18b project notes and packages. 36 comes after tags so that its completion covers tag names from the start (reordered
2026-11-20).
Tags come before sync because the journal must start from the decisions that already exist in any case ("seeding": a
directory can be tagged before it is marked synced, a device can join late), so tags made earlier cost the journal
nothing extra; meanwhile tags are used on one device.

## Priority

Provisional, agreed 2026-11-17, with the first places reordered on 2026-11-20 for the goal above; the value and
complexity scores (1 to 10, Claude's estimates) are still to be settled with the person. Items 22 to 33, 35 and
43 are not placed yet.

Next, agreed 2026-11-27 (friction week, on the `improvements` branch, after the combined CSV, 39 renaming by the
`name` cell, 41 no `bin`/`obj` special case, 42 moving by the `directory` cell and 40 removing the retention and tags
exports, all built that day, and 44 the tag index's find page, built the same evening; 36 bash completion built on
2026-11-28): the list below.

1. 9 sharing decisions between devices (designed 2026-11-28)
2. 20 the Filo directory, 18b project notes and packages
3. 5a progress line, 5b `--needing-retention`
4. 13 removing empty directories
5. 21 script mode, 34 received documents (needs 21 and 9)
6. 6 one decision for all copies
7. 16 undoing an update batch, 17 suggested rules
8. 15 where the space goes
9. 7a–7c verifying content, 8 backup awareness, 9d snapshots to the backup machine
10. 10 a summary for scheduled scans
11. Later a and c–e

## Items

Each item: what it is, then its value (V) and complexity (C), then any notes from earlier discussions. Notes are
starting points for the design, not decisions, unless they say so.

### 5a. Progress line in the report (V6, C2)
A line in `filo report` showing how far dating has got: files with a `file_date`, files with a retention, and per
directory what is left. The report's "Where most need one" list already covers part of the last.

### 5b. `filo export --needing-retention` (V6, C2)
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

### 9. Sharing decisions between devices (V9, C9)
Designed with the person on 2026-11-28, topic by topic, merging the former 9a (marking synced directories), 9b (a
device ID) and 9c (sharing decisions): none of the three is a working feature alone. Built in slices on one branch,
one PR.

A synced directory is on several devices, each with its own register; a decision made on one (a retention, a tag,
a rule) should be known on the others. Sharing the database file itself is rejected: a sync service copies
`filo.db` and its WAL and SHM files separately and not as transactions, so a copy can be stale or corrupt; the
`flock` is local, so writes on two devices between syncs give a conflict copy and one side is silently lost; and
most of the register is per device (root paths, inodes, ctimes, mount points, unreadable flags, scan history). So
each device keeps its own register, and devices exchange **decisions** through one signed, append-only journal per
device in `.filo/` at the top of each synced directory. A device writes only its own journal and reads all of them,
so the sync never makes a conflict copy. Tested on 2026-11-13: a file dated on the laptop, then moved and renamed on
the desktop, kept its retention on the laptop's next scan, because the move was paired by hash.

- **The frame.** Whoever can write into a synced directory can already delete or change its files on every device
  through the sync service. The journal must never let a writer do more than that: a journal line only records; it
  never deletes, renames or moves a file. Deletes are the sync service's business (it deletes, or makes a conflict
  copy); Filo only records that it deleted a file.
- **The device.** Each register gets a device ID (128 random bits, written in lower-case base32), an ed25519 key and
  a name (the hostname, which the person can change), in `meta`, made with the database or by the schema upgrade.
  The device is its register: a restored register is the same device, a reinstall without it is a new device, and a
  register copied to another machine is caught (below). Every `--db` is its own device. A phone that only syncs
  files has none. This replaces the "no device column" of `design.md` (Register) and the rejected "Device tracking
  in the database".
- **The synced directory.** Any directory inside a root, or a root itself, marked by the person. Filo knows it by a
  random ID in its mark, never by its path, so it can sit at a different path on each device (`~/Sync` on one, the
  root `/data/Sync` on another); each register maps the ID to its root and directory, and a root absorbing another
  rewrites that. One synced directory never holds another (refused when marking or joining; reported when found).
  The mark is tool agnostic: Filo reads nothing from a sync service's configuration, and the docs say "synced
  directory", not a product's name. The walk still skips `.filo`, but notes one that holds a mark; a mark not joined
  is reported by every scan until it is joined or ignored. The same ID at two places (a copy of the directory) is
  reported, never guessed, and at most one is joined. A joined directory gone from its place while its ID is found
  once elsewhere (moved) is reported, and nothing is imported or written for it until the person confirms the new
  place. A lost mark is written back from the register. Root settings (hidden files, ignores) stay each device's
  own; decisions about files a device does not scan stay pending, counted, not listed each run.
- **Trust.** Every line is signed with its device's key. A mark found by a scan is reported automatically, but
  sharing starts only with a one-time confirmed join, which makes clear where the sync comes from: the synced
  directory, its ID, which device marked it and when, and for each device its name, key fingerprint (compared with
  `filo device` on that device), first and last seen, number of decisions, and who vouched for it. A trusted device
  can vouch for a new one (a signed `trust` line), so a new device is approved on one device, not on each. Filo
  cannot know that a device has left (reinstalled, broken): the device list shows when each was last seen, and a
  device is **retired** by a signed line naming its last line; its lines up to there stay valid, anything after is
  refused, and its journal is never deleted, so a device joining later still gets its decisions. A retired device
  that is still alive sees the line, stops writing, and must join again as a new device. Accepted risk: a
  compromised trusted device.
- **What is shared.** Every decision about a file, directory or content in the synced directory, whatever made it
  (a command, the CSV, a rule, a duplicates CSV); what a device only observes is not (roots and their settings,
  path history, inodes, scans, events, exports, snapshots, review history). Only decisions about files inside the
  synced directory are written, and a journal's decisions apply only to files inside that same synced directory,
  so a hash match can never reach a copy elsewhere.
  - File decisions: a file's date; its retention or policy; each tag added or removed; and what any rule decides
    for each file (both devices deciding the same merges).
  - Rules whose directory is inside the synced directory, relative to its top. A rule on a directory above it is
    shown, and the person is asked, per rule, to copy it to the synced directory's top, move it there, or leave it:
    when the directory is marked or joined, and when such a rule is added later; the question shows a dry run (a
    copied rule outranks rules on the directories between), and no terminal or `-y` leaves it. `filo rules` flags
    such rules. A rule left above still has its per-file outcomes shared. Needs copying or moving one rule into a
    subdirectory, which `rules copy` refuses today.
  - Policies, by a shared ID (so a rename is a change of name, not a new policy), with their global effect: a
    policy changed on one device changes it everywhere, its unsynced files included; changing one says where else
    it applies. Removing one is shared, and happens on a device only where nothing uses it.
  - Duplicates: a content always kept as separate copies, when a copy is inside (global, like a policy); members of
    sets of records inside; excluded directories inside, and one above it as "the whole synced directory is
    excluded".
  - A tag index inside the synced directory: shared, and written by one device only (the one that added it, until
    handed over), so two devices never rewrite the same pages.
  - Facts, which never conflict: **identity** (the first decision about a file gives it a random shared file ID;
    a device's scan journals what it paired for such a file: moved, renamed, edited, returned), renames and moves
    by `filo update`, and deletes by Filo (another device closes the row as "deleted by Filo on laptop" once the
    file is gone there, so a copy restored later is a new file everywhere). Two facts that contradict each other
    are ignored, and each device's own pairing stands.
- **Finding the file.** A file decision names the file's shared ID, content hash H and path P relative to the
  synced directory. The receiving device uses the file the ID is mapped to; the first time, the one file with H at
  P, else the one file with H, else the one with H that was once at P (path history). With none, the decision is
  pending (the sync has not delivered the file yet) and tried again on every run; with several, reported, never
  guessed. A retention needs the hash (an edited file needs a new decision); a tag follows the file through an edit.
  So a file decided on the desktop, then moved or renamed there, gets the decision on the laptop whether the
  journal or the file arrives first.
- **Ordering and conflicts, without clocks.** Each decision lists the decisions it replaces: those in effect on its
  device for that thing when it was made. One that replaces all those in effect wins; otherwise both stay, a
  conflict. Journals arrive in any order, so a decision that replaces one not yet received marks it replaced ahead.
  Equal values merge silently. The things decided are small, so changes to different parts of a file do not
  conflict: a file's date, a file's retention, one tag on one file, a policy, a rule (directory and pattern), a
  content, a set member, an excluded directory, an index's writer. **An open conflict never makes anything deletable
  sooner**: the longer retention or policy value applies, a tag stays, a directory stays excluded. A conflict is
  counted by `filo report`, shown with both sides (device, time) by `filo show`, and marked in the combined CSV's
  `conflict` column (blank, or the field in conflict); any new decision about that thing, from any command on any
  device, resolves it. Times are shown, never used to decide.
- **Timing.** Every run imports and applies the journals before it changes anything, so a new decision always
  lists what it replaces; it reads only what is new (an offset and the last line's hash; a mismatch means the
  journal is read and checked again from the start). A Filo started at boot may run before the sync service has
  caught up; everything then fails safe: decisions wait for their files, files wait as "new, needs a date". A line
  saying what a file's content is becomes an **expectation**: when that content is found, the decision applies and
  the content counts as arrived, never as an edit; a file with an open expectation and other content (half
  written, the old version) is arriving, and nothing is applied or written about it. Without an expectation (the
  file arrived before its line, or an edit on this device), an edit fact is written only once the content is the
  same on two scans in a row. Nothing expires: an expectation open more than 7 days is listed by `filo report` as
  waiting, usually a sync problem (a rule excluding the file, a stopped service). A trusted device not seen for 30
  days is warned about by every command that imports; in a terminal Filo asks whether to retire it, saying what
  that means; no, or no terminal, snoozes the question for 30 days on this device; `-y` does not answer it.
  `filo delete` in a synced directory shows each retention's origin and when the other devices were last seen.
  Accepted risks: a delete made on a decision another device has changed but not yet synced; a delete on one
  device racing an edit on another (the sync service's choice).
- **Identity cases.** After a reinstall the device is new: it joins (offered to retire devices not seen for a
  while, such as its old self) and imports everything; only decisions about synced files come back (the rest
  needs a snapshot, 9d). A register restored from a snapshot or backup finds its own journal ahead of it, as does
  one copied to a second machine, and Filo cannot tell which: in a terminal it asks (restored: take the lines in
  and go on; copied: this device needs a new ID); without one, nothing is written to that synced directory and it
  is reported. A fork (two different lines with one number) is always a copy: it stops and reports. `filo device
  new` gives a copied register a new ID and key. A device joining later reads every journal from the start.
  **Seeding**: marking writes every current decision under the directory into the journal; joining with decisions
  already made imports first, then writes only those that differ (conflicts, counted in the join's summary).
- **Losing a journal.** Every device keeps every line it has accepted, its own and the others', in its register,
  byte for byte, so the register and the journals back each other up. Its own journal missing, cut short or
  altered (a line it did not write) is rewritten from the register, the same bytes and numbers. Another device's
  with a bad signature or a broken chain stops at the last good line, reported, nothing after it applied; a lost
  journal of a device that is gone is written back from the register's copy by `filo devices restore`, its
  signatures still valid. A lost status file is written again on the next run.
- **Files and format.** `.filo/` holds exactly `mark` (the synced directory's ID, the format version, who marked
  it, signed), `journal.<device-id>` and `status.<device-id>` (last seen, and the highest line applied from each
  journal; written whole, at most hourly). Anything else there is reported, never read (a conflict copy of a
  journal by name). A journal line is one JSON object, a tab, and the base64 ed25519 signature of exactly those
  bytes (no canonical JSON needed; a tab inside a JSON string is always `\t`). Every line has `v` (the format
  version), `dev`, `seq`, `prev` (the SHA-256 of the previous whole line), `time`, `kind`, then what it is about,
  `replaces` and the value. Line 1 is the device itself (ID, public key, name, created); other kinds besides
  decisions and facts are `name`, `trust` and `retire`. A device's key never changes; a new key is a new device.
  Reading checks a line is at most 64 KiB, its paths relative, `/`-separated, with no `..`, empty or absolute
  parts, valid UTF-8, no control characters, at most 4096 bytes, and its values within the limits the commands
  set. A newer format version stops that journal there ("made by a newer Filo"), as a newer database is refused;
  an unknown kind in a known version is counted and skipped. Writing: the lines are stored in the register in the
  transaction of the decisions they record, then appended to the journal and flushed; a torn append is repaired by
  the next run. About 200 bytes a line, so 100,000 decisions is about 20 MB. No new dependency: ed25519, SHA-256
  and base64 are in Go's standard library.
- **Silent failure.** `filo report` gains a Synced part: marks found but not joined, devices not trusted, journals
  missing (a sync rule that skips hidden directories shows up whatever the service), conflicts, pending decisions,
  waiting files, devices not seen for 30 days.
- **Commands.** `filo device` (this device's ID, name and fingerprint), `filo device name NAME`, `filo device new`.
  `filo synced` (the synced directories and marks found), `filo synced add DIR` (marks DIR, or joins it with the
  trust prompt, or confirms a moved one), `filo synced remove DIR` (this device leaves; files and journals
  untouched), `filo synced ignore DIR`. `filo devices [DIR]` (each device: name, short ID, fingerprint, trusted,
  vouched for, retired or not trusted, last seen, lines, whether it has applied this device's latest), `filo
  devices trust|retire|restore DEVICE` (a name or the start of an ID, refused if ambiguous; each shows what it will
  do and asks, or `-y`). Not `filo sync`: Filo never copies files. Bash completion covers them and device names.
- **Where Filo writes.** One more exception to the hard rule: `.filo/` at the top of a synced directory, Filo's own
  files only, a device writing only its own journal and status file (and the mark when it is lost), another
  device's journal only through `filo devices restore`, all through one package with a guard test.
- **Slices**, on one branch: 1 the device identity; 2 the journal package (lines, signing, the chain, limits,
  reading, appending, repair, the mark and status files); 3 marking, detection, joining and trust (`filo synced`,
  `filo devices`, the status file, the report's Synced part); 4 file decisions (shared file IDs, dates,
  retentions, policies on files, tags; importing first; matching; pending and expectations; conflicts); 5 the other
  decisions (policies, rules and the question about rules above, duplicates decisions, a tag index's writer); 6
  facts (identity, renames and moves by update, deletes by Filo; delete's origins; the not-seen warning); 7
  seeding, recovery (the own journal rewritten, restored or copied, forks) and completion. Tested with two or three
  registers and trees and a fake sync service copying files and `.filo/` between them (half lines, any order,
  delays, conflict copies, forged, replayed and truncated journals, reinstall, restore, copy).
- **Noted, from earlier items.** A move by the `directory` cell out of a synced directory into an unsynced part of
  its root is a delete on the other devices, so the update's summary notes it (42). Marking a synced directory
  could offer a tag index at its top, the one every device opens (44).
- **Later, not in this item.** Compaction: a device dropping its own lines that a later line of its own replaces;
  every device keeps every line, so it needs its own signed "compacted to line N" line. Handing a synced tag index
  to another device beyond the one command.

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
A report section, or `filo find --sort size`, showing the largest directories and files and how much of it is past
retention or in duplicates, so the person knows which directory to date first.

### 16. Undoing an update batch (V5, C6)
Undo a retention CSV applied by mistake. Every batch is already recorded (`review_batches`, `reviews`), so most of
what it needs is there.

### 17. Suggested rules (V5, C5)
Point out directories where most files already share one policy and offer the `filo rules add` that would cover the
rest. A suggestion only, never applied on its own.

### 18b. Project notes and packages (V5, C5)
Pull a project's files together. By the direction above, Filo writes the list and a standard tool does the copying
(`filo find --tag "project:trip" -0 | tar --null -T - -czf trip.tgz`), or Filo writes a copy script (21). Filo itself
never copies files into the scanned trees: the copies would come back as new files and new duplicate groups.
Project notes (proposed 2026-11-19): Filo could also write one markdown note per project, with a link to each
file, into the Filo directory (20), never into a scanned tree, where each regeneration would show as a changed
file needing a decision. The output is deterministic (sorted, no timestamps, written only when it changed) so two
devices never make conflict copies. A relative link works on a device only if the note and its targets are in the
same synced directory. It is Filo's first write beside the register, so it comes after the delete work. The tag
index pages (built 2026-11-22, `design.md`, Tag index pages) may make these notes unneeded: a project is a tag, and
its tag page lists its files; to settle when 18b is picked.

### 20. The Filo directory (V6, C4)
One directory per device, chosen by the person (for example `~/Sync/Filo`), shared across devices by any sync service,
which Filo is allowed to write into. It carries snapshots (9d) and project notes (18b). The decision journals (9) moved
out on 2026-11-19: they live in `.filo/` of each synced directory. Filo never scans it, even inside a root. Each
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
same in `filo report` was considered as a simpler alternative. The build version would be stamped by `make build`
with `-ldflags "-X main.version=$(VERSION)"`, `VERSION` from `git describe --tags --always --dirty` (`dev` when that
fails), into a `var version = "dev"` in `cmd/filo`, so a plain `go build` still gives `dev`.

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
Several items lean on regular scans: 10, 27a, 9.

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
history, events, review and export rows, and its tags. Snapshots hold copies for up to 31 days, and the
copies kept before a schema upgrade are never rotated. So a bill deleted because its seven years were up is still
named in the register, which outlives the retention that deleted it.
- What the history is used for: the report's "deleted by filo" counts and sizes, the "same content as a file
  deleted on …" hint, and (9) telling another device that a missing file was deleted by Filo. None of these needs
  names or paths for long.
- Idea: after a grace period (long enough for every device to sync the delete, for example 90 days), forget a
  deleted file entirely (its rows, paths, events, tags, review and export rows), keeping only counts and sizes for
  the report.
- Open: the period, and whether it is a setting; what happens to snapshots and the pre-upgrade copies; whether the
  register's `freelist` needs a `VACUUM` so forgotten names are really gone from the file; how it fits 9's delete
  records, which other devices read.

### 43. Restoring a register snapshot (V5, C3)
Agreed as wanted on 2026-11-27, design not started; promoted from the deferred improvements, where it was one line.
Restoring is by hand today (see `design.md`, Register snapshots, "Restoring"): with no filo running, move `filo.db`
and its `-wal` and `-shm` files aside and copy a snapshot in, where forgetting the `-wal` file can let SQLite apply
newer changes to the old copy. Worth more now that one update can clear retentions, remove tags, and rename and move
files: the snapshots are the safety net for a bad CSV, so going back should be one command.
- A first idea, not decided: `filo snapshots` lists them; a restore takes the lock, checks the snapshot opens as a
  register, shows what it goes back to, asks, keeps the current register as a snapshot of its own (so a restore can
  be undone), and swaps the files in without a moment with no register.
- **To decide first:** a snapshot restores the register, never the files. A file Filo deleted, renamed or moved
  after the snapshot stays so on disk; the next scan pairs a rename or move by its hash, but a file deleted since
  comes back in the register only to be reported missing, not "deleted by filo". The command should say so before
  it asks.

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

### Later a. `filo find`, the query view (V6, C3)
Viewing the register: `filo find [DIR] [--name GLOB] [--larger SIZE] [--smaller SIZE] [--modified-before DATE]
[--modified-after DATE] [--status S] [--empty] [--sort size|modified|keep_until]`, cut at N rows in a terminal with
"… N more", all rows when piped. It filters on what the register holds: size, mtime, `file_date`, `keep_until`,
status, and first seen (`first_seen_scan`, the register's own date, which a copy or restore cannot reset). Not on
creation time (many filesystems do not record it, and a copy, restore or sync resets it) or access time (not stored;
`relatime` and `noatime` mounts make it unreliable, and Filo reads with `O_NOATIME` so as not to disturb it). Empty
files are shown through it (`--empty`); until then a count line in `filo report` would keep them visible. `filo
find` with `--name` is built (see `design.md`, Finding files); this adds the other filters. Also later (proposed 2026-11-21): `--include-missing` and `--include-deleted`, since `filo find` lists
only files present now.

### Later c. A UI in place of the CSV (V8, C10)
Including viewing and editing the register in place. Everything hangs off the stable file ID, so none of it needs the
register to change shape. Open: the technology.

### Later d. Notes and tasks (V5, C9)
Notes on a file; tasks, some with files and some without; possibly a productivity system built on the register (a
recurring check on a file, such as an annual look at a seven-year record, is a task). A change of scope from "Filo owns
lifecycle" that needs its own discussion first; by the direction above it may be a separate tool reading the register.
Linking files to a project is a tag (`design.md`, Tags). (Holds were dropped on 2026-11-12: a file that must be kept gets a later
retention or `forever`.)
Proposed 2026-11-22, not designed: tasks and events could live on a CalDAV server (the person's calendar and task
lists), each with a link to its files, rather than in Filo.

### Later e. A watcher (V3, C8)
Seeing changes as they happen (`inotify`/`fanotify`) instead of on the next scan. Rejected (below, "A daemon")
rejects a daemon unless near-real-time change detection is needed; a scheduled scan does the job, and inotify needs
one watch per directory and drops events when its queue fills.

## Deferred improvements

Smaller improvements to what exists, noted while it was built. Not prioritised.

- **Register:** skipping a snapshot when a scan changed
  nothing, if registers grow large (every run that writes copies the whole register); an atomic no-replace rename
  for snapshots (needs `golang.org/x/sys`), so a program not taking Filo's lock cannot slip a file in under the
  snapshot's name.
- **Tags:** rules that tag by name pattern (`*British Gas*` under `received` gives `sender:British Gas`), once tags
  have been used by hand (left from 18a, 2026-11-19); possibly, not agreed, tags as an input to rules that set a
  policy.
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
- **Device tracking in the database.** Unneeded while the database is per device and never synced. Revisited on
  2026-11-28 for sharing decisions (9): each register gets a device ID, while the register itself stays per device.
- **One database per root** (the first Go design, with the database in `<root>/.filo`). Several roots on one device
  meant several registers to manage; one database with a `roots` table replaced it on 2026-11-11.
- **Themed command and project names** (the earlier "dirge" idea, funeral-style subcommands). Plain names win.
- **Building search: Filo does not index file contents** (narrowed 2026-11-20). Existing indexers and document
  managers (Paperless-ngx for received documents, desktop indexers) already search inside files. Finding files by
  name and tag over what the register holds (`filo find --name` and `--tag`) is a lookup, not search, and is the goal (see Direction).
