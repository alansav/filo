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

	"github.com/alansav/filo/internal/store"
)

// setTags returns a CSV edit that sets the tags cell of the rows whose name is a key.
func setTags(values map[string]string) func(map[string]string) { return setCells("tags", values) }

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

	// No terminal and nothing refused: still exit 1, since nothing was recorded; a no typed at the question is exit 0.
	clean := filepath.Join(work, "clean.csv")
	filo(ExitOK, "", false, "export", "-o", clean, "--no-scan")
	editCSV(t, clean, setTags(map[string]string{"ideas.txt": ""}))
	ct.contains("no terminal, exit 1", filo(ExitError, "", false, "update", clean).stdout,
		"nothing recorded: there is no terminal to ask in")
	ct.contains("declined, exit 0", filo(ExitOK, "n\n", true, "update", clean).stdout,
		"nothing recorded: not confirmed")
	ct.lacks("tags after -y", filo(ExitOK, "", false, "find", "--tag", "paid", "--no-scan").stdout, oct)
}

// TestFilesCSVKinds: a combined CSV that lost a column is refused as a whole, naming it; one that lost both is not a
// filo CSV; every row refused for its cells is still a combined CSV; a directory named like an export.
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
	ct.contains("lost tags, -y", filo(ExitError, "", false, "update", csvPath, "-y").stderr,
		"has no tags column; put it back")
	write(strings.Replace(data, ",retention,keep_until,", ",ret,keep_until,", 1))
	ct.contains("lost retention", filo(ExitError, "", false, "update", csvPath).stderr,
		"has no retention column; put it back")

	// Every row refused for its cells: still a combined CSV, and the reasons are its own.
	write(data)
	editCSV(t, csvPath, setTags(map[string]string{"ideas.txt": "a*b", "2026-09-02 British Gas Bill.pdf": "a*b",
		"2026-10-03 British Gas Bill.pdf": "a*b"}))
	ct.contains("all refused", filo(ExitError, "", false, "update", csvPath, "-y").stdout, "Update   all.csv",
		`tags "a*b": a tag cannot hold "*"`)

	// Both columns lost: not a filo CSV.
	write(strings.Replace(strings.Replace(data, ",keep_until,tags\n", ",keep_until,labels\n", 1), ",retention,",
		",ret,", 1))
	ct.contains("both lost", filo(ExitError, "", false, "update", csvPath).stderr, "not a filo CSV")

	// A directory named like an export is given as ./duplicates; one named retention or tags, words no export
	// takes any more, needs nothing.
	ct.write("duplicates/x.txt", "x")
	ct.write("tags/y.txt", "y")
	filo(ExitOK, "", false, "scan")
	t.Chdir(ct.root)
	ct.contains("./duplicates", filo(ExitOK, "", false, "export", "./duplicates", "-o", filepath.Join(work, "d.csv"),
		"--no-scan").stdout, "Export #", "1 file")
	ct.contains("tags", filo(ExitOK, "", false, "export", "tags", "-o", filepath.Join(work, "t.csv"),
		"--no-scan").stdout, "Export #", "1 file", "under tags/")
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

// setNames returns a CSV edit that sets the name cell of the rows whose name is a key.
func setNames(values map[string]string) func(map[string]string) { return setCells("name", values) }

