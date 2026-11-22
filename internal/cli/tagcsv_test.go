package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	ct.contains("no terminal", r.stdout, "1 row left as it was, though it removes tags (there is no terminal to ask in")
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
