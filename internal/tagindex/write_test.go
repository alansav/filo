package tagindex

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/store"
)

// fixture is an index on bills in a temporary root.
func fixture(t *testing.T) store.TagIndex {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bills"), 0o755); err != nil {
		t.Fatal(err)
	}
	return store.TagIndex{RootPath: root, Dir: "bills", Name: "index"}
}

func pages(files map[string]string, dirs ...string) Pages {
	p := Pages{Files: map[string][]byte{}, Dirs: dirs}
	for k, v := range files {
		p.Files[k] = []byte(v)
	}
	return p
}

// onDisk is every entry under the index's directory, a file as "path=content", a directory as "path/".
func onDisk(t *testing.T, ix store.TagIndex) []string {
	t.Helper()
	var out []string
	base := ix.DirAbs()
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if p == base {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			out = append(out, rel+"@")
		case d.IsDir():
			out = append(out, rel+"/")
		default:
			b, _ := os.ReadFile(p)
			out = append(out, rel+"="+string(b))
		}
		return nil
	})
	return out
}

func write(t *testing.T, ix *store.TagIndex, p Pages) Result {
	t.Helper()
	res := Write(*ix, p)
	ix.Files = res.Files
	return res
}

func problems(r Result) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.String())
	}
	return out
}

func check(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s =\n%q\nwant\n%q", what, got, want)
	}
}

func TestWrite(t *testing.T) {
	ix := fixture(t)
	first := pages(map[string]string{"index.html": "main", ".index/style.css": "css", ".index/a/x.html": "x1",
		".index/b/y.html": "y"}, ".index", ".index/a", ".index/b")
	res := write(t, &ix, first)
	if res.Written != 4 || res.Removed != 0 || len(res.Problems) != 0 {
		t.Fatalf("first write: %+v", res)
	}
	check(t, "on disk", onDisk(t, ix), []string{".index/", ".index/a/", ".index/a/x.html=x1", ".index/b/", ".index/b/y.html=y",
		".index/style.css=css", "index.html=main"})
	if len(ix.Files) != 7 || ix.Files[".index/a"] != "" || ix.Files["index.html"] != hashOf([]byte("main")) {
		t.Errorf("files recorded: %v", ix.Files)
	}

	// The same pages again: nothing written.
	if res := write(t, &ix, first); res.Written != 0 || res.Removed != 0 || len(res.Problems) != 0 {
		t.Errorf("same pages again: %+v", res)
	}

	// A page changes, a key's directory goes: the page is replaced, the other directory and its page removed.
	second := pages(map[string]string{"index.html": "main", ".index/style.css": "css", ".index/a/x.html": "x2"},
		".index", ".index/a")
	if res := write(t, &ix, second); res.Written != 1 || res.Removed != 1 || len(res.Problems) != 0 {
		t.Errorf("second write: %+v", res)
	}
	check(t, "on disk", onDisk(t, ix), []string{".index/", ".index/a/", ".index/a/x.html=x2", ".index/style.css=css",
		"index.html=main"})
	if _, ok := ix.Files[".index/b"]; ok {
		t.Errorf("a removed directory is still recorded: %v", ix.Files)
	}
}

