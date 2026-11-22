package tagindex

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

func tag(t *testing.T, s string) tags.Tag {
	t.Helper()
	tg, err := tags.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func file(t *testing.T, rel, date string, mtime time.Time, ts ...string) store.IndexFile {
	f := store.IndexFile{Rel: rel, FileDate: date, MtimeNS: mtime.UnixNano()}
	for _, s := range ts {
		f.Tags = append(f.Tags, tag(t, s))
	}
	return f
}

var bills = store.TagIndex{RootPath: "/home/u/Documents", Dir: "bills", Name: "index"}

// sample is a small index: two senders, a bare tag, a file with no sender and one with no tag.
func sample(t *testing.T) []store.IndexFile {
	may := time.Date(2026, 5, 1, 23, 30, 0, 0, time.UTC)
	return []store.IndexFile{
		file(t, "bills/2026/09/gas.pdf", "2026-09-02", may, "urgent", "sender:British Gas", "type:bill"),
		file(t, "bills/2026/10/gas.pdf", "2026-10-03", may, "sender:British Gas", "type:bill"),
		file(t, "bills/water.pdf", "", may, "sender:Thames Water"),
		file(t, "bills/note.txt", "", may, "type:note"),
		file(t, "bills/scan.pdf", "2026-05-01", may),
	}
}

// describe writes the data as lines, for comparing.
func describe(d Data) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", d.Name, d.Title)
	for i, f := range d.Files {
		fmt.Fprintf(&b, "file %d %s %s %d tags\n", i, f.Date, f.Rel, len(f.Tags))
	}
	for _, k := range append(d.Keys, d.Bare) {
		fmt.Fprintf(&b, "key %q %s files %d none %v\n", k.Key, k.Dir, k.Files, k.None)
		for _, v := range k.Values {
			fmt.Fprintf(&b, "  %s %s %v\n", v.Tag, v.Page, v.Files)
		}
	}
	fmt.Fprintf(&b, "untagged %v\n", d.Untagged)
	return b.String()
}

func TestBuild(t *testing.T) {
	d, err := Build(bills, sample(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `index bills
file 0 2026-10-03 2026/10/gas.pdf 2 tags
file 1 2026-09-02 2026/09/gas.pdf 3 tags
file 2 2026-05-01 note.txt 1 tags
file 3 2026-05-01 scan.pdf 0 tags
file 4 2026-05-01 water.pdf 1 tags
key "sender" sender files 3 none [2 3]
  sender:British Gas sender/british-gas-` + PageName(tag(t, "sender:British Gas"))[12:20] + `.html [0 1]
  sender:Thames Water sender/thames-water-` + PageName(tag(t, "sender:Thames Water"))[13:21] + `.html [4]
key "type" type files 3 none [3 4]
  type:bill type/bill-` + PageName(tag(t, "type:bill"))[5:13] + `.html [0 1]
  type:note type/note-` + PageName(tag(t, "type:note"))[5:13] + `.html [2]
key "" _ files 1 none []
  urgent _/urgent-` + PageName(tag(t, "urgent"))[7:15] + `.html [1]
untagged [3]
`
	if got := describe(d); got != want {
		t.Errorf("Build =\n%s\nwant\n%s", got, want)
	}

	// The root itself: the files keep their paths, and the title is the root's name.
	root := store.TagIndex{RootPath: "/home/u/Documents", Name: "tags"}
	d, err = Build(root, sample(t)[:1])
	if err != nil || d.Title != "Documents" || d.Name != "tags" || d.Files[0].Rel != "bills/2026/09/gas.pdf" {
		t.Errorf("Build of the root = %+v, %v", d, err)
	}

	// No files: an index with nothing in it, and the bare tags' directory still named.
	d, err = Build(bills, nil)
	if err != nil || len(d.Files) != 0 || len(d.Keys) != 0 || d.Bare.Dir != BareDir {
		t.Errorf("Build of nothing = %+v, %v", d, err)
	}
}

func TestHash(t *testing.T) {
	d1, _ := Build(bills, sample(t))
	d2, _ := Build(bills, sample(t))
	if d1.Hash() != d2.Hash() {
		t.Error("the same data hashes differently")
	}
	changed := sample(t)
	changed[2].Tags = nil
	d3, _ := Build(bills, changed)
	renamed := bills
	renamed.Name = "tags"
	d4, _ := Build(renamed, sample(t))
	for _, d := range []Data{d3, d4} {
		if d.Hash() == d1.Hash() {
			t.Errorf("a change keeps the hash: %s", describe(d))
		}
	}
}

func TestPageName(t *testing.T) {
	for _, c := range []struct{ tag, slug string }{
		{"sender:British Gas", "british-gas-"},
		{"sender:British-Gas", "british-gas-"},
		{"sender:  British -- Gas!", "british-gas-"},
		{"sender:Café", "caf-"},
		{"sender:東京ガス", ""},
		{"sender:" + strings.Repeat("ab ", 30), strings.Repeat("ab-", 13) + "a-"},
		{"_x", "x-"},
		{"index", "index-"},
	} {
		got := PageName(tag(t, c.tag))
		if !strings.HasPrefix(got, c.slug) || len(got) != len(c.slug)+8+len(".html") || strings.HasPrefix(got, "_") ||
			got == "index.html" {
			t.Errorf("PageName(%s) = %s, want %s + 8 hex digits", c.tag, got, c.slug)
		}
	}
	a, b := PageName(tag(t, "sender:British Gas")), PageName(tag(t, "sender:British-Gas"))
	if a == b {
		t.Errorf("two tags with one slug share a page: %s", a)
	}
	if c := PageName(tag(t, "sender:BRITISH GAS")); c != a {
		t.Errorf("a tag's page changes with its case: %s, %s", a, c)
	}
	if k := PageName(tag(t, "from:British Gas")); k == a {
		t.Errorf("one value under two keys has one page name: %s", k)
	}
}
