package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

// The scenarios of filo delete FILE.csv (docs/design.md, Duplicates, "The scenarios"): A a CSV run twice, B several
// CSVs, C editing the CSV, D files changing before the run, E inside one run; and a copy that is, on disk, the same
// file as the one kept. Each starts from its own tree.

// dupTest is a cliTest with duplicates helpers.
type dupTest struct{ *cliTest }

// newDupTest writes the files (path -> content) and adds the root.
func newDupTest(t *testing.T, files map[string]string) dupTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local))
	for rel, content := range files {
		ct.write(rel, content)
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	return dupTest{ct}
}

// export exports the duplicates (under the root's dir, if given) to work/name and returns its path.
func (d dupTest) export(name string, dir ...string) string {
	d.t.Helper()
	p := filepath.Join(d.work, name)
	args := []string{"export", "duplicates"}
	for _, rel := range dir {
		args = append(args, d.in(rel))
	}
	d.filo(ExitOK, "", false, append(args, "-o", p)...)
	return p
}

// decide sets the action cells of the rows for the given paths (relative to the root); a path not given keeps its
// cell. An action of "-" deletes the row from the CSV.
func (d dupTest) decide(csvPath string, actions map[string]string) {
	d.t.Helper()
	editCSV(d.t, csvPath, func(row map[string]string) {
		rel, err := filepath.Rel(d.root, filepath.Join(row["directory"], row["name"]))
		if err != nil {
			d.t.Fatal(err)
		}
		switch a, ok := actions[filepath.ToSlash(rel)]; {
		case !ok:
		case a == "-":
			row[dropRow] = "1"
		default:
			row["action"] = a
		}
	})
}

// run runs filo delete -y on the CSV, wanting exit code code, and returns its output.
func (d dupTest) run(code int, csvPath string) string {
	d.t.Helper()
	return d.filo(code, "", false, "delete", "-y", csvPath).stdout
}

// there checks which paths exist.
func (d dupTest) there(when string, want map[string]bool) {
	d.t.Helper()
	for rel, w := range want {
		if got := d.exists(rel); got != w {
			d.t.Errorf("%s: %s exists = %v, want %v", when, rel, got, w)
		}
	}
}

// groups returns how many duplicate groups an export finds now (not counted ones left out).
func (d dupTest) groups() int {
	d.t.Helper()
	p := filepath.Join(d.work, "count-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".csv")
	d.filo(ExitOK, "", false, "export", "duplicates", "-o", p)
	if _, err := os.Stat(p); err != nil {
		return 0
	}
	n := map[string]bool{}
	for _, r := range readRows(d.t, p) {
		n[r["group"]] = true
	}
	return len(n)
}

// setRetentions gives files (path relative to the root -> retention) a retention through a retention CSV.
func (d dupTest) setRetentions(values map[string]string) {
	d.t.Helper()
	p := filepath.Join(d.work, "r-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".csv")
	d.filo(ExitOK, "", false, "export", "retention", "-o", p)
	editCSV(d.t, p, func(row map[string]string) {
		rel, _ := filepath.Rel(d.root, filepath.Join(row["directory"], row["name"]))
		if v, ok := values[filepath.ToSlash(rel)]; ok {
			row["retention"] = v
		}
	})
	d.filo(ExitOK, "", false, "update", p)
}

var contracts = map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract",
	"c/contract.pdf": "contract"}

// TestDuplicatesDeleteRunTwice: A1, a CSV run, edited further and run again; A2, a group decided differently
// between two runs of the same CSV.
func TestDuplicatesDeleteRunTwice(t *testing.T) {
	t.Run("A1: the groups done are reported as gone, the new decisions carried out", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract",
			"a/LICENSE": "MIT", "b/LICENSE": "MIT", "a/notes.txt": "notes", "b/notes.txt": "notes"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete",
			"a/LICENSE": "always", "b/LICENSE": "always"})
		out := d.run(ExitOK, p)
		d.contains("first run", out, "deleted 1 copy", "1 group not decided")
		d.there("first run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": false, "a/LICENSE": true,
			"b/LICENSE": true, "a/notes.txt": true, "b/notes.txt": true})
		if n := d.groups(); n != 1 {
			t.Errorf("groups after the first run = %d, want 1 (notes.txt; LICENSE is always)", n)
		}

		d.decide(p, map[string]string{"a/notes.txt": "delete", "b/notes.txt": "keep"})
		out = d.run(ExitOK, p)
		d.contains("second run", out, "deleted 1 copy", "contract.pdf", "already done")
		d.there("second run", map[string]bool{"a/contract.pdf": true, "a/notes.txt": false, "b/notes.txt": true})
	})

	t.Run("A2: the second run does what the CSV now says", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "keep", "c/contract.pdf": "keep"})
		d.contains("records", d.run(ExitOK, p), "set of 3 records")
		if n := d.groups(); n != 0 {
			t.Errorf("groups after recording = %d, want 0", n)
		}
		d.contains("records again", d.run(ExitOK, p), "already done: the 3 copies are a set of records")
		d.decide(p, map[string]string{"c/contract.pdf": "delete"})
		d.contains("then delete one", d.run(ExitOK, p), "deleted 1 copy")
		d.there("second run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "c/contract.pdf": false})
	})
}

