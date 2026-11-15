package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// TestDeleteRoundTrip runs filo delete the ways a person would: without a terminal, declined, file by file (delete,
// a new retention, skip, quit), all, and -y; with a file changed since its retention was set, folders emptied by the
// run, a rescan afterwards, and a deleted file restored.
func TestDeleteRoundTrip(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	ctx, db, work, root := ct.ctx, ct.db, ct.work, ct.root
	in, write, exists, filo, contains := ct.in, ct.write, ct.exists, ct.filo, ct.contains
	write("old/a.txt", "a")
	write("old/sub/b.txt", "b")
	write("keep/c.txt", "c")
	write("keep/d.txt", "d")
	write("e.txt", "e")
	write("x/f.txt", "f")
	if err := os.Symlink("e.txt", in("link")); err != nil {
		t.Fatal(err)
	}
	gone := func(when string, want map[string]bool) {
		t.Helper()
		for rel, g := range want {
			if exists(rel) == g {
				t.Errorf("%s: %s exists = %v, want %v", when, rel, !g, !g)
			}
		}
	}

	filo(ExitOK, "", false, "roots", "add", root)
	csvPath := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", csvPath)
	past := map[string]string{}
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt", "link"} {
		past[n] = "2026-11-01"
	}
	editCSV(t, csvPath, setRetention(past))
	filo(ExitOK, "", false, "update", csvPath)

	// Without a terminal it only lists; declined, nothing happens.
	r := filo(ExitOK, "", false, "delete")
	contains("no terminal", r.stdout, "Ready to delete   7 files . 6 B . "+root, "Past their retention   7 files . 6 B",
		"    until        size", "    2026-11-01    1 B   old/sub/b.txt", "    2026-11-01    5 B   link",
		"Nothing deleted: there is no terminal to ask in")
	contains("declined", filo(ExitOK, "n\n", true, "delete").stdout,
		"Delete all 7, select one by one, list them all, or none? [a/s/l/N] ", "Nothing deleted.")
	contains("enter", filo(ExitOK, "\n", true, "delete").stdout, "Nothing deleted.")
	contains("unknown answer", filo(ExitOK, "maybe\n", true, "delete").stdout,
		"answer a (all), s (select), l (list) or n (none)", "Nothing deleted.")
	gone("declined", map[string]bool{"old/a.txt": false, "e.txt": false})

	// File by file under keep/: c gets a new retention (a period is refused without a file_date), d changed since its
	// retention was set and is skipped, not deleted.
	write("keep/d.txt", "d, edited")
	r = filo(ExitOK, "s\nr\n7y\nsoon\n2030-01-01\nd\n", true, "delete", in("keep"))
	contains("select", r.stdout, "Ready to delete   2 files . 2 B . "+root+", under keep/",
		"[1/2]  past its retention . until 2026-11-01 . 1 B", "          "+in("keep/c.txt"),
		"d delete . r new retention . s skip . q quit  > ", "[refused] retention 7y needs a file_date",
		`[refused] "soon" is not a retention`, "[ok] kept until 2030-01-01", "Done   deleted 0 files . freed 0 B",
		"Skipped 1: nothing was changed", "keep/d.txt: changed since its retention was set",
		"  - 1 file given a new retention")
	gone("select", map[string]bool{"keep/c.txt": false, "keep/d.txt": false})

	// Skip one, quit at the next: both are left.
	r = filo(ExitOK, "s\ns\nq\n", true, "delete", in("old"))
	contains("skip and quit", r.stdout, "Done   deleted 0 files", "  - 2 files left for later")

	// -y deletes without asking, and removes the folders it emptied, up to the root.
	r = filo(ExitOK, "", false, "delete", "-y", in("old"))
	contains("-y", r.stdout, "Done   deleted 2 files . freed 2 B . removed 2 empty folders")
	gone("-y", map[string]bool{"old/a.txt": true, "old/sub": true, "old": true})

	// All, half an hour later (a run is told apart from the last by when it started): e, the symlink and x/f.txt go; d is still a candidate in the register (no scan since its edit), so it is
	// checked again and skipped again; keep/ still holds c, so it stays.
	setNow(t, time.Date(2026, 11, 13, 12, 30, 0, 0, time.Local))
	r = filo(ExitOK, "a\n", true, "delete")
	contains("all", r.stdout, "Past their retention ", " 4 files . 3 B", "Done   deleted 3 files . freed 2 B . removed 1 "+
		"empty folder", "Skipped 1:")
	gone("all", map[string]bool{"e.txt": true, "link": true, "x": true, "keep/c.txt": false, "keep/d.txt": false})
	if !exists(".") {
		t.Fatal("the root was removed")
	}

	// A rescan finds nothing missing: the deleted files are recorded as deleted. d's edit is recorded now.
	r = filo(ExitOK, "", false, "scan")
	contains("rescan", r.stdout, "\n  - 1 changed\n\nEvents")

	// A file restored from a backup is a new file with no retention, with a hint.
	write("old/a.txt", "a")
	r = filo(ExitOK, "", false, "scan")
	contains("restored", r.stdout, "\n  - 1 new\n\nEvents", "same content as a file filo deleted",
		"same content as a file filo deleted on 2026-11-13; a new file, it needs a retention")
	s, err := store.Open(ctx, store.Options{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Files(ctx, today(), store.Filter{})
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Path == "old/a.txt" && it.Status != store.StatusUndated {
			t.Errorf("restored file is %s, want undated", it.Status)
		}
	}

	// The report counts what filo deleted; the last scan shown is a real scan.
	r = filo(ExitOK, "", false, "report")
	contains("report", r.stdout, "deleted by filo: 5 files, 4 B freed (last run 2026-11-13 12:30: 3 files, 2 B)",
		"new 1, changed 0, moved 0, missing 0", "deleted   e.txt", "new       old/a.txt")

	filo(ExitUsage, "", false, "delete", in("old"), in("keep"))
}

