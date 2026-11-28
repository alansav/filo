package report

import (
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// section returns the lines of the plain report from the one starting with heading up to the next blank line.
func section(t *testing.T, r Report, heading string) []string {
	t.Helper()
	var b strings.Builder
	r.Render(view.New(view.Options{Out: &b}))
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

func TestRenderUnreadable(t *testing.T) {
	tests := []struct {
		name  string
		rows  []store.UnreadableRow
		total int64
		want  []string
	}{
		{"none", nil, 0, []string{"Unreadable files   none"}},
		{"all shown", []store.UnreadableRow{{Root: "/r", Rel: "a", Since: 2},
			{Root: "/r", Rel: "b", Since: 5}}, 2, []string{
			"Unreadable files   2",
			"    since",
			"    scan #2   /r/a",
			"    scan #5   /r/b",
		}},
		{"more than the top", []store.UnreadableRow{{Root: "/r", Rel: "a", Since: 2}}, 1235, []string{
			"Unreadable files   1,235",
			"    since",
			"    scan #2   /r/a",
			"    ... 1,234 more files",
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

func TestRenderRetention(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want []string
	}{
		{"nothing to decide", Report{Status: map[store.Status]int64{store.StatusKept: 3, store.StatusForever: 1}},
			[]string{
				"Retention   4 files and symlinks",
				"  [ok] no file needs a retention",
				"  - 3 kept until a later date",
				"  - 1 kept forever",
			}},
		{"undecided and expired", Report{
			Roots: []store.RootTotal{{Root: store.Root{Path: "/r"}}},
			Status: map[store.Status]int64{store.StatusUndated: 1200, store.StatusChanged: 2,
				store.StatusExpired: 1},
			Undecided:   1202,
			UndecidedIn: []DirGroup{{Root: "/r", Rel: "docs", Files: 1202, Bytes: 2048}},
		}, []string{
			"Retention   1,203 files and symlinks",
			"  [!] 1,202 need a retention (no retention 1,200, past retention, changed since it was set 2)",
			"    Where most need one (to depth 2)",
			"    files      size",
			"    1,202   2.0 KiB   docs",
			"    Next: filo export [DIR] -o FILE, then filo update FILE, or filo rules add DIR POLICY",
			"  [!] 1 past its retention: see filo delete",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := section(t, tc.r, "Retention")
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
	want := []DirGroup{{Root: "/r", Rel: "docs/x", Files: 3, Bytes: 20},
		{Root: "/r", Rel: ".", Files: 1, Bytes: 1}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRenderSnapshots(t *testing.T) {
	latest := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	tests := []struct {
		name string
		sn   store.SnapshotInfo
		want []string
	}{
		{"none yet", store.SnapshotInfo{Dir: "/d"}, []string{"Register snapshots   none yet",
			"    one is written after each command that changes the register"}},
		{"current", store.SnapshotInfo{Dir: "/d", Count: 2, Latest: latest, LatestWrite: latest}, []string{
			"Register snapshots   2, latest 2026-11-13 10:00", "    /d"}},
		{"behind the latest write", store.SnapshotInfo{Dir: "/d", Count: 1, Latest: latest,
			LatestWrite: latest.Add(time.Hour)}, []string{"Register snapshots   1, latest 2026-11-13 10:00", "    /d",
			"  [!] the register changed at 2026-11-13 11:00, after the latest snapshot; see the warning after each " +
				"command"}},
		{"only future-dated ones", store.SnapshotInfo{Dir: "/d", Future: 2, LatestWrite: latest}, []string{
			"Register snapshots   none",
			"  [!] none in /d, though the register has changed; see the warning after each command",
			"  [!] 2 snapshots are dated in the future (was the clock ahead?); kept, but not counted as the latest"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := section(t, Report{Snapshots: tc.sn}, "Register snapshots")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestRenderDuplicatesAndEvents(t *testing.T) {
	var b strings.Builder
	Report{Roots: []store.RootTotal{{Root: store.Root{Path: "/r"}}},
		Duplicates: store.Duplicates{NotCounted: store.NotCounted{Dirs: 1, Always: 2, RecordSets: 1}},
		Events:     []store.EventRow{{Event: store.Event{Kind: "error", Detail: "permission denied"}, ScanID: 4, RootPath: "/r"}},
	}.Render(view.New(view.Options{Out: &b}))
	for _, want := range []string{"Duplicates   no groups\n",
		"  [kept] 1 excluded directory, 2 contents marked always and 1 set of identical files kept as separate files, " +
			"these are not counted as duplicates (use \"filo duplicates\" to view)\n",
		"    scan #4   error   .   permission denied\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
}

// TestNotCountedLine: only what there is is named, in order; nothing is no line.
func TestNotCountedLine(t *testing.T) {
	tests := []struct {
		n    store.NotCounted
		want string
	}{
		{store.NotCounted{}, ""},
		{store.NotCounted{Always: 1},
			`1 content marked always, these are not counted as duplicates (use "filo duplicates" to view)`},
		{store.NotCounted{RecordSets: 6}, `6 sets of identical files kept as separate files, these are not counted ` +
			`as duplicates (use "filo duplicates" to view)`},
		{store.NotCounted{Dirs: 2, RecordSets: 3}, `2 excluded directories and 3 sets of identical files kept as ` +
			`separate files, these are not counted as duplicates (use "filo duplicates" to view)`},
		{store.NotCounted{Dirs: 1, Always: 2, RecordSets: 1}, `1 excluded directory, 2 contents marked always and 1 ` +
			`set of identical files kept as separate files, these are not counted as duplicates (use "filo ` +
			`duplicates" to view)`},
	}
	for _, tc := range tests {
		if got := NotCountedLine(tc.n); got != tc.want {
			t.Errorf("NotCountedLine(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestRenderEmptyRetention(t *testing.T) {
	if got := section(t, Report{}, "Retention"); strings.Join(got, "\n") != "Retention   none" {
		t.Errorf("got %q, want one line", got)
	}
}