// TestDuplicatesDeleteSeveralCSVs: B1, two CSVs of one group run in turn; B2, a CSV whose copies another run
// deleted; B3, exports of overlapping directories.
func TestDuplicatesDeleteSeveralCSVs(t *testing.T) {
	t.Run("B1: each run does what its CSV says, for the files still as exported", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract"})
		one, two := d.export("1.csv"), d.export("2.csv")
		d.decide(one, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete"})
		d.decide(two, map[string]string{"a/contract.pdf": "delete", "b/contract.pdf": "keep"})
		d.run(ExitOK, two)
		d.there("2.csv", map[string]bool{"a/contract.pdf": false, "b/contract.pdf": true})
		d.contains("1.csv", d.run(ExitOK, one), "no copy would be left")
		d.there("1.csv", map[string]bool{"b/contract.pdf": true})
	})

	t.Run("B2: rows of copies another run deleted are reported and ignored", func(t *testing.T) {
		d := newDupTest(t, contracts)
		one, two := d.export("1.csv"), d.export("2.csv")
		d.decide(two, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete",
			"c/contract.pdf": "keep"})
		d.run(ExitOK, two)
		d.decide(one, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete",
			"c/contract.pdf": "delete"})
		d.contains("1.csv", d.run(ExitOK, one), "deleted 1 copy", "b/contract.pdf: gone")
		d.there("1.csv", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": false, "c/contract.pdf": false})
	})

	t.Run("B3: an export of one directory has the group's copies outside it", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("a.csv", "a")
		d.decide(p, map[string]string{"a/contract.pdf": "delete", "b/contract.pdf": "keep",
			"c/contract.pdf": "delete"})
		d.run(ExitOK, p)
		d.there("a.csv", map[string]bool{"a/contract.pdf": false, "b/contract.pdf": true, "c/contract.pdf": false})
	})
}

// TestDuplicatesDeleteEditedCSV: C1, a row deleted from the CSV; C4, a copy deleted with a longer retention than
// every copy kept. (C2, C3 and C5 are the plan's: TestPlanStructure and TestPlanGroupRules.)
func TestDuplicatesDeleteEditedCSV(t *testing.T) {
	t.Run("C1: a row deleted from the CSV whose file is there skips the group", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "-"})
		d.contains("run", d.run(ExitOK, p), "c/contract.pdf", "its row was deleted from the CSV")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "c/contract.pdf": true})
	})

	t.Run("C1: a row deleted from the CSV whose file is gone is ignored", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "-"})
		if err := os.Remove(d.in("c/contract.pdf")); err != nil {
			t.Fatal(err)
		}
		d.contains("run", d.run(ExitOK, p), "deleted 1 copy")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": false})
	})

	t.Run("C4: keeping the copy with the shorter retention is refused, the longer one allowed", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"2020/terms.pdf": "terms", "2021/terms.pdf": "terms"})
		d.setRetentions(map[string]string{"2020/terms.pdf": "2027-12-31", "2021/terms.pdf": "2028-12-31"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"2020/terms.pdf": "keep", "2021/terms.pdf": "delete"})
		d.contains("shorter kept", d.run(ExitError, p), "2021/terms.pdf", "keep it instead, or keep both")
		d.there("shorter kept", map[string]bool{"2020/terms.pdf": true, "2021/terms.pdf": true})

		d.decide(p, map[string]string{"2020/terms.pdf": "delete", "2021/terms.pdf": "keep"})
		d.contains("longer kept", d.run(ExitOK, p), "deleted 1 copy")
		d.there("longer kept", map[string]bool{"2020/terms.pdf": false, "2021/terms.pdf": true})
	})
}

