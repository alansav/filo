package cli

import (
	"strings"
	"testing"
	"time"
)

// newSetTest is a register of receipts, one of them given a policy by a rule.
func newSetTest(t *testing.T) *cliTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local))
	for _, name := range []string{"coffee", "lunch", "taxi"} {
		ct.write("receipts/"+name+".pdf", name)
	}
	ct.write("old/2015 phone.pdf", "phone")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "policies", "add", "bills", "7y")
	ct.filo(ExitOK, "", false, "rules", "add", ct.in("receipts"), "*lunch*", "bills")
	return ct
}

// TestSetGolden: filo set's output for the designed cases, plain: first values (no question), values replaced
// (asked, at a terminal), a refusal, a file left past its retention, a clear, and a dry run.
func TestSetGolden(t *testing.T) {
	ct := newSetTest(t)
	in := ct.in
	var out string
	run := func(code int, stdin string, terminal bool, args ...string) {
		r := ct.filo(code, stdin, terminal, args...)
		out += "$ filo " + strings.Join(args, " ") + "\n\n" + r.stdout + r.stderr + "\n"
	}
	run(ExitOK, "", false, "set", in("receipts/taxi.pdf"), "--file-date", "2024-03-31", "--retention", "7y")
	run(ExitError, "", false, "set", in("receipts/coffee.pdf"), in("receipts/lunch.pdf"), in("receipts/taxi.pdf"),
		"--retention", "7y")
	run(ExitOK, "y\n", true, "set", in("receipts/coffee.pdf"), in("receipts/lunch.pdf"), in("receipts/taxi.pdf"),
		"--file-date", "2024-03-31", "--retention", "7y")
	run(ExitOK, "", false, "set", in("old/2015 phone.pdf"), "--file-date", "2015-02-01", "--retention", "7d")
	run(ExitOK, "", false, "set", in("receipts/taxi.pdf"), "--retention", "forever", "--dry-run")
	run(ExitOK, "y\n", true, "set", in("receipts/coffee.pdf"), in("receipts/taxi.pdf"), "--no-retention")
	golden(t, "set.txt", ct.root, out)
}

// TestSet: what it changes, the questions, and the refusals.
func TestSet(t *testing.T) {
	ct := newSetTest(t)
	in, filo, contains, lacks := ct.in, ct.filo, ct.contains, ct.lacks
	taxi := in("receipts/taxi.pdf")
	filo(ExitOK, "", false, "set", taxi, "--file-date", "2024-03-31", "--retention", "bills")
	contains("show after set", filo(ExitOK, "", false, "show", taxi).stdout, "kept until 2031-03-31",
		"retention   bills (7y)\n", "file_date   2024-03-31", "by filo set, on this content")

	// Replacing without a terminal changes nothing, and says so; declined at a terminal, the same.
	contains("no terminal", filo(ExitOK, "", false, "set", taxi, "--retention", "10y").stdout,
		"Nothing changed: there is no terminal to ask in")
	contains("declined", filo(ExitOK, "n\n", true, "set", taxi, "--retention", "10y").stdout, "Nothing changed.")
	contains("still bills", filo(ExitOK, "", false, "show", taxi).stdout, "retention   bills (7y)\n")
	// l lists every file before asking again.
	contains("list", filo(ExitOK, "l\nn\n", true, "set", taxi, "--retention", "10y").stdout, "[y/N/l]")

	// The same values: nothing to do, no question.
	out := filo(ExitOK, "", false, "set", taxi, "--retention", "bills").stdout
	contains("unchanged", out, "Unchanged   retention bills . 1 file has these values already")
	lacks("unchanged", out, "Replace")

	// Refused paths: a directory, one not in the register, one outside every root; nothing changes.
	ct.write("new.txt", "new")
	r := filo(ExitError, "", false, "set", in("receipts"), in("new.txt"), taxi, "/", "--retention", "forever")
	contains("refusals", r.stdout, "Nothing was changed: 3 of 4 files were refused",
		in("receipts")+"   a directory", in("new.txt")+"   not in the register yet", "/   outside every root")
	contains("still bills", filo(ExitOK, "", false, "show", taxi).stdout, "retention   bills (7y)\n")
	contains("no such policy", filo(ExitError, "", false, "set", taxi, "--retention", "nope").stdout,
		"no policy named nope (filo policies add nope VALUE creates one)")

	// Usage.
	for _, args := range [][]string{{"set", taxi}, {"set", taxi, "--file-date", "2024-01-01", "--no-file-date"},
		{"set", taxi, "--retention", "7y", "--no-retention"}, {"set", taxi, "--retention", "7q"},
		{"set", taxi, "--file-date", "31/03/2024"}, {"set", taxi, "--file-date", ""}, {"set", "--retention", "7y"}} {
		filo(ExitUsage, "", false, args...)
	}
	contains("-h", filo(ExitOK, "", false, "set", "-h").stdout, "filo set FILE... [flags]", "--no-file-date")
}
