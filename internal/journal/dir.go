package journal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/fsid"
)

// FiloDir is the directory at the top of a synced directory that holds Filo's files. Scans never walk it.
const FiloDir = ".filo"

// MarkName is the mark: the synced directory's ID, signed by the device that marked it.
const MarkName = "mark"

// JournalName is the name of the journal of device dev.
func JournalName(dev string) string { return "journal." + dev }

// StatusName is the name of the status file of device dev.
func StatusName(dev string) string { return "status." + dev }

// MaxFile is the largest file in .filo/ that is read: far more than any journal of decisions needs (100,000 lines
// are about 20 MB), so a file that size is not a journal.
const MaxFile = 1 << 30

// ErrNoFiloDir is a synced directory with no .filo/.
var ErrNoFiloDir = errors.New("no " + FiloDir + " directory")

// Dir is the .filo/ of one synced directory, opened so that nothing done through it leaves it: every file is
// reached through the directory opened (os.Root), never by path, and nothing is followed that is not a real
// directory or a regular file.
type Dir struct {
	path string // .filo/'s absolute path, for messages
	root *os.Root
}

// Open opens the .filo/ at the top of the synced directory top (ErrNoFiloDir if there is none).
func Open(top string) (*Dir, error) { return open(top, false) }

// Make opens the .filo/ at the top of top, making it (mode 0700) if there is none.
func Make(top string) (*Dir, error) { return open(top, true) }

func open(top string, mk bool) (*Dir, error) {
	t, err := os.OpenRoot(top)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	path := filepath.Join(top, FiloDir)
	fi, err := t.Lstat(FiloDir)
	if errors.Is(err, fs.ErrNotExist) && mk {
		if err := t.Mkdir(FiloDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("make %s: %w", path, err)
		}
		fi, err = t.Lstat(FiloDir)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", top, ErrNoFiloDir)
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}
	r, err := t.OpenRoot(FiloDir)
	if err != nil {
		return nil, err
	}
	if opened, err := r.Stat("."); err != nil || !os.SameFile(fi, opened) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	return &Dir{path: path, root: r}, nil
}

// Close closes the directory.
func (d *Dir) Close() error { return d.root.Close() }

// Path is .filo/'s absolute path.
func (d *Dir) Path() string { return d.path }

// EntryKind is what a name in .filo/ is.
type EntryKind int

const (
	Other   EntryKind = iota // not Filo's: reported, never read
	Mark                     // the mark
	Journal                  // a device's journal
	Status                   // a device's status file
)

// Entry is one name in .filo/.
type Entry struct {
	Name string
	Kind EntryKind
	Dev  string // the device a journal or status file is of
	// Note says what an Other is, when Filo can tell: "a copy of journal.<id>" for a sync service's conflict copy,
	// "not a regular file" for one of Filo's names that is a directory or a link.
	Note string
}

// List lists .filo/, each name as what it is. Filo's own temporary files (a replace in progress here, or on
// another device) are left out.
func (d *Dir) List() ([]Entry, error) {
	f, err := d.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		name := de.Name()
		if isTemp(name) {
			continue
		}
		e := classify(name)
		if e.Kind != Other && !de.Type().IsRegular() {
			e = Entry{Name: name, Kind: Other, Note: "not a regular file"}
		}
		out = append(out, e)
	}
	return out, nil
}

// classify says what name is by its name alone.
func classify(name string) Entry {
	if name == MarkName {
		return Entry{Name: name, Kind: Mark}
	}
	for _, p := range []struct {
		prefix, what string
		kind         EntryKind
	}{{"journal.", "journal", Journal}, {"status.", "status file", Status}} {
		rest, ok := strings.CutPrefix(name, p.prefix)
		if !ok {
			continue
		}
		if device.ValidID(rest) {
			return Entry{Name: name, Kind: p.kind, Dev: rest}
		}
		if len(rest) > device.IDLen && device.ValidID(rest[:device.IDLen]) {
			return Entry{Name: name, Kind: Other, Dev: rest[:device.IDLen],
				Note: "a copy of " + p.prefix + rest[:device.IDLen]}
		}
	}
	return Entry{Name: name, Kind: Other}
}

// tempPrefix starts the name of a file being written in place of another (Replace).
const tempPrefix = ".filo-"

func isTemp(name string) bool {
	return strings.HasPrefix(name, tempPrefix) && strings.HasSuffix(name, ".tmp")
}

