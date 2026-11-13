package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

// readRows reads an exported CSV as rows keyed by column name.
func readRows(t *testing.T, path string) []map[string]string {
	t.Helper()
	recs, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(mustRead(t, path), []byte("\ufeff")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, h := range recs[0] {
			row[h] = rec[i]
		}
		out = append(out, row)
	}
	return out
}

// setDelete returns a CSV edit that sets the delete cell of the rows whose absolute path is a key.
func setDelete(values map[string]string) func(map[string]string) {
	return func(row map[string]string) {
		if v, ok := values[row["directory"]+"/"+row["name"]]; ok {
			row["delete"] = v
		}
	}
}

// TestDuplicatesRoundTrip runs the duplicates workflow the ways a person would: export, decide, update (refused
// groups, the confirmation, no terminal, a dry run), delete (the kept copy gone, select with unmark, all), and undo
// a separate content.
func TestDuplicatesRoundTrip(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
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
	lacks := func(what, out string, nots ...string) {
		t.Helper()
		for _, n := range nots {
			if strings.Contains(out, n) {
				t.Errorf("%s has %q:\n%s", what, n, out)
			}
		}
	}

	for _, d := range []string{"a", "b", "c"} {
		write(d+"/contract.pdf", "contract")
	}
	write("a/LICENSE", "MIT")
	write("b/LICENSE", "MIT")
	write("a/x", "x")
	write("c/x", "x")
	write("a/doc", "doc")
	write("b/doc", "doc")
	write("a/y", "y")
	write("b/y", "y")
	write("a/z", "z")
	write("b/z", "z")
	write("a/p.jpg", "photo") // a hard link: one file with two paths, not a duplicate
	if err := os.Link(in("a/p.jpg"), in("b/p-link.jpg")); err != nil {
		t.Fatal(err)
	}
	write("a/q", "q") // a hard link with a real copy elsewhere: two copies
	if err := os.Link(in("a/q"), in("b/q-link")); err != nil {
		t.Fatal(err)
	}
	write("c/q", "q")
	filo(ExitOK, "", false, "roots", "add", root)

	// b/doc is kept forever: it may not go while a/doc, which has no retention, stays.
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", in("b"), "-o", rcsv)
	editCSV(t, rcsv, setRetention(map[string]string{"doc": "forever"}))
	filo(ExitOK, "", false, "update", rcsv)

	// The export: every group, hard links as one copy.
	dcsv := filepath.Join(work, "d.csv")
	r := filo(ExitOK, "", false, "export", "duplicates", "-o", dcsv)
	contains("export", r.stdout, "Exported 7 duplicate groups (15 copies)", "filo update "+dcsv)
	rows := readRows(t, dcsv)
	paths := map[string]map[string]string{}
	for _, row := range rows {
		paths[row["directory"]+"/"+row["name"]] = row
	}
	if len(rows) != 15 || paths[in("a/p.jpg")] != nil || paths[in("b/p-link.jpg")] != nil {
		t.Fatalf("exported rows: %d, want 15, without the hard-linked photo: %v", len(rows), paths)
	}
	if paths[in("a/q")] == nil || paths[in("b/q-link")] != nil || paths[in("a/q")]["copies"] != "2" {
		t.Errorf("a/q and b/q-link should be one copy of two: %v", paths[in("a/q")])
	}
	if paths[in("b/doc")]["retention"] != "forever" {
		t.Errorf("b/doc retention = %q, want forever shown", paths[in("b/doc")]["retention"])
	}

	decide := map[string]string{
		in("a/contract.pdf"): "N", in("b/contract.pdf"): "Y", in("c/contract.pdf"): "yes", // two to delete
		in("a/LICENSE"): "N", in("b/LICENSE"): "n", // separate
		in("a/x"): "Y", in("c/x"): "Y", // every copy Y
		in("a/doc"): "N", in("b/doc"): "Y", // the forever copy may not go
		in("a/z"): "N",                 // b/z blank
		in("a/q"): "Y", in("c/q"): "N", // a/q has a hard link
		// y: all blank, left alone
	}
	editCSV(t, dcsv, setDelete(decide))

	// Without a terminal nothing is recorded; the refusals are listed.
	r = filo(ExitError, "", false, "update", dcsv)
	contains("no terminal", r.stderr, "every copy is marked Y, which would leave none",
		in("b/doc")+" is kept until forever, longer than every copy marked N",
		"every copy of a decided group needs Y or N (1 is blank or missing)", "1 other hard link",
		"frees nothing")
	contains("no terminal", r.stdout, "1 content to be marked separate", in("a/LICENSE"),
		"Nothing recorded (there is no terminal to ask in", "1 group left alone")
	lacks("no terminal", r.stderr, "contract.pdf")
	contains("nothing marked", filo(ExitOK, "", false, "delete").stdout, "No files are past their retention.")

	// A dry run says what would happen; declining records nothing.
	r = filo(ExitError, "", false, "update", "--dry-run", dcsv)
	contains("dry run", r.stdout, "Dry run: nothing was written.",
		"Would record: 2 copies to delete in 1 group, 1 content marked separate.", "would ask before recording")
	r = filo(ExitError, "n\n", true, "update", dcsv)
	contains("declined", r.stdout, "Record these decisions (2 copies to delete in 1 group, 1 content marked "+
		"separate)? [y/n]", "Nothing recorded (not confirmed)")

	// Confirmed: the groups that passed are recorded; nothing is deleted.
	r = filo(ExitError, "y\n", true, "update", dcsv)
	contains("confirmed", r.stdout, "Recorded: 2 copies to delete in 1 group, 1 content marked separate.",
		"Nothing has been deleted. Run filo delete")
	for _, rel := range []string{"b/contract.pdf", "c/contract.pdf", "a/x", "c/x", "b/doc"} {
		if !exists(rel) {
			t.Errorf("update deleted %s", rel)
		}
	}
	contains("report", filo(ExitOK, "", false, "report").stdout,
		"1 content marked separate (2 files): identical, but not counted as duplicates",
		"2 copies marked for deletion; see filo delete")

	// The separate content is no longer exported; a row moved into another group is refused with both groups.
	d2 := filepath.Join(work, "d2.csv")
	contains("export again", filo(ExitOK, "", false, "export", "duplicates", "-o", d2).stdout,
		"Exported 6 duplicate groups")
	if strings.Contains(string(mustRead(t, d2)), "LICENSE") {
		t.Error("a separate content was exported again")
	}
	editCSV(t, d2, func(row map[string]string) {
		if row["directory"]+"/"+row["name"] == in("a/y") {
			row["group"] = "1"
			row["delete"] = "N"
		}
	})
	r = filo(ExitError, "n\n", true, "update", d2)
	contains("wrong group", r.stderr, "not 1 (a sorted or pasted row?)", "is in the wrong group")

	// filo delete lists the group with its kept copy; without a terminal it deletes nothing.
	r = filo(ExitOK, "", false, "delete")
	contains("list", r.stdout, "Duplicate copies marked for deletion: 2 files in 1 group, 16 B",
		"contract.pdf, 8 B each", "    keep    "+in("a/contract.pdf"), "    delete  "+in("b/contract.pdf"),
		"    delete  "+in("c/contract.pdf"), "Nothing deleted: there is no terminal")

	// The copy kept was moved away after the update: nothing in the group is deleted.
	if err := os.Rename(in("a/contract.pdf"), filepath.Join(work, "aside.pdf")); err != nil {
		t.Fatal(err)
	}
	r = filo(ExitOK, "", false, "delete", "-y")
	contains("kept copy gone", r.stdout, "Deleted 0 files", "no other copy could be checked, so it is not deleted",
		in("a/contract.pdf")+": no longer at this path")
	if !exists("b/contract.pdf") || !exists("c/contract.pdf") {
		t.Fatal("a copy was deleted while the kept copy was gone")
	}
	if err := os.Rename(filepath.Join(work, "aside.pdf"), in("a/contract.pdf")); err != nil {
		t.Fatal(err)
	}

	// File by file: unmark b, delete c.
	r = filo(ExitOK, "s\nu\nd\n", true, "delete")
	contains("select", r.stdout, "(1/2) duplicate", "[d]elete, [u]nmark, [s]kip, [q]uit?", "Deleted 1 file (8 B).",
		"Unmarked 1 copy")
	if !exists("a/contract.pdf") || !exists("b/contract.pdf") || exists("c/contract.pdf") {
		t.Errorf("after select: a %v, b %v, c %v; want a and b kept, c deleted", exists("a/contract.pdf"),
			exists("b/contract.pdf"), exists("c/contract.pdf"))
	}
	contains("nothing left", filo(ExitOK, "", false, "delete").stdout, "No files are past their retention.")
	contains("event", filo(ExitOK, "", false, "report").stdout, "duplicate; the copy kept is "+in("a/contract.pdf"))

	// Undo the separate content: blank its cells.
	scsv := filepath.Join(work, "s.csv")
	r = filo(ExitOK, "", false, "export", "duplicates", "--separate", "-o", scsv)
	contains("separate export", r.stdout, "Exported 1 content marked separate (2 copies)")
	for _, row := range readRows(t, scsv) {
		if row["delete"] != "N" || row["name"] != "LICENSE" {
			t.Errorf("separate export row %v, want LICENSE with N", row)
		}
	}
	editCSV(t, scsv, func(row map[string]string) { row["delete"] = "" })
	r = filo(ExitOK, "", false, "update", scsv)
	contains("undo", r.stdout, "Recorded: 1 content no longer separate.")
	d3 := filepath.Join(work, "d3.csv")
	filo(ExitOK, "", false, "export", "duplicates", "-o", d3)
	if !strings.Contains(string(mustRead(t, d3)), "LICENSE") {
		t.Error("LICENSE is still separate after the undo")
	}

	// A Y in an export of separate contents is refused; so is a retention row naming a duplicates export.
	editCSV(t, d3, setDelete(map[string]string{in("a/LICENSE"): "N", in("b/LICENSE"): "N"}))
	filo(ExitOK, "y\n", true, "update", d3)
	s2 := filepath.Join(work, "s2.csv")
	filo(ExitOK, "", false, "export", "duplicates", "--separate", "-o", s2)
	editCSV(t, s2, setDelete(map[string]string{in("b/LICENSE"): "Y"}))
	contains("Y in separate", filo(ExitError, "n\n", true, "update", s2).stderr, "a Y in an export of separate contents")
	bad := filepath.Join(work, "bad.csv")
	row := readRows(t, d3)[0]
	if err := os.WriteFile(bad, []byte("id,export,retention\n"+row["id"]+","+row["export"]+",7y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contains("retention row", filo(ExitError, "", false, "update", bad).stderr, "is a duplicates export, not a retention export")
}

// TestDeleteListsMarkedCopies: a marked copy also past its retention is listed once, under retention; a symlink to a
// marked copy is named; a mark whose copy changed since no longer counts.
func TestDeleteListsMarkedCopies(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
	work := canonicalTemp(t)
	env := map[string]string{"FILO_DB": filepath.Join(work, "filo.db")}
	root := filepath.Join(work, "home")
	in := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	for _, f := range []struct{ rel, content string }{{"a/m", "m"}, {"b/m", "m"}, {"a/e", "e"}, {"b/e", "e"},
		{"a/s", "s"}, {"b/s", "s"}} {
		if err := os.MkdirAll(filepath.Dir(in(f.rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in(f.rel), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../b/m", in("a/to-m")); err != nil {
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
	filo(ExitOK, "", false, "roots", "add", root)
	dcsv := filepath.Join(work, "d.csv")
	filo(ExitOK, "", false, "export", "duplicates", "-o", dcsv)
	editCSV(t, dcsv, setDelete(map[string]string{in("a/m"): "N", in("b/m"): "Y", in("a/e"): "N", in("b/e"): "Y",
		in("a/s"): "N", in("b/s"): "Y"}))
	filo(ExitOK, "", false, "update", dcsv)

	// b/e is also past its retention; b/s changes and a scan records it.
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "", false, "export", "retention", in("b"), "-o", rcsv)
	editCSV(t, rcsv, setRetention(map[string]string{"e": "2026-11-01"}))
	filo(ExitOK, "", false, "update", rcsv)
	if err := os.WriteFile(in("b/s"), []byte("s, edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "scan")

	out := filo(ExitOK, "", false, "delete").stdout
	for _, want := range []string{"Past their retention: 1 file", "2026-11-01", in("b/e"),
		"Duplicate copies marked for deletion: 1 file in 1 group", "    delete  " + in("b/m"),
		"the symlink " + in("a/to-m") + " points to it and would break",
		"1 mark no longer counts: the copy changed since it was marked"} {
		if !strings.Contains(out, want) {
			t.Errorf("delete list lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, in("b/e")) != 1 {
		t.Errorf("b/e is listed %d times, want once:\n%s", strings.Count(out, in("b/e")), out)
	}

	// Quit at the first copy: nothing is deleted.
	r := filo(ExitOK, "s\nq\n", true, "delete")
	if !strings.Contains(r.stdout, "Deleted 0 files") || !strings.Contains(r.stdout, "Left 2 files for later.") {
		t.Errorf("quit:\n%s", r.stdout)
	}
}

// TestDuplicatesNeverLastCopy: found in the code review of 2026-11-14. A kept copy that is itself past its retention,
// or that is on disk the same file as the copy marked for deletion (a bind mount seen by two roots), never counts as
// kept, so one run cannot remove every copy.
func TestDuplicatesNeverLastCopy(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
	work := canonicalTemp(t)
	env := map[string]string{"FILO_DB": filepath.Join(work, "filo.db")}
	root := filepath.Join(work, "home")
	in := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	for _, f := range []struct{ rel, content string }{{"a/k", "k"}, {"b/k", "k"}, {"a/m", "m"}, {"b/m", "m"}} {
		if err := os.MkdirAll(filepath.Dir(in(f.rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in(f.rel), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	filo := func(wantCode int, args ...string) result {
		t.Helper()
		r := runIn(t, ctx, env, strings.NewReader(""), args...)
		if r.code != wantCode {
			t.Fatalf("filo %v: code %d, want %d\n%s%s", args, r.code, wantCode, r.stdout, r.stderr)
		}
		return r
	}
	exists := func(rel string) bool {
		_, err := os.Lstat(in(rel))
		return err == nil
	}
	filo(ExitOK, "roots", "add", root)
	dcsv := filepath.Join(work, "d.csv")
	filo(ExitOK, "export", "duplicates", "-o", dcsv)
	editCSV(t, dcsv, setDelete(map[string]string{in("a/k"): "N", in("b/k"): "Y", in("a/m"): "N", in("b/m"): "Y"}))
	filo(ExitOK, "update", dcsv)

	// a/k, the copy kept, is then given a retention that has passed: this run will delete it.
	rcsv := filepath.Join(work, "r.csv")
	filo(ExitOK, "export", "retention", in("a"), "-o", rcsv)
	editCSV(t, rcsv, setRetention(map[string]string{"k": "2026-11-01"}))
	filo(ExitOK, "update", rcsv)

	// b/m is, on disk, the file a/m (another root's bind mount, faked: the device and inode of a/m).
	t.Cleanup(fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
		if p == in("b/m") {
			p = in("a/m")
		}
		id, err := real(p)
		id.Dev = 1
		return id, err
	}))

	r := filo(ExitOK, "delete", "-y")
	for _, want := range []string{in("a/k") + ": past its own retention, so this run may delete it too",
		in("a/m") + ": the same file as " + in("b/m") + " (a hard link or a bind mount)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("delete lacks %q:\n%s", want, r.stdout)
		}
	}
	if !exists("b/k") || exists("a/k") {
		t.Errorf("a/k exists %v, b/k exists %v; want a/k deleted for its retention and b/k kept", exists("a/k"),
			exists("b/k"))
	}
	if !exists("a/m") || !exists("b/m") {
		t.Error("a copy of m was deleted through what is one file on disk")
	}

	// The export now treats the two paths of m as one copy, so there is no group to decide.
	d2 := filepath.Join(work, "d2.csv")
	if out := filo(ExitOK, "export", "duplicates", "-o", d2).stdout; !strings.Contains(out, "No duplicate groups") {
		t.Errorf("export of one file seen twice:\n%s", out)
	}
}

// TestUpdateRetentionWithGroupColumn: a retention CSV with a group column of the person's own is still a retention
// CSV (found in the code review of 2026-11-14).
func TestUpdateRetentionWithGroupColumn(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
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
	if r := run(t, ctx, env, "roots", "add", root); r.code != ExitOK {
		t.Fatal(r.stderr)
	}
	rcsv := filepath.Join(work, "r.csv")
	if r := run(t, ctx, env, "export", "retention", "-o", rcsv); r.code != ExitOK {
		t.Fatal(r.stderr)
	}
	rows := readRows(t, rcsv)
	data := "id,export,retention,group\n" + rows[0]["id"] + "," + rows[0]["export"] + ",forever,bills\n"
	if err := os.WriteFile(rcsv, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, ctx, env, "update", rcsv)
	if r.code != ExitOK || !strings.Contains(r.stdout, "Updated 1 file") {
		t.Errorf("update of a retention CSV with a group column: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
