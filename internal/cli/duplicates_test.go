package cli

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/store"
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

// TestUpdateRetentionWithGroupColumn: a retention CSV with a group column of the person's own is still a retention
// CSV (found in the code review of 2026-11-14).
func TestUpdateRetentionWithGroupColumn(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
	ct.write("a.txt", "a")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	rcsv := filepath.Join(ct.work, "r.csv")
	ct.filo(ExitOK, "", false, "export", "retention", "-o", rcsv)
	rows := readRows(t, rcsv)
	data := "id,export,retention,group\n" + rows[0]["id"] + "," + rows[0]["export"] + ",forever,bills\n"
	if err := os.WriteFile(rcsv, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	ct.contains("update of a retention CSV with a group column", ct.filo(ExitOK, "", false, "update", rcsv).stdout,
		"Retention update", "1 file given a retention")
}

// TestUpdateRefusesDuplicatesCSV: decisions to delete are not stored any more (docs/design.md, Duplicates), so filo
// update refuses a duplicates CSV, in one line, pointing at filo delete, and records nothing.
func TestUpdateRefusesDuplicatesCSV(t *testing.T) {
	ct := newCLITest(t)
	ct.write("a/contract.pdf", "contract")
	ct.write("b/contract.pdf", "contract")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	dcsv := filepath.Join(ct.work, "d.csv")
	ct.filo(ExitOK, "", false, "export", "duplicates", "-o", dcsv)
	editCSV(t, dcsv, setDelete(map[string]string{ct.in("a/contract.pdf"): "N", ct.in("b/contract.pdf"): "Y"}))
	r := ct.filo(ExitError, "", false, "update", dcsv)
	if want := "filo: " + dcsv + " is a duplicates CSV: filo update records retentions only; carry it out with " +
		"filo delete " + dcsv + "\n"; r.stderr != want || r.stdout != "" {
		t.Errorf("stdout %q, stderr %q; want only %q", r.stdout, r.stderr, want)
	}
	// filo delete lists only files past their retention: there is nothing marked to delete.
	ct.contains("delete", ct.filo(ExitOK, "", false, "delete").stdout, "No files are past their retention.")
	if !ct.exists("a/contract.pdf") || !ct.exists("b/contract.pdf") {
		t.Error("a copy was deleted")
	}
}

// TestExportMergesOneFileSeenTwice: a file seen at two paths (a folder bind-mounted into a second root, faked with
// the device and inode of the first) is one copy on disk, so it is no duplicate group, though the register holds it
// as two files.
func TestExportMergesOneFileSeenTwice(t *testing.T) {
	ct := newCLITest(t)
	ct.write("a/m", "m")
	ct.write("b/m", "m")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	t.Cleanup(fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
		if p == ct.in("b/m") {
			p = ct.in("a/m")
		}
		id, err := real(p)
		id.Dev = 1
		return id, err
	}))
	out := ct.filo(ExitOK, "", false, "export", "duplicates", "-o", filepath.Join(ct.work, "d.csv")).stdout
	ct.contains("export", out, "Duplicates export   no groups", "\nNothing exported.\n")
}

// TestDistinctOnDiskRecords: copies merged as one file on disk (a bind mount) are decided again: two records of one
// set plus the same file as one of them seen again is no group, whichever path holds the record; without records it
// is a group of two.
func TestDistinctOnDiskRecords(t *testing.T) {
	ct := newCLITest(t)
	for _, rel := range []string{"a", "b", "c"} {
		ct.write(rel, "x")
	}
	t.Cleanup(fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
		if p == ct.in("c") {
			p = ct.in("a") // c is a seen through a bind mount
		}
		id, err := real(p)
		id.Dev = 1
		return id, err
	}))
	cp := func(rel string, set int64) store.DupCopy {
		c := store.DupCopy{RecordSet: set}
		c.RootPath, c.Path = ct.root, rel
		return c
	}
	tests := []struct {
		name   string
		copies []store.DupCopy
		group  bool
	}{
		{"records a and b, c the same file as a", []store.DupCopy{cp("a", 1), cp("b", 1), cp("c", 0)}, false},
		{"the record held on the bind-mounted path", []store.DupCopy{cp("a", 0), cp("b", 1), cp("c", 1)}, false},
		{"no records: a group of two", []store.DupCopy{cp("a", 0), cp("b", 0), cp("c", 0)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := distinctOnDisk([]store.DupContent{{Hash: "h", Size: 1, Copies: tc.copies}})
			if (len(got) == 1) != tc.group {
				t.Errorf("groups after merging = %d, want group %v", len(got), tc.group)
			}
			if tc.group && len(got[0].Copies) != 2 {
				t.Errorf("copies = %d, want 2 (c merged into a)", len(got[0].Copies))
			}
		})
	}
}
