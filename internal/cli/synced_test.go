package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFilo is the sync service between two test trees: every file of from's DIR/.filo copied into to's, unless what
// is there is newer (the same journal with more lines).
func copyFilo(t *testing.T, from, to *cliTest, dir string) {
	t.Helper()
	src, dst := from.in(dir+"/.filo"), to.in(dir+"/.filo")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	des, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		b, err := os.ReadFile(filepath.Join(src, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// A journal the destination has more of (its device wrote on there) is newer: the sync keeps it.
		if have, err := os.ReadFile(filepath.Join(dst, de.Name())); err == nil && len(have) > len(b) &&
			bytes.HasPrefix(have, b) {
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, de.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncedMarkJoinTrustRetire(t *testing.T) {
	a, b := newCLITest(t), newCLITest(t)
	a.write("Sync/x.txt", "x")
	b.write("Sync/keep.txt", "k")
	a.filo(0, "", false, "device", "name", "laptop")
	b.filo(0, "", false, "device", "name", "desktop")
	a.filo(0, "", false, "roots", "add", a.root)
	b.filo(0, "", false, "roots", "add", b.root)

	out := a.filo(0, "", false, "synced", "add", a.in("Sync")).stdout
	a.contains("mark", out, "Synced directory marked", "which scans skip")
	a.filo(1, "", false, "synced", "add", a.in("Sync")) // joined already

	copyFilo(t, a, b, "Sync")
	out = b.filo(0, "", false, "scan").stdout
	b.contains("scan", out, "Synced   1 mark found", "Next: filo synced")
	out = b.filo(0, "", false, "synced").stdout
	b.contains("synced", out, "a synced directory this device has not joined", "filo synced add")

	// No terminal, no -y: the join is shown, nothing done.
	out = b.filo(0, "", false, "synced", "add", b.in("Sync")).stdout
	b.contains("join shown", out, "Join", "marked by laptop", "Devices sharing decisions there", "laptop",
		"Compare each fingerprint", "Nothing changed: there is no terminal")
	out = b.filo(0, "y\n", true, "synced", "add", b.in("Sync")).stdout
	b.contains("joined", out, "Trust these devices and share decisions in", "Joined", "1 device trusted")
	out = b.filo(0, "", false, "devices").stdout
	b.contains("devices", out, "laptop", "trusted", "desktop", "this device")

	// Back on a: desktop is seen, not trusted, until trusted.
	copyFilo(t, b, a, "Sync")
	out = a.filo(0, "", false, "scan").stdout
	a.contains("a's scan", out, "1 device not trusted")
	out = a.filo(0, "", false, "synced").stdout
	a.contains("a's synced", out, "desktop", "is not trusted: filo devices trust desktop")
	out = a.filo(0, "y\n", true, "devices", "trust", "desktop").stdout
	a.contains("trust", out, "fingerprint", "Trust desktop?", "Trusted   desktop")
	out = a.filo(0, "", false, "devices", a.in("Sync")).stdout
	a.contains("devices after trusting", out, "desktop", "trusted")
	a.contains("report", a.filo(0, "", false, "report").stdout, "Synced   1 joined", "no problems")
	a.lacks("devices after trusting", out, "not trusted")
	a.filo(1, "", false, "devices", "trust", "desktop") // trusted already
	a.filo(1, "", false, "devices", "trust", "nobody")  // no such device
	a.filo(1, "", false, "devices", "retire", "laptop") // itself

	out = a.filo(0, "", false, "devices", "retire", "desktop", "-y").stdout
	a.contains("retire", out, "Retired   desktop")
	out = a.filo(0, "", false, "devices").stdout
	a.contains("after retiring", out, "retired by laptop")

	// b reads its retirement and stops writing there.
	copyFilo(t, a, b, "Sync")
	b.filo(0, "", false, "scan")
	out = b.filo(0, "", false, "synced").stdout
	b.contains("b retired", out, "retired this device here")

	// Leaving.
	out = b.filo(0, "", false, "synced", "remove", b.in("Sync"), "-y").stdout
	b.contains("leave", out, "Left")
	if !b.exists("Sync/keep.txt") || !b.exists("Sync/.filo/mark") {
		t.Fatal("leaving changed the directory")
	}
}

func TestSyncedIgnoreAndRefusals(t *testing.T) {
	a, b := newCLITest(t), newCLITest(t)
	a.write("Sync/x.txt", "x")
	b.write("Sync/y.txt", "y")
	a.filo(0, "", false, "roots", "add", a.root)
	b.filo(0, "", false, "roots", "add", b.root)
	a.filo(0, "", false, "synced", "add", a.in("Sync"))
	copyFilo(t, a, b, "Sync")
	b.filo(0, "", false, "scan")
	out := b.filo(0, "", false, "synced", "ignore", b.in("Sync")).stdout
	b.contains("ignore", out, "Ignored", "no longer reported")
	out = b.filo(0, "", false, "scan").stdout
	b.lacks("scan after ignoring", out, "mark found")
	b.filo(1, "", false, "synced", "add", b.in("Sync")) // ignored: remove it first
	b.filo(0, "", false, "synced", "remove", b.in("Sync"))
	out = b.filo(0, "", false, "scan").stdout
	b.contains("scan after forgetting the ignore", out, "1 mark found")

	// Nesting, a directory outside every root, wrong arguments.
	a.write("Sync/inner/z.txt", "z")
	a.filo(1, "", false, "synced", "add", a.in("Sync/inner"))
	a.filo(1, "", false, "synced", "add", t.TempDir())
	a.filo(2, "", false, "synced", "add")
	a.filo(2, "", false, "synced", "ignore", a.in("Sync"), "-y")
	a.filo(2, "", false, "synced", "frobnicate", "x")
	a.filo(2, "", false, "devices", "trust")
}

func TestSyncedFoundProblemSaysWhatToDo(t *testing.T) {
	a := newCLITest(t)
	a.write("Old/.filo/notes.txt", "from an old tool")
	a.write("Old/x.txt", "x")
	a.filo(0, "", false, "roots", "add", a.root)
	out := a.filo(0, "", false, "synced").stdout
	a.contains("found problem", out, "no mark Filo can read", "remove it if it is not a synced directory's")
	a.filo(1, "", false, "synced", "ignore", a.in("Old"))
}

// syncedPair is two registers sharing Sync, both trusting each other, each holding files (rel under Sync: content).
func syncedPair(t *testing.T, files map[string]string) (a, b *cliTest) {
	t.Helper()
	a, b = newCLITest(t), newCLITest(t)
	for rel, c := range files {
		a.write("Sync/"+rel, c)
		b.write("Sync/"+rel, c)
	}
	a.filo(0, "", false, "device", "name", "laptop")
	b.filo(0, "", false, "device", "name", "desktop")
	a.filo(0, "", false, "roots", "add", a.root)
	b.filo(0, "", false, "roots", "add", b.root)
	a.filo(0, "", false, "synced", "add", a.in("Sync"))
	copyFilo(t, a, b, "Sync")
	b.filo(0, "", false, "synced", "add", b.in("Sync"), "-y")
	copyFilo(t, b, a, "Sync")
	a.filo(0, "", false, "devices", "trust", "desktop", "-y")
	copyFilo(t, a, b, "Sync")
	b.filo(0, "", false, "scan")
	return a, b
}

func TestDecisionsAreSharedThroughTheCommands(t *testing.T) {
	a, b := syncedPair(t, map[string]string{"bills/gas.pdf": "gas", "x.pdf": "x"})
	a.filo(0, "", false, "set", a.in("Sync/bills/gas.pdf"), "--file-date", "2026-01-01", "--retention", "7y")
	a.filo(0, "", false, "tag", a.in("Sync/bills/gas.pdf"), "sender:British Gas")
	copyFilo(t, a, b, "Sync")
	out := b.filo(0, "", false, "show", b.in("Sync/bills/gas.pdf")).stdout
	b.contains("b's show", out, "7y", "2026-01-01", "sender:British Gas", "Shared", "from laptop")
	if strings.Count(out, "sender:British Gas") != 2 { // once under Tags, once under Shared
		t.Errorf("the shared tag is not shown once under Shared:\n%s", out)
	}

	// A conflict: each sets x.pdf otherwise before seeing the other.
	a.filo(0, "", false, "set", a.in("Sync/x.pdf"), "--file-date", "2026-01-01", "--retention", "2020-02-02")
	b.filo(0, "", false, "set", b.in("Sync/x.pdf"), "--file-date", "2026-01-01", "--retention", "forever")
	copyFilo(t, a, b, "Sync")
	copyFilo(t, b, a, "Sync")
	out = a.filo(0, "", false, "scan").stdout
	a.contains("a's scan", out, "1 decision in conflict")
	out = a.filo(0, "", false, "show", a.in("Sync/x.pdf")).stdout
	a.contains("a's show of the conflict", out, "forever", "in conflict", "devices disagree")
	csv := filepath.Join(a.work, "all.csv")
	a.filo(0, "", false, "export", "-o", csv)
	a.contains("a's export", string(mustRead(t, csv)), ",forever,forever,,retention\n")
	out = a.filo(0, "", false, "report").stdout
	a.contains("a's report", out, "1 decision in conflict")
	// A new decision resolves it.
	a.filo(0, "", false, "set", a.in("Sync/x.pdf"), "--retention", "3y", "-y")
	copyFilo(t, a, b, "Sync")
	out = b.filo(0, "", false, "show", b.in("Sync/x.pdf")).stdout
	b.contains("b after resolving", out, "3y")
	b.lacks("b after resolving", out, "in conflict")
}
