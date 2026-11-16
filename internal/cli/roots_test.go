package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/store"
)

// TestRootsGolden: filo roots, roots add and roots set, their whole plain output (docs/design.md, Terminal output,
// Scan and roots).
func TestRootsGolden(t *testing.T) {
	ct := newCLITest(t)
	write, filo, in := ct.write, ct.filo, ct.in
	write("Documents/tax/2024.pdf", "tax")
	write("Pictures/beach.jpg", "sand")
	write(".config/app.conf", "conf")
	write("my docs/a.txt", "a")
	write("notes.txt", "notes")
	write(".cache/x", "x")
	var out strings.Builder
	step := func(title string, code int, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(code, "", false, args...).stdout + "\n")
	}

	step("roots (none yet)", ExitOK, "roots")
	step("roots add Documents --no-scan", ExitOK, "roots", "add", in("Documents"), "--no-scan")
	filo(ExitOK, "", false, "roots", "add", in("Pictures"), "--ignore", "raw")
	filo(ExitOK, "", false, "roots", "add", in(".config"))
	filo(ExitOK, "", false, "roots", "add", in("my docs"), "--include-hidden")
	// Taking in four roots: .config is now skipped (hidden), the others had other settings, so the Next line names
	// the roots set command, with build kept, that would scan .config again.
	step("roots add home --ignore build", ExitOK, "roots", "add", ct.root, "--ignore", "build")

	// A root on a filesystem of its own: the mount point is noted when it is added and in the list.
	disk := filepath.Join(ct.work, "disk")
	if err := os.MkdirAll(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	sameDevice(t, map[string]uint64{disk: 2})
	step("roots add disk --no-scan", ExitOK, "roots", "add", disk, "--no-scan")
	step("roots", ExitOK, "roots")
	sameDevice(t, nil)

	step("roots set home --include-hidden --ignore build (covers more)", ExitOK,
		"roots", "set", ct.root, "--include-hidden", "--ignore", "build")
	step("roots set home --include-hidden --ignore build (again)", ExitOK,
		"roots", "set", ct.root, "--include-hidden", "--ignore", "build")
	filo(ExitOK, "", false, "scan", ct.root) // registers the hidden files
	step("roots set home --ignore build --ignore Pictures (skips more)", ExitOK,
		"roots", "set", ct.root, "--ignore", "build", "--ignore", "Pictures")

	got := took.ReplaceAllString(out.String(), "<took>")
	got = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d`).ReplaceAllString(got, "<time>")
	got = strings.ReplaceAll(got, ct.root, "<root>") // the root first: it is inside the work directory
	got = strings.ReplaceAll(got, ct.work, "<work>")
	golden(t, "roots.txt", ct.root, got)
}

// TestRootsAddInterrupted: Ctrl-C during the first scan of a new root keeps the root, writes nothing from the scan,
// and says so in one line with the command to run, whatever error the scan surfaced it as (SQLite has its own). An
// error that only looks like a cancellation, with the run not cancelled, is reported as it is.
func TestRootsAddInterrupted(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool  // the run is cancelled (Ctrl-C)
		err    error // what the scan returns
		want   string
	}{
		{"ctrl-c surfacing as sqlite's own error", true, errors.New("apply scan: interrupted (9)"),
			"filo: scan interrupted; the root is added, nothing from the scan was written: filo scan "},
		{"a cancellation error, but no ctrl-c", false, fmt.Errorf("walk: %w", context.Canceled),
			"filo: walk: context canceled\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ct := newCLITest(t)
			ct.write("a.txt", "a")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ct.ctx = ctx
			old := runScan
			runScan = func(context.Context, *store.Store, reconcile.Options) (reconcile.Summary, error) {
				if tc.cancel {
					cancel()
				}
				return reconcile.Summary{}, tc.err
			}
			t.Cleanup(func() { runScan = old })
			r := ct.filo(ExitError, "", false, "roots", "add", ct.root)
			if !strings.HasPrefix(r.stderr, tc.want) || strings.Count(r.stderr, "\n") != 1 {
				t.Errorf("stderr %q, want one line starting %q", r.stderr, tc.want)
			}
			runScan = old
			ct.ctx = context.Background()
			ct.contains("roots", ct.filo(ExitOK, "", false, "roots").stdout, "never scanned")
		})
	}
}

// TestRootsAddNextKeepsIgnores: the suggested roots set command keeps every --ignore the root has, even when a root
// it took in is hidden only by a name filo always skips (node_modules), which no setting changes.
func TestRootsAddNextKeepsIgnores(t *testing.T) {
	ct := newCLITest(t)
	ct.write("node_modules/build/x.js", "x")
	ct.write(".config/app.conf", "conf")
	ct.filo(ExitOK, "", false, "roots", "add", ct.in("node_modules/build"), "--no-scan")
	ct.filo(ExitOK, "", false, "roots", "add", ct.in(".config"), "--no-scan")
	out := ct.filo(ExitOK, "", false, "roots", "add", ct.root, "--ignore", "build", "--no-scan").stdout
	ct.contains("roots add", out, "Took in 2 roots, with their history and retentions\n",
		"Next: filo roots set "+ct.root+" --include-hidden --ignore build, to scan .config again\n")
	ct.lacks("roots add", out, "to scan node_modules")
	one := newCLITest(t)
	one.write("a/x", "x")
	one.filo(ExitOK, "", false, "roots", "add", one.in("a"), "--no-scan")
	one.contains("roots add", one.filo(ExitOK, "", false, "roots", "add", one.root, "--no-scan").stdout,
		"Took in 1 root, with its history and retentions\n")
}

// TestRootsSub: a value given to a flag as the next argument is not taken for the subcommand.
func TestRootsSub(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"add", "DIR"}, "add"},
		{[]string{"--db", "x.db", "add", "DIR"}, "add"},
		{[]string{"--ignore", "build", "add", "DIR"}, "add"},
		{[]string{"--workers", "2", "set", "DIR"}, "set"},
		{[]string{"--ignore=build", "add", "DIR"}, "add"},
		{[]string{"--include-hidden", "add", "DIR"}, "add"},
		{nil, ""},
	}
	for _, tc := range tests {
		if got, _ := rootsSub(tc.args); got != tc.want {
			t.Errorf("rootsSub(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// TestRootsSetUnchangedTakesNoSnapshot: roots set with the settings a root already has writes nothing, so it takes
// no snapshot that would rotate a useful one away.
func TestRootsSetUnchangedTakesNoSnapshot(t *testing.T) {
	ct := newCLITest(t)
	ct.write("a.txt", "a")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root, "--include-hidden")
	snaps := func() int {
		entries, err := os.ReadDir(store.SnapshotDir(ct.db))
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := snaps()
	ct.contains("roots set", ct.filo(ExitOK, "", false, "roots", "set", ct.root, "--include-hidden").stdout,
		"Settings unchanged")
	if after := snaps(); after != before {
		t.Errorf("snapshots %d after an unchanged roots set, want %d", after, before)
	}
}

// TestRootsAddNoScanOneNext: roots add --no-scan that suggests a roots set ends with that one next step.
func TestRootsAddNoScanOneNext(t *testing.T) {
	ct := newCLITest(t)
	ct.write(".config/app.conf", "conf")
	ct.filo(ExitOK, "", false, "roots", "add", ct.in(".config"), "--no-scan")
	out := ct.filo(ExitOK, "", false, "roots", "add", ct.root, "--no-scan").stdout
	if n := strings.Count(out, "Next: "); n != 1 {
		t.Errorf("%d Next lines, want 1:\n%s", n, out)
	}
	ct.contains("roots add", out, "Next: filo roots set ")
}

func TestShellQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/you/FiloTest", "/home/you/FiloTest"},
		{"/home/you/my docs", "'/home/you/my docs'"},
		{"/data/it's", `'/data/it'\''s'`},
		{"~/x", "'~/x'"},
		{"", "''"},
	}
	for _, tc := range tests {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