// regular looks at name (not following a link) and returns it if it is a regular file, and, for a write, of one
// link: a hard link to another file would let a write here change that file. Where the number of links cannot be
// read (0), a write is refused.
func (d *Dir) regular(name string, write bool) (fs.FileInfo, error) {
	fi, err := d.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s/%s is not a regular file", d.path, name)
	}
	if n := fsid.Links(fi); n > 1 || (write && n != 1) {
		return nil, fmt.Errorf("%s has other names (hard links), or their number cannot be read",
			filepath.Join(d.path, name))
	}
	return fi, nil
}

// openChecked opens name with flag (without blocking) and checks it is the regular file looked at, not one swapped
// in since.
func (d *Dir) openChecked(name string, flag int) (*os.File, error) {
	fi, err := d.regular(name, flag != os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	f, err := d.root.OpenFile(name, flag|noBlock, 0)
	if err != nil {
		return nil, err
	}
	if got, err := f.Stat(); err != nil || !os.SameFile(fi, got) {
		f.Close()
		return nil, fmt.Errorf("%s/%s changed while it was opened", d.path, name)
	}
	return f, nil
}

// Read reads name from offset to its end (at most MaxFile bytes in all).
func (d *Dir) Read(name string, offset int64) ([]byte, error) {
	f, err := d.openChecked(name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > MaxFile {
		return nil, fmt.Errorf("%s/%s is %d bytes, too large to be Filo's", d.path, name, fi.Size())
	}
	if offset > fi.Size() {
		return nil, nil // shorter than where reading stopped: the caller finds the line before it gone
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, MaxFile-offset))
}

// Exists reports whether name is in .filo/ (as anything).
func (d *Dir) Exists(name string) (bool, error) {
	_, err := d.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ErrNotAt is an Append to a file that is not the size it was expected to be: a torn append left part of a line at
// its end, or it changed. Nothing is written: the caller writes the file whole again (Replace).
var ErrNotAt = errors.New("the file is not where it was left")

// Append adds lines to the end of name, each with its newline, in one write, then flushes it to the disk. name
// must exist (Create makes a journal), be a regular file of one link, and be size bytes long (ErrNotAt if not), so
// a line is never joined onto part of one.
func (d *Dir) Append(name string, size int64, lines [][]byte) error {
	if len(lines) == 0 {
		return nil
	}
	f, err := d.openChecked(name, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != size {
		f.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("%s: %w", filepath.Join(d.path, name), ErrNotAt)
	}
	if _, err := f.Write(joinLines(lines)); err != nil {
		f.Close()
		return fmt.Errorf("append to %s/%s: %w", d.path, name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("append to %s/%s: %w", d.path, name, err)
	}
	return f.Close()
}

// Create makes name, which must not exist, holding lines. A file it could not write whole is removed again, so
// a later Create can make it.
func (d *Dir) Create(name string, lines [][]byte) error {
	f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noBlock, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Join(d.path, name), err)
	}
	_, err = f.Write(joinLines(lines))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		d.root.Remove(name) // the file just made, O_EXCL: no one else's
		return fmt.Errorf("write %s: %w", filepath.Join(d.path, name), err)
	}
	return d.syncDir()
}

// Replace writes name whole, holding lines: through a temporary file in .filo/, flushed and renamed into place, so
// it is never seen half written. Whatever name was is replaced; a directory or a link there is refused.
func (d *Dir) Replace(name string, lines [][]byte) error {
	if _, err := d.root.Lstat(name); err == nil {
		if _, err := d.regular(name, true); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp, f, err := d.createTemp()
	if err != nil {
		return fmt.Errorf("write %s/%s: %w", d.path, name, err)
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			d.root.Remove(tmp)
		}
	}()
	if _, err := f.Write(joinLines(lines)); err != nil {
		return fmt.Errorf("write %s/%s: %w", d.path, name, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("write %s/%s: %w", d.path, name, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := d.root.Rename(tmp, name); err != nil {
		return fmt.Errorf("write %s/%s: %w", d.path, name, err)
	}
	done = true
	return d.syncDir()
}

func (d *Dir) createTemp() (string, *os.File, error) {
	for range 10 {
		name := tempPrefix + rand.Text()[:10] + ".tmp"
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noBlock, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, f, nil
	}
	return "", nil, fs.ErrExist
}

// syncDir flushes .filo/ itself, so a new name survives a power cut.
func (d *Dir) syncDir() error {
	f, err := d.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
