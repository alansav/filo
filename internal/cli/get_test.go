package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// newGetTest is a register with a file on a policy and an undated file whose name holds a tab.
func newGetTest(t *testing.T) *cliTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local))
	ct.write("r/coffee.pdf", "coffee")
	ct.write("r/tab\tname.pdf", "tab")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "set", ct.in("r/coffee.pdf"), "--file-date", "2024-03-31", "--retention", "7y")
	return ct
}

// TestGet: the layouts, piped and at a terminal, the order of the fields, and the errors.
func TestGet(t *testing.T) {
	ct := newGetTest(t)
	in, filo, contains := ct.in, ct.filo, ct.contains
	coffee, tab := in("r/coffee.pdf"), in("r/tab\tname.pdf")
	check := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", what, got, want)
		}
	}

	check("one field", filo(ExitOK, "", false, "get", coffee, "--retention").stdout, "7y\n")
	check("one field, none", filo(ExitOK, "", false, "get", tab, "--retention").stdout, "\n")
	check("one file, fields in the order given", filo(ExitOK, "", false, "get", coffee, "--keep-until",
		"--retention", "--file-date").stdout, "keep_until\t2031-03-31\nretention\t7y\nfile_date\t2024-03-31\n")
	check("one file at a terminal", filo(ExitOK, "", true, "get", coffee, "--retention", "--keep-until").stdout,
		"retention    7y\nkeep_until   2031-03-31\n")
	check("several files, piped", filo(ExitOK, "", false, "get", coffee, tab, "--retention", "--status").stdout,
		"7y\tkept\t"+coffee+"\n\tundated\t"+strings.ReplaceAll(tab, "\t", `\t`)+"\n")
	out := filo(ExitOK, "", true, "get", coffee, tab, "--size", "--retention").stdout
	contains("several files at a terminal", out, "size   retention", "   6   7y          "+coffee,
		"   3               "+strings.ReplaceAll(tab, "\t", `\t`))

	// The other fields have values.
	out = filo(ExitOK, "", false, "get", coffee, "--duplicates", "--size", "--sha256", "--root", "--first-seen",
		"--modified").stdout
	contains("other fields", out, "duplicates\tcounted\n", "size\t6\n", "sha256\t", "root\t"+ct.root+"\n")
	if !regexp.MustCompile(`first_seen\t\d{4}-\d\d-\d\d\n`).MatchString(out) ||
		!regexp.MustCompile(`modified\t\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d`).MatchString(out) {
		t.Errorf("first_seen or modified not a date:\n%s", out)
	}

	// A path that cannot be read: an error line, the others still printed, exit 1, the layout kept.
	r := filo(ExitError, "", false, "get", in("r/nope.pdf"), coffee, "--retention")
	contains("error", r.stderr, in("r/nope.pdf")+" is not in the register yet")
	check("the others", r.stdout, "7y\t"+coffee+"\n")
	contains("a directory", filo(ExitError, "", false, "get", in("r"), "--size").stderr, "is a directory")

	// Usage.
	for _, args := range [][]string{{"get", coffee}, {"get", "--retention"}, {"get", coffee, "--nope"}} {
		filo(ExitUsage, "", false, args...)
	}
	filo(ExitUsage, "", false, "get", coffee, "--size", "-0") // no -0: names are always escaped
	contains("-h", filo(ExitOK, "", false, "get", "-h").stdout, "filo get FILE... --FIELD...")
}
