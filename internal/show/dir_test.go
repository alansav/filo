package show

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

func TestRenderDir(t *testing.T) {
	at := func(d string) string {
		tm, _ := time.ParseInLocation(time.DateOnly, d, time.Local)
		return tm.Add(12 * time.Hour).UTC().Format(time.RFC3339)
	}
	file := func(rel string, size int64, st store.Status, keep string) store.DirEntry {
		return store.DirEntry{FileItem: store.FileItem{RootPath: "/home/u/Documents", Path: rel, Size: size,
			Status: st, KeepUntil: keep}, Present: true}
	}
	copies := file("bills/2022-01 British Gas.pdf", 188416, store.StatusKept, "2029-01-31")
	copies.Dup = store.DupCopies
	record := file("bills/terms.pdf", 5120, store.StatusForever, "forever")
	record.Dup = store.DupRecord
	missing := store.DirEntry{FileItem: store.FileItem{RootPath: "/home/u/Documents", Path: "bills/2024-12 Gas.pdf",
		Size: 92160}, GoneUTC: at("2026-11-18")}
	d := Dir{Reachable: true, DirShown: store.DirShown{RootPath: "/home/u/Documents", Dir: "bills",
		LastScanUTC: at("2026-11-20"),
		Files: []store.DirEntry{
			file("bills/2019-04 Council Tax.pdf", 90112, store.StatusExpired, "2026-11-01"),
			copies,
			missing,
			file("bills/2023/2023-01 Gas.pdf", 97280, store.StatusKept, "2030-01-31"),
			file("bills/2023/2023-02 Gas.pdf", 93184, store.StatusUndated, ""),
			file("bills/scans/old/0001.pdf", 1024, store.StatusUndated, ""),
			record,
		},
		Deleted: []string{"bills/2015-06.pdf", "bills/2023/2016.pdf"},
		Rules: []store.Rule{{Dir: "bills", Pattern: "*Council*", Policy: "council", PolicyValue: "3y"},
			{Dir: "bills", Pattern: "*", Policy: "bills", PolicyValue: "7y"},
			{Dir: "", Pattern: "*.pdf", Policy: "documents"}},
		RulesBelow: []store.Rule{{Dir: "bills/2023", Pattern: "*", Policy: "old", PolicyValue: "2030-12-31"}},
	}}

	var b bytes.Buffer
	v := view.New(view.Options{Out: &b, Home: "/home/u"})
	d.Render(v, 1, 1, false)
	want := `bills   directory . 2 directories . 4 files . 367 KiB . ~/Documents
  Directory
    path   ~/Documents/bills
    root   ~/Documents   reachable . last scan 2026-11-20
    files  3 present . 1 missing . 1 deleted by filo
  Rules   in the order tried
    ~/Documents/bills   *Council*   council (3y)
    ~/Documents/bills   *           bills (7y)
    ~/Documents         *.pdf       documents (no value yet)
  Directories   2
    2023/    2 files . 186 KiB   [!] 1 needs a decision
    scans/    1 file . 1.0 KiB   [!] 1 needs a decision
  Files   4
    2019-04 Council Tax.pdf   88.0 KiB   [!] expired 2026-11-01
    2022-01 British Gas.pdf    184 KiB   kept until 2029-01-31 . has copies
    2024-12 Gas.pdf           90.0 KiB   [!] missing since 2026-11-18
    terms.pdf                  5.0 KiB   forever . a separate record
`
	if b.String() != want {
		t.Errorf("dir:\n%s\nwant:\n%s", b.String(), want)
	}

	b.Reset()
	d.Render(v, 2, 3, true)
	want = `[2/3] bills and below   directory . 3 directories . 7 files . 554 KiB . ~/Documents
  Directory
    path   ~/Documents/bills
    root   ~/Documents   reachable . last scan 2026-11-20
    files  6 present . 1 missing . 2 deleted by filo
  Rules   in the order tried
    ~/Documents/bills   *Council*   council (3y)
    ~/Documents/bills   *           bills (7y)
    ~/Documents         *.pdf       documents (no value yet)
  bills/   4 files . [!] 2 need a decision
    2019-04 Council Tax.pdf   88.0 KiB   [!] expired 2026-11-01
    2022-01 British Gas.pdf    184 KiB   kept until 2029-01-31 . has copies
    2024-12 Gas.pdf           90.0 KiB   [!] missing since 2026-11-18
    terms.pdf                  5.0 KiB   forever . a separate record
  bills/2023/   2 files . [!] 1 needs a decision
    rule * -> old (2030-12-31)
    2023-01 Gas.pdf   95.0 KiB   kept until 2030-01-31
    2023-02 Gas.pdf   91.0 KiB   [!] undated
  bills/scans/   no files
  bills/scans/old/   1 file . [!] 1 needs a decision
    0001.pdf   1.0 KiB   [!] undated
`
	if b.String() != want {
		t.Errorf("dir -r:\n%s\nwant:\n%s", b.String(), want)
	}

	// An unreachable root, no rules, no files.
	empty := Dir{DirShown: store.DirShown{RootPath: "/media/disk", Dir: ""}, Why: "not mounted"}
	b.Reset()
	empty.Render(v, 1, 1, false)
	want = `disk   directory . 0 files . 0 B . /media
  Directory
    path   /media/disk
    root   /media/disk   [!] not reachable: not mounted
    files  0 present
  Rules   none
  Files   none
`
	if b.String() != want {
		t.Errorf("empty:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestLookDir(t *testing.T) {
	root := t.TempDir()
	if d := LookDir(store.DirShown{}, store.Root{Path: root}); !d.Reachable {
		t.Errorf("reachable root: %+v", d)
	}
	gone := filepath.Join(root, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if d := LookDir(store.DirShown{}, store.Root{Path: gone}); d.Reachable || d.Why == "" {
		t.Errorf("gone root: %+v", d)
	}
}
