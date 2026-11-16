# Filo

Go CLI that keeps a SQLite register of files, tracks where they live, and drives a review-and-dispose workflow so
files do not pile up forever. The full design is in `docs/design.md`. Read it before changing behaviour, and update it
on the same branch when a design decision changes (in its own commit, first in the series; see "After writing code").

## Current state

Iterations 1 (scan, register, report), 2 (lifespan: retention through the retention CSV, `filo export retention` then
`filo update`) and 3 (delete: `filo delete [DIR] [-y]` lists the files past their retention, asks, and deletes the
chosen ones, each only after it is re-read and found unchanged) are done, as are register snapshots. The duplicates
redesign (`docs/design.md`, Duplicates: `filo delete FILE.csv`, records, always, `filo duplicates`; schema 8) is done
and merged. The terminal output is done (every command prints through `internal/view`, see "Terminal output"). Named
retention policies (`docs/design.md`, Lifespan, Named retention policies; schema 9; `filo policies`) are done and
merged. Replacing an existing `-o` file after asking (section 5 item 11) and every export scanning first, `--no-scan`
to skip it (item 12), are done. Next in `docs/design.md` section 5: directory rules (design first: name patterns and
empty directories are in Open questions).
**`internal/dispose` is the only code allowed to delete the user's files**, and only from `filo delete`; everything
else stays read-only on them, and nothing overwrites an existing file without asking (only an `-o` output file,
see the hard rules). Stay inside the current piece of work.

## Hard rules

- Filo must not modify, move, rename, delete or overwrite any file outside its own data directory (or the `--db` path
  given, its companion files and its `<db>.snapshots/` directory) except through `filo delete`, or the later work
  that calls for it, and the output file named with `-o`: if that file exists, Filo asks before replacing it (no
  terminal: refused; `-y` does not answer it; see `docs/design.md`, Lifespan). Anything destructive shows what it
  will do and asks first (or needs `-y`).
- Treat file names and the register as untrusted input: delete only inside a root (`internal/dispose` checks), and
  print through `internal/view` (its Renderer escapes every text it is given with `view.Escape`, then adds only its
  own colour codes); stderr only through the CLI's `errorLine` (each line escaped, `filo: ` before it), and the
  progress line. Never write text straight to `os.Stdout` or `os.Stderr` (`TestOutputGoesThroughTheRenderer` guards
  it). Write each line of output with one call (a newline inside a write is escaped, since only a file name puts one
  there).
- In `internal/store`, every write goes through `s.commit(tx)` or `s.exec`, so a run that changes the register gets
  its snapshot (`TestWritesGoThroughTheHelpers` guards it).
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
  `duplicates`, `policies`, `revert`. No themed names.

## Commands

You run inside the dev container, so run Go commands directly. (From the host they need the `scripts/dev.sh run`
prefix.)

- Build: `go build -o bin/filo ./cmd/filo`
- Test: `go test ./...`
- Vet and lint: `go vet ./...` and `staticcheck ./...`
- Format: `gofmt -l -w .`
- Throwaway test tree: `scripts/testtree.sh init DIR` then `scripts/testtree.sh mutate DIR`, with DIR under your own
  home directory (for example `~/FiloTest`) or a temp directory. `mutate` makes a directory and a file unreadable;
  `scripts/testtree.sh unlock DIR` undoes that, and is needed before `rm -rf DIR`
- Example dev run, always with a throwaway database and tree:
  `bin/filo --db /tmp/f.db roots add DIR` (adds and scans), `bin/filo --db /tmp/f.db scan`,
  `bin/filo --db /tmp/f.db report`, then `bin/filo --db /tmp/f.db export retention DIR/notes -o /tmp/l.csv`, fill in
  `retention`, `update /tmp/l.csv` and `delete` (export refuses an existing file, so remove `/tmp/l.csv` between runs).
  `delete` really deletes: run it only with a throwaway `--db` whose roots are a throwaway tree

Run `go vet` and the tests before saying a change is done.

## After writing code

One section 5 item per branch (and so per PR), and each PR is a complete, working feature, never part of one: a
large item is built in slices on the one branch, and the PR is opened only when the last slice is done. The user
reads every commit on its own, so a piece of work is committed as a short series of small commits on the current
branch, never on `main`:

1. Commit as you go, lower layers first, each commit with its own tests: the `docs/design.md` change alone first,
   then the store and schema, then the internal package(s), then the `internal/view` output, then the `cmd/filo`
   wiring and end-to-end tests. Before each commit, `gofmt`, `go vet`, `staticcheck` and `go test ./...` must all
   pass, so every commit builds and is green on its own. Aim for about 100 changed lines of non-test code per
   commit; a small change stays one commit. Each message says what the commit adds and, if part of the work is
   still to come, what (for example "not called by any command yet").
2. When the series is done, run one code review over all of it (the `code-review` skill, level `medium`, on
   `main..HEAD`), fix what it finds, run the checks again, and commit the fixes as separate commits, one
   "Review fixes for <subject>" commit per commit they belong to, so each can be read next to what it fixes.
3. Stop and report the commits, each with a one-line summary. One review round only: do not review the fixes again.

Stop and ask instead of committing if a check fails and the fix is not obvious, if a finding needs a design decision
or a change to `docs/design.md`, if a finding looks wrong or out of scope, or if anything else is unclear.

## Layout

- `cmd/filo/`: the command-line entry point, kept thin.
- `internal/`: the logic, in small packages (walking, hashing, the store, reconcile, report, retention CSV,
  duplicates CSV, sheet (what spreadsheets do to a CSV), dispose, view (the terminal output: one Renderer and its
  components; commands hand it data and never format output themselves)). Keep OS-specific code (hidden files,
  inodes, symlinks, path separators, the terminal's width) behind small functions.
- `scripts/`: helper scripts (`dev.sh`, `testtree.sh`).
- `.devcontainer/Containerfile`: the dev container. `docs/design.md`: the design.

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