// pipeTerminal is a terminal on which nobody ever types.
type pipeTerminal struct{ *io.PipeReader }

func (pipeTerminal) IsTerminal() bool { return true }

// TestDeleteInterruptedAtPrompt checks that Ctrl-C (a cancelled context) ends a wait for an answer, and nothing is
// deleted.
func TestDeleteInterruptedAtPrompt(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	ctx, env, work, root := ct.ctx, ct.env, ct.work, ct.root
	ct.write("a.txt", "a")
	run(t, ctx, env, "roots", "add", root)
	csvPath := filepath.Join(work, "r.csv")
	run(t, ctx, env, "export", "retention", "-o", csvPath)
	editCSV(t, csvPath, setRetention(map[string]string{"a.txt": "2026-11-01"}))
	run(t, ctx, env, "update", csvPath)

	pr, pw := io.Pipe()
	defer pw.Close()
	cctx, cancel := context.WithCancel(ctx) // as Ctrl-C does
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	var out termOut // the question can be seen, so filo asks
	var errb bytes.Buffer
	code := Run(cctx, []string{"delete"}, pipeTerminal{pr}, &out, &errb, func(k string) string { return env[k] })
	if code != ExitError || !strings.Contains(errb.String(), "interrupted") {
		t.Errorf("code %d, stderr %q; want 1 and interrupted", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("a.txt is gone after an interrupted prompt: %v", err)
	}
}

// updateGolden rewrites the golden files: go test ./internal/cli -run Golden -update-golden
var updateGolden = flag.Bool("update-golden", false, "rewrite the golden files in testdata")

// golden compares out, with root shown as <root>, with testdata/name, or rewrites it with -update-golden.
func golden(t *testing.T, name, root, out string) {
	t.Helper()
	got := strings.ReplaceAll(out, root, "<root>")
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-golden to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// TestDeleteGolden: filo delete's whole plain output for the designed scenarios (docs/design.md, Terminal output):
// the list without a terminal, select mode, and -y with a skip.
func TestDeleteGolden(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	work, root, write, filo := ct.work, ct.root, ct.write, ct.filo
	write("tax/2017 return.pdf", "tax return")
	write("notes/meeting.txt", "notes")
	write("video/2015/summer holiday in the south of France with the whole family, day 3.mp4", "video")
	filo(ExitOK, "", false, "roots", "add", root)
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", "-o", rcsv)
	editCSV(t, rcsv, setRetention(map[string]string{"2017 return.pdf": "2024-12-31", "meeting.txt": "2025-06-30",
		"summer holiday in the south of France with the whole family, day 3.mp4": "2025-09-01"}))
	filo(ExitOK, "", false, "update", rcsv)

	golden(t, "delete-list.txt", root, filo(ExitOK, "", false, "delete").stdout)
	// Select (files come in path order): delete the notes, give the tax return a new retention, quit at the video.
	golden(t, "delete-select.txt", root, filo(ExitOK, "s\nd\nr\n2030-01-01\nq\n", true, "delete").stdout)
	// -y, with the video changed since its retention was set.
	write("video/2015/summer holiday in the south of France with the whole family, day 3.mp4", "edited")
	golden(t, "delete-yes.txt", root, filo(ExitOK, "", false, "delete", "-y").stdout)
}

// termOut is a stdout that says it is a terminal, so the output is cut to an overview as in a real one.
type termOut struct{ bytes.Buffer }

func (*termOut) IsTerminal() bool { return true }

// runTerm runs filo with stdout as a terminal, and stdin as a terminal if terminalIn.
func runTerm(t *testing.T, ct *cliTest, env map[string]string, stdin string, terminalIn bool, args ...string) (int,
	string) {
	t.Helper()
	var out termOut
	var errb bytes.Buffer
	var in io.Reader = strings.NewReader(stdin)
	if terminalIn {
		in = terminalInput{strings.NewReader(stdin)}
	}
	code := Run(ct.ctx, args, in, &out, &errb, func(k string) string { return env[k] })
	return code, out.String() + errb.String()
}

// TestDeleteCutList: in a terminal the list shows 10 per section; "all" then shows every file and asks again, so
// nothing is deleted that was not on screen (docs/design.md, Terminal output). With no terminal to ask in, the whole
// list is shown. TERM=dumb or NO_COLOR give no colour codes.
func TestDeleteCutList(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	past := map[string]string{}
	for i := range 12 {
		name := fmt.Sprintf("f%02d.txt", i)
		ct.write(name, name)
		past[name] = "2026-11-01"
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	csvPath := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "retention", "-o", csvPath)
	editCSV(t, csvPath, setRetention(past))
	ct.filo(ExitOK, "", false, "update", csvPath)
	plain := map[string]string{"FILO_DB": ct.db, "NO_COLOR": "1"}

	code, out := runTerm(t, ct, plain, "a\nn\n", true, "delete")
	if code != ExitOK || !strings.Contains(out, "... 2 more files") || !strings.Contains(out, "f11.txt") ||
		!strings.Contains(out, "Delete these 12 files? [y/N]") || !strings.Contains(out, "Nothing deleted.") {
		t.Errorf("all, then no (code %d):\n%s", code, out)
	}
	if strings.Index(out, "f11.txt") < strings.Index(out, "... 2 more files") {
		t.Errorf("f11.txt should first appear in the full list, after the overview:\n%s", out)
	}
	if !ct.exists("f00.txt") {
		t.Fatal("a file was deleted after no")
	}
	code, out = runTerm(t, ct, plain, "", false, "delete")
	if code != ExitOK || strings.Contains(out, "more files") || !strings.Contains(out, "f11.txt") {
		t.Errorf("no terminal to ask in: want the whole list (code %d):\n%s", code, out)
	}
	for _, env := range []map[string]string{plain, {"FILO_DB": ct.db, "TERM": "dumb"}} {
		if _, out := runTerm(t, ct, env, "n\n", true, "delete"); strings.Contains(out, "\x1b[") {
			t.Errorf("colour codes with %v:\n%q", env, out)
		}
	}
	if _, out := runTerm(t, ct, map[string]string{"FILO_DB": ct.db}, "n\n", true, "delete"); !strings.Contains(out,
		"\x1b[") {
		t.Errorf("no colour in a terminal:\n%q", out)
	}
	code, out = runTerm(t, ct, plain, "a\ny\n", true, "delete")
	if code != ExitOK || !strings.Contains(out, "Done   deleted 12 files") {
		t.Errorf("all, then yes (code %d):\n%s", code, out)
	}
}

// TestDeleteNoQuestionWhenOutputRedirected: with stdout in a file (filo delete > log), the question could not be
// seen, so filo does not ask even though the keyboard could answer: it lists everything and deletes nothing.
func TestDeleteNoQuestionWhenOutputRedirected(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	ct.write("a.txt", "a")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	csvPath := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "retention", "-o", csvPath)
	editCSV(t, csvPath, setRetention(map[string]string{"a.txt": "2026-11-01"}))
	ct.filo(ExitOK, "", false, "update", csvPath)
	r := runIn(t, ct.ctx, ct.env, terminalInput{strings.NewReader("a\n")}, "delete") // stdout is a plain buffer
	if r.code != ExitOK || strings.Contains(r.stdout, "[a/s/l/N]") ||
		!strings.Contains(r.stdout, "Nothing deleted: there is no terminal to ask in") {
		t.Errorf("code %d:\n%s", r.code, r.stdout)
	}
	if !ct.exists("a.txt") {
		t.Fatal("a.txt was deleted with the question out of sight")
	}
}

// failingTerm is a terminal whose output fails after its first n writes, as a hung-up terminal does.
type failingTerm struct{ n int }

func (*failingTerm) IsTerminal() bool { return true }

func (f *failingTerm) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("input/output error")
	}
	f.n--
	return len(p), nil
}