// TestRenameGolden: renaming by the name cell (docs/design.md, Tags, "Renaming a file by its name cell"): a dry
// run, the renames asked about and made, the next scan finding nothing, and the refusals.
func TestRenameGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in, work := ct.filo, ct.in, ct.work
	ct.write("notes/plan.txt ", "plan")
	ct.write("tax/2023 retrun.pdf", "return")
	ct.write("tax/other.pdf", "other")
	filo(ExitOK, "", false, "scan")
	filo(ExitOK, "", false, "tag", in("tax/2023 retrun.pdf"), "type:tax")
	var out strings.Builder
	step := func(title, stdin string, code int, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(code, stdin, true, args...).stdout + "\n")
	}
	csvPath := filepath.Join(work, "all.csv")
	filo(ExitOK, "", false, "export", "-o", csvPath, "--no-scan")
	editCSV(t, csvPath, setNames(map[string]string{
		"2023 retrun.pdf": "2023 return.pdf",
		"plan.txt ":       "plan.txt",
		"ideas.txt":       "ideas.md",
	}))
	step("update all.csv --dry-run", "", ExitOK, "update", csvPath, "--dry-run")
	step("update all.csv (yes)", "y\n", ExitOK, "update", csvPath)
	for _, rel := range []string{"tax/2023 return.pdf", "notes/plan.txt", "notes/ideas.md"} {
		if !ct.exists(rel) {
			t.Errorf("%s missing after the renames", rel)
		}
	}
	for _, rel := range []string{"tax/2023 retrun.pdf", "notes/plan.txt ", "notes/ideas.txt"} {
		if ct.exists(rel) {
			t.Errorf("%s still there after the renames", rel)
		}
	}
	step("scan (after the renames)", "", ExitOK, "scan")
	ct.contains("tags kept", filo(ExitOK, "", false, "find", "--tag", "type:tax", "--no-scan").stdout,
		in("tax/2023 return.pdf"))
	ct.contains("show", filo(ExitOK, "", false, "show", in("tax/2023 return.pdf")).stdout,
		"moved from  "+in("tax/2023 retrun.pdf"))

	// Refused before the question: hiding, a /, another file's name, and a file changed since the last scan.
	again := filepath.Join(work, "again.csv")
	filo(ExitOK, "", false, "export", "-o", again, "--no-scan")
	ct.write("notes/ideas.md", "ideas, longer now")
	editCSV(t, again, setNames(map[string]string{
		"2023 return.pdf":                 "other.pdf",
		"plan.txt":                        ".plan.txt",
		"2026-09-02 British Gas Bill.pdf": "old/bill.pdf",
		"ideas.md":                        "ideas.txt",
	}))
	step("update again.csv (refused)", "", ExitError, "update", again)
	got := strings.ReplaceAll(out.String(), work, "<work>")
	got = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got, "<time>")
	got = regexp.MustCompile(`files seen \. [^.]+ s \.`).ReplaceAllString(got, "files seen . <elapsed> .")
	golden(t, "rename.txt", ct.root, got)
}

// TestRenameNoTerminal: a rename asks; with no terminal and no -y nothing is renamed (exit 1), with -y it is.
func TestRenameNoTerminal(t *testing.T) {
	ct := newTagTest(t)
	csvPath := filepath.Join(ct.work, "all.csv")
	ct.filo(ExitOK, "", false, "export", "-o", csvPath, "--no-scan")
	editCSV(t, csvPath, setNames(map[string]string{"ideas.txt": "ideas.md"}))
	ct.contains("no terminal", ct.filo(ExitError, "", false, "update", csvPath).stdout, "nothing recorded")
	if !ct.exists("notes/ideas.txt") {
		t.Fatal("renamed without asking")
	}
	ct.contains("-y", ct.filo(ExitOK, "", false, "update", csvPath, "-y").stdout, "1 file renamed")
	if !ct.exists("notes/ideas.md") || ct.exists("notes/ideas.txt") {
		t.Error("not renamed with -y")
	}
	// The same CSV again: the file has the name already, so nothing changes.
	ct.contains("again", ct.filo(ExitOK, "", false, "update", csvPath, "-y").stdout, "3 rows unchanged")
	// An older CSV whose file was renamed since: refused.
	old := filepath.Join(ct.work, "old.csv")
	ct.filo(ExitOK, "", false, "export", "-o", old, "--no-scan")
	editCSV(t, old, setNames(map[string]string{"ideas.md": "ideas.markdown"}))
	newer := filepath.Join(ct.work, "new.csv")
	ct.filo(ExitOK, "", false, "export", "-o", newer, "--no-scan")
	editCSV(t, newer, setNames(map[string]string{"ideas.md": "ideas.text"}))
	ct.filo(ExitOK, "", false, "update", newer, "-y")
	ct.contains("moved since", ct.filo(ExitError, "", false, "update", old, "-y").stdout,
		"moved or renamed since export #2")
}

func TestMoveRowNotes(t *testing.T) {
	rn := func(rel, dir, name string) store.Rename {
		return store.Rename{Candidate: store.Candidate{FileItem: store.FileItem{RootPath: "/r", Path: rel}},
			Dir: dir, Name: name}
	}
	for _, tc := range []struct{ rel, dir, name, note string }{
		{"inbox/bill.pdf", "archive", "bill.pdf", ""},
		{"inbox/bill.pdf", "inbox ", "bill.pdf", "only spaces in the directory changed"},
		{"a/b/bill.pdf", "a/ b", "bill.pdf", "only spaces in the directory changed"},
		{"inbox/bill.pdf", "archive", "bill.pd", "extension changed"},
	} {
		rows := moveRows([]store.Rename{rn(tc.rel, tc.dir, tc.name)})
		if len(rows) != 1 || rows[0].Note != tc.note {
			t.Errorf("%s -> %s/%s: rows %+v, want note %q", tc.rel, tc.dir, tc.name, rows, tc.note)
		}
	}
}

