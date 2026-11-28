package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/store"
)

// TestExportGolden: filo export's whole plain output (docs/design.md, Terminal output, Export): a combined export
// of a directory with a file_date read from a file name, one with nothing to export, and a duplicates export. The CSVs
// themselves are tested in the round trips.
func TestExportGolden(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local))
	write, filo, in := ct.write, ct.filo, ct.in
	write("tax/2023-03-14 statement.pdf", "statement")
	write("tax/notes.txt", "notes")
	write("photos/a.jpg", "photo")
	write("photos/a copy.jpg", "photo")
	if err := os.MkdirAll(in("empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", ct.root)
	var out strings.Builder
	step := func(title string, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(ExitOK, "", false, args...).stdout + "\n")
	}
	csv := func(name string) string { return filepath.Join(ct.work, name) }

	step("export tax", "export", in("tax"), "-o", csv("l.csv"))
	step("export empty", "export", in("empty"), "-o", csv("e.csv"))
	step("export duplicates", "export", "duplicates", "-o", csv("my d.csv"))

	got := strings.ReplaceAll(out.String(), ct.root, "<root>") // the root first: it is inside the work directory
	got = strings.ReplaceAll(got, ct.work, "<work>")
	golden(t, "export.txt", ct.root, got)
}

// TestExportReplace: an existing -o file is replaced only after asking (docs/design.md, Lifespan, "An existing FILE
// is replaced only after asking"): no terminal refuses, no or Enter keeps it, yes replaces it with mode 0600 and no
// temporary file left; a directory, a symlink and a device are never replaced; -y is not a flag of export.
func TestExportReplace(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	write, filo, contains, lacks := ct.write, ct.filo, ct.contains, ct.lacks
	write("a.txt", "a")
	write("b.txt", "a") // a duplicate group, for the duplicates export
	filo(ExitOK, "", false, "roots", "add", ct.root)
	l := filepath.Join(ct.work, "l.csv")
	old := []byte("my notes\n")
	if err := os.WriteFile(l, old, 0o644); err != nil {
		t.Fatal(err)
	}
	same := func(when string) {
		t.Helper()
		if got := mustRead(t, l); string(got) != string(old) {
			t.Errorf("%s: l.csv changed to %q", when, got)
		}
	}

	r := filo(ExitError, "y\n", false, "export", "-o", l)
	contains("no terminal", r.stderr, l+" already exists", "terminal")
	same("no terminal")
	filo(ExitUsage, "y\n", true, "export", "-y", "-o", l)
	same("-y")
	for _, answer := range []string{"n\n", "\n", ""} {
		r = filo(ExitOK, answer, true, "export", "-o", l)
		contains("declined", r.stdout, "Replace "+l+"? [y/N]", "Nothing exported.")
		lacks("declined", r.stdout, "Export #")
		same(fmt.Sprintf("answer %q", answer))
	}

	r = filo(ExitOK, "y\n", true, "export", "-o", l)
	contains("yes", r.stdout, "Replace "+l+"? [y/N]", "Export #1   2 files")
	contains("CSV", string(mustRead(t, l)), "id,export,directory,name")
	if fi, err := os.Stat(l); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("l.csv after replacing: %v, %v; want mode 0600", fi, err)
	}
	d := filepath.Join(ct.work, "d.csv")
	filo(ExitOK, "", false, "export", "duplicates", "-o", d)
	contains("duplicates", filo(ExitOK, "yes\n", true, "export", "duplicates", "-o", d).stdout,
		"Replace "+d+"? [y/N]", "Export #3 (duplicates)")
	entries, err := os.ReadDir(ct.work)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("left behind: %s", e.Name())
		}
	}

	// Never replaced, whatever the answer: a directory, a symlink (nor its target) and a device.
	dir := filepath.Join(ct.work, "dir.csv")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ct.work, "link.csv")
	if err := os.Symlink(l, link); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, l)
	for _, p := range []string{dir, link, os.DevNull} {
		r = filo(ExitError, "y\n", true, "export", "-o", p)
		contains(p, r.stderr, p+" is not a regular file")
		lacks(p, r.stdout, "Replace")
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link.csv is no longer a symlink: %v, %v", fi, err)
	}
	if got := mustRead(t, l); string(got) != string(before) {
		t.Errorf("the symlink's target changed")
	}

	// The register and its own files are never offered, whatever the answer.
	snaps, err := filepath.Glob(ct.db + ".snapshots/*")
	if err != nil || len(snaps) == 0 {
		t.Fatalf("no snapshot to test with: %v", err)
	}
	for _, p := range []string{ct.db, ct.db + ".lock", snaps[0]} {
		r = filo(ExitError, "y\n", true, "export", "-o", p)
		contains(p, r.stderr, "is filo's register or one of its files")
		lacks(p, r.stdout, "Replace")
	}

	// A command that cannot run asks nothing: a DIR outside every root.
	r = filo(ExitError, "y\n", true, "export", ct.work, "-o", l)
	lacks("DIR outside the roots", r.stdout, "Replace")
}

