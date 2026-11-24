package duplicates

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
)

func TestParseAction(t *testing.T) {
	tests := []struct {
		in      string
		want    Action
		wantErr bool
	}{
		{"", None, false},
		{"  ", None, false},
		{"delete", Delete, false},
		{" Delete ", Delete, false},
		{"D", Delete, false},
		{"keep", Keep, false},
		{"k", Keep, false},
		{"ALWAYS", Always, false},
		{"a", Always, false},
		{"del", None, true},
		{"delete this", None, true},
		{"Y", None, true}, // the first design's word
		{"N", None, true},
		{"x", None, true},
	}
	for _, tc := range tests {
		got, err := ParseAction(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseAction(%q) = %v, %v; want %v, error %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// csvOf builds a duplicates CSV with the given rows (export, group, id, action), in the order given.
func csvOf(rows ...[4]string) []byte {
	var b strings.Builder
	b.WriteString(sheet.BOM + strings.Join(Header, ",") + "\r\n")
	for _, r := range rows {
		// id, export, group, copies, directory, name, size, modified, file_date, retention, keep_until, action
		b.WriteString(r[2] + "," + r[0] + "," + r[1] + ",2,/r,x,1,2026-11-15 10:00,,,," + r[3] + "\r\n")
	}
	return []byte(b.String())
}

func TestReadCSVRefusesOtherCSVs(t *testing.T) {
	tests := []struct {
		name string
		data string
		want error
	}{
		{"the first design's CSV, with a delete column",
			sheet.BOM + "id,export,group,copies,directory,name,size,modified,file_date,retention,keep_until,delete\r\n" +
				"1,1,1,2,/r,x,1,2026-11-15 10:00,,,,Y\r\n", ErrOlderCSV},
		{"a retention CSV",
			sheet.BOM + "id,export,directory,name,size,modified,file_date,retention,keep_until\r\n", ErrRetentionCSV},
		{"no action column", sheet.BOM + "id,export,group,copies\r\n", ErrNoAction},
		{"not a filo CSV", "a,b,c\r\n1,2,3\r\n", ErrNotDuplicatesCSV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadCSV(bytes.NewReader([]byte(tc.data))); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// exported is export #7 as the register recorded it: group 1 holds files 1, 2 and 3; group 2 holds files 4 and 5;
// group 3 holds files 6 and 7. Export 8 holds file 9 in group 1.
var exported = map[int64]map[int64]int64{
	7: {1: 1, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3},
	8: {9: 1},
	9: {1: 1, 2: 1, 3: 1}, // overlaps export 7's group 1 (B3)
}

// plan reads the CSV and plans it against exported, returning each group's outcome as "group: what" and the
// problems not tied to a group as "line: message".
func plan(t *testing.T, data []byte) (map[GroupKey]GroupPlan, []string) {
	t.Helper()
	p, err := ReadCSV(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	plans, problems := Plan(p, exported)
	out := map[GroupKey]GroupPlan{}
	for _, g := range plans {
		out[g.Key] = g
	}
	var msgs []string
	for _, pr := range problems {
		msgs = append(msgs, pr.Msg)
	}
	return out, msgs
}

// TestPlanGroupRules: each row of the group rules table (docs/design.md, Duplicates), for group 1 of export 7.
func TestPlanGroupRules(t *testing.T) {
	g1 := func(a1, a2, a3 string) []byte {
		return csvOf([4]string{"7", "1", "1", a1}, [4]string{"7", "1", "2", a2}, [4]string{"7", "1", "3", a3})
	}
	tests := []struct {
		name       string
		data       []byte
		status     Status
		reason     string // a part of it
		keep, del  []int64
		alwaysWant bool
	}{
		{"all blank: not decided", g1("", "", ""), NotDecided, "", nil, nil, false},
		{"some blank, some filled: refused", g1("keep", "delete", ""), Refused, "every copy needs a value", nil, nil,
			false},
		{"one keep, the rest delete", g1("keep", "delete", "d"), Decided, "", []int64{1}, []int64{2, 3}, false},
		{"two keep, one delete: records and a deletion", g1("keep", "k", "delete"), Decided, "", []int64{1, 2},
			[]int64{3}, false},
		{"all keep: records only", g1("keep", "keep", "keep"), Decided, "", []int64{1, 2, 3}, nil, false},
		{"every row always", g1("always", "a", "ALWAYS"), Decided, "", nil, nil, true},
		{"always with anything else: refused", g1("always", "always", "keep"), Refused,
			"always is about the content, so it goes on every row", nil, nil, false},
		{"every row delete: refused", g1("delete", "delete", "delete"), Refused,
			"no copy would be left; to remove this content entirely, give it a past retention", nil, nil, false},
		{"C3: an unknown value refuses the group", g1("keep", "del", "delete"), Refused,
			`"del" is not an action`, nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plans, problems := plan(t, tc.data)
			g := plans[GroupKey{7, 1}]
			if g.Status != tc.status || !strings.Contains(g.Reason, tc.reason) || !slices.Equal(g.Keep, tc.keep) ||
				!slices.Equal(g.Delete, tc.del) || g.Always != tc.alwaysWant {
				t.Errorf("got %+v, want status %v, reason with %q, keep %v, delete %v, always %v", g, tc.status,
					tc.reason, tc.keep, tc.del, tc.alwaysWant)
			}
			if len(problems) > 0 {
				t.Errorf("problems %q, want none", problems)
			}
		})
	}
}

// TestPlanStructure: scenarios C1, C2 and C5. A row not as exported refuses the groups it touches; the others are
// still planned. Sorting is fine. A row deleted from the CSV is noted for the check against the disk.
func TestPlanStructure(t *testing.T) {
	decided2 := [][4]string{{"7", "2", "4", "keep"}, {"7", "2", "5", "delete"}}
	tests := []struct {
		name    string
		rows    [][4]string
		refused []GroupKey // groups refused for their structure
		reason  string     // a part of each refused group's reason
	}{
		{"C2: a row's group edited refuses both groups it touches",
			append([][4]string{{"7", "1", "1", "keep"}, {"7", "1", "2", "delete"}, {"7", "3", "3", "delete"},
				{"7", "3", "6", "keep"}, {"7", "3", "7", "delete"}}, decided2...),
			[]GroupKey{{7, 1}, {7, 3}}, "in group 1 of export #7, not group 3"},
		{"C2: a row from another export pasted in",
			append([][4]string{{"7", "1", "1", "keep"}, {"7", "1", "2", "delete"}, {"7", "1", "3", "delete"},
				{"7", "1", "9", "delete"}}, decided2...),
			[]GroupKey{{7, 1}}, "is not in export #7"},
		{"C2: a row duplicated",
			append([][4]string{{"7", "1", "1", "keep"}, {"7", "1", "2", "delete"}, {"7", "1", "3", "delete"},
				{"7", "1", "3", "delete"}}, decided2...),
			[]GroupKey{{7, 1}}, "listed more than once"},
		{"C2: a row duplicated into another group, the original first",
			append([][4]string{{"7", "1", "1", "keep"}, {"7", "1", "2", "delete"}, {"7", "1", "3", "delete"},
				{"7", "3", "6", "keep"}, {"7", "3", "7", "delete"}, {"7", "3", "3", "keep"}}, decided2...),
			[]GroupKey{{7, 1}, {7, 3}}, "export again"},
		{"C2: a row duplicated into another group, the copy first",
			append([][4]string{{"7", "3", "3", "keep"}, {"7", "1", "1", "keep"}, {"7", "1", "2", "delete"},
				{"7", "1", "3", "delete"}, {"7", "3", "6", "keep"}, {"7", "3", "7", "delete"}}, decided2...),
			[]GroupKey{{7, 1}, {7, 3}}, "export again"},
		{"C2: an export this register does not have",
			append([][4]string{{"12", "1", "1", "keep"}, {"12", "1", "2", "delete"}}, decided2...),
			[]GroupKey{{12, 1}}, "export #12 is not a duplicates export of this register"},
		{"C5: sorted rows are fine",
			[][4]string{{"7", "1", "3", "delete"}, {"7", "2", "5", "delete"}, {"7", "1", "1", "keep"},
				{"7", "2", "4", "keep"}, {"7", "1", "2", "delete"}},
			nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plans, problems := plan(t, csvOf(tc.rows...))
			for _, k := range tc.refused {
				if g := plans[k]; g.Status != Refused || !strings.Contains(g.Reason, tc.reason) {
					t.Errorf("group %v: %+v, want refused with %q", k, g, tc.reason)
				}
			}
			if g := plans[GroupKey{7, 2}]; g.Status != Decided || !slices.Equal(g.Delete, []int64{5}) {
				t.Errorf("group 2, untouched, = %+v; want decided, deleting 5", g)
			}
			if len(problems) > 0 {
				t.Errorf("problems %q, want none", problems)
			}
		})
	}

	t.Run("B3: a file with different actions in two exports refuses both groups", func(t *testing.T) {
		plans, _ := plan(t, csvOf([4]string{"7", "1", "1", "keep"}, [4]string{"7", "1", "2", "delete"},
			[4]string{"7", "1", "3", "delete"}, [4]string{"9", "1", "1", "delete"}, [4]string{"9", "1", "2", "keep"},
			[4]string{"9", "1", "3", "delete"}))
		for _, k := range []GroupKey{{7, 1}, {9, 1}} {
			if g := plans[k]; g.Status != Refused || !strings.Contains(g.Reason, "give it one action") {
				t.Errorf("group %v = %+v, want refused for its conflicting actions", k, g)
			}
		}
	})
	t.Run("B3: the same actions in two exports, or one export left blank, are fine", func(t *testing.T) {
		for _, tc := range []struct {
			actions [3]string // export 9's rows for files 1, 2, 3
			want    Status    // export 9's group
		}{
			{[3]string{"keep", "delete", "delete"}, Decided},
			{[3]string{"", "", ""}, NotDecided},
		} {
			plans, _ := plan(t, csvOf([4]string{"7", "1", "1", "keep"}, [4]string{"7", "1", "2", "delete"},
				[4]string{"7", "1", "3", "delete"}, [4]string{"9", "1", "1", tc.actions[0]},
				[4]string{"9", "1", "2", tc.actions[1]}, [4]string{"9", "1", "3", tc.actions[2]}))
			if g := plans[GroupKey{7, 1}]; g.Status != Decided {
				t.Errorf("export #9 %q: group 7/1 = %+v, want decided", tc.actions, g)
			}
			if g := plans[GroupKey{9, 1}]; g.Status != tc.want {
				t.Errorf("export #9 %q: group 9/1 = %+v, want status %d", tc.actions, g, tc.want)
			}
		}
	})
	t.Run("a partial CSV: groups deleted from it are not planned, a blank one is left alone", func(t *testing.T) {
		plans, problems := plan(t, csvOf([4]string{"7", "2", "4", "keep"}, [4]string{"7", "2", "5", "delete"},
			[4]string{"7", "3", "6", ""}, [4]string{"7", "3", "7", ""}))
		if len(plans) != 2 || plans[GroupKey{7, 2}].Status != Decided || plans[GroupKey{7, 3}].Status != NotDecided {
			t.Errorf("plans = %+v, want group 2 decided, group 3 not decided and group 1 (not in the CSV) absent", plans)
		}
		if len(problems) > 0 {
			t.Errorf("problems %q, want none", problems)
		}
	})
	t.Run("C1: a row deleted from the CSV is noted, for the check against the disk", func(t *testing.T) {
		plans, _ := plan(t, csvOf([4]string{"7", "1", "1", "keep"}, [4]string{"7", "1", "2", "delete"}))
		if g := plans[GroupKey{7, 1}]; g.Status != Decided || !slices.Equal(g.Missing, []int64{3}) {
			t.Errorf("group 1 = %+v, want decided with file 3 missing from the CSV", g)
		}
	})
	t.Run("a row that cannot be read is a problem of its line, and refuses its group", func(t *testing.T) {
		plans, problems := plan(t, csvOf([4]string{"7", "1", "x", "keep"}, [4]string{"7", "1", "2", "delete"},
			[4]string{"7", "1", "3", "delete"}, [4]string{"7", "zz", "4", "keep"}))
		if g := plans[GroupKey{7, 1}]; g.Status != Refused || !strings.Contains(g.Reason, `id "x"`) {
			t.Errorf("group 1 = %+v, want refused for its unreadable row", g)
		}
		if len(problems) != 1 || !strings.Contains(problems[0], `group "zz"`) {
			t.Errorf("problems = %q, want the row whose group cannot be read", problems)
		}
	})
}

// TestWriteCSV: one row per copy, groups numbered in order, the action cell blank except keep on a copy that is one
// of a set of records (F3: a new copy of records is exported with them as keep, itself blank).
func TestWriteCSV(t *testing.T) {
	cp := func(id int64, rel string, set int64) store.DupCopy {
		c := store.DupCopy{RecordSet: set}
		c.FileID, c.RootPath, c.Path, c.Size = id, "/r", rel, 4
		return c
	}
	groups := []store.DupContent{
		{Hash: "h1", Copies: []store.DupCopy{cp(1, "a/x", 0), cp(2, "b/x", 0)}},
		{Hash: "h2", Copies: []store.DupCopy{cp(3, "2020/terms.pdf", 5), cp(4, "2021/terms.pdf", 5),
			cp(5, "new/terms.pdf", 0)}},
	}
	var b bytes.Buffer
	if err := WriteCSV(&b, 9, groups, time.UTC); err != nil {
		t.Fatal(err)
	}
	p, err := ReadCSV(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range p.Rows {
		got = append(got, strings.Join([]string{itoa(r.Key.Export), itoa(r.Key.Group), itoa(r.FileID), r.Action.String()}, " "))
	}
	want := []string{"9 1 1 ", "9 1 2 ", "9 2 3 keep", "9 2 4 keep", "9 2 5 "}
	if !slices.Equal(got, want) {
		t.Errorf("rows read back = %q, want %q", got, want)
	}
	if !strings.HasPrefix(b.String(), sheet.BOM+strings.Join(Header, ",")) || !strings.HasSuffix(Header[len(Header)-1], "action") {
		t.Errorf("header: %q", strings.SplitN(b.String(), "\n", 2)[0])
	}
	// As exported, the group with records is partly filled: refused until the new copy has a value.
	plans, _ := Plan(p, map[int64]map[int64]int64{9: {1: 1, 2: 1, 3: 2, 4: 2, 5: 2}})
	for _, g := range plans {
		if g.Key == (GroupKey{9, 2}) && (g.Status != Refused || !strings.Contains(g.Reason, "every copy needs a value")) {
			t.Errorf("group 2 as exported = %+v, want refused until the new copy is decided", g)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestReadCSVLimits: ReadCSV reads through package sheet's limits, so a CSV over them is refused whole.
func TestReadCSVLimits(t *testing.T) {
	data := "id,export,group,action\n" + strings.Repeat("\n", sheet.MaxRows)
	if _, err := ReadCSV(strings.NewReader(data)); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Errorf("ReadCSV with too many rows = %v, want refused", err)
	}
}