// TestDuplicatesDeleteChangedFiles: D1-D4, files changing between the export and the run. The whole group is
// skipped, so a file is deleted only if it is still exactly the content decided on.
func TestDuplicatesDeleteChangedFiles(t *testing.T) {
	decided := map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "delete"}
	all := map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "c/contract.pdf": true}
	t.Run("D1: a copy edited after the export", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		d.write("c/contract.pdf", "contract, signed")
		d.contains("run", d.run(ExitOK, p), "c/contract.pdf", "changed since the export: export again")
		d.there("run", all)
	})

	t.Run("D2: every copy edited the same way", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		for rel := range contracts {
			d.write(rel, "contract, v2")
		}
		d.filo(ExitOK, "", false, "scan")
		d.contains("run", d.run(ExitOK, p), "changed since the export: export again")
		d.there("run", all)
	})

	t.Run("D3: a copy moved, and the move scanned", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		if err := os.Rename(d.in("c/contract.pdf"), d.in("c/contract-old.pdf")); err != nil {
			t.Fatal(err)
		}
		d.filo(ExitOK, "", false, "scan")
		d.contains("run", d.run(ExitOK, p), "c/contract.pdf: moved since the export (now ", "contract-old.pdf): export again")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "c/contract-old.pdf": true})
	})

	t.Run("D4: the copy kept cannot be read", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		if err := os.Chmod(d.in("a/contract.pdf"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d.in("a/contract.pdf"), 0o644) })
		d.contains("run", d.run(ExitOK, p), "a/contract.pdf", "cannot be read")
		d.there("run", all)
	})

	t.Run("D4: a folder that cannot be read is never gone", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		if err := os.Chmod(d.in("c"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d.in("c"), 0o755) })
		d.contains("run", d.run(ExitOK, p), "c/contract.pdf", "can't check")
		os.Chmod(d.in("c"), 0o755)
		d.there("run", all)
	})
}

// TestDuplicatesDeleteInsideARun: E1-E4, copies gone by the time of the run, and an interrupted run.
func TestDuplicatesDeleteInsideARun(t *testing.T) {
	decided := map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "delete"}
	remove := func(t *testing.T, d dupTest, rels ...string) {
		for _, rel := range rels {
			if err := os.Remove(d.in(rel)); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("E1: one copy to delete gone: the other deleted", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		remove(t, d, "b/contract.pdf")
		d.contains("run", d.run(ExitOK, p), "deleted 1 copy", "b/contract.pdf: gone")
		d.there("run", map[string]bool{"a/contract.pdf": true, "c/contract.pdf": false})
	})

	t.Run("E2: every copy to delete gone: reported", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		remove(t, d, "b/contract.pdf", "c/contract.pdf")
		d.contains("run", d.run(ExitOK, p), "already done", "removed outside filo")
		d.there("run", map[string]bool{"a/contract.pdf": true})
	})

	t.Run("E3: the copy to keep gone: skipped", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, decided)
		remove(t, d, "a/contract.pdf")
		d.contains("run", d.run(ExitOK, p), "no copy would be left")
		d.there("run", map[string]bool{"b/contract.pdf": true, "c/contract.pdf": true})
	})

	t.Run("E4: an interrupted run keeps what it did; the next reports it as done", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract",
			"a/report.pdf": "report", "b/report.pdf": "report"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete",
			"a/report.pdf": "keep", "b/report.pdf": "delete"})
		out := d.filo(ExitOK, "s\nd\nq\n", true, "delete", p).stdout
		d.contains("select, then quit", out, "[1/2]", "d do it . s skip . q quit", "[ok] deleted 1 copy",
			"1 group left for later")
		d.there("quit", map[string]bool{"b/contract.pdf": false, "b/report.pdf": true})
		d.contains("next run", d.run(ExitOK, p), "already done", "deleted 1 copy")
		d.there("next run", map[string]bool{"a/contract.pdf": true, "a/report.pdf": true, "b/report.pdf": false})
	})
}

