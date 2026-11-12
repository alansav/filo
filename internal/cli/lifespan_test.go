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

// setDates returns a CSV edit that sets keep_until on the rows whose name is a key.
func setDates(dates map[string]string) func(map[string]string) {
	return func(row map[string]string) {
		if d, ok := dates[row["name"]]; ok {
			row["keep_until"] = d
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
		"Needing a keep_until date: 4 files (no keep_until 4)", root+"/docs", "filo export lifespan")

	// Export: -o is required, and an existing file is never overwritten.
	r1 := filepath.Join(work, "r1.csv")
	filo(ExitUsage, "", false, "export", "lifespan")
	filo(ExitUsage, "", false, "export", "-o", r1)
	contains("export", filo(ExitOK, "", false, "export", "lifespan", "-o", r1).stdout,
		"Exported 4 files to "+r1+" (export 1), 4 without a keep_until.", "filo update "+r1)
	contains("export again", filo(ExitError, "", false, "export", "lifespan", "-o", r1).stderr, "already exists")
	data, err := os.ReadFile(r1)
	if err != nil {
		t.Fatal(err)
	}
	contains("CSV", string(data), "id,export,directory,name,size,modified,keep_until\n",
		","+root+"/docs,2023-03 invoice.pdf,1,", ","+root+"/📷 Pictures,🏖️ beach.jpg,4,")

	// Set dates; an impossible date is refused with its line, and the other rows still apply.
	editCSV(t, r1, setDates(map[string]string{"a.txt": "2027-01-01", "2023-03 invoice.pdf": "2026-11-12",
		"c.txt": "2027-02-29"}))
	r := filo(ExitError, "", false, "update", r1, "--dry-run")
	contains("dry run", r.stdout, "Dry run: nothing was written.", "Would set 2 keep_until dates")
	wantDates("after a dry run", map[string]string{"a.txt": " undated"})
	r = filo(ExitError, "", false, "update", r1)
	contains("update", r.stdout, "Set 2 keep_until dates, cleared 0 dates.", "1 row unchanged",
		"1 row refused")
	contains("update", r.stderr, `r1.csv line 4: keep_until "2027-02-29" is not a date`)
	wantDates("after update", map[string]string{"a.txt": "2027-01-01 kept",
		"docs/2023-03 invoice.pdf": "2026-11-12 kept", "docs/c.txt": " undated"})

	// The same CSV again changes nothing; editing it again replaces the dates it set.
	contains("update again", filo(ExitError, "", false, "update", r1).stdout, "Set 0 keep_until dates")
	editCSV(t, r1, setDates(map[string]string{"a.txt": "2028-01-01", "c.txt": "2026-11-11"}))
	r = filo(ExitError, "", false, "update", r1)
	contains("re-edited", r.stdout, "Set 1 keep_until date")
	contains("re-edited", r.stderr, "keep_until 2026-11-11 is not after today")
	wantDates("re-edited", map[string]string{"a.txt": "2028-01-01 kept"})

	// A day later the invoice is past its date: a deletion candidate, listed by delete and the report.
	setNow(t, day(13))
	r = filo(ExitOK, "", false, "delete")
	contains("delete", r.stdout, "Past their keep_until date: 1 file", "2026-11-12", in("docs/2023-03 invoice.pdf"),
		"filo deletes nothing")
	contains("report", filo(ExitOK, "", false, "report").stdout, "Past keep_until: 1 file; see filo delete",
		"Kept until a later date: 1 file")

	// A blank cell clears a date, but only when confirmed in a terminal.
	r2 := filepath.Join(work, "r2.csv")
	filo(ExitOK, "", false, "export", "lifespan", "-o", r2)
	editCSV(t, r2, setDates(map[string]string{"a.txt": ""}))
	contains("no terminal", filo(ExitOK, "", false, "update", r2).stdout,
		"Left 1 date whose keep_until cell is blank (there is no terminal to ask in")
	r = filo(ExitOK, "n\n", true, "update", r2)
	contains("declined", r.stdout, "1 file has a keep_until date but a blank cell", "2028-01-01  "+in("a.txt"),
		"Clear the keep_until date of 1 file? [y/n]", "Left 1 date whose keep_until cell is blank (not confirmed)")
	wantDates("declined", map[string]string{"a.txt": "2028-01-01 kept"})
	contains("confirmed", filo(ExitOK, "y\n", true, "update", r2).stdout, "cleared 1 date")
	wantDates("confirmed", map[string]string{"a.txt": " undated"})

	// An older export cannot overwrite a date changed from a later one.
	editCSV(t, r1, setDates(map[string]string{"a.txt": "2029-01-01"}))
	contains("stale", filo(ExitError, "", false, "update", r1).stderr,
		in("a.txt")+": its keep_until was changed after export 1; export again")

	// A file changed since the export is refused; once past its date, a changed file needs a new date.
	r3 := filepath.Join(work, "r3.csv")
	filo(ExitOK, "", false, "export", "lifespan", in("docs"), "-o", r3)
	write("docs/c.txt", "changed")
	write("docs/2023-03 invoice.pdf", "changed too")
	filo(ExitOK, "", false, "scan")
	editCSV(t, r3, setDates(map[string]string{"c.txt": "2027-06-30"}))
	contains("changed", filo(ExitError, "", false, "update", r3).stderr,
		in("docs/c.txt")+": changed since export 3; export again to set its date")
	wantDates("changed", map[string]string{"docs/2023-03 invoice.pdf": "2026-11-12 changed"})
	contains("delete after a change", filo(ExitOK, "", false, "delete").stdout, "No files are past their keep_until date.")

	// A row added in the spreadsheet is refused.
	if err := os.WriteFile(r3, append(mustRead(t, r3), []byte("9999,3,/x,y,1,m,2027-01-01\n")...), 0o600); err != nil {
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
	if err := os.WriteFile(old, []byte("id,export,reason,path,size,modified,review_date,action,retain_until\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, context.Background(), env, "update", old)
	if r.code != ExitError || !strings.Contains(r.stderr, "export again with: filo export lifespan") {
		t.Errorf("code %d\n%s", r.code, r.stderr)
	}
}
