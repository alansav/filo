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

// A page Filo cannot remove keeps its directory Filo's, so the next run tries again, and the directory is not then
// called someone else's.
func TestWriteKeepsADirectoryWithAFiloFileLeft(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files in a read-only directory")
	}
	ix := fixture(t)
	write(t, &ix, pages(map[string]string{"index.html": "main", ".index/a/x.html": "x"}, ".index", ".index/a"))
	if err := os.Chmod(ix.Abs(".index/a"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ix.Abs(".index/a"), 0o755) })
	res := write(t, &ix, pages(map[string]string{"index.html": "main"}, ".index"))
	if len(res.Problems) != 1 || !strings.HasPrefix(res.Problems[0].String(), ".index/a/x.html could not be removed") {
		t.Errorf("problems = %q", problems(res))
	}
	if h, ok := ix.Files[".index/a"]; !ok || h != "" {
		t.Errorf("the directory is no longer recorded as Filo's: %v", ix.Files)
	}
	if err := os.Chmod(ix.Abs(".index/a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if res := write(t, &ix, pages(map[string]string{"index.html": "main"}, ".index")); len(res.Problems) != 0 ||
		res.Removed != 1 {
		t.Errorf("the next run: %+v", res)
	}
	check(t, "on disk", onDisk(t, ix), []string{".index/", "index.html=main"})
}

// The main page's temporary file is written in the pages directory, which no scan sees.
func TestTempFilesInThePagesDirectory(t *testing.T) {
	ix := fixture(t)
	w := writer{ix: ix, res: Result{Files: map[string]string{}}}
	if got := w.tempDir("index.html"); got != "." {
		t.Errorf("tempDir before the pages directory is made = %s", got)
	}
	w.res.Files[".index"] = ""
	for name, want := range map[string]string{"index.html": ".index", ".index/style.css": ".index",
		".index/a/x.html": ".index/a"} {
		if got := w.tempDir(name); got != want {
			t.Errorf("tempDir(%s) = %s, want %s", name, got, want)
		}
	}
}

// TestWriteNeverFollowsASwappedDirectory: a pages directory, or a key's, swapped for a symlink to a directory outside
// the index while the pages are written (another device syncing the directory) is never followed: nothing lands
// outside, and the pages go into the directories Filo opened (docs/design.md, Tag index pages, "Filo's own files").
func TestWriteNeverFollowsASwappedDirectory(t *testing.T) {
	for _, swapped := range []string{".index", ".index/a"} {
		t.Run(swapped, func(t *testing.T) {
			ix := fixture(t)
			outside := t.TempDir()
			p := pages(map[string]string{"index.html": "main", ".index/style.css": "css", ".index/a/x.html": "x"},
				".index", ".index/a")
			write(t, &ix, p)
			moved := ix.Abs(swapped) + "-moved"
			once := false
			beforeWrite = func(name string) {
				if once {
					return
				}
				once = true
				if err := os.Rename(ix.Abs(swapped), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, ix.Abs(swapped)); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { beforeWrite = nil })
			// Every page changes, so each is written again.
			res := write(t, &ix, pages(map[string]string{"index.html": "main 2", ".index/style.css": "css 2",
				".index/a/x.html": "x 2"}, ".index", ".index/a"))
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Errorf("written outside the index: %v", entries)
			}
			// The pages go into the directories opened. NAME.html's temporary file is in the pages directory, and
			// its rename into the index's directory goes by .index, now a symlink out: refused, not followed.
			wantProblems, wantWritten := []string(nil), 3
			if swapped == ".index" {
				wantProblems = []string{"index.html could not be written: path escapes from parent"}
				wantWritten = 2
			}
			check(t, "problems", problems(res), wantProblems)
			if res.Written != wantWritten {
				t.Errorf("written %d, want %d", res.Written, wantWritten)
			}
			if tmps, _ := filepath.Glob(filepath.Join(moved, ".*.tmp")); len(tmps) != 0 {
				t.Errorf("temporary files left: %v", tmps)
			}
			want := map[string]string{".index": "css 2", ".index/a": "x 2"}
			if b, err := os.ReadFile(filepath.Join(moved, map[string]string{".index": "style.css",
				".index/a": "x.html"}[swapped])); err != nil || string(b) != want[swapped] {
				t.Errorf("the page in the directory opened = %q, %v; want %q", b, err, want[swapped])
			}
		})
	}
}
