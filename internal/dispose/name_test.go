package dispose

import (
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	ignore := []string{"Thumbs.db"}
	for _, tc := range []struct {
		old, name     string
		includeHidden bool
		want          string // "" = allowed; otherwise a word the reason holds
	}{
		{"Britsh Gas bill.pdf", "British Gas bill.pdf", false, ""},
		{"bill.pdf", "Bill.pdf", false, ""},
		{"notes.txt ", "notes.txt", false, ""},
		{"report .pdf", "report.pdf", false, ""},
		{".bashrc.bak", ".bashrc.old", true, ""},
		{"a", "2026/bill.pdf", false, "cannot hold /"},
		{"a", "", false, "not a file name"},
		{"a", ".", false, "not a file name"},
		{"a", "..", false, "not a file name"},
		{"a", "x\ty", false, "control character"},
		{"a", "x\ny", false, "control character"},
		{"a", "x\x1b[2Jy", false, "control character"},
		{"a", strings.Repeat("x", 256), false, "256 bytes"},
		{"a", strings.Repeat("x", 255), false, ""},
		{"bill.pdf", ".bill.pdf", true, "never hides"},
		{".notes", "notes", true, "never unhides"},
		{".a", ".filo-delete-x", true, "filo's own"},
		{"a", "Thumbs.db", false, "skips that name"},
		{"a", "node_modules", false, "skips that name"},
		{"bad\xffname", "goodname", false, "not valid UTF-8"},
		{"a", "bad\xff", false, "not valid UTF-8"},
	} {
		got := CheckName(tc.old, tc.name, tc.includeHidden, ignore)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("CheckName(%q, %q) = %q, want %q", tc.old, tc.name, got, tc.want)
		}
	}
}
