# Filo

Go CLI that keeps a SQLite register of files, tracks where they live, and drives a review-and-dispose workflow so
files do not pile up forever. The full design is in `docs/design.md`. Read it before changing behaviour, and update it
in the same commit when a design decision changes.

## Current state

Iteration 1: scan, register, report. **Read-only on the user's files.** Later iterations (review CSV, quarantine and
delete, revert, UI) are listed in `docs/design.md`. Stay inside the current iteration.

## Hard rules

- Filo must not modify, move, rename or delete any file outside its own `.filo` directory until the iteration that
  calls for it. Anything destructive defaults to dry-run.
- Fail safe: a file with missing or ambiguous data is "new, needs review", never "eligible for deletion". A
  retain-until date is a floor, not a trigger. Holds veto everything.
- Never guess. Duplicates and ambiguous moves are reported, not resolved. A directory that cannot be read must never
  cause its files to be marked missing.
- During development, never run Filo against a real home directory or any path outside the repo or a throwaway test
  tree. Always pass `--root` and, where useful, `--db`.
- Say clearly when code is untested. Check current library versions and docs instead of relying on memory.
- Do not add a dependency without asking first.
- Command names stay plain: `scan`, `report`, `review`, `restore`, `revert`. No themed names.

## Commands

Go runs in the dev container. From the host, prefix commands with `scripts/dev.sh run`. If you are already inside the
container, run them directly.

- Build: `scripts/dev.sh run go build -o bin/filo ./cmd/filo`
- Test: `scripts/dev.sh run go test ./...`
- Vet and lint: `scripts/dev.sh run go vet ./...` and `scripts/dev.sh run staticcheck ./...`
- Format: `scripts/dev.sh run gofmt -l -w .`
- Shell in the container: `scripts/dev.sh shell`
- Throwaway test tree: `scripts/testtree.sh init DIR` then `scripts/testtree.sh mutate DIR`, with DIR inside the
  container's home or a temp directory

Run `go vet` and the tests before saying a change is done.

## Layout

- `cmd/filo/`: the command-line entry point, kept thin.
- `internal/`: the logic, in small packages (walking, hashing, the store, reconcile, report). Keep OS-specific code
  (hidden files, inodes, symlinks, path separators) behind small functions.
- `scripts/`: helper scripts (`dev.sh`, `testtree.sh`).
- `.devcontainer/Containerfile`: the dev container. `docs/design.md`: the design.

## Conventions

- Idiomatic Go: wrap errors with `%w`, return errors instead of panicking, pass `context.Context` where work can be
  cancelled. User-facing errors are one clear line.
- Tests are table-driven and use `t.TempDir()`. No test may touch the real home directory.
- The database is created automatically at `<root>/.filo/filo.db`. `--root`, `--db`, `FILO_ROOT` and `FILO_DB`
  override the defaults. One writer at a time, and each scan is applied in a single transaction.
- Keep the schema changes small and versioned (`meta.schema_version`).
