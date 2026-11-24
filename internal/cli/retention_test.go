package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
)

// setNow fixes the keep_until clock for a test.
func setNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// editCSV rewrites an exported retention CSV: edit gets each row by column name.
func editCSV(t *testing.T, path string, edit func(row map[string]string)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\ufeff")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	head := recs[0]
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write(head)
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, h := range head {
			row[h] = rec[i]
		}
		edit(row)
		if row[dropRow] != "" {
			continue
		}
		out := make([]string, len(head))
		for i, h := range head {
			out[i] = row[h]
		}
		w.Write(out)
	}
	w.Flush()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dropRow, set to anything by an edit, leaves the row out of the CSV.
const dropRow = "\x00drop"

// setRetention returns a CSV edit that sets retention on the rows whose name is a key.
func setRetention(values map[string]string) func(map[string]string) {
	return setCells("retention", values)
}

// accepting returns edit after accepting every file_date the export read from a file name (removing its !), as a
// person who checked them does.
func accepting(edit func(map[string]string)) func(map[string]string) {
	return func(row map[string]string) {
		row["file_date"] = strings.TrimPrefix(row["file_date"], "!")
		edit(row)
	}
}

// setCells returns a CSV edit that sets column col on the rows whose name is a key.
func setCells(col string, values map[string]string) func(map[string]string) {
	return func(row map[string]string) {
		if v, ok := values[row["name"]]; ok {
			row[col] = v
		}
	}
}

// terminalInput is standard input that claims to be a terminal, with the answers typed into it.
type terminalInput struct{ *strings.Reader }

func (terminalInput) IsTerminal() bool { return true }