// TestDeleteStopsWhenOutputFails: once output fails, nothing is deleted, with or without a question: what is deleted
// must have been shown. The run exits 1 with the reason.
func TestDeleteStopsWhenOutputFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin io.Reader
	}{
		{"answered a", []string{"delete"}, terminalInput{strings.NewReader("a\n")}},
		{"-y", []string{"delete", "-y"}, strings.NewReader("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct := newCLITest(t)
			setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
			ct.write("a.txt", "a")
			ct.filo(ExitOK, "", false, "roots", "add", ct.root)
			csvPath := filepath.Join(ct.work, "r.csv")
			ct.filo(ExitOK, "", false, "export", "retention", "-o", csvPath)
			editCSV(t, csvPath, setRetention(map[string]string{"a.txt": "2026-11-01"}))
			ct.filo(ExitOK, "", false, "update", csvPath)
			var errb bytes.Buffer
			env := map[string]string{"NO_COLOR": "1"}
			for k, v := range ct.env {
				env[k] = v
			}
			code := Run(ct.ctx, tc.args, tc.stdin, &failingTerm{n: 2}, &errb, func(k string) string { return env[k] })
			if code != ExitError || !strings.Contains(errb.String(), "input/output error") {
				t.Errorf("code %d, stderr %q; want 1 and the write error", code, errb.String())
			}
			if !ct.exists("a.txt") {
				t.Fatal("a.txt was deleted though the output had failed")
			}
		})
	}
}
