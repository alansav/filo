package cli

import (
	"os"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

// filo duplicates, exclude and include (docs/design.md, Duplicates, "filo duplicates" and Output): what is not
// counted as duplicates, and undoing it.

// backupTree is a tree with a backup folder: Documents/report.pdf and Documents/notes.txt are each copied once into
// Backup, and two copies of contract.pdf are outside it.
var backupTree = map[string]string{
	"Documents/report.pdf":        "report",
	"Documents/notes.txt":         "notes",
	"Backup/Documents/report.pdf": "report",
	"Backup/Documents/notes.txt":  "notes",
	"Backup/photo.jpg":            "photo",
	"a/contract.pdf":              "contract",
	"b/contract.pdf":              "contract",
}

// TestDuplicatesList: nothing stored, then each kind listed.
func TestDuplicatesList(t *testing.T) {
	d := newDupTest(t, backupTree)
	out := d.filo(ExitOK, "", false, "duplicates").stdout
	d.contains("nothing stored", out, "Not counted as duplicates   none", "filo duplicates exclude DIR")

	d.filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup"))
	out = d.filo(ExitOK, "", false, "duplicates").stdout
	d.contains("an exclusion", out, "Not counted as duplicates   1 excluded directory", "Excluded directories",
		d.in("Backup"), "3 files . 16 B", "Next: filo duplicates include PATH to count one as a duplicate again")
}

// TestDuplicatesExclude: exclude and include a directory, with what it replaces, covers and changes.
func TestDuplicatesExclude(t *testing.T) {
	d := newDupTest(t, backupTree)
	filo, contains := d.filo, d.contains

	out := filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup/Documents")).stdout
	contains("exclude a subfolder", out, "Excluded   "+d.in("Backup/Documents"),
		"2 files under it no longer count as duplicates (still scanned, retentions unchanged)",
		"2 duplicate groups fewer: 1 left", "Next: filo duplicates include "+d.in("Backup/Documents")+" to undo it")
	if n := d.groups(); n != 1 {
		t.Errorf("groups after excluding = %d, want 1 (contract.pdf)", n)
	}

	out = filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup")).stdout
	contains("exclude its parent", out, "Excluded   "+d.in("Backup"), "3 files under it",
		"replaces 1 exclusion under it: Documents", "duplicate groups unchanged: 1")

	contains("already covered", filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup/Documents")).stdout,
		"Already excluded   "+d.in("Backup/Documents"), "through "+d.in("Backup"))
	contains("already itself", filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup")).stdout,
		"Already excluded   "+d.in("Backup"))

	contains("include a subfolder", filo(ExitOK, "", false, "duplicates", "include", d.in("Backup/Documents")).stdout,
		"Not excluded itself   "+d.in("Backup/Documents"), d.in("Backup")+" is: filo duplicates include "+d.in("Backup")+
			" counts all of it again")
	out = filo(ExitOK, "", false, "duplicates", "include", d.in("Backup")).stdout
	contains("include", out, "Included again   "+d.in("Backup"), "3 files under it count as duplicates again",
		"2 duplicate groups more: 3", "Next: filo export duplicates -o FILE to decide them")
	if n := d.groups(); n != 3 {
		t.Errorf("groups after including = %d, want 3", n)
	}
	contains("nothing to include", filo(ExitOK, "", false, "duplicates", "include", d.in("Backup")).stdout,
		"Nothing to include   "+d.in("Backup"), "not excluded")

	// The whole root may be excluded; outside every root, or a file, is refused.
	contains("the whole root", filo(ExitOK, "", false, "duplicates", "exclude", d.root).stdout, "Excluded",
		"3 duplicate groups fewer: 0 left")
	r := filo(ExitError, "", false, "duplicates", "exclude", d.work)
	contains("outside every root", r.stderr, "not inside any root")
	r = filo(ExitError, "", false, "duplicates", "exclude", d.in("a/contract.pdf"))
	contains("a file", r.stderr, "not a directory")
}

// TestDuplicatesInclude: a copy of an always content makes the content a duplicate again; a copy that is a record
// removes its whole set; a file that is neither says so; a record under an excluded folder has its own kind undone
// and says the folder is still excluded.
func TestDuplicatesInclude(t *testing.T) {
	d := newDupTest(t, map[string]string{"a/LICENSE": "MIT", "b/LICENSE": "MIT", "2020/terms.pdf": "terms",
		"2021/terms.pdf": "terms", "a/notes.txt": "notes", "b/notes.txt": "notes"})
	p := d.export("d.csv")
	d.decide(p, map[string]string{"a/LICENSE": "always", "b/LICENSE": "always", "2020/terms.pdf": "keep",
		"2021/terms.pdf": "keep"})
	d.run(ExitOK, p)
	if n := d.groups(); n != 1 {
		t.Fatalf("groups = %d, want 1 (notes.txt)", n)
	}
	out := d.filo(ExitOK, "", false, "duplicates").stdout
	d.contains("list", out, "Not counted as duplicates   1 content marked always . 1 set of identical files kept as separate files",
		"Contents marked always",
		"LICENSE . 3 B each . 2 copies", "a/LICENSE", "Kept as separate files", "terms.pdf . 5 B each . 2 separate files",
		"2020/terms.pdf")

	out = d.filo(ExitOK, "", false, "duplicates", "include", d.in("a/LICENSE")).stdout
	d.contains("always", out, "Duplicate again   LICENSE . 3 B each . 2 copies",
		"no longer marked always: 1 duplicate group more")
	out = d.filo(ExitOK, "", false, "duplicates", "include", d.in("2021/terms.pdf")).stdout
	d.contains("record", out, "Duplicates again   terms.pdf . 5 B each . 2 separate files",
		"no longer kept as separate files: 1 duplicate group more")
	if n := d.groups(); n != 3 {
		t.Errorf("groups = %d, want 3", n)
	}
	out = d.filo(ExitOK, "", false, "duplicates", "include", d.in("a/notes.txt")).stdout
	d.contains("neither", out, "Nothing to include   "+d.in("a/notes.txt"),
		"not excluded, marked always or kept as a separate file: it already counts as a duplicate")
	r := d.filo(ExitError, "", false, "duplicates", "include", d.in("nowhere.txt"))
	d.contains("not in the register", r.stderr, "nowhere.txt")

	// A record under an excluded folder: the record goes, the folder stays excluded.
	p = d.export("e.csv")
	d.decide(p, map[string]string{"2020/terms.pdf": "keep", "2021/terms.pdf": "keep"})
	d.run(ExitOK, p)
	d.filo(ExitOK, "", false, "duplicates", "exclude", d.in("2021"))
	out = d.filo(ExitOK, "", false, "duplicates", "include", d.in("2021/terms.pdf")).stdout
	d.contains("record under an exclusion", out, "Duplicates again   terms.pdf",
		d.in("2021")+" is still excluded: filo duplicates include "+d.in("2021")+" counts it again")
}

// TestDuplicatesUsage: the subcommands' arguments.
func TestDuplicatesUsage(t *testing.T) {
	d := newDupTest(t, backupTree)
	for _, args := range [][]string{{"duplicates", "exclude"}, {"duplicates", "include"},
		{"duplicates", "exclude", "a", "b"}, {"duplicates", "frobnicate"}, {"duplicates", "list", "x"}} {
		d.filo(ExitUsage, "", false, args...)
	}
}

// TestDuplicatesGolden: the whole plain output of filo duplicates, exclude and include (docs/design.md, Duplicates,
// Output): the list with each kind, and each command's answers.
func TestDuplicatesGolden(t *testing.T) {
	files := map[string]string{"2020/terms.pdf": "terms", "2021/terms.pdf": "terms"}
	for k, v := range backupTree {
		files[k] = v
	}
	for _, dir := range []string{"a", "b", "c", "d", "e"} {
		files["projects/"+dir+"/LICENSE"] = "MIT"
	}
	d := newDupTest(t, files)
	d.setRetentions(map[string]string{"2020/terms.pdf": "2027-12-31", "2021/terms.pdf": "2028-12-31"})
	p := d.export("d.csv")
	actions := map[string]string{"2020/terms.pdf": "keep", "2021/terms.pdf": "keep"}
	for _, dir := range []string{"a", "b", "c", "d", "e"} {
		actions["projects/"+dir+"/LICENSE"] = "always"
	}
	d.decide(p, actions)
	d.run(ExitOK, p)

	var out string
	run := func(args ...string) {
		out += "$ filo " + args[0]
		for _, a := range args[1:] {
			out += " " + a
		}
		out += "\n\n" + d.filo(ExitOK, "", false, args...).stdout + "\n"
	}
	run("duplicates", "exclude", d.in("Backup/Documents"))
	run("duplicates", "exclude", d.in("Backup"))
	run("duplicates", "exclude", d.in("Backup/Documents"))
	run("duplicates")
	run("duplicates", "include", d.in("Backup/Documents"))
	run("duplicates", "include", d.in("Backup"))
	run("duplicates", "include", d.in("projects/a/LICENSE"))
	run("duplicates", "include", d.in("2020/terms.pdf"))
	run("duplicates", "include", d.in("Documents/notes.txt"))
	run("duplicates")
	golden(t, "duplicates.txt", d.root, out)
}

// TestDuplicatesReviewFindings: the cases found in the code review of slice 4 (2026-11-16).
func TestDuplicatesReviewFindings(t *testing.T) {
	records := func(t *testing.T, files map[string]string, keep ...string) dupTest {
		d := newDupTest(t, files)
		p := d.export("d.csv")
		actions := map[string]string{}
		for _, rel := range keep {
			actions[rel] = "keep"
		}
		d.decide(p, actions)
		d.run(ExitOK, p)
		return d
	}
	terms := map[string]string{"2020/terms.pdf": "terms", "2021/terms.pdf": "terms"}

	t.Run("the exclusion of a folder since deleted can be removed", func(t *testing.T) {
		d := newDupTest(t, backupTree)
		d.filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup"))
		if err := os.RemoveAll(d.in("Backup")); err != nil {
			t.Fatal(err)
		}
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("Backup")).stdout,
			"Included again   "+d.in("Backup"))
		d.contains("list", d.filo(ExitOK, "", false, "duplicates").stdout, "Not counted as duplicates   none")
	})

	t.Run("a record whose folder went can be included", func(t *testing.T) {
		d := records(t, terms, "2020/terms.pdf", "2021/terms.pdf")
		if err := os.RemoveAll(d.in("2021")); err != nil {
			t.Fatal(err)
		}
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("2021/terms.pdf")).stdout,
			"Duplicates again   terms.pdf", "no longer kept as separate files")
	})

	t.Run("a mistyped path says what it is not", func(t *testing.T) {
		d := newDupTest(t, backupTree)
		d.contains("include", d.filo(ExitError, "", false, "duplicates", "include", d.in("Bakup")).stderr,
			"not a file in the register, nor an excluded directory")
	})

	t.Run("the list counts as the report does", func(t *testing.T) {
		// Records that are also always are listed as always only.
		d := records(t, terms, "2020/terms.pdf", "2021/terms.pdf")
		s, err := store.Open(d.ctx, store.Options{DBPath: d.db})
		if err != nil {
			t.Fatal(err)
		}
		f, err := s.FileAt(d.ctx, 1, "2020/terms.pdf")
		if err == nil {
			err = s.AddAlways(d.ctx, f.Hash, time.Now())
		}
		if cerr := s.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		out := d.filo(ExitOK, "", false, "duplicates").stdout
		d.contains("always and records", out, "Not counted as duplicates   1 content marked always . ")
		d.lacks("always and records", out, "Kept as separate files")

		// A set with a new copy is a duplicate group again, so it is not listed; include still removes it.
		d = records(t, terms, "2020/terms.pdf", "2021/terms.pdf")
		d.write("2022/terms.pdf", "terms")
		d.filo(ExitOK, "", false, "scan")
		d.contains("a new copy", d.filo(ExitOK, "", false, "duplicates").stdout, "Not counted as duplicates   none")
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("2020/terms.pdf")).stdout,
			"no longer kept as separate files")
	})

	t.Run("a hard link of a record is a record, and counts as the list does", func(t *testing.T) {
		d := records(t, terms, "2020/terms.pdf", "2021/terms.pdf")
		if err := os.Link(d.in("2020/terms.pdf"), d.in("2020/z-link.pdf")); err != nil {
			t.Fatal(err)
		}
		d.filo(ExitOK, "", false, "scan")
		d.contains("list", d.filo(ExitOK, "", false, "duplicates").stdout, "terms.pdf . 5 B each . 2 separate files")
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("2020/z-link.pdf")).stdout,
			"Duplicates again   z-link.pdf . 5 B each . 2 separate files", "no longer kept as separate files")
	})

	t.Run("include counts copies as the list does, leaving out excluded ones", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/LICENSE": "MIT", "b/LICENSE": "MIT", "c/LICENSE": "MIT",
			"Backup/LICENSE": "MIT"})
		d.filo(ExitOK, "", false, "duplicates", "exclude", d.in("Backup"))
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/LICENSE": "always", "b/LICENSE": "always", "c/LICENSE": "always"})
		d.run(ExitOK, p)
		d.contains("list", d.filo(ExitOK, "", false, "duplicates").stdout, "LICENSE . 3 B each . 3 copies")
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("a/LICENSE")).stdout,
			"Duplicate again   LICENSE . 3 B each . 3 copies")
	})

	t.Run("-h shows the help page", func(t *testing.T) {
		d := newDupTest(t, backupTree)
		for _, args := range [][]string{{"duplicates", "-h"}, {"duplicates", "exclude", "-h"},
			{"duplicates", "include", "-h"}} {
			d.contains(args[len(args)-2], d.filo(ExitOK, "", false, args...).stdout, "filo duplicates exclude DIR")
		}
	})

	t.Run("one file under a folder: the verb agrees", func(t *testing.T) {
		d := newDupTest(t, backupTree)
		d.contains("exclude", d.filo(ExitOK, "", false, "duplicates", "exclude", d.in("a")).stdout,
			"1 file under it no longer counts as duplicates")
		d.contains("include", d.filo(ExitOK, "", false, "duplicates", "include", d.in("a")).stdout,
			"1 file under it counts as duplicates again")
	})
}
