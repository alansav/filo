package cli

import (
	"context"
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
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
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
	exists := func(rel string) bool {
		_, err := os.Lstat(in(rel))
		return err == nil
	}
	write("old/a.txt", "a")
	write("old/sub/b.txt", "b")
	write("keep/c.txt", "c")
	write("keep/d.txt", "d")
	write("e.txt", "e")
	write("x/f.txt", "f")
	if err := os.Symlink("e.txt", in("link")); err != nil {
		t.Fatal(err)
	}
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
	contains("no terminal", r.stdout, "Past their retention: 7 files, 6 B", "2026-11-01", in("old/sub/b.txt"),
		in("link"), "Nothing deleted: there is no terminal to ask in")
	contains("declined", filo(ExitOK, "n\n", true, "delete").stdout, "Delete [a]ll, [s]elect or [n]one?",
		"Nothing deleted.")
	contains("unknown answer", filo(ExitOK, "maybe\n", true, "delete").stdout, "Nothing deleted.")
	gone("declined", map[string]bool{"old/a.txt": false, "e.txt": false})

	// File by file under keep/: c gets a new retention (a period is refused without a file_date), d changed since its
	// retention was set and is skipped, not deleted.
	write("keep/d.txt", "d, edited")
	r = filo(ExitOK, "s\nr\n7y\nsoon\n2030-01-01\nd\n", true, "delete", in("keep"))
	contains("select", r.stdout, "Past their retention under "+in("keep")+": 2 files",
		"(1/2) 2026-11-01", "[d]elete, [r]etention…, [s]kip, [q]uit?", "retention 7y needs a file_date",
		`"soon" is not a retention`, "kept until 2030-01-01", "Deleted 0 files (0 B).", "Skipped 1:",
		in("keep/d.txt")+": changed since its retention was set", "Gave 1 file a new retention.")
	gone("select", map[string]bool{"keep/c.txt": false, "keep/d.txt": false})

	// Skip one, quit at the next: both are left.
	r = filo(ExitOK, "s\ns\nq\n", true, "delete", in("old"))
	contains("skip and quit", r.stdout, "Deleted 0 files", "Left 2 files for later.")

	// -y deletes without asking, and removes the folders it emptied, up to the root.
	r = filo(ExitOK, "", false, "delete", "-y", in("old"))
	contains("-y", r.stdout, "Deleted 2 files (2 B).", "Removed 2 empty folders.")
	gone("-y", map[string]bool{"old/a.txt": true, "old/sub": true, "old": true})

	// All, half an hour later (a run is told apart from the last by when it started): e, the symlink and x/f.txt go; d is still a candidate in the register (no scan since its edit), so it is
	// checked again and skipped again; keep/ still holds c, so it stays.
	setNow(t, time.Date(2026, 11, 13, 12, 30, 0, 0, time.Local))
	r = filo(ExitOK, "a\n", true, "delete")
	contains("all", r.stdout, "Past their retention: 4 files", "Deleted 3 files (2 B).", "Skipped 1:",
		"Removed 1 empty folder.")
	gone("all", map[string]bool{"e.txt": true, "link": true, "x": true, "keep/c.txt": false, "keep/d.txt": false})
	if !exists(".") {
		t.Fatal("the root was removed")
	}

	// A rescan finds nothing missing: the deleted files are recorded as deleted. d's edit is recorded now.
	r = filo(ExitOK, "", false, "scan")
	contains("rescan", r.stdout, "new 0   changed 1   moved 0   missing 0")

	// A file restored from a backup is a new file with no retention, with a hint.
	write("old/a.txt", "a")
	r = filo(ExitOK, "", false, "scan")
	contains("restored", r.stdout, "new 1", "missing 0", "same content as a file filo deleted",
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
	contains("report", r.stdout, "Deleted by filo: 5 files, 4 B freed (last run 2026-11-13 12:30: 3 files, 2 B)",
		"new 1, changed 0, moved 0, missing 0", "deleted   e.txt", "new       old/a.txt")

	filo(ExitUsage, "", false, "delete", in("old"), in("keep"))
}

// pipeTerminal is a terminal on which nobody ever types.
type pipeTerminal struct{ *io.PipeReader }

func (pipeTerminal) IsTerminal() bool { return true }

// TestDeleteInterruptedAtPrompt checks that Ctrl-C (a cancelled context) ends a wait for an answer, and nothing is
// deleted.
func TestDeleteInterruptedAtPrompt(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local))
	work := canonicalTemp(t)
	env := map[string]string{"FILO_DB": filepath.Join(work, "filo.db")}
	root := filepath.Join(work, "home")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
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
	r := runIn(t, cctx, env, pipeTerminal{pr}, "delete")
	if r.code != ExitError || !strings.Contains(r.stderr, "interrupted") {
		t.Errorf("code %d, stderr %q; want 1 and interrupted", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("a.txt is gone after an interrupted prompt: %v", err)
	}
}