// TestDuplicatesDeleteSameFile: a copy that is, on disk, the same file as the copy kept is never deleted: a folder
// bind-mounted after the export (faked with the device and inode of the kept copy), and a hard link.
func TestDuplicatesDeleteSameFile(t *testing.T) {
	t.Run("a bind mount", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete"})
		t.Cleanup(fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
			if p == d.in("b/contract.pdf") {
				p = d.in("a/contract.pdf")
			}
			id, err := real(p)
			id.Dev = 1
			return id, err
		}))
		d.contains("run", d.run(ExitOK, p), "the same file as", "(a hard link or a bind mount)")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true})
	})

	t.Run("a copy with another hard link can be kept but not deleted", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract"})
		if err := os.Link(d.in("b/contract.pdf"), d.in("b/z-link.pdf")); err != nil {
			t.Fatal(err)
		}
		d.filo(ExitOK, "", false, "scan")
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete"})
		d.contains("run", d.run(ExitOK, p), "has another hard link")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "b/z-link.pdf": true})
	})
}

// TestDuplicatesDeleteAsking: without a terminal and without -y nothing is done; declined, nothing is done; and a CSV
// that is not one is refused in one line.
func TestDuplicatesDeleteAsking(t *testing.T) {
	d := newDupTest(t, contracts)
	p := d.export("d.csv")
	d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "delete"})
	all := map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true, "c/contract.pdf": true}
	d.contains("no terminal", d.filo(ExitOK, "", false, "delete", p).stdout, "Will delete", "Nothing deleted")
	d.there("no terminal", all)
	d.contains("declined", d.filo(ExitOK, "n\n", true, "delete", p).stdout,
		"Delete 2 copies? [y/s/l/N]", "Nothing deleted")
	d.there("declined", all)

	r := filepath.Join(d.work, "r.csv")
	d.filo(ExitOK, "", false, "export", "retention", "-o", r)
	d.contains("a retention CSV", d.filo(ExitError, "", false, "delete", r).stderr, "this is a retention CSV")
}

// TestDuplicatesDeleteGolden: filo delete FILE.csv's whole plain output (docs/design.md, Terminal output): a run
// with -y (a group to delete with a symlink that would break, a set of records, a content always, a refused group,
// one not decided), the same CSV run again, and select mode.
func TestDuplicatesDeleteGolden(t *testing.T) {
	files := map[string]string{"2020/terms.pdf": "terms", "2021/terms.pdf": "terms", "a/notes.txt": "notes",
		"b/notes.txt": "notes", "a/report.pdf": "report", "c/report.pdf": "report"}
	for _, dir := range []string{"a", "b", "c"} {
		files[dir+"/contract.pdf"] = "contract"
		files[dir+"/LICENSE"] = "MIT"
	}
	d := newDupTest(t, files)
	if err := os.Symlink(d.in("b/contract.pdf"), d.in("link")); err != nil {
		t.Fatal(err)
	}
	d.filo(ExitOK, "", false, "scan")
	p := d.export("d.csv")
	d.decide(p, map[string]string{"2020/terms.pdf": "keep", "2021/terms.pdf": "k", "a/LICENSE": "always",
		"b/LICENSE": "a", "c/LICENSE": "always", "a/contract.pdf": "keep", "b/contract.pdf": "delete",
		"c/contract.pdf": "d", "a/report.pdf": "delete", "c/report.pdf": "delete"})
	golden(t, "delete-duplicates-yes.txt", d.root, d.run(ExitError, p))
	golden(t, "delete-duplicates-again.txt", d.root, d.run(ExitError, p))

	d.decide(p, map[string]string{"a/notes.txt": "keep", "b/notes.txt": "delete", "a/report.pdf": "keep"})
	golden(t, "delete-duplicates-select.txt", d.root, d.filo(ExitOK, "s\ns\nd\n", true, "delete", p).stdout)
}

