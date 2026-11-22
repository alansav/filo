package walk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkipsPath(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"app/bin", "plain/bin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "app", "app.csproj"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel    string
		hidden bool
		ignore []string
		want   bool
	}{
		{"notes/a.txt", false, nil, false},
		{".config/a.txt", false, nil, true},
		{".config/a.txt", true, nil, false},
		{"notes/.a.txt", false, nil, true},
		{"build/a.txt", false, []string{"build"}, true},
		{"x/node_modules/a.js", true, nil, true},
		{"x/.filo-delete-123/a.txt", true, nil, true},
		{"x/.filo-delete-123", true, nil, false}, // a file named so is a file
		{"app/bin/a.dll", false, nil, true},
		{"plain/bin/a.sh", false, nil, false},
		{"app/bin", false, nil, false}, // the last name is the file itself
	}
	for _, tt := range tests {
		if got := SkipsPath(root, tt.rel, tt.hidden, tt.ignore); got != tt.want {
			t.Errorf("SkipsPath(%q, hidden %v, ignore %v) = %v, want %v", tt.rel, tt.hidden, tt.ignore, got, tt.want)
		}
	}
}
