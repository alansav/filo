package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// copyFilo is the sync service between two test trees: every file of from's DIR/.filo copied into to's.
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
