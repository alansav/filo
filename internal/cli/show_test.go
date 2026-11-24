package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newShowTest is a register with a file on a policy (a copy of it elsewhere), a file moved since its first scan and
// one gone missing.
func newShowTest(t *testing.T) *cliTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local))
	ct.write("bills/2022-01 British Gas.pdf", "gas bill")
	ct.write("backup/2022-01.pdf", "gas bill")
	ct.write("inbox/water.pdf", "water bill")
	ct.write("notes.txt", "notes")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "policies", "add", "bills", "7y")
	csv := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "retention", "-o", csv)
	editCSV(t, csv, accepting(setRetention(map[string]string{"2022-01 British Gas.pdf": "bills"})))
	ct.filo(ExitOK, "y\n", true, "update", csv)
	if err := os.Rename(ct.in("inbox/water.pdf"), ct.in("bills/2025-03 Water.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ct.in("notes.txt")); err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "scan")
	return ct
}

// TestShowGolden: filo show of three files (kept on a policy with a copy; moved and undated; missing), plain.
func TestShowGolden(t *testing.T) {
	ct := newShowTest(t)
	out := ct.filo(ExitOK, "", false, "show", ct.in("bills/2022-01 British Gas.pdf"),
		ct.in("bills/2025-03 Water.pdf"), ct.in("notes.txt")).stdout
	out = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(out, "<time>")
	out = strings.ReplaceAll(out, time.Now().Format(time.DateOnly), "<today>")
	golden(t, "show.txt", ct.root, out)
}

// TestShow: the lookups, the flags and the errors.
func TestShow(t *testing.T) {
	ct := newShowTest(t)
	in, filo, contains, lacks := ct.in, ct.filo, ct.contains, ct.lacks

	// The old path of a moved file finds it; --history lists both paths.
	out := filo(ExitOK, "", false, "show", "--history", in("inbox/water.pdf")).stdout
	contains("history", out, "2025-03 Water.pdf   [!] needs a retention", "  Paths   2 paths, newest first",
		in("bills/2025-03 Water.pdf")+"   ", in("inbox/water.pdf")+"   ")
	lacks("history", out, "moved from", "[1/")

	// --verify reads the file; changed on disk, the modified line says so without reading it.
	contains("verify", filo(ExitOK, "", false, "show", "--verify", in("bills/2022-01 British Gas.pdf")).stdout,
		"content matches the register (read now)")
	ct.write("bills/2022-01 British Gas.pdf", "gas bill, edited")
	out = filo(ExitOK, "", false, "show", in("bills/2022-01 British Gas.pdf")).stdout
	contains("changed", out, "[!] changed on disk since the last scan: run filo scan")
	lacks("changed", out, "read now")
	contains("changed, verified", filo(ExitOK, "", false, "show", "--verify",
		in("bills/2022-01 British Gas.pdf")).stdout, "[!] differs from the register (read now)")
	if err := os.Remove(in("bills/2022-01 British Gas.pdf")); err != nil {
		t.Fatal(err)
	}
	contains("gone", filo(ExitOK, "", false, "show", in("bills/2022-01 British Gas.pdf")).stdout,
		"[!] not on disk")

	// Errors: each path that cannot be shown is a line on stderr, the others are still shown, and the exit is 1.
	ct.write(".hidden/a.txt", "hidden")
	ct.write("new.txt", "new")
	outside := filepath.Join(ct.work, "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := filo(ExitError, "", false, "show", in("new.txt"), in("bills/2025-03 Water.pdf"), outside,
		in(".hidden/a.txt"), in("gone/never.txt"))
	contains("errors", r.stderr, in("new.txt")+" is not in the register yet: run filo scan",
		outside+" is not in the register (outside every root)",
		in(".hidden/a.txt")+" is not in the register: its root's settings skip it",
		in("gone/never.txt")+" is not in the register yet: run filo scan")
	contains("the one shown", r.stdout, "[2/5] 2025-03 Water.pdf")
	if strings.Count(r.stdout, "  File\n") != 1 {
		t.Errorf("show with errors shows other than one file:\n%s", r.stdout)
	}

	// A file in a directory filo cannot read is still shown from the register, the path line saying why.
	ct.write("locked/a.txt", "locked")
	filo(ExitOK, "", false, "scan")
	if err := os.Chmod(in("locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(in("locked"), 0o755) })
	if _, err := os.Lstat(in("locked/a.txt")); err == nil {
		t.Log("running as root: the locked directory can be read anyway")
	} else {
		contains("locked", filo(ExitOK, "", false, "show", in("locked/a.txt")).stdout, "a.txt   [!] needs a retention",
			"[!] cannot be checked: ")
	}
	if err := os.Chmod(in("locked"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Through a symlinked directory, a file is found whether it is on disk or gone.
	alias := filepath.Join(ct.work, "alias")
	if err := os.Symlink(ct.root, alias); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(alias, "bills", "2025-03 Water.pdf")
	contains("alias, on disk", filo(ExitOK, "", false, "show", via).stdout, "2025-03 Water.pdf   [!] needs a retention")
	if err := os.Remove(in("bills/2025-03 Water.pdf")); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "scan")
	contains("alias, missing", filo(ExitOK, "", false, "show", via).stdout, "2025-03 Water.pdf   [!] missing since")

	// Usage.
	filo(ExitUsage, "", false, "show")
	contains("tilde", filo(ExitError, "", false, "show", "~/x").stderr, "starts with ~")
	contains("-h", filo(ExitOK, "", false, "show", "-h").stdout, "filo show PATH... [-r] [--history] [--verify]")
}

// TestShowWritesNothing: filo show leaves the register as it was: no snapshot, no change.
func TestShowWritesNothing(t *testing.T) {
	ct := newShowTest(t)
	snaps := func() int {
		entries, err := os.ReadDir(ct.db + ".snapshots")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := snaps()
	info, err := os.Stat(ct.db)
	if err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "show", "--verify", "--history", ct.in("bills/2022-01 British Gas.pdf"))
	after, err := os.Stat(ct.db)
	if err != nil {
		t.Fatal(err)
	}
	if snaps() != before || !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() {
		t.Errorf("show wrote: snapshots %d -> %d, register %v/%d -> %v/%d", before, snaps(), info.ModTime(),
			info.Size(), after.ModTime(), after.Size())
	}
}

// TestShowDirGolden: filo show of a directory, then of the root with -r, plain.
func TestShowDirGolden(t *testing.T) {
	ct := newShowTest(t)
	ct.write("bills/2023/2023-01 Gas.pdf", "gas 2023")
	ct.filo(ExitOK, "", false, "rules", "add", ct.in("bills/2023"), "bills")
	ct.filo(ExitOK, "", false, "scan")
	var out string
	for _, args := range [][]string{{"show", ct.in("bills")}, {"show", "-r", ct.root}} {
		out += "$ filo " + strings.Join(args, " ") + "\n\n" + ct.filo(ExitOK, "", false, args...).stdout + "\n"
	}
	out = strings.ReplaceAll(out, time.Now().Format(time.DateOnly), "<today>")
	out = strings.ReplaceAll(out, ct.root, "<root>") // the root first: it is inside the work directory
	out = strings.ReplaceAll(out, ct.work, "<work>")
	golden(t, "show-dir.txt", ct.root, out)
}

// TestShowDir: a directory gone from the disk is shown from the register; --recursive is -r.
func TestShowDir(t *testing.T) {
	ct := newShowTest(t)
	if err := os.RemoveAll(ct.in("backup")); err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "scan")
	ct.contains("gone", ct.filo(ExitOK, "", false, "show", ct.in("backup")).stdout, "backup   directory . 1 file",
		"2022-01.pdf   8 B   [!] missing since")
	ct.contains("--recursive", ct.filo(ExitOK, "", false, "show", "--recursive", ct.root).stdout,
		"home and below   directory", "  home/bills/  ")
	ct.filo(ExitError, "", false, "show", ct.in("never"))

	// A directory on disk the register holds nothing under: an error, as for a file.
	if err := os.MkdirAll(ct.in("new/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	ct.write(".git/config", "x")
	r := ct.filo(ExitError, "", false, "show", ct.in("new"), ct.in(".git"))
	ct.contains("unknown directories", r.stderr, ct.in("new")+" is not in the register yet: run filo scan",
		ct.in(".git")+" is not in the register: its root's settings skip it")
	if r.stdout != "" {
		t.Errorf("unknown directories shown:\n%s", r.stdout)
	}
}
