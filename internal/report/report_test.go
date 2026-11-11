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
