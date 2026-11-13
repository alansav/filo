package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/store"
)

// TestSnapshots checks the register snapshots through the commands: written after commands that change the
// register, never recorded by a scan even with the database inside the root, shown by the report, and a warning
// (not a failure) when one cannot be written.
func TestSnapshots(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	root := canonicalTemp(t)
	db := filepath.Join(root, "filo.db") // inside the root on purpose
	env := map[string]string{"FILO_DB": db}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	filo := func(wantCode int, args ...string) result {
		t.Helper()
		r := run(t, ctx, env, args...)
		if r.code != wantCode {
			t.Fatalf("filo %v: code %d, want %d\n%s%s", args, r.code, wantCode, r.stdout, r.stderr)
		}
		return r
	}

	filo(ExitOK, "roots", "add", root)
	r := filo(ExitOK, "scan")
	if !strings.Contains(r.stdout, "1 file seen") || !strings.Contains(r.stdout, "new 0") {
		t.Errorf("the scan saw the database or its snapshots:\n%s", r.stdout)
	}
	r = filo(ExitOK, "report")
	if !strings.Contains(r.stdout, "Register snapshots\n  2 in "+store.SnapshotDir(db)+", latest ") {
		t.Errorf("report lacks the snapshot line:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "after the latest snapshot") {
		t.Errorf("report warns though the latest snapshot follows the latest write:\n%s", r.stdout)
	}

	// The snapshot directory's place is taken: the scan still succeeds, with a warning, and the report says so.
	if err := os.RemoveAll(store.SnapshotDir(db)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SnapshotDir(db), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = filo(ExitOK, "scan")
	if !strings.Contains(r.stderr, "filo: warning: register snapshot failed") {
		t.Errorf("no warning for a failed snapshot:\n%s", r.stderr)
	}
	r = filo(ExitOK, "report")
	if !strings.Contains(r.stdout, "warning: cannot read "+store.SnapshotDir(db)+": not a directory") {
		t.Errorf("report does not warn about missing snapshots:\n%s", r.stdout)
	}
}
