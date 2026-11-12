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
		{"1965-07-04", "1965-07-04", false}, // past dates are fine
		{"2027-02-29", "", true},            // not a leap year
		{"2027-02-30", "", true},
		{"2027-06-31", "", true},
		{"2027-13-01", "", true},
		{"2027-6-3", "", true},
		{"06/07/2027", "", true},
		{"01.06.2027", "", true},
		{"forever", "", true}, // file_date is a date only
		{"7y", "", true},
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

func TestParseRetention(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"7y", "7y", false},
		{" 7Y ", "7y", false},
		{"18m", "18m", false},
		{"6w", "6w", false},
		{"30d", "30d", false},
		{"1000y", "1000y", false},
		{"forever", "forever", false},
		{"Forever", "forever", false},
		{"2030-03-14", "2030-03-14", false},
		{"2015-01-01", "2015-01-01", false}, // in the past: the file is simply past its retention
		{"0y", "", true},
		{"1001y", "", true},
		{"+7y", "", true},
		{"7", "", true},
		{"7x", "", true},
		{"y", "", true},
		{"7 y", "", true},
		{"-1y", "", true},
		{"2027-02-29", "", true},
		{"14/03/2030", "", true},
		{"indefinitely", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRetention(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseRetention(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDateFromName(t *testing.T) {
	const today = "2026-11-11" // so the latest date read is 2026-12-11
	tests := map[string]string{
		"2023-03-14 invoice.pdf":                 "2023-03-14",
		"invoice_2023_03_14.pdf":                 "2023-03-14",
		"scan.2023.03.14.jpg":                    "2023-03-14",
		"IMG_20230714_1032.jpg":                  "2023-07-14",
		"2023-03 invoice.pdf":                    "2023-03-31", // the month's last day
		"2024-02 rent.pdf":                       "2024-02-29",
		"report-2023.03.pdf":                     "2023-03-31",
		"1965-07-04 beach.jpg":                   "1965-07-04",
		"2026-12-11 booking.pdf":                 "2026-12-11", // 30 days ahead: still read
		"2026-12-12 booking.pdf":                 "",           // further ahead: not a date
		"1245-06 charter.pdf":                    "",           // before 1900
		"1899-12-31.txt":                         "",
		"2099-01-01 plan.md":                     "",
		"2019 report.pdf":                        "", // a lone year is not read
		"03-04-2023 letter.pdf":                  "", // day and month cannot be told apart
		"14-03-2023.pdf":                         "",
		"2023-01-01 to 2023-12-31 statement.pdf": "",           // two dates: never guessed
		"2023-03-14 copy of 20230314.pdf":        "2023-03-14", // the same date twice
		"2023-02-30 typo.pdf":                    "",
		"2023-13 report.pdf":                     "",
		"2023-03-14-2.pdf":                       "2023-03-14",
		"2023-03_14.pdf":                         "", // mixed separators
		"12023-03-14.pdf":                        "", // part of a longer number
		"2023-03-145.pdf":                        "",
		"202303141.jpg":                          "",
		"20230314":                               "2023-03-14",
		"notes.md":                               "",
		"":                                       "",
	}
	for name, want := range tests {
		if got := DateFromName(name, today); got != want {
			t.Errorf("DateFromName(%q) = %q, want %q", name, got, want)
		}
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
		{FileID: 7, RootPath: "/r", Path: "docs/2023-03 invoice.pdf", Size: 10, MtimeNS: today.UnixNano(),
			FileDate: "2023-03-31"},
		{FileID: 8, RootPath: "/r", Path: "=evil", Size: 1, MtimeNS: 1, Retention: "2026-01-01", KeepUntil: "2026-01-01"},
		{FileID: 9, RootPath: "/r", Path: "a/b/x,\"quoted\"\nname", Size: 1, MtimeNS: 1, FileDate: "2023-01-01",
			Retention: "7y", KeepUntil: "2030-01-01"},
		{FileID: 10, RootPath: "/r", Path: "🗂️ archive/👨‍👩‍👧 family.md", Size: 1, MtimeNS: 1},
		{FileID: 11, RootPath: "/r", Path: "tax/2019.pdf", Size: 1, MtimeNS: 1, Retention: "forever", KeepUntil: "forever"},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, 3, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\ufeffid,export,directory,name,size,modified,file_date,retention,keep_until\n") {
		t.Errorf("no BOM and header: %q", out[:60])
	}
	for _, want := range []string{
		"7,3,/r/docs,2023-03 invoice.pdf,10,2026-11-11 15:00,2023-03-31,,\n",
		"8,3,/r,'=evil,1,", ",,2026-01-01,2026-01-01\n",
		"9,3,/r/a/b,\"x,\"\"quoted\"\"\nname\",1,", ",2023-01-01,7y,2030-01-01\n",
		"10,3,/r/🗂️ archive,👨‍👩‍👧 family.md,1,",
		"11,3,/r/tax,2019.pdf,1,", ",,forever,forever\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV lacks %q:\n%s", want, out)
		}
	}
	// Read back untouched: every row comes back with what it was exported with (the register sees no change).
	p, err := ReadCSV(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	row := func(line int, id int64, date, ret, keep string) store.LifespanRow {
		return store.LifespanRow{Line: line, ExportID: 3, FileID: id, FileDate: date, HasFileDate: true,
			Retention: ret, KeepUntil: keep, HasKeepUntil: true}
	}
	want := []store.LifespanRow{
		row(2, 7, "2023-03-31", "", ""),
		row(3, 8, "", "2026-01-01", "2026-01-01"),
		row(4, 9, "2023-01-01", "7y", "2030-01-01"),
		row(6, 10, "", "", ""),
		row(7, 11, "", "forever", "forever"),
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
	const head = "id,export,directory,name,size,modified,file_date,retention,keep_until\n"
	r := func(line int, id int64, date, ret, keep string) store.LifespanRow {
		return store.LifespanRow{Line: line, ExportID: 2, FileID: id, FileDate: date, HasFileDate: true,
			Retention: ret, KeepUntil: keep, HasKeepUntil: true}
	}
	tests := []struct {
		name     string
		in       string
		want     []store.LifespanRow
		problems []string // substrings, one per problem, in order
		err      error
	}{
		{name: "periods, dates, forever and blanks",
			in: head + "1,2,/d,a,1,m,2023-03-14,7Y,\n2,2,/d,b,1,m,,,\n3,2,/d,c,1,m,,Forever,\n4,2,/d,e,1,m,,2015-06-30,\n",
			want: []store.LifespanRow{r(2, 1, "2023-03-14", "7y", ""), r(3, 2, "", "", ""), r(4, 3, "", "forever", ""),
				r(5, 4, "", "2015-06-30", "")}},
		{name: "keep_until cells are passed on for the register to compare",
			in:   head + "1,2,/d,a,1,m,2023-03-14,7y,2030-03-14\n2,2,/d,b,1,m,,forever,FOREVER\n",
			want: []store.LifespanRow{r(2, 1, "2023-03-14", "7y", "2030-03-14"), r(3, 2, "", "forever", "forever")}},
		{name: "empty spreadsheet rows are skipped",
			in:   head + "1,2,/d,a,1,m,,2027-01-01,\n,,,,,,,,\n\n",
			want: []store.LifespanRow{r(2, 1, "", "2027-01-01", "")}},
		{name: "semicolons, BOM, CRLF, reordered and extra columns, no keep_until or file_date",
			in: "\ufeffretention;notes;export;id\r\n2027-02-03;mine;5;1\r\n;;5;2\r\n",
			want: []store.LifespanRow{
				{Line: 2, ExportID: 5, FileID: 1, Retention: "2027-02-03"},
				{Line: 3, ExportID: 5, FileID: 2},
			}},
		{name: "tabs", in: "id\texport\tfile_date\tretention\n1\t2\t2023-01-01\t10y\n",
			want: []store.LifespanRow{{Line: 2, ExportID: 2, FileID: 1, FileDate: "2023-01-01", HasFileDate: true,
				Retention: "10y"}}},
		{name: "a quoted name with commas and a newline keeps the line numbers",
			in:   head + "1,2,/d,\"a,b\nc\",1,m,,,\n2,2,/d,q,1,m,,2027-01-01,\n",
			want: []store.LifespanRow{r(2, 1, "", "", ""), r(4, 2, "", "2027-01-01", "")}},
		{name: "bad rows are refused one by one",
			in: head +
				"x,2,/d,a,1,m,,7y,\n" +
				"1,,/d,a,1,m,,7y,\n" +
				"2,2,/d,a,1,m,,30/06/2027,\n" +
				"3,2,/d,a,1,m,,2027-02-29,\n" +
				"4,2,/d,a,1,m,,+1y,\n" +
				"5,2,/d,a,1,m,14/03/2023,7y,\n" +
				"6,2,/d,a,1,m,,,,2027-01-01\n" +
				"7,2,/d,a,1,m,2023-01-01,7y,\n",
			want: []store.LifespanRow{r(9, 7, "2023-01-01", "7y", "")},
			problems: []string{"not a file ID", "not an export number", `retention "30/06/2027" is not a retention`,
				`retention "2027-02-29" is not a retention`, `retention "+1y" is not a retention`,
				`file_date "14/03/2023" is not a date`, `"2027-01-01" is outside the columns`}},
		{name: "a file listed twice is refused both times",
			in:       head + "1,2,/d,a,1,m,,1y,\n1,2,/d,a,1,m,,2y,\n3,2,/d,c,1,m,,,\n",
			want:     []store.LifespanRow{r(4, 3, "", "", "")},
			problems: []string{"more than once", "more than once"}},
		{name: "an old review CSV", in: "id,export,reason,path,size,modified,review_date,action,retain_until\n",
			err: ErrOldCSV},
		{name: "an old lifespan CSV", in: "id,export,directory,name,size,modified,keep_until\n", err: ErrOldCSV},
		{name: "not a lifespan CSV", in: "a,b,c\n1,2,3\n", err: ErrNotLifespanCSV},
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
	_, err := ReadCSV(strings.NewReader("id,export,retention\n1,2,\"2027-01-01,\n"))
	if err == nil {
		t.Fatal("want an error for an unterminated quote")
	}
}
