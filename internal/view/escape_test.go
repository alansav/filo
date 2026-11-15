package view

import "testing"

func TestEscape(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain name.pdf", "plain name.pdf"},
		{"report.pdf\n", `report.pdf\n`}, // a name that ends in a newline does not end the line it is printed on
		{"line\nnext\tcol", `line\nnext\tcol`},
		{"fake\n  2020-01-01  1 KB  /home/u/harmless.txt\n", `fake\n  2020-01-01  1 KB  /home/u/harmless.txt\n`},
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
		if got := Escape(tc.in); got != tc.want {
			t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFit(t *testing.T) {
	tests := []struct {
		text string
		cols int
		want string
	}{
		{"hashing · ~/Pictures", 0, "hashing · ~/Pictures"},  // no width: never cut
		{"hashing · ~/Pictures", 21, "hashing · ~/Pictures"}, // 20 columns fit in 21
		{"hashing · ~/Pictures", 20, "hashing · ~/Pictur…"},  // one short of the edge
		{"walking · ~/写真/2024", 15, "walking · ~/…"},         // a wide character never half fits
		{"walking · ~/a\x1b[2Jb", 0, "walking · ~/a\\x1b[2Jb"},
		{"walking", 1, ""}, // nothing fits without wrapping // escaped first
		{"walking · ~/a\x1b[2Jb", 18, "walking · ~/a\\x1…"}, // and cut after escaping
	}
	for _, tc := range tests {
		if got := Fit(tc.text, tc.cols); got != tc.want {
			t.Errorf("Fit(%q, %d) = %q, want %q", tc.text, tc.cols, got, tc.want)
		}
	}
}
