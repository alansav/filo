package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// askReplace decides whether an export may write csvPath (docs/design.md, Lifespan, "An existing FILE is replaced
// only after asking"). A path that does not exist: go ahead, with asked nil. A regular file: go ahead only if the
// person says yes at a terminal, with asked the file as it was when they said it; no, or Enter, prints "Nothing
// exported." and does not go ahead. Without a terminal, for anything that is not a regular file (a directory, a
// symlink, a device), and for the register at dbPath or one of its companion files, an error. -y does not answer
// this question: export has no -y.
func (e *env) askReplace(ctx context.Context, csvPath, dbPath string) (asked fs.FileInfo, ok bool, err error) {
	fi, err := os.Lstat(csvPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("check %s: %w", csvPath, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file (a folder, link or device); choose another name",
			csvPath)
	}
	if registerFile(fi, csvPath, dbPath) {
		return nil, false, fmt.Errorf("%s is filo's register or one of its files; choose another name", csvPath)
	}
	if !e.canPrompt() {
		return nil, false, fmt.Errorf("%s already exists; run filo export in a terminal to be asked before it is "+
			"replaced, or choose another name", csvPath)
	}
	yes, err := e.askOne(ctx, "Replace "+e.view.Tilde(csvPath)+"?", nil)
	if err != nil {
		return nil, false, interruptedUpdate(err) // "nothing was recorded": true of an export stopped here too
	}
	if !yes {
		e.view.Next("Nothing exported.")
		return nil, false, nil
	}
	return fi, true, nil
}

// registerFile reports whether fi, found at csvPath, is the register at dbPath, one of its companion files (SQLite's
// journal, WAL and shared memory, the lock) or a file in its snapshot directory. Compared as files, not names, so a
// symlinked folder on either path is no way round it.
func registerFile(fi fs.FileInfo, csvPath, dbPath string) bool {
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm", dbPath + ".lock"} {
		if other, err := os.Stat(p); err == nil && os.SameFile(fi, other) {
			return true
		}
	}
	dir, err := os.Stat(filepath.Dir(csvPath))
	snaps, err2 := os.Stat(dbPath + ".snapshots")
	return err == nil && err2 == nil && os.SameFile(dir, snaps)
}

// writeExport writes csvPath and records the export: record stores it and calls the function it is given with the
// export's ID, which writes the CSV through writeCSV. With asked nil, csvPath is created and never replaces a file;
// otherwise the CSV is written to a temporary file next to it and, once the export is recorded, renamed over it if
// csvPath is still the file asked describes, so a failure or a crash never leaves half a CSV. A failed write leaves
// the old file as it was (or removes the new one) and records nothing.
func writeExport(csvPath string, asked fs.FileInfo, record func(write func(int64) error) (int64, error),
	writeCSV func(w io.Writer, id int64) error) (int64, error) {
	var file *os.File
	var err error
	if asked == nil {
		// O_EXCL: a file that appeared since the check is never overwritten, whatever it is.
		file, err = os.OpenFile(csvPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return 0, fmt.Errorf("%s was created by something else while filo was exporting; nothing exported",
				csvPath)
		}
	} else {
		// CreateTemp makes the file with mode 0600, in the same folder so the rename stays on one filesystem. A short
		// fixed name: one built from csvPath's could pass the longest name a folder allows.
		file, err = os.CreateTemp(filepath.Dir(csvPath), ".filo-export-*.tmp")
	}
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", csvPath, err)
	}
	written := file.Name() // csvPath itself, or the temporary file
	id, err := record(func(id int64) error {
		bw := bufio.NewWriter(file)
		if err := writeCSV(bw, id); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if asked != nil {
			// Checked here, before the export is recorded, and again just before the rename below.
			return stillAsked(csvPath, asked)
		}
		return nil
	})
	if err == nil && asked != nil {
		// The export is recorded: only now is the old file replaced. If this fails, the export is recorded with no
		// CSV, which costs an export number and nothing else.
		if err = stillAsked(csvPath, asked); err == nil {
			err = os.Rename(written, csvPath)
		}
	}
	if err != nil {
		file.Close()
		os.Remove(written) // created by this command a moment ago, and incomplete or not wanted
		return 0, fmt.Errorf("export to %s: %w", csvPath, err)
	}
	return id, nil
}

// stillAsked checks that csvPath is still the regular file the person was asked about, unchanged: the same file, size
// and modification time (an editor saving into the same file keeps the first).
func stillAsked(csvPath string, asked fs.FileInfo) error {
	now, err := os.Lstat(csvPath)
	if err != nil || !now.Mode().IsRegular() || !os.SameFile(asked, now) || now.Size() != asked.Size() ||
		!now.ModTime().Equal(asked.ModTime()) {
		return fmt.Errorf("%s changed after you were asked about it; it was left as it is", csvPath)
	}
	return nil
}
