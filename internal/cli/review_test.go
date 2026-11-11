package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setNow fixes the review clock for a test.
func setNow(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// editCSV rewrites an exported review CSV: edit gets each row (by column name) and its absolute path.
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

func TestReviewRoundTrip(t *testing.T) {
	skipWindows(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local))
	env := map[string]string{"FILO_DB": filepath.Join(t.TempDir(), "filo.db")}
	work := canonicalTemp(t)
	tree := filepath.Join(work, "FiloTest")
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "testtree.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sh := func(cmd string) {
		t.Helper()
		if out, err := exec.Command(bash, script, cmd, tree).CombinedOutput(); err != nil {
			t.Fatalf("testtree.sh %s: %v\n%s", cmd, err, out)
		}
	}
	ctx := context.Background()
	filo := func(wantCode int, args ...string) result {
		t.Helper()
		r := run(t, ctx, env, args...)
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
	asRoot := os.Geteuid() == 0
	in := func(rel string) string { return filepath.Join(tree, filepath.FromSlash(rel)) }

	sh("init")
	emoji := in("📷 Pictures/🏖️ beach.jpg")
	if err := os.MkdirAll(filepath.Dir(emoji), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emoji, []byte("sand"), 0o644); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "roots", "add", tree)
	filo(ExitOK, "scan")
	sh("mutate")
	t.Cleanup(func() { sh("unlock") })
	filo(ExitOK, "scan")

	// The queue: 17 present entries (15 files, 2 symlinks). The possible move and (unless root) private.txt have
	// their own reasons; the rest have no review date.
	undated, unreadable := "15", "unreadable"
	if asRoot {
		undated, unreadable = "16", ""
	}
	out := filo(ExitOK, "review").stdout
	contains("review", out, "Review queue: 17 files", "no review date", undated, "possible moves", "Folders with the most",
		in("notes"), in("📷 Pictures"))
	if unreadable != "" {
		contains("review", out, unreadable)
	}
	contains("report", filo(ExitOK, "report").stdout, "Review queue: 17 files (", "see filo review")

	// Bulk dates: dry run first, then for real. notes/ holds 4 files.
	out = filo(ExitOK, "review", "set-date", in("notes"), "+6m", "--dry-run").stdout
	contains("set-date dry run", out, "would set review date 2027-05-11 on 4 files under "+in("notes"))
	out = filo(ExitOK, "review", "set-date", in("notes"), "+6m").stdout
	contains("set-date", out, "Set review date 2027-05-11 on 4 files", "The review queue now has 13 files.")
	filo(ExitUsage, "review", "set-date", in("notes"), "2026-11-11")
	filo(ExitUsage, "review", "set-date", in("notes"), "11/12/2027")
	contains("review notes", filo(ExitOK, "review", in("notes")).stdout, "Review queue under "+in("notes")+": empty")

	// Export: never over an existing file.
	csvPath := filepath.Join(work, "review 🗂️.csv")
	out = filo(ExitOK, "review", "export", "-o", csvPath).stdout
	contains("export", out, "Exported 13 files to "+csvPath+" (export 1)", "filo review import")
	if r := filo(ExitError, "review", "--db", env["FILO_DB"], "export", "-o", csvPath); !strings.Contains(r.stderr,
		"already exists") {
		t.Errorf("second export over the file: %s", r.stderr)
	}
	if fi, err := os.Stat(csvPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("CSV mode: %v, %v", fi, err)
	}
	data, _ := os.ReadFile(csvPath)
	contains("CSV", string(data), emoji, "possible move of "+in("docs/report-2024.txt")+" (same name, different content)")

	// After the export, img1.bin changes: its row must be refused.
	if err := os.WriteFile(in("photos/img1.bin"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "scan")

	editCSV(t, csvPath, func(row map[string]string) {
		switch row["path"] {
		case emoji, in("docs/receipt-tyre.txt"), in("photos/img1.bin"):
			row["review_date"] = "+1y"
		case in("photos/img1-copy2.bin"):
			row["action"] = "delete"
		case in("archive/report-2024.txt"), in("docs/private.txt"):
			row["action"] = "delete" // a possible move, and unreadable: both refused
		case in("photos/img2-renamed.bin"):
			row["review_date"] = "30/06/2027" // ambiguous: refused
		case in("projects/app/app.csproj"):
			row["review_date"], row["retain_until"] = "2028-01-01", "+7y"
		case in("photos/img1-copy.bin"):
			row["retain_until"] = "+6y" // alone: kept until then, reviewed on that date
		case in("projects/app/src/main.cs"):
			row["reason"] = "+6y" // the wrong column: refused, not skipped
		}
	})

	before := filo(ExitOK, "review").stdout
	r := filo(ExitError, "review", "import", csvPath, "--dry-run")
	contains("dry run", r.stdout, "Dry run: nothing was written.", "Would apply")
	if after := filo(ExitOK, "review").stdout; after != before {
		t.Errorf("dry run changed the queue:\nbefore %s\nafter %s", before, after)
	}

	r = filo(ExitError, "review", "import", csvPath)
	wantErr := []string{
		in("photos/img1.bin") + ": changed since export 1",
		in("archive/report-2024.txt") + ": a possible move: only a review date is accepted",
		`"30/06/2027" is not a date`,
		`reason column was changed to "+6y"`,
	}
	if !asRoot {
		wantErr = append(wantErr, in("docs/private.txt")+": flagged unreadable")
	}
	contains("import stderr", r.stderr, wantErr...)
	// receipt-tyre.txt went missing in the mutate scan, so it is not in the queue at all.
	// On re-import the retain_until-only row matches the recorded floor, so it counts as blank, not imported again.
	applied, queued, already, edited := "Applied 4 decisions: 3 review dates, 1 file marked delete.", "9", "3", "10"
	if asRoot { // private.txt is readable, so its delete is accepted
		applied, queued, already, edited = "Applied 5 decisions: 3 review dates, 2 files marked delete.", "8", "4", "9"
	}
	contains("import stdout", r.stdout, applied, "filo deletes nothing", "left blank",
		"1 file has only a retain_until date: kept until then, when it comes back for review.")
	for _, l := range strings.Split(strings.TrimSpace(r.stderr), "\n") {
		if !strings.HasPrefix(l, "filo: review 🗂️.csv line ") {
			t.Errorf("refusal line not in the expected form: %q", l)
		}
	}

	// Nothing on disk was touched: the file marked delete is still there.
	if _, err := os.Stat(in("photos/img1-copy2.bin")); err != nil {
		t.Errorf("a file marked delete was touched: %v", err)
	}
	out = filo(ExitOK, "review").stdout
	contains("queue after import", out, "Review queue: "+queued+" files")

	// The same CSV again: already imported, and the refusals repeat.
	r = filo(ExitError, "review", "import", csvPath)
	contains("re-import", r.stdout, "Applied 0 decisions", already+" rows already imported")

	// Editing the file marked delete brings it back as changed since review.
	if err := os.WriteFile(in("photos/img1-copy2.bin"), []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "scan")
	contains("queue after edit", filo(ExitOK, "review").stdout, "Review queue: "+edited+" files", "changed since review")

	// A year on, the dated files are due.
	setNow(t, time.Date(2027, 11, 12, 12, 0, 0, 0, time.Local))
	contains("queue a year on", filo(ExitOK, "review").stdout, "review due")
}

