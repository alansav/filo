package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// filo policies (docs/design.md, Named retention policies; slice 3): list, add, set, move, remove and show, with
// their questions. Files are put on policies through the retention CSV, as a person would.

// policyTest is a tree of bills on the policy bills (7y), one without a file_date, and a policy bils (a typo, no value)
// holding one file.
type policyTest struct{ *cliTest }

func newPolicyTest(t *testing.T) policyTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	for _, rel := range []string{"bills/2015-06.pdf", "bills/2022-01.pdf", "bills/undated.pdf", "bils/x.pdf",
		"notes.txt"} {
		ct.write(rel, rel)
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "policies", "add", "bills", "7y")
	csv := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "retention", "-o", csv)
	// 2015-06.pdf: until 2022-06-30 (past); 2022-01.pdf: until 2029-01-31; undated.pdf: forever for now.
	editCSV(t, csv, setRetention(map[string]string{"2015-06.pdf": "bills", "2022-01.pdf": "bills",
		"undated.pdf": "forever", "x.pdf": "bils"}))
	ct.filo(ExitOK, "y\n", true, "update", csv)
	return policyTest{ct}
}

// TestPoliciesAdd: A4, and adding with and without a value.
func TestPoliciesAdd(t *testing.T) {
	p := newPolicyTest(t)
	p.contains("add", p.filo(ExitOK, "", false, "policies", "add", "Payslips", "forever").stdout,
		"Added   payslips . forever")
	p.contains("add without a value", p.filo(ExitOK, "", false, "policies", "add", "tax").stdout,
		"Added   tax . no value yet")
	p.contains("exists", p.filo(ExitError, "", false, "policies", "add", "BILLS", "10y").stderr,
		"bills: a policy of that name exists already (filo policies set bills VALUE changes it)")
	p.contains("forever", p.filo(ExitError, "", false, "policies", "add", "forever").stderr, "not forever")
	p.contains("a period", p.filo(ExitError, "", false, "policies", "add", "7y").stderr, "starting with a letter")
	p.contains("a bad value", p.filo(ExitError, "", false, "policies", "add", "x", "7q").stderr,
		`"7q" is not a policy value`)
	p.contains("a name as a value", p.filo(ExitError, "", false, "policies", "add", "x", "bills").stderr,
		`"bills" is not a policy value`)
}

// TestPoliciesList: E1 and E2's flags: a policy with no value, a date that has passed, files without a file_date.
func TestPoliciesList(t *testing.T) {
	p := newPolicyTest(t)
	p.filo(ExitOK, "", false, "policies", "add", "last-year", "2025-12-31")
	out := p.filo(ExitOK, "", false, "policies").stdout
	p.contains("list", out, "Policies   3 . 2 need attention", "bills       7y           from each file's file_date",
		"2 files", "bils        no value", "1 file   [!] needs a retention",
		"last-year   2025-12-31", "0 files   [!] passed: set a new date, or remove it")

	// undated.pdf goes on bills (as forever first, then bills: its file_date is blank, so the CSV refuses that) --
	// it reaches "needs a file_date" through a change of the policy: forever, then 7y.
	p.filo(ExitOK, "", false, "policies", "set", "bills", "forever", "-y")
	csv := filepath.Join(p.work, "r2.csv")
	p.filo(ExitOK, "", false, "export", "retention", "-o", csv)
	editCSV(t, csv, setRetention(map[string]string{"undated.pdf": "bills"}))
	p.filo(ExitOK, "", false, "update", csv)
	p.filo(ExitOK, "", false, "policies", "set", "bills", "7y", "-y")
	p.contains("without a file_date", p.filo(ExitOK, "", false, "policies").stdout,
		"3 files   [!] 1 without a file_date")

	p.contains("empty", newEmptyPolicies(t), "Policies   none yet",
		"Next: filo policies add NAME VALUE, or type a policy name in a retention CSV")
}

func newEmptyPolicies(t *testing.T) string {
	ct := newCLITest(t)
	return ct.filo(ExitOK, "", false, "policies").stdout
}

