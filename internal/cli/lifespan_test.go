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

	"github.com/alansav/filo/internal/store"
)

// setNow fixes the keep_until clock for a test.
func setNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// editCSV rewrites an exported lifespan CSV: edit gets each row by column name.
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

// setRetention returns a CSV edit that sets retention on the rows whose name is a key.
func setRetention(values map[string]string) func(map[string]string) {
	return setCells("retention", values)
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

func TestLifespanRoundTrip(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.Local) }
	setNow(t, day(11))
	work := canonicalTemp(t)
	db := filepath.Join(work, "filo.db")
	env := map[string]string{"FILO_DB": db}
	root := filepath.Join(work, "home")
	in := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	write := func(rel, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(in(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in(rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "a")
	write("docs/2023-03 invoice.pdf", "b")
	write("docs/c.txt", "c")
	write("📷 Pictures/🏖️ beach.jpg", "sand")
	ctx := context.Background()
	filo := func(wantCode int, stdin string, terminal bool, args ...string) result {
		t.Helper()
		var r result
		if terminal {
			r = runIn(t, ctx, env, terminalInput{strings.NewReader(stdin)}, args...)
		} else {
			r = runIn(t, ctx, env, strings.NewReader(stdin), args...)
		}
		if r.code != wantCode {
			t.Fatalf("filo %v: code %d, want %d\n%s%s", args, r.code, wantCode, r.stdout, r.stderr)
		}
		return r
	}
	contains := func(what, out string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s lacks %q:\n%s", what, w, out)
			}
		}
	}
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
		"Needing a retention: 4 files (no retention 4)", root+"/docs", "filo export lifespan")

	// Export: -o is required, and an existing file is never overwritten.
	r1 := filepath.Join(work, "r1.csv")
	filo(ExitUsage, "", false, "export", "lifespan")
	filo(ExitUsage, "", false, "export", "-o", r1)
	contains("export", filo(ExitOK, "", false, "export", "lifespan", "-o", r1).stdout,
		"Exported 4 files to "+r1+" (export 1), 4 without a retention.",
		"file_date was filled in from the name of 1 file", "filo update "+r1)
	contains("export again", filo(ExitError, "", false, "export", "lifespan", "-o", r1).stderr, "already exists")
	contains("CSV", string(mustRead(t, r1)), "id,export,directory,name,size,modified,file_date,retention,keep_until\n",
		","+root+"/docs,2023-03 invoice.pdf,1,", ",2023-03-31,,\n", // file_date read from the name
		","+root+"/📷 Pictures,🏖️ beach.jpg,4,")

	// Set retentions: a period from the suggested file_date (already past), forever, a fixed date. An impossible
	// date and a period without a file_date are refused with their lines; the other rows still apply.
	editCSV(t, r1, setRetention(map[string]string{"a.txt": "2027-01-01", "2023-03 invoice.pdf": "3y",
		"c.txt": "2027-02-29", "🏖️ beach.jpg": "Forever"}))
	r := filo(ExitError, "", false, "update", r1, "--dry-run")
	contains("dry run", r.stdout, "Dry run: nothing was written.", "Would update 3 files",
		"1 file would be past their retention")
	wantDates("after a dry run", map[string]string{"a.txt": " undated"})
	r = filo(ExitError, "", false, "update", r1)
	contains("update", r.stdout, "Updated 3 files, cleared 0 retentions.",
		"1 file is past their retention (check for a typo, such as 7d for 7y); see filo delete:",
		"  3y (until 2026-03-31)  "+in("docs/2023-03 invoice.pdf"), "1 row refused")
	contains("update", r.stderr, `r1.csv line 4: retention "2027-02-29" is not a retention`)
	wantDates("after update", map[string]string{"a.txt": "2027-01-01 kept",
		"docs/2023-03 invoice.pdf": "2026-03-31 expired", "docs/c.txt": " undated",
		"📷 Pictures/🏖️ beach.jpg": "forever forever"})

	// The same CSV again changes nothing. Editing it again replaces what it set; a period on a file without a
	// file_date is refused; typing in keep_until is refused.
	contains("update again", filo(ExitError, "", false, "update", r1).stdout, "Updated 0 files")
	editCSV(t, r1, setRetention(map[string]string{"2023-03 invoice.pdf": "", "c.txt": "7y"}))
	editCSV(t, r1, setCells("file_date", map[string]string{"2023-03 invoice.pdf": ""}))
	editCSV(t, r1, setCells("keep_until", map[string]string{"a.txt": "2030-01-01"}))
	r = filo(ExitError, "", false, "update", r1)
	contains("re-edited", r.stderr, in("docs/c.txt")+": retention 7y needs a file_date",
		in("a.txt")+`: keep_until was changed to "2030-01-01", but filo works it out; change retention instead`)
	contains("re-edited, no terminal", r.stdout, "Left 1 retention whose cell is blank (there is no terminal")
	editCSV(t, r1, setCells("keep_until", map[string]string{"a.txt": ""}))
	editCSV(t, r1, setCells("file_date", map[string]string{"c.txt": "2026-01-15"}))
	r = filo(ExitOK, "y\n", true, "update", r1)
	contains("re-edited, confirmed", r.stdout, "1 file has a retention but a blank cell",
		"3y (until 2026-03-31)  "+in("docs/2023-03 invoice.pdf"), "Clear the retention of 1 file? [y/n]",
		"Updated 1 file, cleared 1 retention.")
	wantDates("re-edited", map[string]string{"docs/c.txt": "2033-01-15 kept", "docs/2023-03 invoice.pdf": " undated"})

	// A day later, the re-export shows what was set, with keep_until worked out.
	setNow(t, day(12))
	r2 := filepath.Join(work, "r2.csv")
	filo(ExitOK, "", false, "export", "lifespan", "-o", r2)
	contains("re-export", string(mustRead(t, r2)), ",2026-01-15,7y,2033-01-15\n", ",,forever,forever\n",
		",,2027-01-01,2027-01-01\n", ",2023-03-31,,\n")

	// A file past its retention is listed by delete and the report.
	editCSV(t, r2, setRetention(map[string]string{"2023-03 invoice.pdf": "2026-11-10", "a.txt": "2027-02-01"}))
	filo(ExitOK, "", false, "update", r2)
	r = filo(ExitOK, "", false, "delete")
	contains("delete", r.stdout, "Past their retention: 1 file", "2026-11-10", in("docs/2023-03 invoice.pdf"),
		"filo deletes nothing")
	contains("report", filo(ExitOK, "", false, "report").stdout, "Past their retention: 1 file; see filo delete",
		"Kept until a later date: 2 files", "Kept forever: 1 file")

	// An older export cannot overwrite a retention changed from a later one.
	editCSV(t, r1, setRetention(map[string]string{"a.txt": "2029-01-01"}))
	contains("stale", filo(ExitError, "", false, "update", r1).stderr,
		in("a.txt")+": its retention was changed after export 1; export again")

	// A file changed since the export is refused; once past its retention, a changed file needs a new one.
	r3 := filepath.Join(work, "r3.csv")
	filo(ExitOK, "", false, "export", "lifespan", in("docs"), "-o", r3)
	write("docs/c.txt", "changed")
	write("docs/2023-03 invoice.pdf", "changed too")
	filo(ExitOK, "", false, "scan")
	editCSV(t, r3, setRetention(map[string]string{"c.txt": "2027-06-30"}))
	contains("changed", filo(ExitError, "", false, "update", r3).stderr,
		in("docs/c.txt")+": changed since export 3; export again to set its retention")
	wantDates("changed", map[string]string{"docs/2023-03 invoice.pdf": "2026-11-10 changed"})
	contains("delete after a change", filo(ExitOK, "", false, "delete").stdout, "No files are past their retention.")

	// A row added in the spreadsheet is refused.
	if err := os.WriteFile(r3, append(mustRead(t, r3), []byte("9999,3,/x,y,1,m,,2027-01-01,\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	contains("added row", filo(ExitError, "", false, "update", r3).stderr, "file 9999 is not in export 3 (rows cannot be added)")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpdateNotALifespanCSV(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"FILO_DB": filepath.Join(dir, "filo.db")}
	old := filepath.Join(dir, "old.csv")
	if err := os.WriteFile(old, []byte("id,export,directory,name,size,modified,keep_until\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, context.Background(), env, "update", old)
	if r.code != ExitError || !strings.Contains(r.stderr, "from an older filo; export again with: filo export lifespan") {
		t.Errorf("code %d\n%s", r.code, r.stderr)
	}
}
