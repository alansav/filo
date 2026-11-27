package sheet

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestRowRaw(t *testing.T) {
	sh, _ := Open([]byte("id,export,name\n1,2,\" notes.txt \"\n3,4\n"), "id", "export")
	var got []string
	if err := sh.Each(func(r Row) { got = append(got, r.Raw("name")+"|"+r.Get("name")+"|"+r.Raw("missing")) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{" notes.txt |notes.txt|", "||"}) {
		t.Errorf("rows %q, want the cell with its spaces from Raw, trimmed from Get", got)
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
		{"a bare quote leaves the error to csv", head + "1,12\" ruler\n" + "1," + strings.Repeat("x", MaxRecord) + "\n",
			"line 2, column 5: bare"},
		{"escaped quotes inside quotes", head + `1,"a ""b"", c"` + "\n" + strings.Repeat("\n", MaxRows-2), ""},
		{"text after a closing quote leaves the error to csv", head + `1,"a"b` + "\n", "extraneous or missing \""},
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

// TestReadFile: a file over MaxSize is refused from its size, before it is read.
func TestReadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.csv")
	if err := os.WriteFile(p, []byte("id,export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(p); err != nil || string(data) != "id,export\n" {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if err := os.Truncate(p, MaxSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(p); !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadFile over MaxSize = %v, want ErrTooLarge", err)
	}
}

func TestNewWriter(t *testing.T) {
	var b strings.Builder
	cw, err := NewWriter(&b, []string{"id", "name"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cw.Write([]string{"1", "a,b"}); err != nil {
		t.Fatal(err)
	}
	cw.Flush()
	if got, want := b.String(), BOM+"id,name\n1,\"a,b\"\n"; got != want || cw.Error() != nil {
		t.Errorf("wrote %q, %v; want %q", got, cw.Error(), want)
	}
}

func TestFileCells(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	mtime := time.Date(2026, 5, 1, 23, 30, 59, 0, time.UTC).UnixNano()
	tests := []struct {
		root, rel string
		want      []string
	}{
		{"/home/u", "doc.txt", []string{"/home/u", "doc.txt", "12", "2026-05-02 00:30"}},
		{"/home/u", "a/b/doc.txt", []string{"/home/u/a/b", "doc.txt", "12", "2026-05-02 00:30"}},
		{"/home/u", "a/=sum.txt", []string{"/home/u/a", "'=sum.txt", "12", "2026-05-02 00:30"}},
		{"/", "hosts", []string{"/", "hosts", "12", "2026-05-02 00:30"}},
		{"/", "etc/hosts", []string{"/etc", "hosts", "12", "2026-05-02 00:30"}},
	}
	for _, tt := range tests {
		if got := FileCells(tt.root, tt.rel, 12, mtime, loc); !slices.Equal(got, tt.want) {
			t.Errorf("FileCells(%q, %q) = %q, want %q", tt.root, tt.rel, got, tt.want)
		}
	}
}

func TestReadIDs(t *testing.T) {
	sh, _ := Open([]byte("id,export,tags\n7,2,x\n0,2,x\nx,2,x\n7,0,x\n7,y,x\n7,2,x,typed\n"), "id", "export")
	want := []struct {
		id, ex  int64
		problem string
	}{
		{7, 2, ""},
		{0, 0, `id "0" is not a file ID`},
		{0, 0, `id "x" is not a file ID`},
		{0, 0, `export "0" is not an export number`},
		{0, 0, `export "y" is not an export number`},
		{0, 0, `"typed" is outside the columns; put it under tags`},
	}
	i := 0
	err := sh.Each(func(row Row) {
		id, ex, problem := ReadIDs(row, "tags")
		if i < len(want) && (id != want[i].id || ex != want[i].ex || problem != want[i].problem) {
			t.Errorf("line %d: %d, %d, %q; want %+v", row.Line, id, ex, problem, want[i])
		}
		i++
	})
	if err != nil || i != len(want) {
		t.Errorf("%d rows, %v", i, err)
	}
}
