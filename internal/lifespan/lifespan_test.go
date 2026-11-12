package lifespan

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

var today = time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)

func TestParseDate(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"  ", "", false},
		{"2027-06-30", "2027-06-30", false},
		{" 2027-06-30 ", "2027-06-30", false},
		{"2028-02-29", "2028-02-29", false}, // a leap day
		{"forever", "forever", false},
		{" Forever ", "forever", false},
		{"FOREVER", "forever", false},
		{"indefinitely", "", true}, // one word only
		{"never", "", true},
		{"keep", "", true},
		{"9999-12-31", "9999-12-31", false}, // a real date, if someone types one
		{"2027-02-29", "", true},            // not a leap year
		{"2027-02-30", "", true},
		{"2027-06-31", "", true},
		{"2027-13-01", "", true},
		{"2027-00-10", "", true},
		{"2027-6-3", "", true},
		{"27-06-30", "", true},
		{"06/07/2027", "", true},
		{"30/06/2027", "", true},
		{"01.06.2027", "", true},
		{"2027-06-30 00:00:00", "", true},
		{"+1y", "", true}, // durations are not accepted in the CSV
		{"+30d", "", true},
		{"tomorrow", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDate(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseDate(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

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
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteThenRead(t *testing.T) {
	items := []store.FileItem{
		{FileID: 7, RootPath: "/r", Path: "docs/2023-03 invoice.pdf", Size: 10, MtimeNS: today.UnixNano()},
		{FileID: 8, RootPath: "/r", Path: "=evil", Size: 1, MtimeNS: 1, KeepUntil: "2026-01-01"},
		{FileID: 9, RootPath: "/r", Path: "a/b/x,\"quoted\"\nname", Size: 1, MtimeNS: 1, KeepUntil: "2030-01-01"},
		{FileID: 10, RootPath: "/r", Path: "🗂️ archive/👨‍👩‍👧 family.md", Size: 1, MtimeNS: 1},
		{FileID: 11, RootPath: "/r", Path: "tax/2019.pdf", Size: 1, MtimeNS: 1, KeepUntil: "forever"},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, 3, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\ufeffid,export,directory,name,size,modified,keep_until\n") {
		t.Errorf("no BOM and header: %q", out[:60])
	}
	for _, want := range []string{
		"7,3,/r/docs,2023-03 invoice.pdf,10,2026-11-11 15:00,\n",
		"8,3,/r,'=evil,1,", ",2026-01-01\n",
		"9,3,/r/a/b,\"x,\"\"quoted\"\"\nname\",1,", ",2030-01-01\n",
		"10,3,/r/🗂️ archive,👨‍👩‍👧 family.md,1,",
		"11,3,/r/tax,2019.pdf,1,", ",forever\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV lacks %q:\n%s", want, out)
		}
	}
	// Read back untouched: every row comes back with the date it was exported with (the register sees no change).
	p, err := ReadCSV(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []store.LifespanRow{
		{Line: 2, ExportID: 3, FileID: 7},
		{Line: 3, ExportID: 3, FileID: 8, KeepUntil: "2026-01-01"},
		{Line: 4, ExportID: 3, FileID: 9, KeepUntil: "2030-01-01"},
		{Line: 6, ExportID: 3, FileID: 10},
		{Line: 7, ExportID: 3, FileID: 11, KeepUntil: "forever"},
	}
	if len(p.Problems) != 0 || len(p.Rows) != len(want) {
		t.Fatalf("untouched read = %+v", p)
	}
	for i := range want {
		if p.Rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, p.Rows[i], want[i])
		}
	}
}

