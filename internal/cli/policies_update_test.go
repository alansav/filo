package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// TestUpdatePolicies: policy names in the retention column (docs/design.md, Named retention policies): filo update
// lists the policies a CSV would create and asks once; declined or without a terminal, nothing is recorded; a dry
// run lists them; an export shows a file's policy, and an untouched row of it is unchanged.
func TestUpdatePolicies(t *testing.T) {
	setup := func(t *testing.T) (*cliTest, string) {
		ct := newCLITest(t)
		setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
		for _, rel := range []string{"bills/2019-03.pdf", "bills/2019-04.pdf", "pay/2020-01.pdf", "notes.txt"} {
			ct.write(rel, rel)
		}
		ct.filo(ExitOK, "", false, "roots", "add", ct.root)
		csv := filepath.Join(ct.work, "r.csv")
		ct.filo(ExitOK, "", false, "export", "-o", csv)
		editCSV(t, csv, setRetention(map[string]string{"2019-03.pdf": "bils", "2019-04.pdf": "Bils",
			"2020-01.pdf": "payslip", "notes.txt": "forever"}))
		return ct, csv
	}

	t.Run("confirmed", func(t *testing.T) {
		ct, csv := setup(t)
		out := ct.filo(ExitOK, "y\n", true, "update", csv).stdout
		ct.contains("update", out, "New policies", "2 . bils, payslip", "bils      2 rows", "payslip   1 row",
			"they have no value yet: their files need a retention until filo policies set NAME VALUE",
			"Apply the update? Nothing is recorded otherwise. [y/N/l]", "[ok] 2 policies created: bils, payslip", "[ok] 1 file given a retention",
			"[!] 3 files put on a policy with no value yet: they need a retention until it has one")
		golden(t, "update-policies.txt", ct.root, out)

		// The export shows each file's policy (2019-03.pdf's file_date was read from its name); an untouched CSV of
		// it is unchanged, not refused.
		csv2 := filepath.Join(ct.work, "r2.csv")
		ct.filo(ExitOK, "", false, "export", "-o", csv2)
		got := map[string]string{}
		for _, r := range readRows(t, csv2) {
			got[r["name"]] = r["retention"]
		}
		if got["2019-03.pdf"] != "bils" || got["2020-01.pdf"] != "payslip" || got["notes.txt"] != "forever" {
			t.Errorf("exported retentions = %v", got)
		}
		ct.contains("untouched", ct.filo(ExitOK, "", false, "update", csv2).stdout, "4 rows unchanged")
	})

	t.Run("declined", func(t *testing.T) {
		ct, csv := setup(t)
		out := ct.filo(ExitOK, "n\n", true, "update", csv).stdout
		ct.contains("declined", out, "[!] nothing recorded: not confirmed")
		ct.lacks("declined", out, "given a retention")
	})

	t.Run("no terminal", func(t *testing.T) {
		ct, csv := setup(t)
		ct.contains("no terminal", ct.filo(ExitError, "", false, "update", csv).stdout,
			"[!] nothing recorded: there is no terminal to ask in; run filo update in a terminal, or with -y")
	})

	t.Run("dry run", func(t *testing.T) {
		ct, csv := setup(t)
		ct.contains("dry run", ct.filo(ExitOK, "", false, "update", "--dry-run", csv).stdout,
			"New policies", "2 . bils, payslip", "- 2 policies would be created",
			"Dry run: nothing was written.")
		ct.lacks("dry run", ct.filo(ExitOK, "", false, "update", "--dry-run", csv).stdout, "Apply the update?")
	})

	t.Run("a cell that is not a retention or a policy name", func(t *testing.T) {
		ct, csv := setup(t)
		editCSV(t, csv, setRetention(map[string]string{"notes.txt": "bills!"}))
		ct.contains("refused", ct.filo(ExitError, "y\n", true, "update", csv).stdout, `"bills!" is not a retention`,
			"or a policy name")
	})

	t.Run("a period policy counts from a file_date read from the name once its ! is removed", func(t *testing.T) {
		ct, csv := setup(t)
		addPolicy(t, ct, "bills", "7y")
		// 2019-03.pdf's file_date, !2019-03-31, was read from its name: left marked, the period has nothing to
		// count from; accepted, it counts from it.
		names := map[string]string{"2019-03.pdf": "bills", "2019-04.pdf": "", "2020-01.pdf": "", "notes.txt": ""}
		editCSV(t, csv, setRetention(names))
		ct.contains("marked", ct.filo(ExitError, "", false, "update", csv).stdout,
			"file_date !2019-03-31 was read from the file name: remove the ! to accept it")
		editCSV(t, csv, setCells("file_date", map[string]string{"2019-03.pdf": "2019-03-31"}))
		ct.contains("update", ct.filo(ExitOK, "", false, "update", csv).stdout, "[ok] 1 file given a retention")
		csv2 := filepath.Join(ct.work, "r2.csv")
		ct.filo(ExitOK, "", false, "export", "-o", csv2)
		for _, r := range readRows(t, csv2) {
			if r["name"] == "2019-03.pdf" && (r["file_date"] != "2019-03-31" || r["keep_until"] != "2026-03-31") {
				t.Errorf("2019-03.pdf: file_date %q, keep_until %q; want 2019-03-31, 2026-03-31", r["file_date"],
					r["keep_until"])
			}
		}
	})
}

// addPolicy adds a policy through the store (filo policies is the next slice).
func addPolicy(t *testing.T, ct *cliTest, name, value string) {
	t.Helper()
	s, err := store.Open(ct.ctx, store.Options{DBPath: ct.db})
	if err != nil {
		t.Fatal(err)
	}
	err = s.AddPolicy(ct.ctx, name, value, time.Now())
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestDeletePromptPolicies: filo delete's new-retention prompt with a policy: one with no value yet says so (it is
// not "still past"); an answer that is neither a retention nor a name mentions policy names.
func TestDeletePromptPolicies(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	ct.write("a.txt", "a")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	csv := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "-o", csv)
	editCSV(t, csv, setRetention(map[string]string{"a.txt": "2026-11-01"}))
	ct.filo(ExitOK, "", false, "update", csv)
	addPolicy(t, ct, "payslip", "")
	out := ct.filo(ExitOK, "s\nr\nbills 2\npayslip\n", true, "delete").stdout
	ct.contains("prompt", out, `"bills 2" is not a retention`, "or a policy name",
		"on policy payslip, which has no value yet: the file needs a retention until it gets one")
	ct.lacks("prompt", out, "still past")
}
