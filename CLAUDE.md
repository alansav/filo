# Filo

Go CLI that keeps a SQLite register of files, tracks where they live, and drives a review-and-dispose workflow so
files do not pile up forever. Three documents, kept apart:

- `docs/design.md`: Filo as it is on `main`, and why. Nothing that is not built. Read it before changing behaviour.
- `docs/roadmap.md`: everything not built yet: the priority, the items (with their numbers), ideas, deferred
  improvements, and ideas rejected (with why). A design is worked out here while it is discussed.
- `docs/history.md`: how Filo was built: the build log, replaced decisions, dated notes.

When work on a roadmap item starts, the first commit of its branch moves its design from `roadmap.md` into
`design.md`, in its own commit (see "After writing code"); a design decision that changes during the work is changed
there on the same branch. When the item is complete, nothing of it is left in `roadmap.md`, and its build notes (dates,
slices, review findings, test lists) go to `history.md`, never into `design.md`. So `roadmap.md` only lists work not
done, and `design.md` only what exists.

## Current state

Everything `docs/design.md` describes is built (schema 16 on branch `sync`: item 9's device identity, synced directories and shared decisions; schema 13
on `main`): scan,
register and report; retention through the CSV (`filo export`, `filo update`); `filo delete [DIR]
[-y]`, which deletes a file only after it is re-read and found unchanged; register snapshots; duplicates (`filo delete
FILE.csv`, `filo duplicates`); the terminal output (every command prints through `internal/view`); named retention
policies (`filo policies`); directory rules (`filo rules`); showing and setting a file's details (`filo show`, `filo
set`, `filo get`); finding files (`filo find`); tags (`filo tag`, `untag`, `tags`, `find --tag`); tag index pages
(`filo indexes`), whose `NAME.html` is a find page with a script (tested with Node from `go test`); the combined CSV (`filo export [DIR] -o FILE`, the one CSV for retentions and tags since the
retention and tags exports were removed), renaming by its `name` cell and moving by its `directory` cell (never a
hidden file; a move leaves the directories it empties); bash completion (`filo completion bash`, tested with bash
from `go test`) (all on branch `improvements` until merged). What is next is in `docs/roadmap.md`, Priority
(2026-11-28: item 9, sharing decisions between devices, which merged 9a, 9b and 9c; being built on branch `sync`).
**`internal/dispose` is the only code allowed to change the user's files**: deleting them only from `filo delete`,
renaming or moving them within their root only from `filo update` (a changed `name` or `directory` cell in the
combined CSV, making the directories a move needs; `TestOnlyDisposeChangesUserFiles` guards it); everything else stays
read-only on them, and nothing overwrites an existing file without asking (only an `-o` output file, and a tag index's
own pages, see the hard rules). Stay inside the current piece of work.

## Hard rules

- Filo must not modify, move, rename, delete or overwrite any file outside its own data directory (or the `--db` path
  given, its companion files and its `<db>.snapshots/` directory) except through `filo delete`; renaming a file, or
  moving it within its root (making the directories it needs), through `filo update`, only after the update has listed
  it and been confirmed (or `-y`), never over an existing file, and never a file that is no longer the one exported
  (see `docs/design.md`, Tags, "Renaming a file by its `name` cell"); and the output file named with `-o`: if that
  file exists, Filo asks before replacing it (no terminal: refused; `-y` does not answer it; see `docs/design.md`,
  Lifespan), and the files of a tag index (`NAME.html` and `.NAME/` in a directory given to `filo indexes add`), which
  Filo creates only where nothing exists, replaces or removes only while still exactly what it wrote (its hash
  matches; anything else is left alone and reported), and removes as a whole only through `filo indexes remove`, which
  shows what it will remove and asks (or `-y`). `internal/tagindex` is the only code that writes or removes them
  (`TestOnlyWriteTouchesTheDisk` guards it); they are Filo's files, not the user's (see `docs/design.md`, Tag index
  pages); and the files of a synced directory's `.filo/` (`mark`, `journal.<device-id>`, `status.<device-id>`), also
  Filo's own: a device writes only its own journal and status file, the mark only when marking or when it is lost,
  and another device's journal only through `filo devices restore` (shows and asks, or `-y`), and never deletes one;
  `internal/journal` is the only code that writes them (`TestOnlyJournalWritesSyncedFiles` guards it; see
  `docs/design.md`, Sharing decisions between devices). A journal line never deletes, renames or moves a file.
  Anything destructive shows what it will do and asks first (or needs `-y`).
- Treat file names and the register as untrusted input: delete only inside a root (`internal/dispose` checks), and
  print through `internal/view` (its Renderer escapes every text it is given with `view.Escape`, then adds only its
  own colour codes; the two exceptions are `filo find -0`'s raw path list, written by the Renderer and refused when
  stdout is a terminal, and `filo __complete`'s candidates for bash, also written raw by the Renderer, never to a
  terminal and never one holding a control character); stderr only through the CLI's `errorLine` (each line escaped, `filo: ` before it), and the
  progress line. Never write text straight to `os.Stdout` or `os.Stderr` (`TestOutputGoesThroughTheRenderer` guards
  it). Write each line of output with one call (a newline inside a write is escaped, since only a file name puts one
  there).
- In `internal/store`, every write goes through `s.commit(tx)` or `s.exec`, so a run that changes the register gets
  its snapshot (`TestWritesGoThroughTheHelpers` guards it). The one exception is `s.commitRecord(tx)`, used only by a
  scan that found nothing (its record is all it writes, so it takes no snapshot; see `docs/design.md`, Register
  snapshots).
- Fail safe: a file with missing or ambiguous data is "new, needs a date", never "eligible for deletion". Reaching a
  keep-until date makes a file a candidate that the person confirms, never a trigger. A file is deleted only if it
  can be read and is still exactly the content its retention was set on; one that cannot be read is never deleted.
- Never guess. Duplicates and ambiguous moves are reported, not resolved. A directory that cannot be read must never
  cause its files to be marked missing.
- During development, never run Filo against a real home directory or any path outside the repo or a throwaway test
  tree. Always pass `--db`, and only add roots inside the repo or a throwaway test tree.
- Say clearly when code is untested. Check current library versions and docs instead of relying on memory.
- Do not add a dependency without asking first.
- Commit only as described in "After writing code" below. Never `git push`, amend, rebase, reset or force anything,
  and do not set a git identity. The user reviews the commits and pushes from the host.
- Before changing any file in the repo, check the branch with `git branch --show-current`. If it is `main`, stop and
  ask the user to check out a branch; do not create or switch branches yourself.
- Command names stay plain and easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete`,
  `duplicates`, `policies`, `rules`, `revert`. No themed names.

