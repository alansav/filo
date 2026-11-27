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

// setTags returns a CSV edit that sets the tags cell of the rows whose name is a key.
func setTags(values map[string]string) func(map[string]string) { return setCells("tags", values) }

// TestTagsCSVGolden: filo export tags and filo update of a tags CSV (docs/design.md, Tags, "The tags CSV"): the
// export, a dry run, an update that removes tags asked about and confirmed, and one refused rows and declined.
func TestTagsCSVGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in, work := ct.filo, ct.in, ct.work
	sep, oct := in("received/2026/09/2026-09-02 British Gas Bill.pdf"), in("received/2026/10/2026-10-03 British Gas Bill.pdf")
	filo(ExitOK, "", false, "tag", sep, oct, "sender:British Gas", "type:bill")
	var out strings.Builder
	step := func(title, stdin string, code int, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(code, stdin, true, args...).stdout + "\n")
	}
	csvPath := filepath.Join(work, "tags.csv")
	step("export tags -o tags.csv --no-scan", "", ExitOK, "export", "tags", "-o", csvPath, "--no-scan")
	editCSV(t, csvPath, setTags(map[string]string{
		"2026-09-02 British Gas Bill.pdf": "sender:british gas; type:bill; project:house",
		"2026-10-03 British Gas Bill.pdf": "sender:British Gas",
		"ideas.txt":                       "urgent",
	}))
	step("update tags.csv --dry-run", "", ExitOK, "update", csvPath, "--dry-run")
	step("update tags.csv (removing a tag: yes)", "y\n", ExitOK, "update", csvPath)
	step("update tags.csv (again: nothing to change)", "", ExitOK, "update", csvPath)

	// A row whose file was tagged since the export, and a tag the rules refuse: refused; the removal declined.
	again := filepath.Join(work, "again.csv")
	filo(ExitOK, "", false, "export", "tags", "-o", again, "--no-scan")
	filo(ExitOK, "", false, "tag", in("notes/ideas.txt"), "later")
	editCSV(t, again, setTags(map[string]string{
		"2026-09-02 British Gas Bill.pdf": "",
		"2026-10-03 British Gas Bill.pdf": "a*b",
		"ideas.txt":                       "",
	}))
	step("update again.csv (refused rows, removal declined)", "n\n", ExitError, "update", again)
	got := strings.ReplaceAll(out.String(), work, "<work>")
	got = regexp.MustCompile(`as of the last scan, \d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got,
		"as of the last scan, <time>")
	golden(t, "tags-csv.txt", ct.root, got)

	// No terminal: nothing is removed, and the run says why.
	r := filo(ExitError, "", false, "update", again)
	ct.contains("no terminal", r.stdout, "1 row left as it was, though it removes tags (there is no terminal to ask in",
		"or with -y, to remove them")

	// A dry run with -y says it removes them without asking.
	ct.contains("dry -y", filo(ExitError, "", false, "update", again, "--dry-run", "-y").stdout,
		"1 of them would lose tags (-y removes them without asking)")
	// -y removes them without asking, here in a terminal with nothing typed; they are counted and listed.
	r = filo(ExitError, "", true, "update", again, "-y")
	ct.contains("-y", r.stdout, "1 file had its tags changed", "1 of them lost tags",
		"received/2026/09/2026-09-02 British Gas Bill.pdf   removed project:house; sender:British Gas; type:bill")
	ct.lacks("-y", r.stdout, "Remove them?", "left as it was")
	ct.contains("tags after -y", filo(ExitOK, "", false, "find", "--no-tag", "type:", "--no-scan").stdout, sep)

	// -y answers only that question: with a retention CSV it is a usage error.
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", rcsv, "--no-scan")
	ct.contains("-y, retention", filo(ExitUsage, "", false, "update", rcsv, "-y").stderr,
		"-y is for a CSV from filo export or export tags")
	// A file that is no CSV filo reads gets its own error, not the one about -y.
	notCSV := filepath.Join(work, "notes.txt")
	if err := os.WriteFile(notCSV, []byte("just notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ct.contains("-y, not a CSV", filo(ExitError, "", false, "update", notCSV, "-y").stderr, "not a filo retention CSV")
}

func TestTagsCSVKinds(t *testing.T) {
	ct := newTagTest(t)
	filo, work := ct.filo, ct.work
	csvPath := filepath.Join(work, "tags.csv")
	filo(ExitOK, "", false, "export", "tags", "-o", csvPath, "--no-scan")
	data, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	// The tags column lost: said so, not read as a retention CSV.
	lost := strings.Replace(string(data), ",tags\n", ",labels\n", 1)
	if err := os.WriteFile(csvPath, []byte(lost), 0o644); err != nil {
		t.Fatal(err)
	}
	ct.contains("lost column", filo(ExitError, "", false, "update", csvPath).stderr, "the CSV has no tags column")
	// A retention CSV's rows given to the tags update are refused, and the other way round.
	r := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", r, "--no-scan")
	editCSV(t, r, setRetention(map[string]string{"ideas.txt": "7y"}))
	tagsFromR := strings.Replace(string(mustRead(t, r)), "file_date,retention,keep_until", "a,b,tags", 1)
	if err := os.WriteFile(csvPath, []byte(tagsFromR), 0o644); err != nil {
		t.Fatal(err)
	}
	ct.contains("retention rows", filo(ExitError, "", false, "update", csvPath).stdout,
		"is a retention export, not a tags export")
}

// TestTagsUpdateInterruptedAtPrompt: Ctrl-C at the question about removing tags ends the run as interrupted, exit 1,
// with no tag removed.
func TestTagsUpdateInterruptedAtPrompt(t *testing.T) {
	ct := newTagTest(t)
	note := ct.in("notes/ideas.txt")
	ct.filo(ExitOK, "", false, "tag", note, "urgent")
	csvPath := filepath.Join(ct.work, "tags.csv")
	ct.filo(ExitOK, "", false, "export", "tags", "-o", csvPath, "--no-scan")
	editCSV(t, csvPath, setTags(map[string]string{"ideas.txt": ""}))

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
}
