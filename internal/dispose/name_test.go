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
		{"a", "2026/bill.pdf", false, "cannot hold /"},
		{"a", "", false, "not a file name"},
		{"a", ".", false, "not a file name"},
		{"a", "..", false, "not a file name"},
		{"a", "x\ty", false, "control character"},
		{"a", "x\ny", false, "control character"},
		{"a", "x\x1b[2Jy", false, "control character"},
		{"a", strings.Repeat("x", 256), false, "256 bytes"},
		{"a", strings.Repeat("x", 255), false, ""},
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

func TestCheckPath(t *testing.T) {
	ignore := []string{"build"}
	for _, tc := range []struct {
		old, new      string
		includeHidden bool
		want          string
	}{
		{"inbox/bill.pdf", "received/2026/11/bill.pdf", false, ""},
		{"inbox/bill.pdf", "bill.pdf", false, ""},
		{"bill.pdf", "inbox/Bill (paid).pdf", false, ""},
		// A hidden file, or one under a hidden directory, stays where it is (it may be .ssh/id_ed25519).
		{".ssh/id_ed25519", "Sync/.x/id_ed25519", true, "never renamed or moved"},
		{".config/a/x", ".config/b/x", true, "never renamed or moved"},
		{".archive/bill.pdf", "inbox/bill.pdf", true, "never renamed or moved"},
		{"inbox/.notes", "inbox/notes", true, "never renamed or moved"},
		{".bashrc.bak", ".bashrc.old", true, "never renamed or moved"},
		{"Sync/.peer/authorized_keys", ".ssh/authorized_keys", true, "never renamed or moved"},
		// Nor is a file hidden by one.
		{"inbox/bill.pdf", ".archive/bill.pdf", true, "never hides"},
		{"inbox/authorized_keys", ".ssh/authorized_keys", true, "never hides"},
		{"bill.pdf", ".bill.pdf", true, "never hides"},
		{"inbox/bill.pdf", ".archive/bill.pdf", false, "never hides"},
		{"inbox/bill.pdf", "x/build/bill.pdf", false, "skips a directory"},
		{"inbox/bill.pdf", "x/node_modules/bill.pdf", false, "skips a directory"},
		{"inbox/bill.pdf", "x/bin/bill.pdf", false, ""}, // no special case for bin and obj
		{"inbox/bill.pdf", "x\ty/bill.pdf", false, "control character"},
		{"inbox/bill.pdf", strings.Repeat("d", 256) + "/bill.pdf", false, "256 bytes"},
		{"inbox/bill.pdf", ".filo-delete-1/bill.pdf", true, "never hides"},
		{"inbox/bill.pdf", "inbox/.bill.pdf", true, "never hides"},
		{"inbox/bill.pdf", "inbox/.plan.txt", false, "never hides"}, // not "skips that name"
		{"inbox/bill.pdf", "x/bad\xff/bill.pdf", false, "not valid UTF-8"},
		{"caf\xe9/bill.pdf", "caf\xe9/Bill.pdf", false, ""},     // a directory the file is in already is not judged
		{"a\tb/bill.pdf", "a\tb/new/bill.pdf", false, ""},       // nor one it stays under
		{"inbox/bad\xffname", "archive/bad\xffname", false, ""}, // a name that does not change is not judged
	} {
		got := CheckPath(tc.old, tc.new, tc.includeHidden, ignore)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("CheckPath(%q, %q) = %q, want %q", tc.old, tc.new, got, tc.want)
		}
	}
}
