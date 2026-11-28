package filecsv

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

func TestWriteAndParse(t *testing.T) {
	mtime := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC).UnixNano()
	item := func(id int64, rel, date, ret, keep string, ts ...tags.Tag) store.FilesItem {
		return store.FilesItem{FileItem: store.FileItem{FileID: id, RootPath: "/home/u/Documents", Path: rel,
			Size: 12, MtimeNS: mtime, FileDate: date, Retention: ret, KeepUntil: keep}, Tags: ts}
	}
	items := []store.FilesItem{
		item(7, "received/bill.pdf", "2026-10-01", "7y", "2033-10-01", tags.Tag{Key: "sender", Value: "British Gas"},
			tags.Tag{Key: "type", Value: "bill"}),
		item(8, "=odd.txt", "!2023-07-14", "", "", tags.Tag{Value: "-draft"}),
		item(9, "top.txt", "", "forever", "forever"),
	}
	var b bytes.Buffer
	if err := WriteCSV(&b, 3, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	want := "\ufeffid,export,directory,name,size,modified,file_date,retention,keep_until,tags\n" +
		"7,3,/home/u/Documents/received,bill.pdf,12,2026-10-03 09:30,2026-10-01,7y,2033-10-01,sender:British Gas; type:bill\n" +
		"8,3,/home/u/Documents,'=odd.txt,12,2026-10-03 09:30,!2023-07-14,,,'-draft\n" +
		"9,3,/home/u/Documents,top.txt,12,2026-10-03 09:30,,forever,forever,\n"
	if b.String() != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", b.String(), want)
	}
	p, err := ParseCSV(b.Bytes())
	if err != nil || len(p.Problems) != 0 || len(p.Rows) != 3 {
		t.Fatalf("ParseCSV: %+v, %v", p, err)
	}
	r := p.Rows[0]
	if r.FileID != 7 || r.ExportID != 3 || r.Line != 2 || r.FileDate != "2026-10-01" || r.Retention != "7y" ||
		!r.HasFileDate || !r.HasKeepUntil || r.KeepUntil != "2033-10-01" ||
		store.JoinTags(r.Tags) != "sender:British Gas; type:bill" {
		t.Errorf("row 1 = %+v", r)
	}
	if r := p.Rows[1]; r.FileDate != "!2023-07-14" || store.JoinTags(r.Tags) != "-draft" {
		t.Errorf("row 2 = %+v, want the mark kept and the formula guard dropped", r)
	}
	if r := p.Rows[2]; r.Retention != store.Forever || len(r.Tags) != 0 {
		t.Errorf("row 3 = %+v", r)
	}
}

func TestParseCSVRows(t *testing.T) {
	csv := "id,export,file_date,retention,keep_until,tags,notes\n" + // a column of the person's own
		"1,3,,7y,,urgent,x\n" +
		"2,3,06/07/2027,,,urgent,\n" + // a date in the locale's format: the whole row refused
		"3,3,,2030-01-01,,a*b,\n" + // a tag the spelling rules refuse: the whole row refused
		"4,3,,,,type:bill; Type:Bill,\n" + // a tag typed twice is one
		"5,3,,,,,\n" +
		"5,4,,,,,\n" + // one file under two exports: both refused
		"x,3,,,,,\n" +
		"6,7,,,,a*b,\n" + // refused for its tags ...
		"6,7,,7y,,,\n" // ... and the other copy refused too, not applied
	p, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 2 || p.Rows[0].FileID != 1 || p.Rows[1].FileID != 4 || len(p.Rows[1].Tags) != 1 {
		t.Errorf("rows = %+v", p.Rows)
	}
	lines := map[int]string{}
	for _, pr := range p.Problems {
		lines[pr.Line] = pr.Msg
	}
	for line, want := range map[int]string{3: "file_date", 4: "tags", 6: "more than once", 7: "more than once",
		8: "id", 9: "tags", 10: "more than once"} {
		if !strings.Contains(lines[line], want) {
			t.Errorf("line %d: %q, want it to say %q (all: %v)", line, lines[line], want, lines)
		}
	}
	if !slices.Equal(p.Exports, []int64{3, 4, 7}) {
		t.Errorf("exports = %v, want every export a row names, refused rows included", p.Exports)
	}
}

