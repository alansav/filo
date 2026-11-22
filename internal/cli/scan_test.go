package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// took matches how long a scan took, which differs between runs.
var took = regexp.MustCompile(`under 0\.1 s|\d+\.\d s|\d+ min \d+ s`)

// TestScanGolden: filo scan's whole plain output (docs/design.md, Terminal output, Scan and roots): a scan with every
// kind of change and a kind cut at reconcile.SampleSize, a clean scan of a directory, and every root with one
// unmounted. Unreadable files are left to TestEndToEnd, since they read differently as root.
func TestScanGolden(t *testing.T) {
	ct := newCLITest(t)
	write, filo := ct.write, ct.filo
	write("docs/contract.txt", "contract")
	write("docs/report.txt", "report, first draft")
	write("notes/todo.md", "todo")
	for i := 1; i <= 12; i++ {
		write(fmt.Sprintf("old/f%02d.txt", i), fmt.Sprintf("old file %d", i))
	}
	filo(ExitOK, "", false, "roots", "add", ct.root)

	// A move, an edit, twelve missing files (two past the ten shown), a possible move and a new file.
	if err := os.Rename(ct.in("docs/contract.txt"), ct.in("notes/contract-final.txt")); err != nil {
		t.Fatal(err)
	}
	write("notes/todo.md", "todo, longer now")
	if err := os.RemoveAll(ct.in("old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ct.in("docs/report.txt")); err != nil {
		t.Fatal(err)
	}
	write("archive/report.txt", "report, final")
	write("new.txt", "new")
	var out strings.Builder
	out.WriteString("$ filo scan\n" + filo(ExitOK, "", false, "scan").stdout)
	out.WriteString("\n$ filo scan docs\n" + filo(ExitOK, "", false, "scan", ct.in("docs")).stdout)

	// A directory on another filesystem is found by every scan: shown, but not a change, so no "no changes" and no
	// next step.
	if err := os.MkdirAll(ct.in("mnt"), 0o700); err != nil {
		t.Fatal(err)
	}
	sameDevice(t, map[string]uint64{ct.in("mnt"): 2})
	out.WriteString("\n$ filo scan (a disk mounted inside the root)\n" + filo(ExitOK, "", false, "scan").stdout)
	sameDevice(t, nil)
	if err := os.Remove(ct.in("mnt")); err != nil {
		t.Fatal(err)
	}

	// A second root whose disk is no longer mounted: reported on stdout in its place, and the other still scanned.
	disk := filepath.Join(ct.work, "disk")
	if err := os.MkdirAll(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	sameDevice(t, map[string]uint64{disk: 2})
	filo(ExitOK, "", false, "roots", "add", disk, "--no-scan")
	sameDevice(t, nil)
	r := filo(ExitError, "", false, "scan")
	if want := "filo: 1 root could not be scanned: " + disk + "\n"; r.stderr != want {
		t.Errorf("stderr %q, want only %q", r.stderr, want)
	}
	out.WriteString("\n$ filo scan (one root unmounted)\n" + r.stdout)

	got := took.ReplaceAllString(out.String(), "<took>")
	got = strings.ReplaceAll(got, ct.root, "<root>") // the root first: it is inside the work directory
	got = strings.ReplaceAll(got, ct.work, "<work>")
	golden(t, "scan.txt", ct.root, got)
}

func TestDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{40, "under 0.1 s"},
		{1240, "1.2 s"},
		{59_970, "1 min 0 s"},    // not "60.0 s"
		{3_599_600, "1 h 0 min"}, // not "60 min 0 s"
		{192_000, "3 min 12 s"},
		{3_900_000, "1 h 5 min"},
	}
	for _, tc := range tests {
		if got := duration(time.Duration(tc.ms) * time.Millisecond); got != tc.want {
			t.Errorf("duration(%d ms) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// TestScanCopyTags: a new file that took the tags of copies whose tags differed is counted and listed with what it
// took; one that took matching tags is not (docs/design.md, Tags, "A copy keeps the tags").
func TestScanCopyTags(t *testing.T) {
	ct := newCLITest(t)
	ct.write("a.pdf", "bill")
	ct.write("b.pdf", "bill")
	ct.write("c.txt", "note")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	s, err := store.Open(ct.ctx, store.Options{DBPath: ct.db})
	if err != nil {
		t.Fatal(err)
	}
	ks, err := s.Known(ct.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, k := range ks {
		ids[k.Path] = k.FileID
	}
	add := func(rel, tag string) {
		tg, err := tags.Parse(tag)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.TagFiles(ct.ctx, []int64{ids[rel]}, []tags.Tag{tg}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	add("a.pdf", "type:bill")
	add("c.txt", "type:note")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ct.write("inbox/copy.pdf", "bill")
	ct.write("inbox/copy.txt", "note")
	out := ct.filo(ExitOK, "", false, "scan").stdout
	ct.contains("scan", out, "1 new file took the tags of copies that differed: check with filo show",
		"inbox/copy.pdf   took the tags of copies that differed: type:bill")
	ct.lacks("scan", out, "inbox/copy.txt   took")
}
