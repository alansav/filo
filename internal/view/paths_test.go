package view

import "testing"

func TestPathLines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		paths   []string
		want    string
		escaped int
	}{
		{"plain names", []string{"/r/a b.pdf", "/r/c.txt "}, "/r/a b.pdf\n/r/c.txt \n", 0},
		{"names escaping changes", []string{"/r/new\nline", "/r/back\\slash", "/r/ok", "/r/\x1b[2Jesc"},
			"/r/new\\nline\n/r/back\\\\slash\n/r/ok\n/r/\\x1b[2Jesc\n", 3},
		{"none", nil, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, b := plainView()
			if n := r.PathLines(tc.paths); n != tc.escaped {
				t.Errorf("escaped %d, want %d", n, tc.escaped)
			}
			if b.String() != tc.want {
				t.Errorf("got %q, want %q", b.String(), tc.want)
			}
		})
	}
}

// TestRawPaths: the names as they are, each followed by a NUL; nothing at all in a terminal.
func TestRawPaths(t *testing.T) {
	paths := []string{"/r/new\nline", "/r/\x1b[2Jesc", "/r/caf\xff"}
	r, b := plainView()
	r.RawPaths(paths)
	if want := "/r/new\nline\x00/r/\x1b[2Jesc\x00/r/caf\xff\x00"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	r, b = termView(80, true)
	r.RawPaths(paths)
	if b.Len() != 0 {
		t.Errorf("wrote %q to a terminal", b.String())
	}
}

// TestCompletions: the mode, then each candidate as it is, one per line; one with a control character left out; and
// nothing at all on a terminal.
func TestCompletions(t *testing.T) {
	cands := []string{`British\ Gas `, "notes (old)/", "new\nline", "esc\x1b[2J", "caf\xc3\xa9"}
	r, b := plainView()
	r.Completions("words", cands)
	if want := "words\nBritish\\ Gas \nnotes (old)/\ncafé\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	r, b = termView(80, true)
	r.Completions("files", cands)
	if b.Len() != 0 {
		t.Errorf("wrote %q to a terminal", b.String())
	}
}