func TestReadCSV(t *testing.T) {
	const head = "id,export,directory,name,size,modified,keep_until\n"
	tests := []struct {
		name     string
		in       string
		want     []store.LifespanRow
		problems []string // substrings, one per problem, in order
		err      error
	}{
		{name: "dates, forever and blanks",
			in: head + "1,2,/d,a,1,m,2027-01-01\n2,2,/d,b,1,m,\n3,2,/d,c,1,m,Forever\n",
			want: []store.LifespanRow{
				{Line: 2, ExportID: 2, FileID: 1, KeepUntil: "2027-01-01"},
				{Line: 3, ExportID: 2, FileID: 2},
				{Line: 4, ExportID: 2, FileID: 3, KeepUntil: "forever"},
			}},
		{name: "empty spreadsheet rows are skipped",
			in:   head + "1,2,/d,a,1,m,2027-01-01\n,,,,,,\n\n",
			want: []store.LifespanRow{{Line: 2, ExportID: 2, FileID: 1, KeepUntil: "2027-01-01"}}},
		{name: "semicolons, BOM, CRLF, reordered and extra columns",
			in: "\ufeffkeep_until;notes;export;id\r\n2027-02-03;mine;5;1\r\n;;5;2\r\n",
			want: []store.LifespanRow{
				{Line: 2, ExportID: 5, FileID: 1, KeepUntil: "2027-02-03"},
				{Line: 3, ExportID: 5, FileID: 2},
			}},
		{name: "tabs", in: "id\texport\tkeep_until\n1\t2\t2027-01-01\n",
			want: []store.LifespanRow{{Line: 2, ExportID: 2, FileID: 1, KeepUntil: "2027-01-01"}}},
		{name: "a quoted name with commas and a newline keeps the line numbers",
			in:   head + "1,2,/d,\"a,b\nc\",1,m,\n2,2,/d,q,1,m,2027-01-01\n",
			want: []store.LifespanRow{{Line: 2, ExportID: 2, FileID: 1}, {Line: 4, ExportID: 2, FileID: 2, KeepUntil: "2027-01-01"}}},
		{name: "bad rows are refused one by one",
			in: head +
				"x,2,/d,a,1,m,2027-01-01\n" +
				"1,,/d,a,1,m,2027-01-01\n" +
				"2,2,/d,a,1,m,30/06/2027\n" +
				"3,2,/d,a,1,m,2027-02-29\n" +
				"4,2,/d,a,1,m,+1y\n" +
				"5,2,/d,a,1,m,,2027-01-01\n" +
				"7,2,/d,a,1,m,2027-01-01\n",
			want: []store.LifespanRow{{Line: 8, ExportID: 2, FileID: 7, KeepUntil: "2027-01-01"}},
			problems: []string{"not a file ID", "not an export number", `"30/06/2027" is not a date`,
				`"2027-02-29" is not a date`, `"+1y" is not a date`, `"2027-01-01" is outside the columns`}},
		{name: "a file listed twice is refused both times",
			in:       head + "1,2,/d,a,1,m,2027-01-01\n1,2,/d,a,1,m,2028-01-01\n3,2,/d,c,1,m,\n",
			want:     []store.LifespanRow{{Line: 4, ExportID: 2, FileID: 3}},
			problems: []string{"more than once", "more than once"}},
		{name: "an old review CSV", in: "id,export,reason,path,size,modified,review_date,action,retain_until\n",
			err: ErrOldReviewCSV},
		{name: "not a lifespan CSV", in: "a,b,c\n1,2,3\n", err: ErrNotLifespanCSV},
		{name: "no keep_until column", in: "id,export,directory,name\n1,2,/d,a\n", err: ErrNotLifespanCSV},
		{name: "empty file", in: "", err: ErrNotLifespanCSV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ReadCSV(strings.NewReader(tc.in))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Rows) != len(tc.want) {
				t.Fatalf("rows = %+v, want %+v (problems %+v)", p.Rows, tc.want, p.Problems)
			}
			for i := range tc.want {
				if p.Rows[i] != tc.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, p.Rows[i], tc.want[i])
				}
			}
			if len(p.Problems) != len(tc.problems) {
				t.Fatalf("problems = %+v, want %d", p.Problems, len(tc.problems))
			}
			for i, w := range tc.problems {
				if !strings.Contains(p.Problems[i].Msg, w) {
					t.Errorf("problem %d = %+v, want it to mention %q", i, p.Problems[i], w)
				}
			}
		})
	}
}

func TestReadCSVBrokenQuote(t *testing.T) {
	_, err := ReadCSV(strings.NewReader("id,export,keep_until\n1,2,\"2027-01-01,\n"))
	if err == nil {
		t.Fatal("want an error for an unterminated quote")
	}
}