func TestRetentionRoundTrip(t *testing.T) {
	ct := newCLITest(t)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.Local) }
	setNow(t, day(11))
	ctx, db, work, root := ct.ctx, ct.db, ct.work, ct.root
	in, write, filo, contains := ct.in, ct.write, ct.filo, ct.contains
	write("a.txt", "a")
	write("docs/2023-03 invoice.pdf", "b")
	write("docs/c.txt", "c")
	write("📷 Pictures/🏖️ beach.jpg", "sand")
	dates := func() map[string]string {
		t.Helper()
		s, err := store.Open(ctx, store.Options{DBPath: db})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		items, err := s.Files(ctx, today(), store.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, it := range items {
			out[it.Path] = it.KeepUntil + " " + string(it.Status)
		}
		return out
	}
	wantDates := func(when string, want map[string]string) {
		t.Helper()
		got := dates()
		for p, w := range want {
			if got[p] != w {
				t.Errorf("%s: %s is %q, want %q", when, p, got[p], w)
			}
		}
	}

	filo(ExitOK, "", false, "roots", "add", root)
	contains("report", filo(ExitOK, "", false, "report").stdout,
		"[!] 4 need a retention (no retention 4)", "   docs\n", "filo export retention")

	// Export: -o is required, and an existing file is not replaced without a terminal to ask in (TestExportReplace).
	r1 := filepath.Join(work, "r1.csv")
	filo(ExitUsage, "", false, "export", "retention")
	filo(ExitUsage, "", false, "export", "-o", r1)
	contains("export", filo(ExitOK, "", false, "export", "retention", "-o", r1).stdout,
		"Export #1 (retention)   4 files", "  - 4 without a retention, 0 with one\n",
		"  [!] 1 file_date read from the file name, marked !: remove the ! to accept it\n",
		"Next: fill in retention (7y, 18m, forever or a date), then filo update "+r1+"\n")
	contains("export again", filo(ExitError, "", false, "export", "retention", "-o", r1).stderr, "already exists")
	contains("CSV", string(mustRead(t, r1)), "id,export,directory,name,size,modified,file_date,retention,keep_until\n",
		","+root+"/docs,2023-03 invoice.pdf,1,", ",!2023-03-31,,\n", // file_date read from the name, marked
		","+root+"/📷 Pictures,🏖️ beach.jpg,4,")

	// Set retentions: a period from the file_date read from the name and accepted (already past), forever, a fixed
	// date. An impossible date and a period without a file_date are refused with their lines; the other rows still
	// apply.
	editCSV(t, r1, accepting(setRetention(map[string]string{"a.txt": "2027-01-01", "2023-03 invoice.pdf": "3y",
		"c.txt": "2027-02-29", "🏖️ beach.jpg": "Forever"})))
	r := filo(ExitError, "", false, "update", r1, "--dry-run")
	contains("dry run", r.stdout, "Retention update (dry run)   r1.csv . export #1 . 4 rows . "+root,
		"Dry run: nothing was written.", "3 files would be given a retention", "1 file would be past its retention")
	wantDates("after a dry run", map[string]string{"a.txt": " undated"})
	r = filo(ExitError, "", false, "update", r1)
	contains("update", r.stdout, "[ok] 3 files given a retention",
		"[!] 1 file is now past its retention. Check for a typo, such as 7d for 7y:",
		"    retention   until", "    3y          2026-03-31   docs/2023-03 invoice.pdf", "[refused] 1 row refused",
		"Refused: fix these rows in the CSV and run filo update again",
		`[refused] line 4    retention "2027-02-29" is not a retention`, "Nothing has been deleted. Next: filo delete")
	if r.stderr != "" {
		t.Errorf("update wrote to stderr: %q", r.stderr)
	}
	wantDates("after update", map[string]string{"a.txt": "2027-01-01 kept",
		"docs/2023-03 invoice.pdf": "2026-03-31 expired", "docs/c.txt": " undated",
		"📷 Pictures/🏖️ beach.jpg": "forever forever"})

	// The same CSV again changes nothing. Editing it again replaces what it set; a period on a file without a
	// file_date is refused; typing in keep_until is refused.
	contains("update again", filo(ExitError, "", false, "update", r1).stdout, "3 rows unchanged")
	editCSV(t, r1, setRetention(map[string]string{"2023-03 invoice.pdf": "", "c.txt": "7y"}))
	editCSV(t, r1, setCells("file_date", map[string]string{"2023-03 invoice.pdf": ""}))
	editCSV(t, r1, setCells("keep_until", map[string]string{"a.txt": "2030-01-01"}))
	r = filo(ExitError, "", false, "update", r1)
	contains("re-edited", r.stdout, "docs/c.txt: retention 7y needs a file_date",
		`a.txt: keep_until was changed to "2030-01-01", but filo works it out; change retention instead`)
	contains("re-edited, no terminal", r.stdout, "[!] 1 retention left as it was, though the cell is blank (there "+
		"is no terminal")
	editCSV(t, r1, setCells("keep_until", map[string]string{"a.txt": ""}))
	editCSV(t, r1, setCells("file_date", map[string]string{"c.txt": "2026-01-15"}))
	r = filo(ExitOK, "y\n", true, "update", r1)
	contains("re-edited, confirmed", r.stdout, "[!] 1 row has a blank retention where one is stored. Recording "+
		"clears it:", "    3y          2026-03-31   docs/2023-03 invoice.pdf",
		"Clear this retention? The other rows are recorded either way. [y/N/l]", "[ok] 1 file given a retention",
		"[ok] 1 retention cleared")
	wantDates("re-edited", map[string]string{"docs/c.txt": "2033-01-15 kept", "docs/2023-03 invoice.pdf": " undated"})

	// A day later, the re-export shows what was set, with keep_until worked out.
	setNow(t, day(12))
	r2 := filepath.Join(work, "r2.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", r2)
	contains("re-export", string(mustRead(t, r2)), ",2026-01-15,7y,2033-01-15\n", ",,forever,forever\n",
		",,2027-01-01,2027-01-01\n", ",!2023-03-31,,\n") // its file_date was cleared: read from the name again

	// A file past its retention is listed by delete and the report.
	editCSV(t, r2, setRetention(map[string]string{"2023-03 invoice.pdf": "2026-11-10", "a.txt": "2027-02-01"}))
	filo(ExitOK, "", false, "update", r2)
	r = filo(ExitOK, "", false, "delete")
	contains("delete", r.stdout, "Past their retention   1 file", "2026-11-10", "   docs/2023-03 invoice.pdf",
		"Nothing deleted: there is no terminal to ask in")
	contains("report", filo(ExitOK, "", false, "report").stdout, "[!] 1 past its retention: see filo delete",
		"- 2 kept until a later date", "- 1 kept forever")

	// An older export cannot overwrite a retention changed from a later one.
	editCSV(t, r1, setRetention(map[string]string{"a.txt": "2029-01-01"}))
	contains("stale", filo(ExitError, "", false, "update", r1).stdout,
		"a.txt: its retention was changed after export 1; export again")

	// A file changed since the export is refused; once past its retention, a changed file needs a new one.
	r3 := filepath.Join(work, "r3.csv")
	filo(ExitOK, "", false, "export", "retention", in("docs"), "-o", r3)
	write("docs/c.txt", "changed")
	write("docs/2023-03 invoice.pdf", "changed too")
	filo(ExitOK, "", false, "scan")
	editCSV(t, r3, setRetention(map[string]string{"c.txt": "2027-06-30"}))
	contains("changed", filo(ExitError, "", false, "update", r3).stdout,
		"docs/c.txt: changed since export 3; export again to set its retention")
	wantDates("changed", map[string]string{"docs/2023-03 invoice.pdf": "2026-11-10 changed"})
	contains("delete after a change", filo(ExitOK, "", false, "delete").stdout, "No files are past their retention.")

	// A row added in the spreadsheet is refused.
	if err := os.WriteFile(r3, append(mustRead(t, r3), []byte("9999,3,/x,y,1,m,,2027-01-01,\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	contains("added row", filo(ExitError, "", false, "update", r3).stdout,
		"file 9999 is not in export 3 (rows cannot be added)")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpdateNotARetentionCSV(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"FILO_DB": filepath.Join(dir, "filo.db")}
	old := filepath.Join(dir, "old.csv")
	if err := os.WriteFile(old, []byte("id,export,directory,name,size,modified,keep_until\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, context.Background(), env, "update", old)
	if r.code != ExitError || !strings.Contains(r.stderr, "from an older filo; export again with: filo export retention") {
		t.Errorf("code %d\n%s", r.code, r.stderr)
	}
}

// TestUpdateGolden: filo update's whole plain output for the designed scenarios (docs/design.md, Terminal output):
// a retention update with refusals and the typo warning, a dry run, a clear confirmed after listing with l, and a
// duplicates update with refusals and a separate content, confirmed.
func TestUpdateGolden(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
	work, root, filo := ct.work, ct.root, ct.filo
	ct.write("receipts/2023-01-01 coffee.pdf", "coffee")
	ct.write("invoices/2019-03.pdf", "invoice")
	ct.write("notes/old draft.txt", "draft")
	ct.write("tax/2020 return.pdf", "tax")
	filo(ExitOK, "", false, "roots", "add", root)
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", rcsv)
	editCSV(t, rcsv, accepting(setRetention(map[string]string{"2023-01-01 coffee.pdf": "7d", "2019-03.pdf": "2027-02-29",
		"old draft.txt": "7y", "2020 return.pdf": "forever"})))
	golden(t, "update-retention-dry.txt", root, filo(ExitError, "", false, "update", "--dry-run", rcsv).stdout)
	golden(t, "update-retention.txt", root, filo(ExitError, "", false, "update", rcsv).stdout)
	// A new export shows the stored retentions; blanking two clears them: listed, l lists them again in full, then
	// yes.
	r2 := filepath.Join(work, "r2.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", r2)
	editCSV(t, r2, setRetention(map[string]string{"2020 return.pdf": "", "2023-01-01 coffee.pdf": ""}))
	golden(t, "update-retention-clear.txt", root, filo(ExitOK, "l\ny\n", true, "update", r2).stdout)

}

// TestCSVTooLarge: filo update and filo delete FILE.csv refuse a CSV over sheet.MaxSize in one line, without
// reading it (the file is sparse: it takes no disk space).
func TestCSVTooLarge(t *testing.T) {
	for _, cmd := range []string{"update", "delete"} {
		t.Run(cmd, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{"FILO_DB": filepath.Join(dir, "filo.db")}
			big := filepath.Join(dir, "big.csv")
			if err := os.WriteFile(big, []byte("id,export,retention\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(big, sheet.MaxSize+1); err != nil {
				t.Fatal(err)
			}
			r := run(t, context.Background(), env, cmd, big)
			if r.code != ExitError || !strings.Contains(r.stderr, sheet.ErrTooLarge.Error()) {
				t.Errorf("code %d\n%s", r.code, r.stderr)
			}
		})
	}
}
