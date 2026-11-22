package tagcsv

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

func TestWriteAndParse(t *testing.T) {
	mtime := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC).UnixNano()
	items := []store.TagItem{
		{FoundFile: store.FoundFile{FileID: 7, Root: "/home/u/Documents", Rel: "received/bill.pdf", Size: 12,
			MtimeNS: mtime}, Tags: []tags.Tag{{Key: "sender", Value: "British Gas"}, {Key: "type", Value: "bill"}}},
		{FoundFile: store.FoundFile{FileID: 8, Root: "/home/u/Documents", Rel: "=odd.txt", MtimeNS: mtime},
			Tags: []tags.Tag{{Value: "-draft"}}},
		{FoundFile: store.FoundFile{FileID: 9, Root: "/home/u/Documents", Rel: "top.txt", MtimeNS: mtime}},
	}
	var b bytes.Buffer
	if err := WriteCSV(&b, 3, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	want := "\ufeffid,export,directory,name,size,modified,tags\n" +
		"7,3,/home/u/Documents/received,bill.pdf,12,2026-10-03 09:30,sender:British Gas; type:bill\n" +
		"8,3,/home/u/Documents,'=odd.txt,0,2026-10-03 09:30,'-draft\n" +
		"9,3,/home/u/Documents,top.txt,0,2026-10-03 09:30,\n"
	if b.String() != want {
		t.Errorf("CSV:\n%s\nwant:\n%s", b.String(), want)
	}
	if !IsTagsCSV(b.Bytes()) {
		t.Error("IsTagsCSV = false for a tags CSV")
	}
	p, err := ParseCSV(b.Bytes())
	if err != nil || len(p.Problems) != 0 || len(p.Rows) != 3 {
		t.Fatalf("ParseCSV: %+v, %v", p, err)
	}
	if got := store.JoinTags(p.Rows[0].Tags); got != "sender:British Gas; type:bill" {
		t.Errorf("row 1 tags = %q", got)
	}
	if got := store.JoinTags(p.Rows[1].Tags); got != "-draft" { // the formula guard dropped
		t.Errorf("row 2 tags = %q", got)
	}
	if len(p.Rows[2].Tags) != 0 || p.Rows[2].FileID != 9 || p.Rows[2].ExportID != 3 || p.Rows[2].Line != 4 {
		t.Errorf("row 3 = %+v", p.Rows[2])
	}
}

func TestParseCSVRows(t *testing.T) {
	csv := "id,export,tags,notes\n" + // a column of the person's own
		"1,2,\"Type:Bill ;  urgent;; type:bill \",mine\n" +
		"2,2,sender:,\n" +
		"x,2,a,\n" +
		"3,2,a*b,\n" +
		"4,2,a,b,c\n" +
		"6,2,\"'=x; y\",\n" +
		"5,2,,\n" +
		"5,2,b,\n"
	p, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, r := range p.Rows {
		rows = append(rows, strconv.FormatInt(r.FileID, 10)+" "+store.JoinTags(r.Tags))
	}
	if want := "1 type:Bill; urgent|6 =x; y"; strings.Join(rows, "|") != want {
		t.Errorf("rows = %q, want %q", strings.Join(rows, "|"), want)
	}
	want := []string{
		`tags "sender:": give a value`,
		`id "x" is not a file ID`,
		`tags "a*b": a tag cannot hold "*"`,
		`"c" is outside the columns; put it under tags`,
		"file 5 of export 2 is listed more than once",
		"file 5 of export 2 is listed more than once",
	}
	if len(p.Problems) != len(want) {
		t.Fatalf("problems = %+v", p.Problems)
	}
	for i := range want {
		if !strings.HasPrefix(p.Problems[i].Msg, want[i]) {
			t.Errorf("problem %d = %q, want %q", i, p.Problems[i].Msg, want[i])
		}
	}
}

func TestKinds(t *testing.T) {
	for _, tc := range []struct {
		header     string
		tags, lost bool
	}{
		{"id,export,directory,name,size,modified,tags", true, false},
		{"id,export,directory,name,size,modified", false, true},
		{"id,export,directory,name,size,modified,file_date,retention,keep_until,tags", false, false},
		{"id,export,group,directory,name,action,tags", false, false},
		{"a,b,c", false, false},
		{"id,export,directory,name,size,modified,keep_until", false, false}, // a retention CSV of an older filo
	} {
		data := []byte(tc.header + "\n")
		if IsTagsCSV(data) != tc.tags || LostTags(data) != tc.lost {
			t.Errorf("%s: IsTagsCSV %v, LostTags %v", tc.header, IsTagsCSV(data), LostTags(data))
		}
	}
	if _, err := ParseCSV([]byte("id,export,directory,name,size,modified\n")); !errors.Is(err, ErrNoTags) {
		t.Errorf("lost tags column: %v", err)
	}
	if _, err := ParseCSV([]byte("a,b,c\n")); !errors.Is(err, ErrNotTagsCSV) {
		t.Errorf("not a tags CSV: %v", err)
	}
}
