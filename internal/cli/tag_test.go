package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

// newTagTest is a register of one root holding two bills and a note, with the inbox not scanned yet.
func newTagTest(t *testing.T) *cliTest {
	t.Helper()
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 22, 12, 0, 0, 0, time.Local))
	ct.write("received/2026/09/2026-09-02 British Gas Bill.pdf", "september bill")
	ct.write("received/2026/10/2026-10-03 British Gas Bill.pdf", "october bill")
	ct.write("notes/ideas.txt", "ideas")
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	return ct
}

// TestTagGolden: filo tag and untag in a terminal (docs/design.md, Tags): tagging two files, a tag given the spelling
// it has already, a file tagged again, a new file registered by itself with its copies' tags, untagging a key, and
// nothing to change.
func TestTagGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in := ct.filo, ct.in
	var out strings.Builder
	step := func(title string, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(ExitOK, "", true, args...).stdout + "\n")
	}
	sep, oct := in("received/2026/09/2026-09-02 British Gas Bill.pdf"), in("received/2026/10/2026-10-03 British Gas Bill.pdf")
	step("tag SEP OCT 'sender:British Gas' type:bill", "tag", sep, oct, "sender:British Gas", "type:bill")
	step("tag NOTE 'sender:british gas' urgent", "tag", in("notes/ideas.txt"), "sender:british gas", "urgent")
	step("tag SEP OCT project:house type:Bill", "tag", sep, oct, "project:house", "type:Bill")
	ct.write("inbox/scan 2026-10-03.pdf", "october bill")
	step("tag INBOX urgent (not seen yet)", "tag", in("inbox/scan 2026-10-03.pdf"), "urgent")
	step("untag SEP OCT project:", "untag", sep, oct, "project:")
	step("untag SEP project:", "untag", sep, "project:")
	step("tag SEP type:bill", "tag", sep, "type:bill")
	step("tag SEP NOTE urgent", "tag", sep, in("notes/ideas.txt"), "urgent")
	ct.write("inbox/copy.pdf", "october bill")
	step("tag INBOX/COPY urgent (not seen yet, with its copies' tag)", "tag", in("inbox/copy.pdf"), "urgent")
	if err := os.Symlink(in("inbox"), in("inbox link")); err != nil {
		t.Fatal(err)
	}
	ct.write("inbox/twice.pdf", "twice")
	step("tag INBOX/TWICE 'INBOX LINK/TWICE' urgent (one file by two paths)", "tag", in("inbox/twice.pdf"),
		in("inbox link/twice.pdf"), "urgent")
	golden(t, "tag.txt", ct.root, out.String())

	// The registered files are known to the next scan, which finds only the symlink new.
	after := filo(ExitOK, "", false, "scan").stdout
	ct.contains("scan after", after, "- 1 new")
	ct.lacks("scan after", after, "moved", "missing", "changed")
}

// TestTagRefusals: all or nothing: a file that cannot be tagged is listed, and no file changes; nor is a new file
// registered.
func TestTagRefusals(t *testing.T) {
	ct := newTagTest(t)
	filo, in, contains := ct.filo, ct.in, ct.contains
	ct.write(".hidden/a.txt", "hidden")
	ct.write("inbox/new.txt", "new")
	if err := os.Symlink("ideas.txt", in("notes/link")); err != nil {
		t.Fatal(err)
	}
	outside := ct.work + "/outside.txt"
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in("moved"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(in("notes/ideas.txt"), in("moved/ideas.txt")); err != nil {
		t.Fatal(err)
	}
	r := filo(ExitError, "", false, "tag", in("inbox/new.txt"), in(".hidden/a.txt"), in("notes/link"), in("notes"),
		outside, in("moved/ideas.txt"), "urgent")
	contains("refusals", r.stdout, "Nothing was changed: 5 of 6 files were refused",
		in(".hidden/a.txt")+"   its root's settings skip it",
		in("notes/link")+"   a symlink: give the file it points to",
		in("notes")+"   a directory: filo tag takes files",
		outside+"   outside every root",
		in("moved/ideas.txt")+"   looks moved from "+in("notes/ideas.txt")+"; run filo scan",
		"Next: fix the files above, or leave them out, and run filo tag again")
	show := filo(ExitError, "", false, "show", in("inbox/new.txt"))
	contains("not registered", show.stderr, "not in the register")

	r = filo(ExitError, "", false, "untag", in("inbox/new.txt"), "urgent")
	contains("untag a file not seen", r.stdout, in("inbox/new.txt")+"   not in the register at this path: it has no tags")
}

func TestTagUsage(t *testing.T) {
	ct := newTagTest(t)
	note := ct.in("notes/ideas.txt")
	t.Chdir(ct.root)
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"tag"}, "give one FILE or more, then one TAG or more"},
		{[]string{"tag", "urgent"}, "give the files first, then the tags: urgent is not on the disk"},
		{[]string{"tag", note}, "give one TAG or more after the files"},
		{[]string{"tag", note, "urgent", "notes"}, "notes is a path here, after a tag"},
		{[]string{"tag", note, "sender:"}, `"sender:": give a value`},
		{[]string{"tag", note, "type:bill;receipt"}, `a tag cannot hold ";"`},
		{[]string{"tag", note, "*gas*"}, `a tag cannot hold "*"`},
		{[]string{"untag", note, "type:*"}, `"*" is not taken here`},
	}
	for _, tt := range tests {
		r := ct.filo(ExitUsage, "", false, tt.args...)
		ct.contains(strings.Join(tt.args, " "), r.stderr, tt.want)
	}
}