// TestPoliciesSet: C1-C4: lengthen without a question; shorten with none newly past without one; shorten putting
// files past their date asks (yes, no, no terminal, -y); a period for files without a file_date asks.
func TestPoliciesSet(t *testing.T) {
	p := newPolicyTest(t)
	out := p.filo(ExitOK, "", false, "policies", "set", "bills", "10y").stdout
	p.contains("lengthen", out, "Done   bills is 10y . 2 files updated")
	p.lacks("lengthen", out, "?")

	p.contains("shorten, none newly past", p.filo(ExitOK, "", false, "policies", "set", "bills", "5y").stdout,
		"Done   bills is 5y . 2 files updated")

	// 3y puts 2022-01.pdf (2025-01-31) past its date.
	p.contains("no terminal", p.filo(ExitOK, "", false, "policies", "set", "bills", "3y").stdout,
		"Shorten bills   5y -> 3y . 2 files", "[!] 1 file would be past its retention:", "2022-01-31",
		"2025-01-31", "bills/2022-01.pdf",
		"Nothing changed: there is no terminal to ask in. Run it in a terminal, or pass -y.")
	p.contains("declined", p.filo(ExitOK, "n\n", true, "policies", "set", "bills", "3y").stdout,
		"Shorten bills for 2 files, putting 1 past its retention? [y/N]", "Nothing changed.")
	p.contains("still 5y", p.filo(ExitOK, "", false, "policies").stdout, "bills   5y")
	out = p.filo(ExitOK, "y\n", true, "policies", "set", "bills", "3y").stdout
	p.contains("confirmed", out, "Done   bills is 3y . 2 files updated . 1 past its retention", "Next: filo delete")
	p.contains("-y", p.filo(ExitOK, "", false, "policies", "set", "bills", "2y", "-y").stdout,
		"Done   bills is 2y")

	// C4: a period for a file without a file_date, from a date policy.
	p.filo(ExitOK, "", false, "policies", "set", "bils", "2030-12-31")
	csv := filepath.Join(p.work, "r2.csv")
	p.filo(ExitOK, "", false, "export", "retention", "-o", csv)
	editCSV(t, csv, setRetention(map[string]string{"undated.pdf": "bils"}))
	p.filo(ExitOK, "", false, "update", csv)
	out = p.filo(ExitOK, "", false, "policies", "set", "bils", "7y").stdout
	p.contains("to a period", out, "[!] 2 files have no file_date to count 7y from: they would need one",
		"bils/x.pdf", "bills/undated.pdf", "Nothing changed: there is no terminal to ask in")
	out = p.filo(ExitOK, "y\n", true, "policies", "set", "bils", "7y").stdout
	p.contains("to a period, confirmed", out, "Change bils for 2 files, leaving 2 without a file_date? [y/N]",
		"Done   bils is 7y . 2 files updated . 2 need a file_date")

	p.contains("unknown", p.filo(ExitError, "", false, "policies", "set", "nope", "1y").stderr,
		"nope: no such policy (filo policies lists them)")
	p.contains("unchanged", p.filo(ExitOK, "", false, "policies", "set", "bils", "7y").stdout,
		"Unchanged   bils is 7y already")
}

// TestPoliciesMoveRemove: D1, D2: a rename into a new name, a merge into an existing policy, and removing a policy in
// use (refused) and unused.
func TestPoliciesMoveRemove(t *testing.T) {
	p := newPolicyTest(t)
	p.contains("in use", p.filo(ExitError, "", false, "policies", "remove", "bils").stderr,
		"bils is used by 1 file: move it first (filo policies move bils NAME)")
	p.contains("rename, no terminal", p.filo(ExitOK, "", false, "policies", "move", "bils", "bilz").stdout,
		"Nothing changed: there is no terminal to ask in")
	p.contains("rename", p.filo(ExitOK, "y\n", true, "policies", "move", "bils", "bilz").stdout,
		"Rename bils to bilz (1 file)? [y/N]", "Done   renamed bils to bilz")
	out := p.filo(ExitOK, "y\n", true, "policies", "move", "bilz", "bills").stdout
	p.contains("merge", out, "Move 1 file from bilz to bills (7y)? [y/N]", "Done   moved 1 file to bills",
		"Next: filo policies remove bilz")
	p.contains("removed", p.filo(ExitOK, "", false, "policies", "remove", "bilz").stdout, "Removed   bilz")
	p.contains("gone", p.filo(ExitOK, "", false, "policies").stdout, "Policies   1")
	p.contains("-y", p.filo(ExitOK, "", false, "policies", "move", "bills", "invoices", "-y").stdout,
		"Done   renamed bills to invoices")
}

// TestPoliciesShow: a policy's value and files, those that need a file_date first.
func TestPoliciesShow(t *testing.T) {
	p := newPolicyTest(t)
	out := p.filo(ExitOK, "", false, "policies", "show", "bills").stdout
	p.contains("show", out, "Policy bills   7y from each file's file_date . 2 files", "file_date    until",
		"2015-06-30   2022-06-30   bills/2015-06.pdf", "2022-01-31   2029-01-31   bills/2022-01.pdf",
		"Next: filo policies set bills VALUE to change it for all of them")
	if i, j := strings.Index(out, "2015-06.pdf"), strings.Index(out, "2022-01.pdf"); i > j {
		t.Errorf("show is not ordered by path:\n%s", out)
	}
	p.contains("unknown", p.filo(ExitError, "", false, "policies", "show", "nope").stderr, "no such policy")
}

// TestPoliciesUsage: the subcommands' arguments.
func TestPoliciesUsage(t *testing.T) {
	ct := newCLITest(t)
	for _, args := range [][]string{{"policies", "add"}, {"policies", "add", "a", "1y", "x"}, {"policies", "set", "a"},
		{"policies", "move", "a"}, {"policies", "remove"}, {"policies", "show"}, {"policies", "frob"},
		{"policies", "list", "x"}} {
		ct.filo(ExitUsage, "", false, args...)
	}
	ct.contains("-h", ct.filo(ExitOK, "", false, "policies", "-h").stdout, "filo policies set NAME VALUE")
}

// TestPoliciesGolden: a session of filo policies, in plain text.
func TestPoliciesGolden(t *testing.T) {
	p := newPolicyTest(t)
	p.filo(ExitOK, "", false, "policies", "add", "last-year", "2025-12-31")
	var out string
	run := func(stdin string, terminal bool, args ...string) {
		out += "$ filo " + strings.Join(args, " ") + "\n\n" + p.filo(ExitOK, stdin, terminal, args...).stdout + "\n"
	}
	run("", false, "policies")
	run("", false, "policies", "show", "bills")
	run("y\n", true, "policies", "set", "bills", "3y")
	run("y\n", true, "policies", "move", "bils", "bills")
	run("", false, "policies", "remove", "bils")
	run("", false, "policies")
	golden(t, "policies.txt", p.root, out)
}
