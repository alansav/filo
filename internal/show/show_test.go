package show

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
	"github.com/alansav/filo/internal/view"
)

func TestTimeLeft(t *testing.T) {
	tests := []struct{ until, want string }{
		{"2026-11-20", "today"},
		{"2026-11-21", "in 1 day"},
		{"2026-12-19", "in 29 days"},
		{"2026-12-20", "in 1 month"},
		{"2029-01-31", "in 2 years 2 months"},
		{"2027-11-20", "in 1 year"},
		{"2027-11-19", "in 11 months"},
		{"2025-08-01", "1 year 3 months ago"},
		{"2026-11-08", "12 days ago"},
		{"bad", ""},
	}
	for _, tt := range tests {
		if got := TimeLeft("2026-11-20", tt.until); got != tt.want {
			t.Errorf("TimeLeft(%s) = %q, want %q", tt.until, got, tt.want)
		}
	}
}

// shownAt is a present regular file as the register would hold it after a scan of the file at abs.
func shownAt(t *testing.T, root, rel string) store.Shown {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := hasher.File(context.Background(), abs)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := fsid.FromFileInfo(fi)
	return store.Shown{FileItem: store.FileItem{RootPath: root, Path: rel, Kind: "file", Size: fi.Size(),
		MtimeNS: fi.ModTime().UnixNano(), Hash: string(res.Sum)}, Present: true, Ino: id.Ino,
		CtimeNS: fsid.ChangeTimeNS(fi)}
}