// TestWriteExportReplace: replacing writes a temporary file and renames it over the old one only once the CSV is
// whole: a failed write, or an old file that is no longer the one asked about, leaves it as it was, with nothing
// recorded and no temporary file left.
func TestWriteExportReplace(t *testing.T) {
	recordOK := func(write func(int64) error) (int64, error) { return 7, write(7) }
	for _, tc := range []struct {
		name    string
		swap    bool // replace the old file with another after asking
		edit    bool // save into the old file after asking
		failCSV bool
		want    string // the file's content afterwards
		wantErr string
	}{
		{name: "replaced", want: "new 7"},
		{name: "write fails", failCSV: true, want: "old", wantErr: "disk full"},
		{name: "changed since asked", swap: true, want: "other", wantErr: "changed"},
		{name: "edited in place since asked", edit: true, want: "old and more", wantErr: "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "l.csv")
			if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			asked, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.swap {
				q := filepath.Join(dir, "other")
				if err := os.WriteFile(q, []byte("other"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(q, p); err != nil {
					t.Fatal(err)
				}
			}
			if tc.edit {
				if err := os.WriteFile(p, []byte("old and more"), 0o644); err != nil { // the same file, written into
					t.Fatal(err)
				}
			}
			id, err := writeExport(p, asked, recordOK, func(w io.Writer, id int64) error {
				if tc.failCSV {
					return errors.New("disk full")
				}
				_, err := fmt.Fprintf(w, "new %d", id)
				return err
			})
			if tc.wantErr == "" && (err != nil || id != 7) {
				t.Fatalf("writeExport: %d, %v", id, err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("writeExport: %v, want an error with %q", err, tc.wantErr)
			}
			if got := string(mustRead(t, p)); got != tc.want {
				t.Errorf("l.csv is %q, want %q", got, tc.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("files left: %v", entries)
			}
		})
	}
}

// TestExportScansFirst: every export scans first unless --no-scan (docs/design.md, Lifespan, "A scan first"): a file
// saved since the last scan is in the CSV; export DIR scans DIR only, export duplicates every root; --no-scan
// exports the register as it is and says so; a root that fails to scan, or Ctrl-C during the scan, stops the export
// with no CSV written.
func TestExportScansFirst(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	write, filo, contains, lacks := ct.write, ct.filo, ct.contains, ct.lacks
	csv := func(name string) string { return filepath.Join(ct.work, name) }
	write("tax/a.txt", "a")
	filo(ExitOK, "", false, "roots", "add", ct.root)

	// Saved since the last scan: in the CSV, after the scan's block.
	write("tax/new.txt", "new")
	write("photos/p.jpg", "photo") // outside tax/
	r := filo(ExitOK, "", false, "export", ct.in("tax"), "-o", csv("1.csv"))
	contains("scan of DIR", r.stdout, "Scan #2   2 files seen", "under tax/", "  - 1 new\n",
		"Export #1   2 files")
	contains("scan of DIR", r.stdout, "  - 1 new\n\nNext: filo report\n\nExport #") // exactly filo scan's output
	contains("CSV of DIR", string(mustRead(t, csv("1.csv"))), ",new.txt,")

	// DIR only: photos/ was not scanned, so a --no-scan export of the whole root does not know p.jpg yet.
	r = filo(ExitOK, "", false, "export", "--no-scan", "-o", csv("2.csv"))
	contains("--no-scan", r.stdout, "Export #2   2 files",
		"  - not scanned first: as of the last scan, 2026-")
	lacks("--no-scan", r.stdout, "Scan   ")
	lacks("--no-scan CSV", string(mustRead(t, csv("2.csv"))), "p.jpg")

	// A duplicates export scans every root: a copy saved in photos/ since makes a group.
	write("photos/a copy.txt", "a")
	r = filo(ExitOK, "", false, "export", "duplicates", ct.in("tax"), "-o", csv("d.csv"))
	contains("duplicates", r.stdout, "Scan #3", "  - 2 new\n", "Export #3 (duplicates)   1 group . 2 copies")
	contains("duplicates CSV", string(mustRead(t, csv("d.csv"))), ",a copy.txt,")

	// A root that cannot be scanned stops the export: nothing written, nothing recorded.
	second := filepath.Join(ct.work, "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", second)
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	r = filo(ExitError, "", false, "export", "duplicates", "-o", csv("x.csv"))
	contains("failed root", r.stdout, "not scanned: ")
	contains("failed root", r.stderr, "nothing exported: a root could not be scanned", "--no-scan")
	if _, err := os.Lstat(csv("x.csv")); err == nil {
		t.Error("x.csv was written after a failed scan")
	}
	r = filo(ExitOK, "", false, "export", "duplicates", "--no-scan", "-o", csv("x.csv"))
	contains("failed root, --no-scan", r.stdout, "Export #4 (duplicates)")

	// --workers is the scan's: checked as filo scan checks it, and refused with --no-scan.
	for _, n := range []string{"0", "65", "-1"} {
		contains("--workers "+n, filo(ExitUsage, "", false, "export", "--workers", n, "-o",
			csv("w.csv")).stderr, "--workers must be between 1 and 64")
	}
	for _, args := range [][]string{{"export", "-o", csv("w.csv")}, {"export", "duplicates", "-o",
		csv("w.csv")}, {"roots", "add", ct.in("tax"), "--no-scan"}} {
		contains("--workers with --no-scan", filo(ExitUsage, "", false, append(args, "--workers", "2",
			"--no-scan")...).stderr, "give one or the other")
	}
	contains("--workers", filo(ExitOK, "", false, "export", ct.in("tax"), "--workers", "1", "-o",
		csv("w.csv")).stdout, "Scan #")

	// Ctrl-C during the scan: nothing exported.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ct.ctx = ctx
	real := runScan
	runScan = func(ctx context.Context, s *store.Store, o reconcile.Options) (reconcile.Summary, error) {
		cancel()
		return reconcile.Summary{}, errors.New("interrupted (9)") // as SQLite reports it: not context.Canceled
	}
	t.Cleanup(func() { runScan = real })
	r = filo(ExitError, "", false, "export", ct.in("tax"), "-o", csv("c.csv"))
	contains("Ctrl-C", r.stderr, "interrupted; nothing was exported")
	if _, err := os.Lstat(csv("c.csv")); err == nil {
		t.Error("c.csv was written after Ctrl-C")
	}
}

// TestExportNoScanLine: --no-scan names the last scan that covered what the CSV holds. A scan of a directory inside a
// root covers only that directory, and a duplicates export holds every root. An empty register shows no scan and no
// stray blank line.
func TestExportNoScanLine(t *testing.T) {
	ct := newCLITest(t)
	write, filo, contains := ct.write, ct.filo, ct.contains
	csv := func(name string) string { return filepath.Join(ct.work, name) }

	r := filo(ExitOK, "", false, "export", "-o", csv("0.csv"))
	if !strings.HasPrefix(r.stdout, "Export   no files") {
		t.Errorf("empty register:\n%s", r.stdout)
	}

	write("tax/a.txt", "a")
	write("photos/a.jpg", "a")
	filo(ExitOK, "", false, "roots", "add", ct.root, "--no-scan")
	filo(ExitOK, "", false, "scan", ct.in("tax"))
	contains("DIR scanned", filo(ExitOK, "", false, "export", ct.in("tax"), "--no-scan", "-o",
		csv("1.csv")).stdout, "  - not scanned first: as of the last scan, ")
	contains("root not scanned whole", filo(ExitOK, "", false, "export", "--no-scan", "-o",
		csv("2.csv")).stdout, "  - not scanned first, and "+ct.root+" has never been scanned whole")

	filo(ExitOK, "", false, "scan")
	second := filepath.Join(ct.work, "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", second, "--no-scan")
	contains("duplicates hold every root", filo(ExitOK, "", false, "export", "duplicates", ct.in("tax"), "--no-scan",
		"-o", csv("d.csv")).stdout, "has never been scanned whole")
	contains("retention holds its root", filo(ExitOK, "", false, "export", ct.in("tax"), "--no-scan",
		"-o", csv("3.csv")).stdout, "  - not scanned first: as of the last scan, ")
}

// TestExportRefusesAGitRepository (docs/design.md, Lifespan, "Never inside a git repository"): a CSV is never written
// inside a git working tree, however the path reaches it; nothing is written and no export is recorded.
func TestExportRefusesAGitRepository(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, base string) string // returns the -o path
		kind  []string                               // "duplicates" or nothing
		ok    bool
	}{
		{"a .git directory where the CSV goes", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "repo", ".git"))
			return filepath.Join(base, "repo", "files.csv")
		}, nil, false},
		{"a .git directory two levels up", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "repo", ".git"))
			mkdir(t, filepath.Join(base, "repo", "a", "b"))
			return filepath.Join(base, "repo", "a", "b", "files.csv")
		}, nil, false},
		{"a .git file (a worktree)", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "wt"))
			if err := os.WriteFile(filepath.Join(base, "wt", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(base, "wt", "files.csv")
		}, nil, false},
		{"a symlinked directory into a repository", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "repo", ".git"))
			mkdir(t, filepath.Join(base, "repo", "out"))
			if err := os.Symlink(filepath.Join(base, "repo", "out"), filepath.Join(base, "link")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(base, "link", "files.csv")
		}, nil, false},
		{"a directory not made yet inside a repository", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "repo", ".git"))
			return filepath.Join(base, "repo", "missing", "deeper", "files.csv")
		}, nil, false},
		{"a directory that cannot be looked into", func(t *testing.T, base string) string {
			if os.Geteuid() == 0 {
				t.Skip("root can look at anything")
			}
			mkdir(t, filepath.Join(base, "locked", "sub"))
			if err := os.Chmod(filepath.Join(base, "locked"), 0o600); err != nil { // no search: nothing in it found
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Join(base, "locked"), 0o755) })
			return filepath.Join(base, "locked", "sub", "files.csv")
		}, nil, false},
		{"a duplicates export", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "repo", ".git"))
			return filepath.Join(base, "repo", "dups.csv")
		}, []string{"duplicates"}, false},
		{"outside any repository", func(t *testing.T, base string) string {
			mkdir(t, filepath.Join(base, "plain"))
			return filepath.Join(base, "plain", "files.csv")
		}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := newCLITest(t)
			ct.write("a.txt", "a")
			ct.filo(ExitOK, "", false, "roots", "add", ct.root)
			base := canonicalTemp(t)
			csv := tt.setup(t, base)
			args := append(append([]string{"export"}, tt.kind...), "-o", csv)
			if tt.ok {
				ct.filo(ExitOK, "", false, args...)
				if _, err := os.Stat(csv); err != nil {
					t.Fatalf("the CSV was not written: %v", err)
				}
				return
			}
			r := ct.filo(ExitError, "", false, args...)
			if !strings.Contains(r.stderr, "cannot tell whether") {
				ct.contains("refusal", r.stderr, "is inside a git repository", "could be committed by mistake")
			}
			if _, err := os.Lstat(csv); err == nil {
				t.Fatal("a CSV was written inside a git repository")
			}
			// Nothing recorded: the next export made is the first.
			out := ct.filo(ExitOK, "", false, "export", "-o", filepath.Join(ct.work, "after.csv")).stdout
			ct.contains("the next export", out, "#1")
		})
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