// TestParseCSVRetentionHalf: the retention cells of each row, read with package retention's rules (moved here from
// the retention CSV's reader when it was removed, roadmap 40).
func TestParseCSVRetentionHalf(t *testing.T) {
	const head = "id,export,directory,name,size,modified,file_date,retention,keep_until,tags\n"
	r := func(line int, id int64, date, ret, keep string) store.RetentionRow {
		return store.RetentionRow{Line: line, ExportID: 2, FileID: id, FileDate: date, HasFileDate: true,
			Retention: ret, KeepUntil: keep, HasKeepUntil: true}
	}
	tests := []struct {
		name     string
		in       string
		want     []store.RetentionRow
		problems []string // substrings, one per problem, in order
	}{
		{name: "periods, dates, forever and blanks",
			in: head + "1,2,/d,a,1,m,2023-03-14,7Y,,\n2,2,/d,b,1,m,,,,\n3,2,/d,c,1,m,,Forever,,\n" +
				"4,2,/d,e,1,m,,2015-06-30,,\n",
			want: []store.RetentionRow{r(2, 1, "2023-03-14", "7y", ""), r(3, 2, "", "", ""), r(4, 3, "", "forever", ""),
				r(5, 4, "", "2015-06-30", "")}},
		{name: "keep_until cells are passed on for the register to compare",
			in:   head + "1,2,/d,a,1,m,2023-03-14,7y, 2030-03-14 ,\n2,2,/d,b,1,m,,forever,FOREVER,\n",
			want: []store.RetentionRow{r(2, 1, "2023-03-14", "7y", "2030-03-14"), r(3, 2, "", "forever", "forever")}},
		{name: "a keep_until a spreadsheet rewrote is refused with the format hint",
			in:       head + "1,2,/d,a,1,m,2023-03-14,7y,3/14/2030,\n",
			problems: []string{`keep_until "3/14/2030" is not a date: use YYYY-MM-DD, for example 2023-03-14 (a spreadsheet may have rewritten it; format the column as text)`}},
		{name: "empty spreadsheet rows are skipped",
			in:   head + "1,2,/d,a,1,m,,2027-01-01,,\n,,,,,,,,,\n\n",
			want: []store.RetentionRow{r(2, 1, "", "2027-01-01", "")}},
		{name: "semicolons, BOM, CRLF, reordered and extra columns, no keep_until or file_date",
			in: "\ufefftags;retention;notes;export;id\r\n;2027-02-03;mine;5;1\r\n;;;5;2\r\n",
			want: []store.RetentionRow{
				{Line: 2, ExportID: 5, FileID: 1, Retention: "2027-02-03"},
				{Line: 3, ExportID: 5, FileID: 2},
			}},
		{name: "tabs", in: "id\texport\tfile_date\tretention\ttags\n1\t2\t2023-01-01\t10y\t\n",
			want: []store.RetentionRow{{Line: 2, ExportID: 2, FileID: 1, FileDate: "2023-01-01", HasFileDate: true,
				Retention: "10y"}}},
		{name: "a quoted name with commas and a newline keeps the line numbers",
			in:   head + "1,2,/d,\"a,b\nc\",1,m,,,,\n2,2,/d,q,1,m,,2027-01-01,,\n",
			want: []store.RetentionRow{r(2, 1, "", "", ""), r(4, 2, "", "2027-01-01", "")}},
		{name: "bad rows are refused one by one",
			in: head +
				"x,2,/d,a,1,m,,7y,,\n" +
				"1,,/d,a,1,m,,7y,,\n" +
				"2,2,/d,a,1,m,,30/06/2027,,\n" +
				"3,2,/d,a,1,m,,2027-02-29,,\n" +
				"4,2,/d,a,1,m,,+1y,,\n" +
				"5,2,/d,a,1,m,14/03/2023,7y,,\n" +
				"6,2,/d,a,1,m,,,,,2027-01-01\n" +
				"7,2,/d,a,1,m,2023-01-01,7y,,\n",
			want: []store.RetentionRow{r(9, 7, "2023-01-01", "7y", "")},
			problems: []string{"not a file ID", "not an export number", `retention "30/06/2027" is not a retention`,
				`retention "2027-02-29" is not a retention`, `retention "+1y" is not a retention`,
				`file_date "14/03/2023" is not a date`, `"2027-01-01" is outside the columns`}},
		{name: "a file_date marked as read from the file name is passed on marked",
			in:   head + "1,2,/d,a,1,m,!2023-03-14,7y,,\n2,2,/d,b,1,m, ! 2023-03-14 ,,,\n",
			want: []store.RetentionRow{r(2, 1, "!2023-03-14", "7y", ""), r(3, 2, "!2023-03-14", "", "")}},
		{name: "a marked file_date that is not a date is refused",
			in: head + "1,2,/d,a,1,m,!14/03/2023,7y,,\n2,2,/d,b,1,m,!,7y,,\n",
			problems: []string{`file_date "!14/03/2023" is not a date: remove the ! and use YYYY-MM-DD`,
				`file_date "!" is not a date`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseCSV([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Rows) != len(tc.want) {
				t.Fatalf("rows = %+v, want %+v (problems %+v)", p.Rows, tc.want, p.Problems)
			}
			for i := range tc.want {
				if p.Rows[i].RetentionRow != tc.want[i] || len(p.Rows[i].Tags) != 0 {
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

// TestParseCSVLimits: a broken quote is an error, and a CSV over package sheet's limits is refused whole.
func TestParseCSVLimits(t *testing.T) {
	if _, err := ParseCSV([]byte("id,export,retention,tags\n1,2,\"2027-01-01,\n")); err == nil {
		t.Error("want an error for an unterminated quote")
	}
	data := "id,export,retention,tags\n" + strings.Repeat("\n", sheet.MaxRows)
	if _, err := ParseCSV([]byte(data)); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Errorf("ParseCSV with too many rows = %v, want refused", err)
	}
}

func TestParseCSVHeader(t *testing.T) {
	for _, tc := range []struct {
		name, csv string
		files     bool
	}{
		{"combined", "id,export,retention,tags\n", true},
		{"retention CSV", "id,export,file_date,retention,keep_until\n", false},
		{"tags CSV", "id,export,tags\n", false},
		{"an old review CSV", "id,export,reason,path,size,modified,review_date,action,retain_until\n", false},
		{"not a CSV of filo's", "a,b,c\n1,2,3\n", false},
		{"empty file", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCSV([]byte(tc.csv))
			if tc.files && err != nil {
				t.Errorf("ParseCSV error %v, want none", err)
			}
			if !tc.files && !errors.Is(err, ErrNotFilesCSV) {
				t.Errorf("ParseCSV error %v, want ErrNotFilesCSV", err)
			}
		})
	}
	if _, err := ParseCSV(make([]byte, sheet.MaxSize+1)); !errors.Is(err, sheet.ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
}

// TestLostColumn: a CSV with id, export and one of retention or tags names the other, with the exports its rows
// name (a row whose export cell cannot be read left out); anything else names none.
func TestLostColumn(t *testing.T) {
	for _, tc := range []struct {
		name, csv string
		missing   string
		exports   []int64
	}{
		{"tags lost", "id,export,file_date,retention\n1,3,,7y\n2,3,,\nx,4,,\n5,6,,\n7,8,,,past\n9,x,,\n", "tags",
			[]int64{3, 4, 6, 8}}, // a row with a bad id, or a cell past the columns, still names its export
		{"broken quote", "id,export,retention\n1,3,\n2,4,\"7y\n", "tags", []int64{3}},
		{"retention lost", "id;export;tags\n1;2;urgent\n", "retention", []int64{2}},
		{"no rows", "id,export,tags\n", "retention", nil},
		{"both there", "id,export,retention,tags\n1,2,,\n", "", nil},
		{"both lost", "id,export,name\n1,2,a\n", "", nil},
		{"a group column of the person's own", "id,export,group,retention\n1,2,1,\n", "tags", []int64{2}},
		{"no ids", "name,retention\na,7y\n", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			missing, exports := LostColumn([]byte(tc.csv))
			if missing != tc.missing || !slices.Equal(exports, tc.exports) {
				t.Errorf("LostColumn = %q, %v; want %q, %v", missing, exports, tc.missing, tc.exports)
			}
		})
	}
}

func TestNameCell(t *testing.T) {
	for _, tc := range []struct{ name, cell string }{
		{"bill.pdf", "bill.pdf"},
		{"=odd.txt", "'=odd.txt"},
		{"-draft", "'-draft"},
		{"'quoted", "''quoted"},
		{"'=both", "''=both"},
		{" spaced ", " spaced "},
		{"bad\xffname", "bad\ufffdname"},
		{"'caf\xe9.txt", "''caf\ufffd.txt"},
	} {
		if got := NameCell(tc.name); got != tc.cell {
			t.Errorf("NameCell(%q) = %q, want %q", tc.name, got, tc.cell)
		}
		if !strings.ContainsRune(tc.cell, '\ufffd') {
			if got := ReadName(NameCell(tc.name)); got != tc.name {
				t.Errorf("ReadName(NameCell(%q)) = %q, want it back as it was", tc.name, got)
			}
		}
	}
	// A cell typed afresh is read as typed, the guard alone dropped.
	for cell, name := range map[string]string{"'=new.txt": "=new.txt", "'abc": "'abc", "new ": "new ", "'": "'"} {
		if got := ReadName(cell); got != name {
			t.Errorf("ReadName(%q) = %q, want %q", cell, got, name)
		}
	}
}

func TestNameColumn(t *testing.T) {
	items := []store.FilesItem{{FileItem: store.FileItem{FileID: 1, RootPath: "/r", Path: "d/'=x.txt"}}}
	SetNameCells(items)
	if items[0].NameCell != "''=x.txt" {
		t.Fatalf("NameCell = %q", items[0].NameCell)
	}
	var b bytes.Buffer
	if err := WriteCSV(&b, 2, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), ",''=x.txt,") {
		t.Errorf("CSV %q, want the recorded name cell", b.String())
	}
	p, err := ParseCSV([]byte("id,export,directory,name,retention,tags\n1,2,/r/d ,\" Bill .pdf \",,\n3,2,/r,'=y,,\n"))
	if err != nil || len(p.Rows) != 2 {
		t.Fatalf("ParseCSV = %+v, %v", p, err)
	}
	if r := p.Rows[0]; !r.HasDirectory || r.DirectoryCell != "/r/d " {
		t.Errorf("row 1 = %+v, want the directory cell with its spaces", r)
	}
	if r := p.Rows[0]; !r.HasName || r.NameCell != " Bill .pdf " || r.Name != " Bill .pdf " {
		t.Errorf("row 1 = %+v, want the name with its spaces", r)
	}
	if r := p.Rows[1]; r.NameCell != "'=y" || r.Name != "=y" {
		t.Errorf("row 2 = %+v, want the guard dropped from the name", r)
	}
	if p, _ := ParseCSV([]byte("id,export,retention,tags\n1,2,,\n")); p.Rows[0].HasName || p.Rows[0].HasDirectory {
		t.Error("HasName or HasDirectory without the column")
	}
}
