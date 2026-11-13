package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVisible(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain name.pdf", "plain name.pdf"},
		{"one line\n", "one line\n"},
		{"line\nnext\tcol", `line\nnext\tcol`},
		{"fake\n  2020-01-01  1 KB  /home/u/harmless.txt\n", `fake\n  2020-01-01  1 KB  /home/u/harmless.txt` + "\n"},
		{`a\x1b.txt`, `a\\x1b.txt`},
		{"🏖️ beach é", "🏖️ beach é"},
		{"erase\x1b[2K\x1b[1Aline", `erase\x1b[2K\x1b[1Aline`},
		{"back\rover", `back\x0dover`},
		{"bell\x07 del\x7f", `bell\x07 del\x7f`},
		{"title\x1b]0;pwned\x07", `title\x1b]0;pwned\x07`},
		{"photo\u202egpj.exe", `photo\u202egpj.exe`},
		{"isolate\u2066x\u2069", `isolate\u2066x\u2069`},
		{"c1 \u009b31m", `c1 \u009b31m`},
		{"bad \xff\xfe utf8", `bad \xff\xfe utf8`},
	}
	for _, tc := range tests {
		if got := string(visible([]byte(tc.in))); got != tc.want {
			t.Errorf("visible(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNamesCannotControlTheTerminal scans a file whose name holds an escape sequence and a bidi override: the
// summary and the report show them as escapes, and no raw control byte reaches the output.
func TestNamesCannotControlTheTerminal(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	work := canonicalTemp(t)
	root := filepath.Join(work, "home")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	evil := "a\x1b[1A\x1b[2Kb\u202ec.txt"
	fake := "x.txt\n  2020-01-01  1 KB  fake.txt"
	for _, name := range []string{evil, fake} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"FILO_DB": filepath.Join(work, "filo.db")}
	ctx := context.Background()
	run(t, ctx, env, "roots", "add", root)
	for _, name := range []string{evil, fake} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"scan"}, {"report"}} {
		r := run(t, ctx, env, args...)
		out := r.stdout + r.stderr
		if !strings.Contains(out, `a\x1b[1A\x1b[2Kb\u202ec.txt`) {
			t.Errorf("filo %v does not show the name escaped:\n%s", args, out)
		}
		if strings.ContainsAny(out, "\x1b\u202e") {
			t.Errorf("filo %v printed a raw control character", args)
		}
		if !strings.Contains(out, `x.txt\n  2020-01-01`) || strings.Contains(out, "\n  2020-01-01") {
			t.Errorf("filo %v let a newline in a name start a line of its own:\n%s", args, out)
		}
	}
}
