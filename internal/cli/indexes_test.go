package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIndexesGolden: filo indexes in a terminal (docs/design.md, Tag index pages): none yet, adding one, a second
// name on the same directory, the list, a page edited by hand flagged after the next change, refreshing, and
// removing one with a file that is not Filo's in it.
func TestIndexesGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in, root := ct.filo, ct.in, ct.root
	var out strings.Builder
	step := func(code int, stdin, title string, args ...string) result {
		t.Helper()
		r := filo(code, stdin, true, args...)
		out.WriteString("$ filo " + title + "\n" + r.stdout + "\n")
		return r
	}
	sep, oct := in("received/2026/09/2026-09-02 British Gas Bill.pdf"), in("received/2026/10/2026-10-03 British Gas Bill.pdf")
	step(ExitOK, "", "indexes (none yet)", "indexes")
	filo(ExitOK, "", false, "tag", sep, oct, "sender:British Gas", "type:bill")
	step(ExitOK, "", "indexes add received", "indexes", "add", in("received"))
	step(ExitOK, "", "indexes add . --name tags (the root)", "indexes", "add", root, "--name", "tags")
	step(ExitOK, "", "indexes", "indexes")

	// A change outside the index's directory does not touch it.
	filo(ExitOK, "", false, "tag", in("notes/ideas.txt"), "urgent")
	// A page edited by hand: the next change to what it shows leaves it, says so on stderr and exits 1; the list flags it.
	if err := os.WriteFile(in("received/index.html"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := filo(ExitError, "", false, "tag", sep, "urgent")
	ct.contains("tag after an edit", r.stdout, "Tagged   1 file")
	ct.contains("tag after an edit", r.stderr, "filo: the change is made, but "+in("received/index.html")+
		" was not brought up to date: received/index.html was changed since Filo wrote it: not replaced (filo indexes lists it)")
	step(ExitOK, "", "indexes (one flagged)", "indexes")
	if b, _ := os.ReadFile(in("received/index.html")); string(b) != "mine" {
		t.Errorf("the edited page was replaced: %q", b)
	}
	if err := os.Remove(in("received/index.html")); err != nil {
		t.Fatal(err)
	}
	step(ExitOK, "", "indexes refresh", "indexes", "refresh")

	// Removing: a file that is not Filo's stays, with its directory, and the run exits 1.
	if err := os.WriteFile(in(".tags/notes.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	step(ExitError, "y\n", "indexes remove . --name tags", "indexes", "remove", root, "--name", "tags")
	step(ExitOK, "y\n", "indexes remove received", "indexes", "remove", in("received"))
	golden(t, "indexes.txt", root, out.String())

	for _, rel := range []string{"tags.html", ".tags/style.css", "received/index.html", "received/.index"} {
		if ct.exists(rel) {
			t.Errorf("%s is still there after remove", rel)
		}
	}
	if !ct.exists(".tags/notes.txt") {
		t.Error("a file that is not Filo's was removed")
	}
	if r := filo(ExitOK, "", false, "indexes"); !strings.Contains(r.stdout, "none yet") {
		t.Errorf("indexes after removing both:\n%s", r.stdout)
	}
}

// TestIndexPagesNotScanned: the pages are Filo's own: a scan after writing them registers no new file, and filo tag
// refuses one.
func TestIndexPagesNotScanned(t *testing.T) {
	ct := newTagTest(t)
	filo, in := ct.filo, ct.in
	filo(ExitOK, "", false, "indexes", "add", in("received"))
	if !ct.exists("received/index.html") || !ct.exists("received/.index/style.css") {
		t.Fatal("the pages were not written")
	}
	r := filo(ExitOK, "", false, "scan")
	ct.lacks("scan", r.stdout, "new")
	r = filo(ExitError, "", false, "tag", in("received/index.html"), "urgent")
	ct.contains("tag a page", r.stdout+r.stderr, "one of filo's own files")
}

// TestIndexPagesFollowChanges: a run that changes the register brings the pages up to date, saying nothing; one that
// only reads writes none.
func TestIndexPagesFollowChanges(t *testing.T) {
	ct := newTagTest(t)
	filo, in := ct.filo, ct.in
	filo(ExitOK, "", false, "indexes", "add", in("received"))
	page := func(rel string) string {
		b, err := os.ReadFile(in(rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if strings.Contains(page("received/index.html"), "sender") {
		t.Fatal("a sender on the page before any file has one")
	}
	r := filo(ExitOK, "", false, "tag", in("received/2026/09/2026-09-02 British Gas Bill.pdf"), "sender:British Gas")
	if r.stderr != "" {
		t.Errorf("tag wrote to stderr: %s", r.stderr)
	}
	if !strings.Contains(page("received/index.html"), "sender") || !ct.exists("received/.index/sender/index.html") {
		t.Errorf("the pages do not show the new tag:\n%s", page("received/index.html"))
	}
	// A new file in the directory: the scan that finds it puts it on the pages.
	ct.write("received/2026/11/2026-11-02 Water.pdf", "water")
	filo(ExitOK, "", false, "scan")
	if !strings.Contains(page("received/.index/_untagged.html"), "2026-11-02 Water.pdf") {
		t.Errorf("the new file is not on the no tags page:\n%s", page("received/.index/_untagged.html"))
	}
	// Reading only: nothing is written (the page's modification time stays).
	fi, err := os.Stat(in("received/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(in("received/index.html"), fi.ModTime().Add(-3600e9), fi.ModTime().Add(-3600e9)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(in("received/index.html"))
	filo(ExitOK, "", false, "tags")
	filo(ExitOK, "", false, "indexes")
	if after, _ := os.Stat(in("received/index.html")); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a run that only read wrote the main page")
	}
	// A pages directory that went: the next change makes it again.
	if err := os.RemoveAll(in("received/.index")); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "untag", in("received/2026/09/2026-09-02 British Gas Bill.pdf"), "sender:")
	if !ct.exists("received/.index/style.css") || ct.exists("received/.index/sender") {
		t.Error("the pages were not written again after their directory went")
	}
}

func TestIndexesRefusals(t *testing.T) {
	ct := newTagTest(t)
	filo, in := ct.filo, ct.in
	ct.write(".hidden/a.txt", "a")
	ct.write("site/index.html", "a page of the person's")
	for _, tt := range []struct {
		code int
		args []string
		want string
	}{
		{ExitUsage, []string{"indexes", "add", in("received"), "--name", ".x"}, "--name .x: an index name holds"},
		{ExitUsage, []string{"indexes", "add", in("received"), in("notes")}, "wrong number of arguments"},
		{ExitUsage, []string{"indexes", "list"}, `unknown indexes command "list"`},
		{ExitUsage, []string{"indexes", "add", in("received"), "-y"}, "-y is for indexes remove"},
		{ExitUsage, []string{"indexes", "--name", "x"}, "--name is for indexes add, remove and refresh"},
		{ExitError, []string{"indexes", "add", in(".hidden")}, "is skipped by its root's settings"},
		{ExitError, []string{"indexes", "add", in("notes/ideas.txt")}, "is not a directory"},
		{ExitError, []string{"indexes", "add", in("site")}, "site/index.html exists already: give another name with --name"},
		{ExitError, []string{"indexes", "add", filepath.Dir(ct.root)}, "not in"},
		{ExitError, []string{"indexes", "remove", in("received")}, "has no index (filo indexes lists them)"},
	} {
		r := filo(tt.code, "", false, tt.args...)
		ct.contains(strings.Join(tt.args, " "), r.stderr, tt.want)
	}
	if ct.exists("received/index.html") || ct.exists("site/.index") {
		t.Error("a refused add wrote pages")
	}

	// Two indexes on one directory: remove and refresh need --name. No terminal and no -y: nothing removed.
	filo(ExitOK, "", false, "indexes", "add", in("received"))
	filo(ExitOK, "", false, "indexes", "add", in("received"), "--name", "tags")
	r := filo(ExitError, "", false, "indexes", "add", in("received"), "--name", "TAGS")
	ct.contains("same name in another case", r.stderr, "has an index named TAGS already, in some case")
	r = filo(ExitUsage, "", false, "indexes", "refresh", in("received"))
	ct.contains("two indexes", r.stderr, "has 2 indexes, index and tags: give one with --name")
	r = filo(ExitOK, "", false, "indexes", "remove", in("received"), "--name", "tags")
	ct.contains("no terminal", r.stdout, "Nothing changed: there is no terminal to ask in")
	if !ct.exists("received/tags.html") {
		t.Error("remove without a terminal removed the page")
	}
	filo(ExitOK, "", false, "indexes", "remove", in("received"), "--name", "tags", "-y")
	if ct.exists("received/tags.html") || ct.exists("received/.tags") || !ct.exists("received/index.html") {
		t.Error("remove -y did not remove just that index")
	}
}

// TestIndexRemoveAway: an index whose root cannot be reached (a drive not mounted, lost or formatted) is removed only
// after saying its files cannot be, and that they would be registered should they come back; one whose directory is
// gone is removed with nothing more to say.
func TestIndexRemoveAway(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 22, 12, 0, 0, 0, time.Local))
	disk := filepath.Join(ct.work, "disk")
	if err := os.MkdirAll(filepath.Join(disk, "bills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disk, "bills", "gas.pdf"), []byte("gas"), 0o600); err != nil {
		t.Fatal(err)
	}
	sameDevice(t, map[string]uint64{disk: 2})
	ct.filo(ExitOK, "", false, "roots", "add", disk)
	ct.filo(ExitOK, "", false, "indexes", "add", filepath.Join(disk, "bills"))
	sameDevice(t, nil) // the drive is no longer mounted

	r := ct.filo(ExitOK, "", false, "indexes", "remove", filepath.Join(disk, "bills"))
	ct.contains("no terminal", r.stdout, "its files cannot be removed", "is not mounted",
		"the next scan registers them as your files", "Nothing changed: there is no terminal to ask in")
	r = ct.filo(ExitOK, "n\n", true, "indexes", "remove", filepath.Join(disk, "bills"))
	ct.contains("declined", r.stdout, "Remove the index anyway, leaving its files? [y/N]", "Nothing changed.")
	r = ct.filo(ExitOK, "y\n", true, "indexes", "remove", filepath.Join(disk, "bills"))
	golden(t, "indexes-remove-away.txt", ct.work, r.stdout)
	if _, err := os.Stat(filepath.Join(disk, "bills", "index.html")); err != nil {
		t.Errorf("the pages of an unreachable root were touched: %v", err)
	}
	if r := ct.filo(ExitOK, "", false, "indexes"); !strings.Contains(r.stdout, "none yet") {
		t.Errorf("the index is still listed:\n%s", r.stdout)
	}

	// A directory gone from a root that is there: its pages went with it.
	tt := newTagTest(t)
	tt.filo(ExitOK, "", false, "indexes", "add", tt.in("notes"))
	if err := os.RemoveAll(tt.in("notes")); err != nil {
		t.Fatal(err)
	}
	r = tt.filo(ExitOK, "", false, "indexes", "remove", tt.in("notes"), "-y")
	tt.contains("gone", r.stdout, "notes is gone, and its pages with it", "Done   removed the index")
}
