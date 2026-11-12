package report

import (
	"strings"
	"testing"

	"github.com/alansav/filo/internal/store"
)

// section returns the lines of the report from the one starting with heading up to the next blank line.
func section(t *testing.T, r Report, heading string) []string {
	t.Helper()
	var b strings.Builder
	if err := r.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, heading) {
			end := i + 1
			for end < len(lines) && lines[end] != "" {
				end++
			}
			return lines[i:end]
		}
	}
	t.Fatalf("no %q section in:\n%s", heading, b.String())
	return nil
}

func TestWriteTextUnreadable(t *testing.T) {
	tests := []struct {
		name  string
		rows  []store.UnreadableRow
		total int64
		want  []string
	}{
		{"none", nil, 0, []string{"Unreadable files: none"}},
		{"all shown", []store.UnreadableRow{{Path: "/r/a", Since: 2}, {Path: "/r/b", Since: 5}}, 2, []string{
			"Unreadable files: 2",
			"  since scan 2  /r/a",
			"  since scan 5  /r/b",
		}},
		{"more than the top", []store.UnreadableRow{{Path: "/r/a", Since: 2}}, 1235, []string{
			"Unreadable files: 1,235",
			"  since scan 2  /r/a",
			"  and 1,234 more",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := section(t, Report{Unreadable: tc.rows, UnreadTotal: tc.total}, "Unreadable files")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestWriteTextLifespan(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want []string
	}{
		{"nothing to decide", Report{Status: map[store.Status]int64{store.StatusKept: 3, store.StatusForever: 1}},
			[]string{
				"Needing a retention: none",
				"Kept until a later date: 3 files",
				"Kept forever: 1 file",
			}},
		{"undecided and expired", Report{
			Status:      map[store.Status]int64{store.StatusUndated: 1200, store.StatusChanged: 2, store.StatusExpired: 1},
			Undecided:   1202,
			UndecidedIn: []DirGroup{{Dir: "/r/docs", Files: 1202, Bytes: 2048}},
		}, []string{
			"Needing a retention: 1,202 files (no retention 1,200, past retention, changed since it was set 2)",
			"  directories with the most (to depth 2):",
			"     1,202     2.0 KiB  /r/docs",
			"  set retentions with: filo export retention [DIR] -o FILE, then filo update FILE",
			"Past their retention: 1 file; see filo delete",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := section(t, tc.r, "Needing a retention")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestDirGroups(t *testing.T) {
	items := []store.FileItem{
		{RootPath: "/r", Path: "a.txt", Kind: "file", Size: 1},
		{RootPath: "/r", Path: "docs/x/y/z.txt", Kind: "file", Size: 10},
		{RootPath: "/r", Path: "docs/x/w.txt", Kind: "file", Size: 10},
		{RootPath: "/r", Path: "docs/x/link", Kind: "symlink", Size: 99},
		{RootPath: "/s", Path: "b", Kind: "file", Size: 5},
	}
	got := DirGroups(items, 2, 2)
	want := []DirGroup{{Dir: "/r/docs/x", Files: 3, Bytes: 20}, {Dir: "/r", Files: 1, Bytes: 1}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