## Commands

You run inside the dev container, so run Go commands directly. (From the host they need the `scripts/dev.sh run`
prefix.) The image is built with Claude Code by default (`scripts/dev.sh build`; `--no-claude` leaves it out, and
`scripts/dev.sh claude` then refuses with how to rebuild).

- Build: `make build` (a static `bin/filo`; `scripts/install.sh` builds it, copies it to `~/.local/bin` and adds the
  completion line to `~/.bashrc`; it is for the user to run on the host, not you)
- Test: `go test ./...` (it also runs the tag index's find page script tests with `node --test`; Node is in the dev
  container, and a container built before Node was added needs a rebuild)
- Vet and lint: `go vet ./...` and `staticcheck ./...`
- Format: `gofmt -l -w .`
- Throwaway test tree: `scripts/testtree.sh init DIR` then `scripts/testtree.sh mutate DIR`, with DIR under your own
  home directory (for example `~/FiloTest`) or a temp directory. `mutate` makes a directory and a file unreadable;
  `scripts/testtree.sh unlock DIR` undoes that, and is needed before `rm -rf DIR`
- Example dev run, always with a throwaway database and tree, in a private directory (Filo refuses a database in a
  directory others can write to, such as `/tmp`): `T=$(mktemp -d)`, then `bin/filo --db $T/f.db roots add DIR` (adds
  and scans), `bin/filo --db $T/f.db scan`, `bin/filo --db $T/f.db report`, then
  `bin/filo --db $T/f.db export DIR/notes -o $T/l.csv`, fill in `retention`, `update $T/l.csv` and `delete`
  (export refuses an existing file, so remove `$T/l.csv` between runs).
  `delete` really deletes: run it only with a throwaway `--db` whose roots are a throwaway tree

