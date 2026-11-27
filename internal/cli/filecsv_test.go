package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// edits returns a CSV edit that applies each edit in turn.
func edits(es ...func(map[string]string)) func(map[string]string) {
	return func(row map[string]string) {
		for _, e := range es {
			e(row)
		}
	}
}

// TestFilesCSVGolden: filo export and filo update of the combined CSV (docs/design.md, Tags, "The combined CSV"):
// the export, a dry run, an update that clears a retention and removes a tag, asked about and confirmed, a CSV
// applied again, and one with a refused row, declined.
func TestFilesCSVGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in, work := ct.filo, ct.in, ct.work
	sep, oct := in("received/2026/09/2026-09-02 British Gas Bill.pdf"), in("received/2026/10/2026-10-03 British Gas Bill.pdf")
	filo(ExitOK, "", false, "tag", sep, oct, "sender:British Gas", "type:bill")
	var out strings.Builder
	step := func(title, stdin string, code int, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(code, stdin, true, args...).stdout + "\n")
	}
	csvPath := filepath.Join(work, "all.csv")
	step("export -o all.csv --no-scan", "", ExitOK, "export", "-o", csvPath, "--no-scan")
	ct.contains("CSV", string(mustRead(t, csvPath)),
		"id,export,directory,name,size,modified,file_date,retention,keep_until,tags\n",
		",2026-09-02 British Gas Bill.pdf,14,", ",!2026-09-02,,,sender:British Gas; type:bill\n")
	editCSV(t, csvPath, accepting(edits(
		setRetention(map[string]string{"2026-09-02 British Gas Bill.pdf": "7y", "2026-10-03 British Gas Bill.pdf": "7y",
			"ideas.txt": "forever"}),
		setTags(map[string]string{"2026-10-03 British Gas Bill.pdf": "sender:British Gas; type:bill; urgent",
			"ideas.txt": "project:house"}))))
	step("update all.csv (no question)", "", ExitOK, "update", csvPath)

	// Clearing a retention and removing a tag: listed, asked once, then recorded.
	again := filepath.Join(work, "again.csv")
	filo(ExitOK, "", false, "export", "-o", again, "--no-scan")
	editCSV(t, again, edits(
		setRetention(map[string]string{"ideas.txt": ""}),
		setTags(map[string]string{"2026-09-02 British Gas Bill.pdf": "sender:British Gas",
			"2026-10-03 British Gas Bill.pdf": "sender:British Gas; type:bill; urgent; paid"})))
	step("update again.csv --dry-run", "", ExitOK, "update", again, "--dry-run")
	step("update again.csv (yes)", "y\n", ExitOK, "update", again)
	step("update again.csv (again: nothing to change)", "", ExitOK, "update", again)

	// A refused row (tags on a file tagged since), and a new policy and a tag removal declined: nothing recorded.
	third := filepath.Join(work, "third.csv")
	filo(ExitOK, "", false, "export", "-o", third, "--no-scan")
	filo(ExitOK, "", false, "tag", in("notes/ideas.txt"), "later")
	editCSV(t, third, edits(
		setRetention(map[string]string{"2026-09-02 British Gas Bill.pdf": "bills"}),
		setTags(map[string]string{"2026-10-03 British Gas Bill.pdf": "", "ideas.txt": "urgent"})))
	step("update third.csv (refused row, declined)", "n\n", ExitError, "update", third)
	got := strings.ReplaceAll(out.String(), work, "<work>")
	got = regexp.MustCompile(`as of the last scan, \d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got,
		"as of the last scan, <time>")
	golden(t, "files-csv.txt", ct.root, got)
	ct.contains("declined: tags kept", filo(ExitOK, "", false, "find", "--tag", "paid", "--no-scan").stdout, oct)
	ct.contains("declined: no policy", filo(ExitOK, "", false, "policies").stdout, "Policies   none yet")

	// No terminal: nothing recorded, and the run says why and how to go on.
	r := filo(ExitError, "", false, "update", third)
	ct.contains("no terminal", r.stdout, "nothing recorded: there is no terminal to ask in; run filo update in a "+
		"terminal, or with -y")
	// -y records it without asking, here in a terminal with nothing typed.
	r = filo(ExitError, "", true, "update", third, "-y")
	ct.contains("-y", r.stdout, "1 file put on a policy with no value yet", "1 file had its tags changed",
		"1 policy created: bills")
	ct.lacks("-y", r.stdout, "Apply the update?", "nothing recorded")
	ct.lacks("tags after -y", filo(ExitOK, "", false, "find", "--tag", "paid", "--no-scan").stdout, oct)
}

// TestFilesCSVKinds: a combined CSV that lost a column is refused as a whole, and a retention CSV given a tags
// column of the person's own stays a retention CSV.
func TestFilesCSVKinds(t *testing.T) {
	ct := newTagTest(t)
	filo, work := ct.filo, ct.work
	csvPath := filepath.Join(work, "all.csv")
	filo(ExitOK, "", false, "export", "-o", csvPath, "--no-scan")
	data := string(mustRead(t, csvPath))
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(csvPath, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.Replace(data, ",keep_until,tags\n", ",keep_until,labels\n", 1))
	ct.contains("lost tags", filo(ExitError, "", false, "update", csvPath).stderr, "has no tags column; put it back")
	write(strings.Replace(data, ",retention,keep_until,", ",ret,keep_until,", 1))
	ct.contains("lost retention", filo(ExitError, "", false, "update", csvPath).stderr,
		"has no retention column; put it back")

	r := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", r, "--no-scan")
	editCSV(t, r, setRetention(map[string]string{"ideas.txt": "forever"}))
	own := strings.Replace(string(mustRead(t, r)), ",keep_until\n", ",keep_until,tags\n", 1)
	if err := os.WriteFile(r, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	ct.contains("own tags column", filo(ExitOK, "", false, "update", r).stdout, "Retention update",
		"1 file given a retention")

	// A directory named like an export is given as ./tags.
	ct.write("tags/x.txt", "x")
	filo(ExitOK, "", false, "scan")
	t.Chdir(ct.root)
	ct.contains("./tags", filo(ExitOK, "", false, "export", "./tags", "-o", filepath.Join(work, "t.csv"),
		"--no-scan").stdout, "Export #", "1 file")
}

// TestFilesUpdateInterruptedAtPrompt: Ctrl-C at the combined update's question ends the run as interrupted, exit 1,
// with nothing recorded, not even the rows that needed no question.
func TestFilesUpdateInterruptedAtPrompt(t *testing.T) {
	ct := newTagTest(t)
	ct.filo(ExitOK, "", false, "tag", ct.in("notes/ideas.txt"), "urgent")
	csvPath := filepath.Join(ct.work, "all.csv")
	ct.filo(ExitOK, "", false, "export", "-o", csvPath, "--no-scan")
	editCSV(t, csvPath, edits(setTags(map[string]string{"ideas.txt": ""}),
		accepting(setRetention(map[string]string{"2026-09-02 British Gas Bill.pdf": "7y"}))))

	pr, pw := io.Pipe()
	defer pw.Close()
	cctx, cancel := context.WithCancel(ct.ctx) // as Ctrl-C does
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	var out termOut
	var errb bytes.Buffer
	code := Run(cctx, []string{"update", csvPath}, pipeTerminal{pr}, &out, &errb, func(k string) string {
		return map[string]string{"NO_COLOR": "1", "FILO_DB": ct.db}[k]
	})
	if code != ExitError || !strings.Contains(errb.String(), "interrupted; nothing was recorded") {
		t.Errorf("code %d, stderr %q; want 1 and interrupted", code, errb.String())
	}
	ct.contains("tags after", ct.filo(ExitOK, "", false, "tags").stdout, "urgent")
	ct.contains("report after", ct.filo(ExitOK, "", false, "report").stdout, "3 need a retention")
}
