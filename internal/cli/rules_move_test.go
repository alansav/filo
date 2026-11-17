package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filo rules move and copy (docs/design.md, Lifespan, Directory rules; slice 4): scenarios E1-E3 through the command.

// TestRulesMoveGolden: the output of a session: a rename on disk and the move that follows it (with a dry run
// first), a copy for a new year, and a move refused because the directory moved to has rules.
func TestRulesMoveGolden(t *testing.T) {
	rt := newRuleTest(t)
	var out strings.Builder
	run := func(code int, args ...string) {
		r := rt.filo(code, "", false, append([]string{"rules"}, args...)...)
		out.WriteString("$ filo rules " + strings.Join(args, " ") + "\n" + r.stdout + r.stderr + "\n")
	}
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank"), "*Statement*", "statements")
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank/2024"), "tax")
	if err := os.Rename(rt.in("bank"), rt.in("banking")); err != nil {
		t.Fatal(err)
	}
	rt.write("banking/AnnualStatement.pdf", "new since the rename")
	rt.filo(ExitOK, "", false, "scan")
	run(ExitOK)
	run(ExitOK, "move", rt.in("bank"), rt.in("banking"), "--dry-run")
	run(ExitOK, "move", rt.in("bank"), rt.in("banking"))
	rt.write("banking/2025/a.pdf", "2025")
	rt.filo(ExitOK, "", false, "scan")
	run(ExitOK, "copy", rt.in("banking/2024"), rt.in("banking/2025"))
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("personal/home"), "*.pdf", "bills")
	run(ExitError, "move", rt.in("banking"), rt.in("personal"))
	run(ExitOK)
	golden(t, "rules-move.txt", rt.root, out.String())
}

