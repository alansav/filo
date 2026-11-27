package walk

import "testing"

func TestSkipsPath(t *testing.T) {
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
		{"app/bin/a.dll", false, nil, false},     // no special case for bin and obj (beside a project file or not)
		{"app/bin/a.dll", false, []string{"bin"}, true},
	}
	for _, tt := range tests {
		if got := SkipsPath(tt.rel, tt.hidden, tt.ignore); got != tt.want {
			t.Errorf("SkipsPath(%q, hidden %v, ignore %v) = %v, want %v", tt.rel, tt.hidden, tt.ignore, got, tt.want)
		}
	}
}
