package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexesGolden: filo indexes in a terminal (docs/design.md, Tag index pages): none yet, adding one, a second
// name on the same directory, the list, refreshing, and removing one with a file that is not Filo's in it.
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
