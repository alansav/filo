package sheet

import (
	"errors"
	"io"
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

// TestReadAllLimit: no more than MaxSize+1 bytes are read, and a file over MaxSize is refused.
func TestReadAllLimit(t *testing.T) {
	r := &counter{}
	if _, err := ReadAll(io.LimitReader(r, MaxSize+10)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadAll over MaxSize = %v, want ErrTooLarge", err)
	}
	if r.n > MaxSize+1+64<<10 { // io.ReadAll may ask for a little more than the limit reader returns
		t.Errorf("ReadAll read %d bytes, want at most about %d", r.n, MaxSize+1)
	}
	if data, err := ReadAll(strings.NewReader("id,export\n")); err != nil || string(data) != "id,export\n" {
		t.Errorf("ReadAll = %q, %v", data, err)
	}
}

// counter is an endless reader of commas that counts what is read from it.
type counter struct{ n int }

func (c *counter) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ','
	}
	c.n += len(p)
	return len(p), nil
}

// TestLimits: a CSV over the row limits is refused before its rows are read, saying where; a separator or line end
// inside quotes does not count.
func TestLimits(t *testing.T) {
	head := "id,export\n"
	tests := []struct {
		name string
		data string
		want string // "" = read
	}{
		{"within", head + "1,2\n", ""},
		{"too many fields", head + "1,2\n" + strings.Repeat(",", MaxFields) + "\n", "line 3 has more than"},
		{"fields inside quotes", head + `1,"` + strings.Repeat(",", MaxFields) + "\"\n", ""},
		{"row too long", head + "1," + strings.Repeat("x", MaxRecord) + "\n", "row on line 2 is over"},
		{"too many rows", head + strings.Repeat("\n", MaxRows), "more than 1048576 rows"},
		{"as many rows as allowed", head + strings.Repeat("\n", MaxRows-1), ""},
		{"line ends inside quotes", head + "1,\"" + strings.Repeat("\n", 1000) + "\"\n" + strings.Repeat("\n", MaxRows-2), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sh, _ := Open([]byte(tc.data), "id", "export")
			if sh == nil {
				t.Fatal("Open found no header")
			}
			err := sh.Each(func(Row) {})
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("Each = %v, want no error", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("Each = %v, want %q", err, tc.want)
			}
		})
	}
	if sh, _ := Open([]byte(strings.Repeat(",", MaxFields)+"id,export\n"), "id", "export"); sh != nil {
		t.Error("Open read a header over MaxFields")
	}
}