// TestDuplicatesDeleteReviewFindings: the cases found in the code review of slice 3 (2026-11-16).
func TestDuplicatesDeleteReviewFindings(t *testing.T) {
	t.Run("one group in two exports with the same actions is carried out once", func(t *testing.T) {
		d := newDupTest(t, contracts)
		one, two := d.export("1.csv"), d.export("2.csv")
		decided := map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete", "c/contract.pdf": "delete"}
		d.decide(one, decided)
		d.decide(two, decided)
		both := filepath.Join(d.work, "both.csv")
		data := mustRead(t, one)
		rest := mustRead(t, two)
		data = append(data, rest[bytes.IndexByte(rest, '\n')+1:]...) // export 2's rows under export 1's
		if err := os.WriteFile(both, data, 0o600); err != nil {
			t.Fatal(err)
		}
		out := d.run(ExitOK, both)
		d.contains("run", out, "Will delete   2 copies in 1 group", "the same as group 1 of export 1, which does it",
			"Done   deleted 2 copies")
		d.lacks("run", out, "Skipped")
		d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": false, "c/contract.pdf": false})
	})

	t.Run("a refused group exits 1 also when nothing is done", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract",
			"a/report.pdf": "report", "b/report.pdf": "report"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete",
			"a/report.pdf": "delete", "b/report.pdf": "delete"})
		d.contains("no terminal", d.filo(ExitError, "", false, "delete", p).stdout, "no copy would be left",
			"Nothing deleted: there is no terminal")
		d.contains("declined", d.filo(ExitError, "n\n", true, "delete", p).stdout, "Nothing deleted.")
	})

	t.Run("records asked for with one copy to keep left: nothing to record, and said so", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "keep"})
		if err := os.Remove(d.in("b/contract.pdf")); err != nil {
			t.Fatal(err)
		}
		d.contains("run", d.run(ExitOK, p), "one copy to keep is left, the others gone (removed outside filo): "+
			"nothing to record")
	})

	t.Run("a mistyped CSV name fails as a file that cannot be opened", func(t *testing.T) {
		d := newDupTest(t, contracts)
		r := d.filo(ExitError, "", false, "delete", filepath.Join(d.work, "dupes.csv"))
		d.contains("stderr", r.stderr, "open "+filepath.Join(d.work, "dupes.csv")+": no such file or directory")
	})

	t.Run("a group with nothing left to do is done, even with an unreadable copy", func(t *testing.T) {
		d := newDupTest(t, map[string]string{"a/LICENSE": "MIT", "b/LICENSE": "MIT"})
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/LICENSE": "always", "b/LICENSE": "always"})
		d.run(ExitOK, p)
		if err := os.Chmod(d.in("b/LICENSE"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d.in("b/LICENSE"), 0o644) })
		d.contains("again", d.run(ExitOK, p), "already done: the content is always")
	})

	t.Run("with two copies to keep, one readable is enough", func(t *testing.T) {
		d := newDupTest(t, contracts)
		p := d.export("d.csv")
		d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "keep", "c/contract.pdf": "delete"})
		if err := os.Chmod(d.in("a/contract.pdf"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d.in("a/contract.pdf"), 0o644) })
		d.contains("run", d.run(ExitOK, p), "deleted 1 copy")
		d.there("run", map[string]bool{"b/contract.pdf": true, "c/contract.pdf": false})
	})
}

// TestDuplicatesDeleteNeedsFileDate: a copy to delete whose policy is a period and which has no file_date is not
// deleted: its retention's end is not known, so no copy kept can be shown to outlast it (code review of policies
// slice 1, 2026-11-16).
func TestDuplicatesDeleteNeedsFileDate(t *testing.T) {
	for _, tc := range []struct{ value, fileDate, want string }{
		{"7y", "NULL", "needs a file_date first: its policy bills (7y)"},
		{"", "'2020-01-01'", "its policy bills has no value yet"},
	} {
		t.Run(tc.want, func(t *testing.T) { dupDeleteOnPolicy(t, tc.value, tc.fileDate, tc.want) })
	}
}

func dupDeleteOnPolicy(t *testing.T, value, fileDate, want string) {
	d := newDupTest(t, map[string]string{"a/contract.pdf": "contract", "b/contract.pdf": "contract"})
	p := d.export("d.csv")
	d.decide(p, map[string]string{"a/contract.pdf": "keep", "b/contract.pdf": "delete"})
	db, err := sql.Open("sqlite", d.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO policies (name, value, added_utc, changed_utc) VALUES ('bills', nullif('` + value + `', ''), 'x', 'x')`,
		`UPDATE files SET policy_id = (SELECT id FROM policies), file_date = ` + fileDate + `, retention = NULL
			WHERE id = (SELECT file_id FROM paths WHERE path = 'b/contract.pdf')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	d.contains("run", d.run(ExitError, p), "b/contract.pdf", want)
	d.there("run", map[string]bool{"a/contract.pdf": true, "b/contract.pdf": true})
}
