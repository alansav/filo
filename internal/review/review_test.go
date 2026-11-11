package review

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
		{"+30d", "2026-12-11", false},
		{"+2w", "2026-11-25", false},
		{"+12m", "2027-11-11", false},
		{"+7y", "2033-11-11", false},
		{"+1M", "2026-12-11", false},
		{"06/07/2027", "", true},
		{"30/06/2027", "", true},
		{"2027-02-30", "", true},
		{"2027-6-3", "", true},
		{"2027-06-30 00:00:00", "", true},
		{"+", "", true},
		{"+d", "", true},
		{"+0d", "", true},
		{"+-3d", "", true},
		{"+3x", "", true},
		{"+5000y", "", true},
		{"tomorrow", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDate(tc.in, today)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("ParseDate(%q) = %q, %v; want %q, error %v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestAddMonthsClampsToMonthEnd(t *testing.T) {
	tests := []struct {
		from string
		n    int
		want string
	}{
		{"2026-01-31", 1, "2026-02-28"},
		{"2028-01-31", 1, "2028-02-29"},
		{"2026-03-31", 1, "2026-04-30"},
		{"2028-02-29", 12, "2029-02-28"},
		{"2026-11-15", 2, "2027-01-15"},
	}
	for _, tc := range tests {
		f, _ := time.Parse(dateLayout, tc.from)
		y, m, d := f.Date()
		if got := addMonths(y, m, d, tc.n).Format(dateLayout); got != tc.want {
			t.Errorf("%s + %d months = %s, want %s", tc.from, tc.n, got, tc.want)
		}
	}
}

func TestCell(t *testing.T) {
	tests := map[string]string{
		"/home/a/doc.txt":  "/home/a/doc.txt",
		"=HYPERLINK(1)":    "'=HYPERLINK(1)",
		"+1":               "'+1",
		"-x":               "'-x",
		"@SUM":             "'@SUM",
		"bad\xffname":      "bad\ufffdname",
		"":                 "",
		"possible move of": "possible move of",
		"/home/a/📷 Pictures/🏖️ beach.jpg": "/home/a/📷 Pictures/🏖️ beach.jpg",
		"/home/a/👨‍👩‍👧 family.md":         "/home/a/👨‍👩‍👧 family.md",
		"🎉=party":                         "🎉=party",
	}
	for in, want := range tests {
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteThenRead(t *testing.T) {
	items := []store.QueueItem{
		{FileID: 7, RootPath: "/r", Path: "docs/a.txt", Size: 10, MtimeNS: today.UnixNano(), Reason: store.ReasonUndated},
		{FileID: 8, RootPath: "/r", Path: "=evil", Size: 1, MtimeNS: 1, Reason: store.ReasonDue, ReviewDate: "2026-01-01",
			RetainUntil: "2030-01-01"},
		{FileID: 9, RootPath: "/r", Path: "x,\"quoted\"\nname", Size: 1, MtimeNS: 1, Reason: store.ReasonPossibleMove,
			MoveFrom: "/r/old/x", MoveDetail: "same name"},
		{FileID: 10, RootPath: "/r", Path: "🗂️ archive/👨‍👩‍👧 family.md", Size: 1, MtimeNS: 1,
			Reason: store.ReasonPossibleMove, MoveFrom: "/r/notes/👨‍👩‍👧 family.md", MoveDetail: "same name"},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, 3, items, time.UTC); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\ufeffid,export,reason,path,") {
		t.Errorf("no BOM and header: %q", out[:40])
	}
	for _, want := range []string{"7,3,no review date,/r/docs/a.txt,10,2026-11-11 15:00,,,", ",/r/=evil,",
		"review due (2026-01-01)", ",2030-01-01\n", "possible move of /r/old/x (same name)",
		"possible move of /r/notes/👨‍👩‍👧 family.md (same name),/r/🗂️ archive/👨‍👩‍👧 family.md,"} {
		if !strings.Contains(out, want) {
			t.Errorf("CSV lacks %q:\n%s", want, out)
		}
	}
	// Read back untouched: every row is blank except 8, whose prefilled retain_until goes to the register to compare.
	p, err := ReadCSV(strings.NewReader(out), today)
	if err != nil {
		t.Fatal(err)
	}
	if p.Blank != 3 || len(p.Problems) != 0 || len(p.Decisions) != 1 ||
		p.Decisions[0] != (store.Decision{Line: 3, ExportID: 3, FileID: 8, RetainUntil: "2030-01-01"}) {
		t.Errorf("untouched read = %+v", p)
	}
}

func TestReadCSV(t *testing.T) {
	const head = "id,export,reason,path,size,modified,review_date,action,retain_until\n"
	tests := []struct {
		name     string
		in       string
		want     []store.Decision
		blank    int
		problems []string // substrings, one per problem, in order
		err      error
	}{
		{name: "date, interval, delete, retain",
			in: head + "1,2,r,/p,1,m,2027-01-01,,\n2,2,r,/q,1,m,+1y,,\n3,2,r,/s,1,m,,delete,\n4,2,r,/t,1,m,+6m,,+7y\n",
			want: []store.Decision{
				{Line: 2, ExportID: 2, FileID: 1, ReviewDate: "2027-01-01"},
				{Line: 3, ExportID: 2, FileID: 2, ReviewDate: "2027-11-11"},
				{Line: 4, ExportID: 2, FileID: 3, Action: "delete"},
				{Line: 5, ExportID: 2, FileID: 4, ReviewDate: "2027-05-11", RetainUntil: "2033-11-11"},
			}},
		{name: "blank rows and empty spreadsheet rows",
			in:    head + "1,2,r,/p,1,m,,,\n,,,,,,,,\n\n2,2,r,/q,1,m,,,\n",
			blank: 2},
		{name: "semicolons, BOM, CRLF, reordered and extra columns, DELETE in capitals",
			in: "\ufeffaction;notes;review_date;export;id\r\nDELETE;mine;;5;1\r\n;;2027-02-03;5;2\r\n",
			want: []store.Decision{
				{Line: 2, ExportID: 5, FileID: 1, Action: "delete"},
				{Line: 3, ExportID: 5, FileID: 2, ReviewDate: "2027-02-03"},
			}},
		{name: "emoji in the display columns do not disturb the decisions",
			in:   head + "1,2,possible move of /r/🎉 old.txt,/r/🎊 new 👨‍👩‍👧.txt,1,m,+30d,,\n",
			want: []store.Decision{{Line: 2, ExportID: 2, FileID: 1, ReviewDate: "2026-12-11"}}},
		{name: "tabs", in: "id\texport\treview_date\taction\n1\t2\t2027-01-01\t\n",
			want: []store.Decision{{Line: 2, ExportID: 2, FileID: 1, ReviewDate: "2027-01-01"}}},
		{name: "a quoted path with commas and a newline keeps the line numbers",
			in:   head + "1,2,r,\"/a,b\nc\",1,m,,,\n2,2,r,/q,1,m,2027-01-01,,\n",
			want: []store.Decision{{Line: 4, ExportID: 2, FileID: 2, ReviewDate: "2027-01-01"}}, blank: 1},
		{name: "bad rows are refused one by one",
			in: head +
				"x,2,r,/p,1,m,2027-01-01,,\n" +
				"1,,r,/p,1,m,2027-01-01,,\n" +
				"2,2,r,/p,1,m,30/06/2027,,\n" +
				"3,2,r,/p,1,m,,keep,\n" +
				"4,2,r,/p,1,m,2027-01-01,delete,\n" +
				"5,2,r,/p,1,m,2026-11-11,,\n" +
				"6,2,r,/p,1,m,2027-01-01,,soon\n" +
				"7,2,r,/p,1,m,2027-01-01,,\n",
			want: []store.Decision{{Line: 9, ExportID: 2, FileID: 7, ReviewDate: "2027-01-01"}},
			problems: []string{"not a file ID", "not an export number", "not a date", "the only action is delete",
				"not both", "not after today", "retain_until"}},
		{name: "a file listed twice is refused both times",
			in:       head + "1,2,r,/p,1,m,2027-01-01,,\n1,2,r,/p,1,m,2028-01-01,,\n3,2,r,/q,1,m,,delete,\n",
			want:     []store.Decision{{Line: 4, ExportID: 2, FileID: 3, Action: "delete"}},
			problems: []string{"more than once", "more than once"}},
		{name: "not a review CSV", in: "a,b,c\n1,2,3\n", err: ErrNotReviewCSV},
		{name: "empty file", in: "", err: ErrNotReviewCSV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ReadCSV(strings.NewReader(tc.in), today)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Decisions) != len(tc.want) {
				t.Fatalf("decisions = %+v, want %+v (problems %+v)", p.Decisions, tc.want, p.Problems)
			}
			for i := range tc.want {
				if p.Decisions[i] != tc.want[i] {
					t.Errorf("decision %d = %+v, want %+v", i, p.Decisions[i], tc.want[i])
				}
			}
			if p.Blank != tc.blank {
				t.Errorf("blank = %d, want %d", p.Blank, tc.blank)
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
	_, err := ReadCSV(strings.NewReader("id,export,review_date,action\n1,2,\"2027-01-01,\n"), today)
	if err == nil {
		t.Fatal("want an error for an unterminated quote")
	}
}