func TestRenameRowNotes(t *testing.T) {
	rn := func(rel, name string) store.Rename {
		return store.Rename{Candidate: store.Candidate{FileItem: store.FileItem{RootPath: "/r", Path: rel}}, Name: name}
	}
	for _, tc := range []struct{ rel, name, note string }{
		{"d/Britsh.pdf", "British.pdf", ""},
		{"d/notes.txt ", "notes.txt", "only spaces changed"},
		{"tax/2023 return.pdf", "2023 return.pd", "extension changed"},
		{".bashrc", ".bashrc_old", ""}, // a hidden name's dot is not an extension
		{".notes.txt", ".notes.md", "extension changed"},
		{"top.txt", "top.md", "extension changed"},
		{"d/0012", "12", "looks reformatted by a spreadsheet"},
		{"d/1E5", "100000", "looks reformatted by a spreadsheet"},
		{"d/1.50", "1.5", "looks reformatted by a spreadsheet"},
		{"d/1-2", "02-Jan", "looks reformatted by a spreadsheet"},
		{"d/3.4.25", "2025-03-04", "looks reformatted by a spreadsheet"},
		{"d/0012.pdf", "12.pdf", ""}, // a name with an extension is text to a spreadsheet
		{"d/2023 report", "2023 Report", ""},
	} {
		rows := renameRows([]store.Rename{rn(tc.rel, tc.name)})
		if rows[0].Note != tc.note {
			t.Errorf("%s -> %s: note %q, want %q", tc.rel, tc.name, rows[0].Note, tc.note)
		}
	}
	rows := renameRows([]store.Rename{rn("top.txt", "x")})
	if rows[0].Dir != "" || rows[0].Old != "top.txt" {
		t.Errorf("a file in the root: %+v", rows[0])
	}
}

// setDirs returns a CSV edit that sets the directory cell of the rows whose name is a key.
func setDirs(values map[string]string) func(map[string]string) { return setCells("directory", values) }

// TestMoveGolden: moving by the directory cell (docs/design.md, Tags, "Moving a file by its directory cell"): a dry
// run, moves into new directories (one under a new name) and to the top of the root, a symlink that would break, the
// emptied directories left, the next scan, and the refusals.
func TestMoveGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in, work := ct.filo, ct.in, ct.work
	ct.write("inbox/bill.pdf", "bill")
	ct.write("inbox/scan.pdf", "scan")
	ct.write("old/x.txt", "x")
	ct.write("kept/y.txt", "y")
	if err := os.Symlink(in("inbox/scan.pdf"), in("scan-link")); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "scan")
	filo(ExitOK, "", false, "tag", in("inbox/bill.pdf"), "type:bill")
	var out strings.Builder
	step := func(title, stdin string, code int, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(code, stdin, true, args...).stdout + "\n")
	}
	csvPath := filepath.Join(work, "all.csv")
	filo(ExitOK, "", false, "export", "-o", csvPath, "--no-scan")
	month := in("received/2026/11")
	editCSV(t, csvPath, edits(
		setDirs(map[string]string{"bill.pdf": month, "scan.pdf": month, "x.txt": ct.root + "/", "y.txt": in("sorted")}),
		setNames(map[string]string{"scan.pdf": "Scan (council).pdf"})))
	step("update all.csv --dry-run", "", ExitOK, "update", csvPath, "--dry-run")
	step("update all.csv (yes)", "y\n", ExitOK, "update", csvPath)
	for rel, want := range map[string]bool{"received/2026/11/bill.pdf": true, "received/2026/11/Scan (council).pdf": true,
		"x.txt": true, "sorted/y.txt": true, "inbox": true, "old": true, "kept": true} {
		if ct.exists(rel) != want {
			t.Errorf("%s exists = %v, want %v", rel, !want, want)
		}
	}
	ct.contains("tags kept", filo(ExitOK, "", false, "find", "--tag", "type:bill", "--no-scan").stdout,
		in("received/2026/11/bill.pdf"))
	ct.contains("show", filo(ExitOK, "", false, "show", in("received/2026/11/bill.pdf")).stdout,
		"moved from  "+in("inbox/bill.pdf"))

	// Refused before the question.
	again := filepath.Join(work, "again.csv")
	filo(ExitOK, "", false, "export", "-o", again, "--no-scan")
	editCSV(t, again, setDirs(map[string]string{
		"bill.pdf":  "received/2026",             // not a full path
		"x.txt":     "/elsewhere",                // outside the root
		"y.txt":     in(".archive"),              // hidden
		"scan-link": in("received/node_modules"), // a name the scan skips
	}))
	step("update again.csv (refused)", "", ExitError, "update", again)
	got := strings.ReplaceAll(out.String(), work, "<work>")
	got = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got, "<time>")
	golden(t, "move.txt", ct.root, got)
}