// TestRulesMoveCopy: E1-E3, a dry run, another root and the arguments.
func TestRulesMoveCopy(t *testing.T) {
	rt := newRuleTest(t)
	bank := rt.in("bank")
	rt.filo(ExitOK, "", false, "rules", "add", bank, "*Statement*", "statements")
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank/2024"), "tax")
	list := func() string { return rt.filo(ExitOK, "", false, "rules").stdout }

	// E1: after a rename on disk, both rules move from the old path, and the flag goes.
	if err := os.Rename(bank, rt.in("banking")); err != nil {
		t.Fatal(err)
	}
	rt.write("banking/AnnualStatement.pdf", "new")
	rt.filo(ExitOK, "", false, "scan")
	if out := list(); strings.Count(out, "[!] no such directory") != 2 {
		t.Errorf("both directories not flagged:\n%s", out)
	}
	before := list()
	rt.contains("dry run", rt.filo(ExitOK, "", false, "rules", "move", bank, rt.in("banking"), "--dry-run").stdout,
		"Dry run   would move 2 rules from bank to banking . policy would be set on 1 file . 1 needs a file_date",
		"needs a file_date   banking/AnnualStatement.pdf", "Dry run: nothing was changed.")
	if after := list(); after != before {
		t.Errorf("a dry run changed the rules:\n%s\nwas\n%s", after, before)
	}
	rt.contains("moved", rt.filo(ExitOK, "", false, "rules", "move", bank, rt.in("banking")).stdout,
		"Done   moved 2 rules from bank to banking . policy set on 1 file . 1 needs a file_date",
		"Next: filo export retention "+rt.in("banking")+" -o FILE, fill in file_date, then filo update FILE")
	out := list()
	rt.contains("at the new place", out, "  banking\n    *Statement*", "  banking/2024\n    *  ")
	rt.lacks("flag gone", out, "no such directory", "  bank\n")

	// E2: a copy to a new directory sets the policy of its files with no retention; OLD keeps its rules.
	rt.write("banking/2025/a.pdf", "a")
	rt.write("banking/2025/b.pdf", "b")
	rt.filo(ExitOK, "", false, "scan")
	rt.contains("copied", rt.filo(ExitOK, "", false, "rules", "copy", rt.in("banking/2024"),
		rt.in("banking/2025")).stdout, "Done   copied 1 rule from banking/2024 to banking/2025 . policy set on 2 files")
	rt.contains("both", list(), "  banking/2024\n    *             tax",
		"  banking/2025\n    *             tax")

	// E3: refused, with nothing changed: NEW or a directory below it has rules (listed); NEW is OLD or inside it;
	// OLD without rules; NEW not on disk.
	before = list()
	r := rt.filo(ExitError, "", false, "rules", "move", rt.in("banking/2024"), rt.in("banking"))
	rt.contains("rules there", r.stdout, "[!] banking has rules already", "  banking\n    *Statement*   statements",
		"  banking/2025\n    *             tax")
	rt.lacks("not the rules moved", r.stdout, "banking/2024")
	rt.contains("rules there", r.stderr,
		"filo: banking: 2 rules at or below it already: remove them first, or add the rules by hand")
	rt.contains("copy up", rt.filo(ExitError, "", false, "rules", "copy", rt.in("banking/2024"),
		rt.in("banking")).stderr, "filo: banking is above banking/2024: a copy there would sit next to the rules it "+
		"copies (copy to another directory)")
	rt.contains("inside", rt.filo(ExitError, "", false, "rules", "move", rt.in("banking"),
		rt.in("banking/2025")).stderr, "filo: banking/2025 is banking or inside it: the rules go to another directory")
	rt.contains("itself", rt.filo(ExitError, "", false, "rules", "copy", rt.in("banking"), rt.in("banking")).stderr,
		"filo: banking is banking or inside it")
	rt.contains("no rules", rt.filo(ExitError, "", false, "rules", "move", rt.in("personal"), rt.in("banking")).stderr,
		"filo: personal: no rules at or below it (filo rules lists them)")
	rt.contains("NEW not there", rt.filo(ExitError, "", false, "rules", "move", rt.in("banking"),
		rt.in("nowhere")).stderr, "nowhere")
	if after := list(); after != before {
		t.Errorf("a refused move changed the rules:\n%s\nwas\n%s", after, before)
	}

	// A move to a directory next to it: the rules leave OLD.
	if err := os.MkdirAll(rt.in("banking/2026"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "rules", "move", rt.in("banking/2025"), rt.in("banking/2026"))
	out = list()
	rt.contains("moved on", out, "  banking/2026\n")
	rt.lacks("moved on", out, "  banking/2025\n")

	// Another root: the rules become that root's, named in full.
	other := filepath.Join(rt.work, "other")
	if err := os.MkdirAll(filepath.Join(other, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "roots", "add", other)
	rt.contains("another root", rt.filo(ExitOK, "", false, "rules", "move", rt.in("banking/2026"),
		filepath.Join(other, "inbox")).stdout, "Done   moved 1 rule from "+rt.in("banking/2026")+" to "+
		filepath.Join(other, "inbox"))
	rt.contains("in the other root", list(), "  inbox\n    *")

	// The arguments.
	for _, args := range [][]string{{"move", bank}, {"copy", bank, "a", "b"}, {"move"}} {
		rt.contains("usage "+strings.Join(args, " "), rt.filo(ExitUsage, "", false,
			append([]string{"rules"}, args...)...).stderr, "move OLD NEW [--dry-run]")
	}
	rt.contains("dry run elsewhere", rt.filo(ExitUsage, "", false, "rules", "set", bank, "x", "--dry-run").stderr,
		"--dry-run is for rules add, move and copy")
	rt.contains("outside", rt.filo(ExitError, "", false, "rules", "copy", rt.in("banking"), rt.work).stderr,
		"not inside any root")
}

// TestRulesMoveReviewFindings: the cases found in the code review of slice 4 (2026-11-17).
func TestRulesMoveReviewFindings(t *testing.T) {
	rt := newRuleTest(t)
	rt.filo(ExitOK, "", false, "rules", "add", rt.in("bank/2024"), "tax")

	// A ~ is named with the argument it is in.
	rt.contains("tilde", rt.filo(ExitError, "", false, "rules", "move", rt.in("bank"), "~/x").stderr, "filo: NEW ~/x")

	// The rules in the way at the root itself are named "." as filo rules names them.
	rt.filo(ExitOK, "", false, "rules", "add", rt.root, "*.txt", "statements")
	r := rt.filo(ExitError, "", false, "rules", "move", rt.in("bank/2024"), rt.root)
	rt.contains("root named .", r.stdout, "[!] "+rt.root+" has rules already", "  .\n    *.txt")

	// In another root, the rules in the way are named in full.
	other := filepath.Join(rt.work, "other")
	if err := os.MkdirAll(filepath.Join(other, "inbox", "2024"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt.filo(ExitOK, "", false, "roots", "add", other)
	rt.filo(ExitOK, "", false, "rules", "add", filepath.Join(other, "inbox", "2024"), "statements")
	r = rt.filo(ExitError, "", false, "rules", "copy", rt.in("bank/2024"), filepath.Join(other, "inbox"))
	rt.contains("in full", r.stdout, "  "+filepath.Join(other, "inbox", "2024")+"\n    *")

	// A rule whose policy has no value says so, and how to give it one.
	rt.contains("no value", rt.filo(ExitOK, "", false, "rules", "copy", rt.in("bank/2024"), rt.in("personal")).stdout,
		"- policy tax has no value: its files need a retention until it has one", "Next: filo policies set tax VALUE")
}