func TestWriteLeavesWhatIsNotFilos(t *testing.T) {
	ix := fixture(t)
	first := pages(map[string]string{"index.html": "main", ".index/a/x.html": "x1", ".index/a/z.html": "z",
		".index/b/y.html": "y"}, ".index", ".index/a", ".index/b")
	write(t, &ix, first)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	put := func(rel, content string) {
		if err := os.WriteFile(ix.Abs(rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("index.html", "edited by hand")    // changed since Filo wrote it
	put(".index/a/new.html", "theirs too") // where Filo now wants a page, not written by it
	put(".index/b/notes.txt", "notes")     // in a directory Filo no longer needs
	if err := os.Remove(ix.Abs(".index/a/z.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ix.Abs(".index/a/z.html")); err != nil { // a symlink where a page was
		t.Fatal(err)
	}

	next := pages(map[string]string{"index.html": "main 2", ".index/a/x.html": "x2", ".index/a/z.html": "z2",
		".index/a/new.html": "new"}, ".index", ".index/a")
	res := write(t, &ix, next)
	check(t, "problems", problems(res), []string{
		".index/a/new.html " + whyNotFilos,
		".index/a/z.html " + whyNotFilos,
		"index.html " + whyChanged,
		".index/b holds files that are not Filo's: it stays",
	})
	if res.Written != 1 || res.Removed != 1 {
		t.Errorf("written %d, removed %d; want 1 (x.html) and 1 (b/y.html)", res.Written, res.Removed)
	}
	check(t, "on disk", onDisk(t, ix), []string{".index/", ".index/a/", ".index/a/new.html=theirs too",
		".index/a/x.html=x2", ".index/a/z.html@", ".index/b/", ".index/b/notes.txt=notes", "index.html=edited by hand"})
	if b, _ := os.ReadFile(outside); string(b) != "theirs" {
		t.Errorf("the symlink's target was written: %q", b)
	}
	for _, name := range []string{"index.html", ".index/a/new.html", ".index/a/z.html", ".index/b"} {
		if _, ok := ix.Files[name]; ok {
			t.Errorf("%s is still recorded as Filo's", name)
		}
	}

	// Once the person moves them aside, the next write puts the pages there.
	for _, rel := range []string{"index.html", ".index/a/new.html", ".index/a/z.html"} {
		if err := os.Remove(ix.Abs(rel)); err != nil {
			t.Fatal(err)
		}
	}
	if res := write(t, &ix, next); len(res.Problems) != 0 || res.Written != 3 {
		t.Errorf("after moving them aside: %+v", res)
	}
}

func TestWriteUnreachable(t *testing.T) {
	ix := fixture(t)
	write(t, &ix, pages(map[string]string{"index.html": "main"}))
	before := maps.Clone(ix.Files)
	if err := os.RemoveAll(ix.DirAbs()); err != nil {
		t.Fatal(err)
	}
	res := Write(ix, pages(map[string]string{"index.html": "main 2"}))
	if len(res.Problems) != 1 || !strings.HasPrefix(res.Problem(), "its directory cannot be reached") ||
		!maps.Equal(res.Files, before) {
		t.Errorf("unreachable: %+v", res)
	}
	// A pages directory Filo did not make: none of its pages are written into it.
	ix = fixture(t)
	if err := os.Mkdir(ix.Abs(".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	res = write(t, &ix, pages(map[string]string{"index.html": "main", ".index/a/x.html": "x"}, ".index", ".index/a"))
	check(t, "problems", problems(res), []string{".index " + whyNotFilos})
	check(t, "on disk", onDisk(t, ix), []string{".index/", "index.html=main"})
}

func TestRemoveAndPlan(t *testing.T) {
	ix := fixture(t)
	write(t, &ix, pages(map[string]string{"index.html": "main", ".index/style.css": "css", ".index/a/x.html": "x"},
		".index", ".index/a"))
	if n, left := Plan(ix); n != 3 || len(left) != 0 {
		t.Errorf("Plan = %d, %v; want 3 and nothing left", n, left)
	}
	res := Remove(ix)
	if res.Removed != 3 || len(res.Problems) != 0 || len(res.Files) != 0 {
		t.Errorf("Remove = %+v", res)
	}
	check(t, "on disk", onDisk(t, ix), nil)

	// A file that is not Filo's stays, and so does its directory.
	ix = fixture(t)
	write(t, &ix, pages(map[string]string{"index.html": "main", ".index/style.css": "css"}, ".index"))
	if err := os.WriteFile(ix.Abs(".index/notes.txt"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ix.Abs(".index/style.css"), []byte("restyled"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, left := Plan(ix)
	var got []string
	for _, p := range left {
		got = append(got, p.String())
	}
	check(t, "left", got, []string{".index/notes.txt is not Filo's: it stays",
		".index/style.css was changed since Filo wrote it: it stays"})
	if n != 1 {
		t.Errorf("Plan counts %d files to remove, want 1", n)
	}
	res = Remove(ix)
	check(t, "problems", problems(res), []string{".index/style.css was changed since Filo wrote it: it stays",
		".index holds files that are not Filo's: it stays"})
	check(t, "on disk", onDisk(t, ix), []string{".index/", ".index/notes.txt=notes", ".index/style.css=restyled"})
}
