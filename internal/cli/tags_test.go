package cli

import (
	"strings"
	"testing"
)

// TestTagsGolden: filo tags and filo tags rename in a terminal (docs/design.md, Tags): no tag yet, the list, a typo
// fixed, a case changed, a key renamed, merging, and a rename that changes nothing.
func TestTagsGolden(t *testing.T) {
	ct := newTagTest(t)
	filo, in := ct.filo, ct.in
	var out strings.Builder
	step := func(title string, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(ExitOK, "", true, args...).stdout + "\n")
	}
	sep, oct := in("received/2026/09/2026-09-02 British Gas Bill.pdf"), in("received/2026/10/2026-10-03 British Gas Bill.pdf")
	step("tags (none yet)", "tags")
	filo(ExitOK, "", false, "tag", sep, oct, "sender:british gas", "type:bill")
	filo(ExitOK, "", false, "tag", in("notes/ideas.txt"), "sendr:British Gaz", "urgent")
	step("tags", "tags")
	step("tags rename 'sendr:British Gaz' 'sendr:British Gas'", "tags", "rename", "sendr:British Gaz",
		"sendr:British Gas")
	step("tags rename 'sender:british gas' 'sender:British Gas'", "tags", "rename", "sender:british gas",
		"sender:British Gas")
	step("tags rename sendr: sender:", "tags", "rename", "sendr:", "sender:")
	step("tags rename type:bill 'sender:British Gas' (merging)", "tags", "rename", "type:bill", "sender:British Gas")
	step("tags rename urgent urgent", "tags", "rename", "urgent", "urgent")
	filo(ExitOK, "", false, "tag", in("notes/ideas.txt"), "kind:bill", "type:bill")
	filo(ExitOK, "", false, "tag", sep, "type:bill")
	step("tags rename type: kind: (merging a key)", "tags", "rename", "type:", "kind:")
	filo(ExitOK, "", false, "tag", oct, "kind:invoice")
	step("tags rename kind:invoice kind:BILL (respelling another file)", "tags", "rename", "kind:invoice", "kind:BILL")
	step("tags", "tags")
	golden(t, "tags.txt", ct.root, out.String())

	r := filo(ExitError, "", false, "tags", "rename", "type:bill", "kind:bill")
	ct.contains("gone", r.stderr, "filo: no file has the tag type:bill (filo tags lists them)")
	r = filo(ExitError, "", false, "tags", "rename", "colour:", "color:")
	ct.contains("no key", r.stderr, "filo: no file has a tag with the key colour: (filo tags lists them)")
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"tags", "rename", "urgent"}, "tags rename takes OLD and NEW"},
		{[]string{"tags", "rename", "sender:", "kind:x"}, "give both as key:"},
		{[]string{"tags", "rename", "a*", "b"}, `OLD "a*": "*" is not taken here`},
		{[]string{"tags", "list"}, `unknown tags command "list"`},
		{[]string{"tags", "rename", "type:", "Type:"}, "OLD and NEW are the same key"},
	} {
		ct.contains(strings.Join(tt.args, " "), filo(ExitUsage, "", false, tt.args...).stderr, tt.want)
	}
}
