package tagindex

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestFindScript runs the find page script's tests (testdata/find_test.js) with Node's own test runner, so go test
// covers the script too. Node is in the dev container; without it the test fails rather than pass untested.
func TestFindScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is not installed: the find page script cannot be tested (rebuild the dev container)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--test", filepath.Join("testdata", "find_test.js"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
