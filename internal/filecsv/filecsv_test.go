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
	if !IsFilesCSV(b.Bytes()) {
		t.Error("IsFilesCSV = false for a combined CSV")
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

func TestParseCSVHeader(t *testing.T) {
	for _, tc := range []struct {
		name, csv string
		files     bool
	}{
		{"combined", "id,export,retention,tags\n", true},
		{"retention CSV", "id,export,file_date,retention,keep_until\n", false},
		{"tags CSV", "id,export,tags\n", false},
		{"duplicates CSV with both", "group,id,export,retention,tags\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFilesCSV([]byte(tc.csv)); got != tc.files {
				t.Errorf("IsFilesCSV = %v, want %v", got, tc.files)
			}
			if _, err := ParseCSV([]byte(tc.csv)); tc.name != "combined" && tc.name != "duplicates CSV with both" &&
				!errors.Is(err, ErrNotFilesCSV) {
				t.Errorf("ParseCSV error %v, want ErrNotFilesCSV", err)
			}
		})
	}
	if _, err := ParseCSV(make([]byte, sheet.MaxSize+1)); !errors.Is(err, sheet.ErrTooLarge) {
		t.Errorf("too large: %v", err)
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
