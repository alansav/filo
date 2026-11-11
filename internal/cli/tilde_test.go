package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTilde(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		in      string
		wantErr bool
		hint    string
	}{
		{"/abs/path", false, ""},
		{"rel/path", false, ""},
		{"./~", false, ""},
		{"a~b", false, ""},
		{"~", true, "write $HOME instead"},
		{"~" + sep + "Documents", true, "write $HOME" + sep + "Documents instead"},
		{"~alice/x", true, "write the full path instead"},
		{"~.txt", true, "or ./~.txt for a name"},
	}
	for _, tt := range tests {
		err := checkTilde("PATH", tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkTilde(%q) = %v, want error %v", tt.in, err, tt.wantErr)
			continue
		}
		if err != nil && !strings.Contains(err.Error(), tt.hint) {
			t.Errorf("checkTilde(%q) = %q, want it to contain %q", tt.in, err, tt.hint)
		}
	}
}

// TestQuotedTildeRefused checks every place a path enters, and that nothing is created in the current directory.
func TestQuotedTildeRefused(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	t.Chdir(cwd)
	db := filepath.Join(t.TempDir(), "f.db")
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"db flag", nil, []string{"--db", "~/f.db", "roots"}, "--db ~/f.db"},
		{"db flag after command", nil, []string{"roots", "--db", "~/f.db"}, "--db ~/f.db"},
		{"FILO_DB", map[string]string{"FILO_DB": "~/f.db"}, []string{"roots"}, "FILO_DB ~/f.db"},
		{"roots add", nil, []string{"--db", db, "roots", "add", "~/docs"}, "PATH ~/docs"},
		{"scan", nil, []string{"--db", db, "scan", "~/docs"}, "PATH ~/docs"},
		{"review", nil, []string{"--db", db, "review", "~/docs"}, "PATH ~/docs"},
		{"set-date", nil, []string{"--db", db, "review", "set-date", "~/docs", "+6m"}, "FOLDER ~/docs"},
		{"export path", nil, []string{"--db", db, "review", "export", "~/docs"}, "PATH ~/docs"},
		{"export -o", nil, []string{"--db", db, "review", "export", "-o", "~/r.csv"}, "-o ~/r.csv"},
		{"import", nil, []string{"--db", db, "review", "import", "~/r.csv"}, "FILE ~/r.csv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := run(t, ctx, tt.env, tt.args...)
			if r.code != ExitError {
				t.Errorf("exit code %d, want %d; stderr %q", r.code, ExitError, r.stderr)
			}
			if !strings.HasPrefix(r.stderr, "filo: "+tt.want+" starts with ~") || strings.Count(r.stderr, "\n") != 1 {
				t.Errorf("stderr %q, want one line starting with %q", r.stderr, "filo: "+tt.want)
			}
		})
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("created %s in the current directory", e.Name())
	}
}
