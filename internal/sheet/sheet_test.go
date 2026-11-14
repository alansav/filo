package sheet

import (
	"slices"
	"strings"
	"testing"
)

func TestCell(t *testing.T) {
	tests := map[string]string{
		"/home/a/doc.txt": "/home/a/doc.txt",
		"=HYPERLINK(1)":   "'=HYPERLINK(1)",
		"+1":              "'+1",
		"-x":              "'-x",
		"@SUM":            "'@SUM",
		"bad\xffname":     "bad\ufffdname",
		"":                "",
		"/home/a/📷 Pictures/🏖️ beach.jpg": "/home/a/📷 Pictures/🏖️ beach.jpg",
		"👨‍👩‍👧 family.md":                 "👨‍👩‍👧 family.md",
		"🎉=party":                         "🎉=party",
	}
	for in, want := range tests {
		if got := Cell(in); got != want {
			t.Errorf("Cell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDropRepeated(t *testing.T) {
	rows := []string{"a1", "b1", "a2", "c1", "a3"}
	var refused []string
	kept := DropRepeated(rows, func(r string) byte { return r[0] }, func(r string) { refused = append(refused, r) })
	if !slices.Equal(kept, []string{"b1", "c1"}) || !slices.Equal(refused, []string{"a1", "a2", "a3"}) {
		t.Errorf("kept %v, refused %v; want [b1 c1], [a1 a2 a3]", kept, refused)
	}
}

// TestOpen: the separator is found from the header, in any case and order; a file without the columns gives the
// headers it could read.
func TestOpen(t *testing.T) {
	data := []byte(BOM + "Export;ID;mine\r\n2;7;x;extra\r\n;;\r\n3;8;\r\n")
	sh, _ := Open(data, "id", "export")
	if sh == nil {
		t.Fatal("Open found no header")
	}
	var got []string
	if err := sh.Each(func(r Row) {
		got = append(got, strings.Join([]string{r.Get("id"), r.Get("export"), r.Get("missing"), r.Beyond()}, "|"))
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"7|2||extra", "8|3||"}) {
		t.Errorf("rows %q", got)
	}
	if sh, headers := Open([]byte("a,b\n"), "id"); sh != nil || len(headers) == 0 || !HasAll(headers[0], "a", "b") {
		t.Errorf("Open without the columns = %v, %v", sh, headers)
	}
}
