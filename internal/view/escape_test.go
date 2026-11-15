package view

import "testing"

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
		if got := string(Visible([]byte(tc.in))); got != tc.want {
			t.Errorf("Visible(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEscapeFinalNewline: a name that ends in a newline must not end the line it is printed on; only a whole-line
// writer keeps a final newline.
func TestEscapeFinalNewline(t *testing.T) {
	if got := Escape("report.pdf\n"); got != `report.pdf\n` {
		t.Errorf("Escape = %q, want the newline escaped", got)
	}
	if got := string(Visible([]byte("a line\n"))); got != "a line\n" {
		t.Errorf("Visible = %q, want the final newline kept", got)
	}
}
