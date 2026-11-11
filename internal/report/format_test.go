package report

import "testing"

func TestSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KiB"}, {1536, "1.5 KiB"}, {100000, "97.7 KiB"},
		{200000, "195 KiB"}, {5 << 20, "5.0 MiB"}, {118 << 30, "118 GiB"}, {3 << 40, "3.0 TiB"},
	}
	for _, tc := range tests {
		if got := Size(tc.n); got != tc.want {
			t.Errorf("Size(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestCountAndPlural(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{{0, "0"}, {999, "999"}, {1000, "1,000"}, {52310, "52,310"}, {1234567, "1,234,567"}, {-1000, "-1,000"}}
	for _, tc := range tests {
		if got := Count(tc.n); got != tc.want {
			t.Errorf("Count(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if Plural(1, "group", "groups") != "1 group" || Plural(2, "group", "groups") != "2 groups" ||
		Plural(0, "file", "files") != "0 files" {
		t.Error("Plural")
	}
}