func TestReviewUsage(t *testing.T) {
	db := filepath.Join(t.TempDir(), "filo.db")
	tests := []struct {
		name     string
		args     []string
		code     int
		inStdout string
		inStderr string
	}{
		{"help review", []string{"help", "review"}, ExitOK, "set-date", ""},
		{"review -h", []string{"review", "-h"}, ExitOK, "retain_until", ""},
		{"empty register", []string{"--db", db, "review"}, ExitOK, "Review queue: empty", ""},
		{"export of an empty queue", []string{"--db", db, "review", "export"}, ExitOK, "", "queue is empty"},
		{"import needs a file", []string{"--db", db, "review", "import"}, ExitUsage, "", "exactly one FILE"},
		{"import of a missing file", []string{"--db", db, "review", "import", "/nonexistent.csv"}, ExitError, "", "open "},
		{"set-date needs two arguments", []string{"--db", db, "review", "set-date", "/x"}, ExitUsage, "", "FOLDER and a DATE"},
		{"set-date with a bad date", []string{"--db", db, "review", "set-date", "/x", "soon"}, ExitUsage, "", "DATE"},
		{"two paths", []string{"--db", db, "review", "a", "b"}, ExitUsage, "", "at most one PATH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, context.Background(), nil, tc.args...)
			if r.code != tc.code || !strings.Contains(r.stdout, tc.inStdout) || !strings.Contains(r.stderr, tc.inStderr) {
				t.Errorf("code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
		})
	}
}

func TestImportNotACSV(t *testing.T) {
	dir := t.TempDir()
	db, bad := filepath.Join(dir, "filo.db"), filepath.Join(dir, "notes.csv")
	if err := os.WriteFile(bad, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, context.Background(), nil, "--db", db, "review", "import", bad)
	if r.code != ExitError || !strings.Contains(r.stderr, "not a filo review CSV") {
		t.Errorf("code %d stderr %s", r.code, r.stderr)
	}
}

func TestSplitSubcommand(t *testing.T) {
	tests := []struct {
		args []string
		sub  string
		rest []string
	}{
		{[]string{"export", "-o", "f.csv"}, "export", []string{"-o", "f.csv"}},
		{[]string{"--db", "x.db", "export", "-o", "f.csv"}, "export", []string{"--db", "x.db", "-o", "f.csv"}},
		{[]string{"--db=x.db", "import", "f.csv"}, "import", []string{"--db=x.db", "f.csv"}},
		{[]string{"--dry-run", "set-date", "/p", "+6m"}, "set-date", []string{"--dry-run", "/p", "+6m"}},
		{[]string{"/home/a/export"}, "", []string{"/home/a/export"}},
		{[]string{"docs", "export"}, "", []string{"docs", "export"}},
		{[]string{"--", "export"}, "", []string{"--", "export"}},
		{[]string{"--db", "export"}, "", []string{"--db", "export"}},
		{nil, "", nil},
	}
	for _, tc := range tests {
		sub, rest := splitSubcommand(tc.args)
		if sub != tc.sub || strings.Join(rest, " ") != strings.Join(tc.rest, " ") {
			t.Errorf("splitSubcommand(%q) = %q, %q; want %q, %q", tc.args, sub, rest, tc.sub, tc.rest)
		}
	}
}

func TestImportNothingFilledIn(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.Local))
	dir := canonicalTemp(t)
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FILO_DB": filepath.Join(dir, "filo.db")}
	ctx := context.Background()
	csvPath := filepath.Join(dir, "r.csv")
	for _, args := range [][]string{{"roots", "add", tree}, {"scan"}, {"review", "export", "-o", csvPath}} {
		if r := run(t, ctx, env, args...); r.code != ExitOK {
			t.Fatalf("filo %v: %d %s", args, r.code, r.stderr)
		}
	}
	r := run(t, ctx, env, "review", "import", csvPath)
	if r.code != ExitOK || !strings.Contains(r.stdout, "1 row left blank") ||
		!strings.Contains(r.stdout, "No row has a review_date, action or retain_until filled in") {
		t.Errorf("code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
