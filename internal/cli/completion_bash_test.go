package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCompletionScriptInBash runs the script filo completion bash prints in a real bash, with a filo built from
// this tree on the PATH: on a Tab bash sets COMP_LINE and COMP_POINT and calls _filo with the word it replaces; the
// candidates come back in COMPREPLY, one per element, spaces and all. (What readline does with them afterwards,
// quoting file names and adding / or a space, needs an interactive terminal: checked by hand.) Bash is in the dev
// container; without it the test fails rather than pass untested.
func TestCompletionScriptInBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("bash is not installed: the completion script cannot be tested")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bin := t.TempDir()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, "filo"),
		"github.com/alansav/filo/cmd/filo").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ct := completionTree(t)
	script := ct.filo(ExitOK, "", false, "completion", "bash").stdout
	if !strings.Contains(script, "complete -F _filo filo") {
		t.Fatalf("script = %q", script)
	}
	scriptPath := filepath.Join(ct.work, "filo.bash")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	gas := strings.ReplaceAll(ct.in("docs/bills 2026/gas.pdf"), " ", `\ `)
	for _, tc := range []struct {
		line, word string
		want       []string
	}{
		{"filo ro", "ro", []string{"roots "}},
		{"filo show " + ct.in("docs") + "/b", ct.in("docs") + "/b", []string{ct.in("docs") + "/bills 2026/"}},
		{"filo tag " + gas + " sender:Bri", "Bri", []string{`British\ Gas `}},
		{"filo tag " + gas + " 'sender:Bob", "sender:Bob", []string{`sender:Bob'\''s Garage' `}},
		{"filo nosuch ", "", nil},
	} {
		// The test's own lines, given to bash as arguments: never part of the program text.
		prog := `source "$1"; COMP_LINE=$2; COMP_POINT=${#COMP_LINE}; _filo filo "$3" "" 2>/dev/null;` +
			` printf '%s\n' "${COMPREPLY[@]}"`
		cmd := exec.CommandContext(ctx, bash, "--norc", "--noprofile", "-c", prog, "bash", scriptPath, tc.line,
			tc.word)
		cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "FILO_DB=" + ct.db,
			"HOME=" + ct.root}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bash for %q: %v", tc.line, err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(got) == 1 && got[0] == "" {
			got = nil
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("bash completes %q to %q, want %q", tc.line, got, tc.want)
		}
	}
}
