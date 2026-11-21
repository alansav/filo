package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newFindTest is a register of one root holding bills, a note, an empty file and a symlink, each file modified on a
// day of its own so the dates in the output are fixed.
func newFindTest(t *testing.T) *cliTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local))
	files := []struct {
		rel, content string
		day          int
	}{
		{"received/2026/10/2026-10-03 British Gas Bill.pdf", "october bill", 3},
		{"received/2026/09/2026-09-02 British Gas Bill.pdf", "september bill", 2},
		{"house/gas safety certificate.pdf", "certificate", 14},
		{"notes/ideas.txt", "ideas", 20},
		{"notes/empty.txt", "", 21},
	}
	for _, f := range files {
		ct.write(f.rel, f.content)
		day := time.Date(2026, 10, f.day, 12, 0, 0, 0, time.Local)
		if err := os.Chtimes(ct.in(f.rel), day, day); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("ideas.txt", ct.in("notes/gas link.txt")); err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	return ct
}

// TestFindGolden: filo find's whole output in a terminal (docs/design.md, Finding files): the scan's block, then
// every file found by root and path with its date and size; a DIR; nothing found; --no-scan; several roots.
func TestFindGolden(t *testing.T) {
	ct := newFindTest(t)
	filo, in := ct.filo, ct.in
	var out strings.Builder
	step := func(title string, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(ExitOK, "", true, args...).stdout + "\n")
	}
	step("find --name '*gas*'", "find", "--name", "*gas*")
	step("find received", "find", in("received"))
	step("find --name '*gaz*'", "find", "--name", "*gaz*")
	step("find notes --no-scan", "find", in("notes"), "--no-scan")

	second := filepath.Join(ct.work, "archive")
	if err := os.MkdirAll(filepath.Join(second, "tax"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "tax", "2018 return.pdf"), []byte("return"), 0o644); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2019, 4, 30, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(filepath.Join(second, "tax", "2018 return.pdf"), day, day); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", second)
	step("find --name '*.PDF' (two roots)", "find", "--name", "*.PDF", "--no-scan")

	got := strings.ReplaceAll(out.String(), ct.root, "<root>")
	got = strings.ReplaceAll(got, ct.work, "<work>")
	got = regexp.MustCompile(`as of the last scan, \d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got,
		"as of the last scan, <time>")
	golden(t, "find.txt", ct.root, got)
}

// TestFindPiped: piped, one absolute path per line and nothing else on stdout, after a scan that finds a file saved
// since the last one (--no-scan does not); -0 writes the names as they are; a name escaping changed is said on stderr
// with exit 1; nothing found is no output, exit 0.
func TestFindPiped(t *testing.T) {
	ct := newFindTest(t)
	filo, in, contains, lacks := ct.filo, ct.in, ct.contains, ct.lacks
	check := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", what, got, want)
		}
	}
	check("--name, piped", filo(ExitOK, "", false, "find", "--name", "*GAS*").stdout, ""+
		in("house/gas safety certificate.pdf")+"\n"+
		in("received/2026/09/2026-09-02 British Gas Bill.pdf")+"\n"+
		in("received/2026/10/2026-10-03 British Gas Bill.pdf")+"\n")
	check("no --name: every file, not the symlink", filo(ExitOK, "", false, "find", in("notes")).stdout,
		in("notes/empty.txt")+"\n"+in("notes/ideas.txt")+"\n")
	check("nothing found", filo(ExitOK, "", false, "find", "--name", "*gaz*").stdout, "")

	// A scan first: a file saved since is found; with --no-scan, it is not, and nothing says so on stdout.
	ct.write("inbox/new gas bill.pdf", "new")
	check("--no-scan", filo(ExitOK, "", false, "find", "--name", "new*", "--no-scan").stdout, "")
	r := filo(ExitOK, "", false, "find", "--name", "new*")
	check("scan first", r.stdout, in("inbox/new gas bill.pdf")+"\n")
	check("scan first, stderr", r.stderr, "")

	// A name holding a newline or a backslash: escaped, said on stderr, exit 1; -0 gives it as it is.
	ct.write("odd/new\nline.txt", "x")
	ct.write(`odd/back\slash.txt`, "y")
	ct.write("odd/plain.txt", "z")
	r = filo(ExitError, "", false, "find", in("odd"))
	check("escaped", r.stdout, in(`odd/back\\slash.txt`)+"\n"+in(`odd/new\nline.txt`)+"\n"+in("odd/plain.txt")+"\n")
	contains("escaped, stderr", r.stderr, "filo: 2 names were escaped and will not match on disk; use -0")
	r = filo(ExitOK, "", false, "find", in("odd"), "-0")
	check("-0", r.stdout, in(`odd/back\slash.txt`)+"\x00"+in("odd/new\nline.txt")+"\x00"+in("odd/plain.txt")+"\x00")
	check("-0, stderr", r.stderr, "")
	r = filo(ExitError, "", false, "find", "--name", "new*line.txt")
	contains("one escaped", r.stderr, "filo: 1 name was escaped")

	// Missing files are not listed; a hard link is listed once, under its first path.
	if err := os.Remove(in("notes/ideas.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(in("notes/empty.txt"), in("notes/z link.txt")); err != nil {
		t.Fatal(err)
	}
	check("missing and hard link", filo(ExitOK, "", false, "find", in("notes")).stdout, in("notes/empty.txt")+"\n")
	out := filo(ExitOK, "", true, "find", in("notes"), "--no-scan").stdout
	contains("hard link noted", out, "notes/empty.txt   and 1 hard link, the same file")
	lacks("hard link noted", out, "z link.txt")
}

// TestFindRefusals: the usage errors, a DIR gone from the disk, and a root that fails to scan.
func TestFindRefusals(t *testing.T) {
	ct := newFindTest(t)
	filo, in, contains := ct.filo, ct.in, ct.contains
	for _, tc := range []struct {
		args []string
		term bool
		want string
	}{
		{[]string{"find", "-0"}, true, "-0 writes raw names for a pipe, such as xargs -0"},
		{[]string{"find", ct.work}, false, "is not inside a root"},
		{[]string{"find", in("notes/ideas.txt")}, false, "is a file: use filo show"},
		{[]string{"find", in("notes"), in("house")}, false, "find takes at most one DIR"},
		{[]string{"find", "--no-scan", "--workers", "2"}, false, "give one or the other"},
		{[]string{"find", "--workers", "0"}, false, "--workers must be between 1 and 64"},
		{[]string{"find", "--nope"}, false, ""},
	} {
		contains(strings.Join(tc.args, " "), filo(ExitUsage, "", tc.term, tc.args...).stderr, tc.want)
	}
	contains("-h", filo(ExitOK, "", false, "find", "-h").stdout, "filo find [DIR] [--name PATTERN]")
	contains("Unix-style -name", filo(ExitOK, "", false, "find", "-name", "ideas.txt").stdout, in("notes/ideas.txt"))

	// A missing file given as DIR is still a file; a directory gone from the disk is no error: its root is scanned,
	// so its files are found missing, and it holds none present.
	if err := os.Remove(in("notes/empty.txt")); err != nil {
		t.Fatal(err)
	}
	contains("missing file", filo(ExitUsage, "", false, "find", in("notes/empty.txt")).stderr, "is a file")
	if err := os.RemoveAll(in("received")); err != nil {
		t.Fatal(err)
	}
	r := filo(ExitOK, "", true, "find", in("received"))
	contains("gone directory", r.stdout, "  - 3 missing", "missing   received/2026/10/2026-10-03 British Gas Bill.pdf",
		"Found   no files . "+ct.root+", under received/")

	// A root that cannot be scanned: nothing listed, exit 1, with --no-scan as the way on.
	second := filepath.Join(ct.work, "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", second)
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	r = filo(ExitError, "", false, "find", "--name", "*gas*")
	if r.stdout != "" {
		t.Errorf("piped, a failed scan listed:\n%s", r.stdout)
	}
	contains("failed root", r.stderr, "could not be scanned", "nothing listed: a root could not be scanned", "--no-scan")
	r = filo(ExitError, "", true, "find", "--name", "*gas*")
	contains("failed root, terminal", r.stdout, "not scanned: ")
	if strings.Contains(r.stdout, "Found") {
		t.Errorf("a failed scan listed:\n%s", r.stdout)
	}
	contains("failed root, --no-scan", filo(ExitOK, "", false, "find", "--name", "*safety*", "--no-scan").stdout,
		in("house/gas safety certificate.pdf"))
}
