# Filo

Go CLI that keeps a SQLite register of files, tracks where they live, and drives a review-and-dispose workflow so
files do not pile up forever. The full design is in `docs/design.md`. Read it before changing behaviour, and update it
in the same commit when a design decision changes.

## Current state

Iterations 1 (scan, register, report), 2 (lifespan: retention through the retention CSV, `filo export retention`
then `filo update`) and 3 (delete: `filo delete [DIR] [-y]` lists the files past their retention, asks, and deletes
the chosen ones, each only after it is re-read and found unchanged) are done, as are register snapshots. The order
of the next work is in `docs/design.md` section 5, starting with named retention policies.
**`internal/dispose` is the only code allowed to delete the user's files**, and only from `filo delete`; everything
else stays read-only on them, and nothing overwrites an existing file. Stay inside the current piece of work.

## Hard rules

- Filo must not modify, move, rename or delete any file outside its own data directory (or the `--db` path given,
  its companion files and its `<db>.snapshots/` directory) except through `filo delete`, or the later work that
  calls for it. Anything destructive shows what it will do and asks first (or needs `-y`).
- Treat file names and the register as untrusted input: delete only inside a root (`internal/dispose` checks), and
  print through the CLI's escaping writer (`safeText`), never straight to `os.Stdout`. Write each line of output with
  one call (a newline inside a write is escaped, since only a file name puts one there); filo's own multi-line text
  goes through `writeLines`.
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
- Do not run `git commit` or `git push`, and do not set a git identity. The user commits from the host.
- Command names stay plain and easy to remember: `roots`, `scan`, `report`, `export`, `update`, `delete`, `revert`.
  No themed names.

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

## Layout

- `cmd/filo/`: the command-line entry point, kept thin.
- `internal/`: the logic, in small packages (walking, hashing, the store, reconcile, report, retention CSV,
  dispose). Keep OS-specific code (hidden files, inodes, symlinks, path separators) behind small functions.
- `scripts/`: helper scripts (`dev.sh`, `testtree.sh`).
- `.devcontainer/Containerfile`: the dev container. `docs/design.md`: the design.

## Conventions

- Idiomatic Go: wrap errors with `%w`, return errors instead of panicking, pass `context.Context` where work can be
  cancelled. User-facing errors are one clear line.
- Tests are table-driven and use `t.TempDir()`. No test may touch the real home directory.
- One database per device holds all roots. It is created automatically in the user data directory
  (`~/.local/share/filo/filo.db` on Linux); `--db` and `FILO_DB` override it. Roots are added explicitly. One writer at a
  time, and each scan is applied in a single transaction. Every run that writes leaves a snapshot in
  `<db>.snapshots/` (rotated; see `docs/design.md`, Register snapshots), so a throwaway `--db` gets one too.
- Keep the schema changes small and versioned (`meta.schema_version`).
