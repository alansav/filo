package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// filo rules (docs/design.md, Lifespan, Directory rules; slice 2): the list, add (with --dry-run), set and remove.

// ruleTest is a tree of bank statements, a 2024 directory and an electric bill, registered and scanned.
type ruleTest struct{ *cliTest }

func newRuleTest(t *testing.T) ruleTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	for _, rel := range []string{"bank/DailyStatement-2019-03.pdf", "bank/QuarterlyStatement-Q1.pdf",
		"bank/bank-2024.pdf", "bank/letter.pdf", "bank/2024/DailyStatement-01.pdf",
		"personal/home/electric/2023-01.pdf", "notes.txt"} {
		ct.write(rel, rel)
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "policies", "add", "statements", "7y")
	return ruleTest{ct}
}

// TestRulesGolden: the agreed output (docs/design.md, Directory rules, Output) of a session: a dry run, adds (one
// creating its policy), the list with a tie and a directory gone, set and remove.
func TestRulesGolden(t *testing.T) {
	rt := newRuleTest(t)
	var out strings.Builder
	run := func(code int, args ...string) {
		r := rt.filo(code, "", false, append([]string{"rules"}, args...)...)
		out.WriteString("$ filo rules " + strings.Join(args, " ") + "\n" + r.stdout + r.stderr + "\n")
	}
	run(ExitOK)
	run(ExitOK, "add", rt.in("bank"), "*Statement*", "statements", "--dry-run")
	run(ExitOK, "add", rt.in("bank"), "*Statement*", "statements")
	run(ExitOK, "add", rt.in("bank"), "*bank*", "banking")
	run(ExitOK, "add", rt.in("bank/2024"), "tax")
	rt.write("bank/new-2024-bank.pdf", "new") // after the rules: *bank* and *2024* tie on it
	rt.filo(ExitOK, "", false, "scan")
	run(ExitOK, "add", rt.in("bank"), "*2024*", "tax")
	if err := os.MkdirAll(rt.in("scans/old"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(ExitOK, "add", rt.in("scans/old"), "statements")
	if err := os.Remove(rt.in("scans/old")); err != nil {
		t.Fatal(err)
	}
	run(ExitOK)
	run(ExitOK, "set", rt.in("bank"), "*statement*", "financial")
	run(ExitOK, "remove", rt.in("bank"), "*bank*")
	run(ExitOK, "remove", rt.in("scans/old"))
	run(ExitOK, rt.in("bank"))
	golden(t, "rules.txt", rt.root, out.String())
}

// TestRulesAdd: scenarios A1-A4 through the command.
func TestRulesAdd(t *testing.T) {
	rt := newRuleTest(t)
	bank := rt.in("bank")

	// A1: a dry run changes nothing and shows the files; add dates them and says how many, terminal or not.
	dry := rt.filo(ExitOK, "", false, "rules", "add", bank, "*Statement*", "statements", "--dry-run").stdout
	rt.contains("dry run", dry, "Dry run   bank . *Statement* -> statements (7y) . policy would be set on 3 files . 3 need a "+
		"file_date", "needs a file_date   bank/QuarterlyStatement-Q1.pdf", "Dry run: nothing was changed.")
	rt.contains("nothing added", rt.filo(ExitOK, "", false, "rules").stdout, "Rules   none yet")
	rt.contains("add in a terminal", rt.filo(ExitOK, "", true, "rules", "add", bank, "*Statement*",
		"statements").stdout, "policy set on 3 files", "3 need a file_date",
		"Next: filo export retention "+bank+" -o FILE, fill in file_date, then filo update FILE")
	rt.contains("add, not a terminal", rt.filo(ExitOK, "", false, "rules", "add", rt.in("personal"), "bills").stdout,
		"Done   personal . * -> bills . policy set on 1 file")

	// A2: a policy that does not exist is created with no value, and said so.
	rt.contains("new policy", rt.filo(ExitOK, "", false, "policies").stdout, "bills", "no value")
	// A3: a pattern the directory has, in another case.
	rt.contains("exists", rt.filo(ExitError, "", false, "rules", "add", bank, "*statement*", "x").stderr,
		"filo: bank: already has the rule *statement* (filo rules set changes its policy)")
	rt.lacks("no policy x", rt.filo(ExitOK, "", false, "policies").stdout, "\n  x ")
	// A4: the wrong number of arguments, a directory outside every root, a file, a bad pattern, a bad policy.
	for _, args := range [][]string{{"add", bank}, {"add", bank, "a", "b", "c"}, {"remove"}, {"set", bank}} {
		rt.contains("usage "+strings.Join(args, " "), rt.filo(ExitUsage, "", false,
			append([]string{"rules"}, args...)...).stderr, "quote a pattern with * or spaces")
	}
	rt.contains("outside", rt.filo(ExitError, "", false, "rules", "add", rt.work, "x").stderr, "not inside any root")
	rt.contains("a file", rt.filo(ExitError, "", false, "rules", "add", rt.in("notes.txt"), "x").stderr,
		"is not a directory: a rule is for a directory's files")
	rt.contains("a pattern with /", rt.filo(ExitError, "", false, "rules", "add", bank, "a/b", "x").stderr,
		`"a/b": a pattern is a file name with * and ?, without /`)
	rt.contains("a bad policy", rt.filo(ExitError, "", false, "rules", "add", bank, "*.pdf", "7y").stderr,
		"starting with a letter")
	rt.contains("dry run elsewhere", rt.filo(ExitUsage, "", false, "rules", "remove", bank, "--dry-run").stderr,
		"--dry-run is for rules add")
	rt.contains("unknown", rt.filo(ExitUsage, "", false, "rules", "ad", bank, "x").stderr,
		`unknown rules command "ad"`)
}

// TestRulesSetRemove: scenarios D1, D2 and F1 through the command.
func TestRulesSetRemove(t *testing.T) {
	rt := newRuleTest(t)
	bank := rt.in("bank")
	rt.filo(ExitOK, "", false, "rules", "add", bank, "statements")

	// D1: set is for new files; the files dated keep their policy.
	rt.contains("set", rt.filo(ExitOK, "", false, "rules", "set", bank, "statements").stdout,
		"Unchanged   bank . * -> statements already")
	rt.contains("set", rt.filo(ExitOK, "", false, "rules", "set", bank, "*", "Financial").stdout,
		"Done   bank . * -> financial (was statements)",
		"- 5 files keep the policy it set: the change is for new files",
		"- policy financial was created with no value", "Next: filo policies set financial VALUE")
	// D2: set and remove name the pattern exactly.
	for _, sub := range []string{"set", "remove"} {
		args := []string{"rules", sub, bank, "*.pdf"}
		if sub == "set" {
			args = append(args, "x")
		}
		rt.contains(sub+" a pattern not stored", rt.filo(ExitError, "", false, args...).stderr,
			"filo: bank: no rule *.pdf there (filo rules lists them)")
	}
	rt.contains("remove", rt.filo(ExitOK, "", false, "rules", "remove", bank).stdout,
		"Removed   bank . * -> financial", "- 5 files keep the policy it set")

	// F1: a rule whose directory went is flagged, and can still be listed, changed and removed by its path.
	if err := os.MkdirAll(rt.in("old/scans"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("old/scans"), "statements")
	if err := os.RemoveAll(rt.in("old")); err != nil {
		t.Fatal(err)
	}
	rt.contains("gone", rt.filo(ExitOK, "", false, "rules").stdout,
		"Rules   1 . 1 directory . 1 needs attention", "  old/scans", "[!] no such directory")
	rt.contains("listed by its path", rt.filo(ExitOK, "", false, "rules", rt.in("old")).stdout, "  old/scans")
	rt.contains("set by its path", rt.filo(ExitOK, "", false, "rules", "set", rt.in("old/scans"), "x").stdout,
		"Done   old/scans . * -> x (was statements)")
	rt.contains("add there", rt.filo(ExitError, "", false, "rules", "add", rt.in("old/scans"), "*.pdf",
		"x").stderr, "old/scans")
	rt.contains("removed by its path", rt.filo(ExitOK, "", false, "rules", "remove", rt.in("old/scans")).stdout,
		"Removed   old/scans . * -> x")
}

// TestRulesListScope: filo rules DIR lists the rules at DIR and below, not those above; several roots get a section
// each.
func TestRulesListScope(t *testing.T) {
	rt := newRuleTest(t)
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank"), "statements")
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank/2024"), "*Daily*", "statements")
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("personal"), "statements")
	out := strings.ReplaceAll(rt.filo(ExitOK, "", false, "rules", rt.in("bank/2024")).stdout, rt.root, "<root>")
	rt.contains("scoped", out, "Rules   1 . 1 directory . <root>, under bank/2024/", "  bank/2024", "*Daily*")
	rt.lacks("scoped", out, "  personal", "  bank\n")

	other := rt.work + "/other"
	if err := os.MkdirAll(other+"/inbox", 0o755); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "roots", "add", other)
	rt.filo(ExitOK, "", false, "rules", "add", other, "*.eml", "statements")
	out = rt.filo(ExitOK, "", false, "rules").stdout
	rt.contains("two roots", strings.ReplaceAll(out, rt.root, "<root>"), "Rules   4 . 4 directories . 2 roots",
		"<root>\n", "other\n", "  .\n    *.eml")
}

// TestRulesReviewFindings: the cases found in the code review of slice 2 (2026-11-16).
func TestRulesReviewFindings(t *testing.T) {
	rt := newRuleTest(t)
	bank := rt.in("bank")
	rt.filo(ExitOK, "", false, "rules", "add", bank, "statements")

	// set naming the policy a rule has changes nothing, so it leaves no snapshot.
	snaps := func() int {
		es, err := os.ReadDir(store.SnapshotDir(rt.db))
		if err != nil {
			t.Fatal(err)
		}
		return len(es)
	}
	before := snaps()
	rt.contains("unchanged", rt.filo(ExitOK, "", false, "rules", "set", bank, "Statements").stdout, "Unchanged")
	if after := snaps(); after != before {
		t.Errorf("a set that changed nothing left a snapshot: %d, was %d", after, before)
	}

	// Scenario C4: a hard link is a file of its own, dated by its own directory's rule (docs/design.md, Directory
	// rules); its other link follows the rule of its directory.
	rt.write("docs/a.pdf", "linked")
	if err := os.MkdirAll(rt.in("private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(rt.in("docs/a.pdf"), rt.in("private/a.pdf")); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "scan")
	rt.contains("first link", rt.filo(ExitOK, "", false, "rules", "add", rt.in("docs"), "statements").stdout,
		"policy set on 1 file")
	rt.contains("other link", rt.filo(ExitOK, "", false, "rules", "add", rt.in("private"), "private").stdout,
		"policy set on 1 file")
	rt.contains("each on its own policy", rt.filo(ExitOK, "", false, "rules").stdout,
		"  docs\n    *   statements", "  private\n    *   private")

	// A DIR with no rules at or below it says so, not "none yet".
	rt.contains("none below", rt.filo(ExitOK, "", false, "rules", rt.in("bank/2024")).stdout,
		"none at or below it (the rules above it are listed by filo rules)")
}