Run `go vet` and the tests before saying a change is done.

## After writing code

One roadmap item per branch (and so per PR), and each PR is a complete, working feature, never part of one: a
large item is built in slices on the one branch, and the PR is opened only when the last slice is done. The user
reads every commit on its own, so a piece of work is committed as a short series of small commits on the current
branch, never on `main`:

1. Commit as you go, lower layers first, each commit with its own tests: the design moved from `docs/roadmap.md` into
   `docs/design.md` alone first, then the store and schema, then the internal package(s), then the `internal/view`
   output (before an internal package that renders through it), then the `cmd/filo` wiring and end-to-end tests.
   Before each commit, `gofmt`, `go vet`, `staticcheck` and `go
   test ./...` must all pass, so every commit builds and is green on its own. Each commit is one atomic unit that does
   exactly what its message says; its size comes second (do not split or merge commits to reach a number of lines). Each
   message says what the commit adds and, if part of the work is still to come, what (for example "not called by any
   command yet").
2. When the series is done (for an item built in slices, when each slice is done), run one code review over it (the
   `code-review` skill, level `medium`, on that series' commits: `main..HEAD`, or from the previous slice's last
   commit), fix what it finds, run the checks again, and commit the fixes as separate commits, one "Review fixes for
   <subject>" commit per commit they belong to, so each can be read next to what it fixes.
3. When the item is complete, after the review fixes of its last slice, one last commit removes what is left of it from
   `docs/roadmap.md` and adds its build notes (slices, review findings, tests) to `docs/history.md`; it is not
   reviewed. Stop and report the commits, each with a one-line summary; after a slice, wait for the user before the
   next one. One review round only: do not review the fixes again.

Stop and ask instead of committing if a check fails and the fix is not obvious, if a finding needs a design decision
or a change to `docs/design.md`, if a finding looks wrong or out of scope, or if anything else is unclear.

## Layout

- `cmd/filo/`: the command-line entry point, kept thin.
- `internal/`: the logic, in small packages (walking, hashing, the store, reconcile, report, retention CSV,
  duplicates CSV, sheet (what spreadsheets do to a CSV), dispose, view (the terminal output: one Renderer and its
  components; commands hand it data and never format output themselves)). Keep OS-specific code (hidden files,
  inodes, symlinks, path separators, the terminal's width) behind small functions.
- `scripts/`: helper scripts (`dev.sh`, `testtree.sh`).
- `.devcontainer/Containerfile`: the dev container. `docs/design.md`: the design as built; `docs/roadmap.md`: what
  is planned; `docs/history.md`: how it was built.

## Conventions

- Idiomatic Go: wrap errors with `%w`, return errors instead of panicking, pass `context.Context` where work can be
  cancelled. User-facing errors are one clear line.
- Say "directory", never "folder" (output, help, docs, comments); only Syncthing's own term stays "a Syncthing
  folder" (see `docs/design.md`, Terminal output).
- Tests are table-driven and use `t.TempDir()`. No test may touch the real home directory.
- One database per device holds all roots. It is created automatically in the user data directory
  (`~/.local/share/filo/filo.db` on Linux); `--db` and `FILO_DB` override it. Roots are added explicitly. One writer at a
  time, and each scan is applied in a single transaction. Every run that writes leaves a snapshot in
  `<db>.snapshots/` (rotated; see `docs/design.md`, Register snapshots), so a throwaway `--db` gets one too.
- Keep the schema changes small and versioned (`meta.schema_version`).