func TestLook(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one")
	sh := shownAt(t, root, "a.txt")
	r := store.Root{Path: root}

	if f := Look(ctx, sh, r, true); f.Disk != DiskUnchanged || f.Verify != VerifyMatches {
		t.Errorf("unchanged: disk %v, verify %v", f.Disk, f.Verify)
	}
	write("a.txt", "two, longer")
	if f := Look(ctx, sh, r, true); f.Disk != DiskChanged || f.Verify != VerifyDiffers {
		t.Errorf("changed: disk %v, verify %v", f.Disk, f.Verify)
	}
	if f := Look(ctx, sh, r, false); f.Verify != VerifyNotAsked {
		t.Errorf("no --verify: verify %v", f.Verify)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if f := Look(ctx, sh, r, true); f.Disk != DiskGone || f.Verify != VerifySkipped {
		t.Errorf("gone: disk %v, verify %v", f.Disk, f.Verify)
	}
	// A file missing from the register: nothing there is as expected; something there is news.
	missing := sh
	missing.Present = false
	if f := Look(ctx, missing, r, false); f.Disk != DiskUnchanged {
		t.Errorf("missing, nothing there: disk %v", f.Disk)
	}
	write("a.txt", "back")
	if f := Look(ctx, missing, r, false); f.Disk != DiskThere {
		t.Errorf("missing, something there: disk %v", f.Disk)
	}
	// An unreachable root says nothing about the file.
	if f := Look(ctx, sh, store.Root{Path: filepath.Join(root, "nowhere")}, true); f.Disk != DiskNoRoot ||
		f.Verify != VerifyFailed {
		t.Errorf("no root: disk %v, verify %v", f.Disk, f.Verify)
	}
	// A symlink compares its target.
	if err := os.Symlink("a.txt", filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	link := store.Shown{FileItem: store.FileItem{RootPath: root, Path: "l", Kind: "symlink"}, Present: true,
		LinkTarget: "a.txt"}
	if f := Look(ctx, link, r, false); f.Disk != DiskUnchanged {
		t.Errorf("symlink: disk %v", f.Disk)
	}
	// Its ctime is not compared, as a scan does not: a chown -h changes nothing.
	link.CtimeNS = 1
	if f := Look(ctx, link, r, false); f.Disk != DiskUnchanged {
		t.Errorf("symlink with another ctime: disk %v", f.Disk)
	}
	// Its target gone: broken now, so changed, as the next scan will record.
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if f := Look(ctx, link, r, false); f.Disk != DiskChanged {
		t.Errorf("symlink broken since: disk %v", f.Disk)
	}
	link.LinkBroken = true
	if f := Look(ctx, link, r, false); f.Disk != DiskUnchanged {
		t.Errorf("symlink broken, as recorded: disk %v", f.Disk)
	}
	link.LinkTarget = "b.txt"
	if f := Look(ctx, link, r, false); f.Disk != DiskChanged {
		t.Errorf("retargeted symlink: disk %v", f.Disk)
	}
}

func TestRender(t *testing.T) {
	mtime := time.Date(2022, 2, 3, 9, 14, 0, 0, time.Local).UnixNano()
	at := func(d string) string { // a scan's time: noon local on that date
		tm, _ := time.ParseInLocation(time.DateOnly, d, time.Local)
		return tm.Add(12 * time.Hour).UTC().Format(time.RFC3339)
	}
	kept := File{Shown: store.Shown{
		FileItem: store.FileItem{RootPath: "/home/u/Documents", Path: "bills/2022-01 British Gas.pdf", Kind: "file",
			Size: 188416, MtimeNS: mtime, Hash: "3f9a", FileDate: "2022-01-31", Retention: "bills", Policy: "bills",
			PolicyValue: "7y", KeepUntil: "2029-01-31", ReviewedUTC: at("2026-11-14"), RuleID: 4,
			Status: store.StatusKept},
		Present: true, FirstSeenScan: 3, FirstSeenUTC: at("2026-11-12"), ReviewedHash: "3f9a", ReviewedBy: "update",
		RuleDir: "bills", RulePattern: "*", LastScanUTC: at("2026-11-20"), CopiesInGroups: true,
		Paths: []store.ShownPath{
			{RootPath: "/home/u/Documents", Path: "bills/2022-01 British Gas.pdf", FromScan: 12, FromUTC: at("2026-11-13")},
			{RootPath: "/home/u/Documents", Path: "inbox/british-gas.pdf", FromScan: 3, ToScan: 12,
				FromUTC: at("2026-11-12"), ToUTC: at("2026-11-13")},
			{RootPath: "/home/u/Downloads", Path: "british-gas.pdf", FromScan: 2, ToScan: 3,
				FromUTC: at("2026-11-11"), ToUTC: at("2026-11-12")},
		},
		Copies: []store.DupCopy{{Candidate: store.Candidate{FileItem: store.FileItem{RootPath: "/home/u/Backup",
			Path: "bills/2022-01.pdf", KeepUntil: "2029-01-31"}}}},
		Tags: []tags.Tag{{Key: "sender", Value: "British Gas"}, {Key: "type", Value: "bill"}},
	}, Verify: VerifyMatches}

	var b bytes.Buffer
	v := view.New(view.Options{Out: &b, Home: "/home/u"})
	kept.Render(v, 1, 2, "2026-11-20", false)
	want := `[1/2] 2022-01 British Gas.pdf   kept until 2029-01-31 . 184 KiB . ~/Documents/bills
  File
    path        ~/Documents/bills/2022-01 British Gas.pdf
    root        ~/Documents
    size        184 KiB
    modified    2022-02-03 09:14   unchanged since the last scan (2026-11-20)
    sha256      3f9a   content matches the register (read now)
    first seen  2026-11-12 (scan #3)
    moved from  ~/Documents/inbox/british-gas.pdf   2026-11-12 to 2026-11-13   ... 1 more: --history
  Retention
    status      kept
    retention   bills (7y), a policy set by rule bills/*
    file_date   2022-01-31
    keep_until  2029-01-31   in 2 years 2 months
    reviewed    2026-11-14, by filo update, on this content
  Tags   2 tags
    sender:British Gas
    type:bill
  Copies   1 other file with the same content . counted as a duplicate
    ~/Backup/bills/2022-01.pdf   until 2029-01-31
`
	if b.String() != want {
		t.Errorf("kept:\n%s\nwant:\n%s", b.String(), want)
	}

	b.Reset()
	kept.Render(v, 1, 1, "2026-11-20", true)
	if got := b.String(); !strings.Contains(got, `  Paths   3 paths, newest first
    ~/Documents/bills/2022-01 British Gas.pdf   2026-11-13 to now
    ~/Documents/inbox/british-gas.pdf   2026-11-12 to 2026-11-13
    ~/Downloads/british-gas.pdf   2026-11-11 to 2026-11-12
`) || strings.Contains(got, "moved from") || strings.HasPrefix(got, "[") {
		t.Errorf("history:\n%s", got)
	}

	// Needing a decision, changed on disk, and gone: the markers, and no file_date line without one.
	undated := kept
	undated.Status, undated.Policy, undated.Retention, undated.KeepUntil, undated.FileDate = store.StatusUndated, "", "", "", ""
	undated.ReviewedUTC, undated.Copies, undated.Paths, undated.Disk, undated.Verify = "", nil, undated.Paths[:1],
		DiskChanged, VerifyNotAsked
	b.Reset()
	undated.Render(v, 1, 1, "2026-11-20", false)
	for _, w := range []string{"2022-01 British Gas.pdf   [!] needs a retention . 184 KiB",
		"modified    2022-02-03 09:14   [!] changed on disk since the last scan: run filo scan",
		"status      [!] undated: no retention", "retention   none", "  Tags   2 tags\n    sender:British Gas\n"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("undated lacks %q:\n%s", w, b.String())
		}
	}
	for _, n := range []string{"file_date", "keep_until", "Copies", "moved from"} {
		if strings.Contains(b.String(), n) {
			t.Errorf("undated has %q:\n%s", n, b.String())
		}
	}

	// No content on record to trust: the next scan reads it again, whatever the lstat says.
	unread := kept
	unread.Hash, unread.Copies, unread.Verify = "", nil, VerifyNotAsked
	b.Reset()
	unread.Render(v, 1, 1, "2026-11-20", false)
	if w := "unchanged since the last scan (2026-11-20); the next scan reads it again"; !strings.Contains(b.String(), w) {
		t.Errorf("unread lacks %q:\n%s", w, b.String())
	}

	expired := kept
	expired.Status, expired.KeepUntil = store.StatusExpired, "2025-08-01"
	b.Reset()
	expired.Render(v, 1, 1, "2026-11-20", false)
	for _, w := range []string{"[!] expired 2025-08-01", "keep_until  2025-08-01   [!] 1 year 3 months ago",
		"status      [!] expired: a deletion candidate (filo delete)"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("expired lacks %q:\n%s", w, b.String())
		}
	}

	deleted := kept
	deleted.Present, deleted.Status, deleted.DeletedUTC, deleted.GoneScan = false, "", at("2026-11-16"), 20
	b.Reset()
	deleted.Render(v, 1, 1, "2026-11-20", false)
	for _, w := range []string{"2022-01 British Gas.pdf   deleted by filo on 2026-11-16 . 184 KiB",
		"  Tags   2 tags . as when it was deleted\n",
		"status      deleted by filo", "keep_until  2029-01-31\n", "modified    2022-02-03 09:14\n"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("deleted lacks %q:\n%s", w, b.String())
		}
	}

	missing := deleted
	missing.DeletedUTC, missing.GoneUTC = "", at("2026-11-18")
	b.Reset()
	missing.Render(v, 1, 1, "2026-11-20", false)
	for _, w := range []string{"[!] missing since 2026-11-18 (scan #20)",
		"status      [!] missing: run filo scan, if it was moved"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("missing lacks %q:\n%s", w, b.String())
		}
	}
}
