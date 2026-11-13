package duplicates

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

func TestParseDecision(t *testing.T) {
	tests := []struct {
		in      string
		want    byte
		wantErr bool
	}{
		{"", store.DecideNone, false},
		{"  ", store.DecideNone, false},
		{"Y", store.DecideDelete, false},
		{"y", store.DecideDelete, false},
		{" Yes ", store.DecideDelete, false},
		{"N", store.DecideKeep, false},
		{"no", store.DecideKeep, false},
		{"x", 0, true},
		{"delete", 0, true},
		{"keep", 0, true},
		{"1", 0, true},
		{"TRUE", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseDecision(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseDecision(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestWriteAndReadCSV(t *testing.T) {
	groups := []store.DupContent{{Hash: "h1", Size: 3, Copies: []store.DupCopy{
		{Candidate: store.Candidate{FileItem: store.FileItem{FileID: 4, RootPath: "/r", Path: "a/=x.txt", Size: 3}}},
		{Candidate: store.Candidate{FileItem: store.FileItem{FileID: 9, RootPath: "/r", Path: "b.txt", Size: 3,
			Retention: "7y", FileDate: "2020-01-01", KeepUntil: "2027-01-01"}}},
	}}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, 12, groups, "N", time.UTC); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"\ufeffid,export,group,copies,directory,name,size,modified,file_date,retention," +
		"keep_until,delete\n", "4,12,1,2,/r/a,'=x.txt,3,", "9,12,1,2,/r,b.txt,3,", "2020-01-01,7y,2027-01-01,N\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV lacks %q:\n%s", want, out)
		}
	}
	if !IsDuplicatesCSV(buf.Bytes()) {
		t.Error("IsDuplicatesCSV = false for a duplicates CSV")
	}
	p, err := ReadCSV(&buf)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.DupRow{{Line: 2, ExportID: 12, FileID: 4, Group: 1, Decision: 'N'},
		{Line: 3, ExportID: 12, FileID: 9, Group: 1, Decision: 'N'}}
	if len(p.Problems) != 0 || len(p.Rows) != 2 || p.Rows[0] != want[0] || p.Rows[1] != want[1] {
		t.Errorf("ReadCSV = %+v, want %+v", p, want)
	}
}

// TestReadCSVSpreadsheet: what a spreadsheet does to the file is accepted; what cannot be read is refused row by row.
func TestReadCSVSpreadsheet(t *testing.T) {
	in := "\ufeffdelete;group;export;id;mine\r\n" +
		"y;1;3;10;\r\n" +
		";;;;\r\n" +
		"n;1;3;11;note\r\n" +
		"maybe;2;3;12;\r\n" +
		"Y;2;3;13;;extra\r\n" +
		"Y;x;3;14;\r\n" +
		"N;3;3;15;\r\n" +
		"Y;3;3;15;\r\n"
	p, err := ReadCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]byte{}
	for _, r := range p.Rows {
		got[r.FileID] = r.Decision
	}
	if len(got) != 2 || got[10] != 'Y' || got[11] != 'N' {
		t.Errorf("rows = %+v, want files 10 (Y) and 11 (N)", p.Rows)
	}
	problems := map[int]string{}
	for _, pr := range p.Problems {
		problems[pr.Line] += pr.Msg
	}
	for line, want := range map[int]string{5: `delete "maybe"`, 6: "outside the columns", 7: "not a group number",
		8: "more than once", 9: "more than once"} {
		if !strings.Contains(problems[line], want) {
			t.Errorf("line %d: problem %q, want %q", line, problems[line], want)
		}
	}
}

func TestReadCSVHeaders(t *testing.T) {
	tests := []struct {
		name, head string
		want       error
	}{
		{"retention CSV", "id,export,directory,name,file_date,retention,keep_until", ErrNotDuplicatesCSV},
		{"delete column removed", "id,export,group,copies,name", ErrNoDelete},
		{"not a CSV of filo's", "a,b,c", ErrNotDuplicatesCSV},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReadCSV(strings.NewReader(tt.head + "\n")); !errors.Is(err, tt.want) {
				t.Errorf("ReadCSV: %v, want %v", err, tt.want)
			}
		})
	}
	if IsDuplicatesCSV([]byte("id,export,retention\n1,1,7y\n")) {
		t.Error("IsDuplicatesCSV = true for a retention CSV")
	}
}
