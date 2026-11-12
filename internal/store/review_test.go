package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	const today = "2026-11-11"
	hash := func(h string) sql.NullString { return sql.NullString{String: h, Valid: true} }
	set := func(it FileItem) FileItem { it.ReviewedUTC = "2026-11-01T00:00:00Z"; return it }
	tests := []struct {
		name string
		row  fileRow
		want Status
	}{
		{"no date", fileRow{item: FileItem{Kind: "file", Hash: "h"}}, StatusUndated},
		{"date in the future", fileRow{item: set(FileItem{Kind: "file", Hash: "h", KeepUntil: "2026-11-12"}),
			reviewedHash: hash("h")}, StatusKept},
		{"date today, unchanged: a candidate", fileRow{item: set(FileItem{Kind: "file", Hash: "h", KeepUntil: today}),
			reviewedHash: hash("h")}, StatusExpired},
		{"date passed, unchanged", fileRow{item: set(FileItem{Kind: "file", Hash: "h", KeepUntil: "2025-01-01"}),
			reviewedHash: hash("h")}, StatusExpired},
		{"edited before its date: still kept", fileRow{item: set(FileItem{Kind: "file", Hash: "h2",
			KeepUntil: "2027-01-01"}), reviewedHash: hash("h")}, StatusKept},
		{"edited, date passed: needs a new date", fileRow{item: set(FileItem{Kind: "file", Hash: "h2",
			KeepUntil: "2025-01-01"}), reviewedHash: hash("h")}, StatusChanged},
		{"date set without a hash (migrated set-date), passed",
			fileRow{item: FileItem{Kind: "file", Hash: "h", KeepUntil: "2025-01-01"}}, StatusChanged},
		{"no hash now, passed", fileRow{item: set(FileItem{Kind: "file", KeepUntil: "2025-01-01"}),
			reviewedHash: hash("h")}, StatusChanged},
		{"symlinks have no content to compare", fileRow{item: set(FileItem{Kind: "symlink", KeepUntil: "2025-01-01"})},
			StatusExpired},
		{"forever", fileRow{item: set(FileItem{Kind: "file", Hash: "h", KeepUntil: Forever}), reviewedHash: hash("h")},
			StatusForever},
		{"forever, edited since: still forever", fileRow{item: set(FileItem{Kind: "file", Hash: "h2",
			KeepUntil: Forever}), reviewedHash: hash("h")}, StatusForever},
		{"forever, set before it was flagged unreadable", fileRow{item: set(FileItem{Kind: "file", Hash: "h",
			UnreadableSince: 3, KeepUntil: Forever}), reviewedAfter: 2, reviewedHash: hash("h")}, StatusUnreadable},
		{"unreadable, never decided", fileRow{item: FileItem{Kind: "file", Hash: "h", UnreadableSince: 3}},
			StatusUnreadable},
		{"unreadable, date set before it was flagged", fileRow{item: set(FileItem{Kind: "file", Hash: "h",
			UnreadableSince: 3, KeepUntil: "2027-01-01"}), reviewedAfter: 2, reviewedHash: hash("h")}, StatusUnreadable},
		{"unreadable, date set since it was flagged", fileRow{item: set(FileItem{Kind: "file", Hash: "h",
			UnreadableSince: 3, KeepUntil: "2027-01-01"}), reviewedAfter: 3, reviewedHash: hash("h")}, StatusKept},
		{"unreadable, date set since, passed: content cannot be checked", fileRow{item: set(FileItem{Kind: "file",
			Hash: "h", UnreadableSince: 3, KeepUntil: today}), reviewedAfter: 3, reviewedHash: hash("h")},
			StatusUnreadable},
		{"possible move, never decided", fileRow{item: FileItem{Kind: "file", Hash: "h"}, possibleMoveIn: 5},
			StatusPossibleMove},
		{"possible move, date set before it", fileRow{item: set(FileItem{Kind: "file", Hash: "h",
			KeepUntil: "2027-01-01"}), reviewedAfter: 4, reviewedHash: hash("h"), possibleMoveIn: 5}, StatusPossibleMove},
		{"possible move, date set since", fileRow{item: set(FileItem{Kind: "file", Hash: "h", KeepUntil: "2027-01-01"}),
			reviewedAfter: 5, reviewedHash: hash("h"), possibleMoveIn: 5}, StatusKept},
		{"unreadable outranks a possible move",
			fileRow{item: FileItem{Kind: "file", Hash: "h", UnreadableSince: 3}, possibleMoveIn: 5}, StatusUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.row, today); got != tc.want {
				t.Errorf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}

// reviewEnv is a register with one root, driven through ApplyScan.
type reviewEnv struct {
	t    *testing.T
	ctx  context.Context
	s    *Store
	db   string
	root Root
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	t.Cleanup(func() { mustClose(t, s) })
	r, err := s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	return &reviewEnv{t: t, ctx: ctx, s: s, db: db, root: r}
}

func (e *reviewEnv) apply(ch Changes) int64 {
	e.t.Helper()
	id, err := e.s.ApplyScan(e.ctx, ScanRecord{RootID: e.root.ID, Started: time.Now()}, ch)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *reviewEnv) known() map[string]Known {
	e.t.Helper()
	ks, err := e.s.Known(e.ctx, e.root.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]Known{}
	for _, k := range ks {
		m[k.Path] = k
	}
	return m
}

// statuses returns path -> status for the whole register.
func (e *reviewEnv) statuses(today string) map[string]Status {
	e.t.Helper()
	q, err := e.s.Files(e.ctx, today, Filter{})
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]Status{}
	for _, it := range q {
		m[it.Path] = it.Status
	}
	return m
}

func (e *reviewEnv) export(today string, now time.Time) (int64, []FileItem) {
	e.t.Helper()
	q, err := e.s.Files(e.ctx, today, Filter{})
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.s.RecordExport(e.ctx, "", now, q, func(int64) error { return nil })
	if err != nil {
		e.t.Fatal(err)
	}
	return id, q
}

func f(path, hash string) NewFile {
	return NewFile{Path: path, Kind: "file", Size: 1, MtimeNS: 1, Hash: hash}
}

func TestFilesStatusAndScope(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("docs/a", "ha"), f("docs/sub/b", "hb"), f("docsx/c", "hc"), f("old/same", "h1"),
		f("📷 Pictures/🏖️ beach.jpg", "hp"), f("📷 Pictures🎉/party.jpg", "hq"), f("📷 Pictures-x/y.jpg", "hr")}})
	k := e.known()
	// Scan 2: old/same goes, new/same arrives with other content: a possible move. docs/a becomes unreadable.
	scan2 := e.apply(Changes{
		Close:      []int64{k["old/same"].PathID},
		New:        []NewFile{f("new/same", "h2")},
		Unreadable: []int64{k["docs/a"].FileID},
		Events: []Event{{Kind: "possible_move", FileID: k["old/same"].FileID, Path: "new/same", OtherPath: "old/same",
			Detail: "same name, different content"}},
	})

	q, err := e.s.Files(e.ctx, "2026-11-11", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FileItem{}
	for _, it := range q {
		got[it.Path] = it
	}
	want := map[string]Status{"docs/a": StatusUnreadable, "docs/sub/b": StatusUndated, "docsx/c": StatusUndated,
		"new/same": StatusPossibleMove, "📷 Pictures/🏖️ beach.jpg": StatusUndated, "📷 Pictures🎉/party.jpg": StatusUndated,
		"📷 Pictures-x/y.jpg": StatusUndated}
	if len(got) != len(want) {
		t.Fatalf("files = %+v, want %v (the missing old/same is not listed)", q, want)
	}
	for p, st := range want {
		if got[p].Status != st {
			t.Errorf("%s: status %q, want %q", p, got[p].Status, st)
		}
	}
	if pm := got["new/same"]; pm.MoveFrom != filepath.Join(e.root.Path, "old", "same") ||
		pm.MoveDetail != "same name, different content" {
		t.Errorf("possible move = %+v", pm)
	}
	if u := got["docs/a"]; u.UnreadableSince != scan2 || u.UnreadableSinceUTC == "" {
		t.Errorf("unreadable = %+v, want flagged since scan %d with its time", u, scan2)
	}

	// The scope "docs" covers docs/ and below, not docsx/.
	q, err = e.s.Files(e.ctx, "2026-11-11", Filter{RootID: e.root.ID, Scope: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range q {
		paths = append(paths, it.Path)
	}
	if !slices.Equal(paths, []string{"docs/a", "docs/sub/b"}) {
		t.Errorf("scoped files = %v", paths)
	}

	// An emoji folder: its siblings that share the prefix ("📷 Pictures🎉", "📷 Pictures-x") are outside it.
	q, err = e.s.Files(e.ctx, "2026-11-11", Filter{RootID: e.root.ID, Scope: "📷 Pictures"})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].Path != "📷 Pictures/🏖️ beach.jpg" || q[0].AbsPath() != filepath.Join(e.root.Path, "📷 Pictures", "🏖️ beach.jpg") {
		t.Errorf("emoji-scoped files = %+v", q)
	}
}

func TestUpdateRetention(t *testing.T) {
	e := newReviewEnv(t)
	const today = "2026-11-11"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("keep", "h1"), f("edited", "h3"), f("flagged", "h4"), f("untouched", "h7"),
		f("old/pm", "h8"), f("past", "h10"), f("gone", "h11"), f("dated", "h12"), f("tax", "h13"), f("nodate", "h14"),
		f("typed", "h15")}})
	k := e.known()
	e.apply(Changes{
		Close: []int64{k["old/pm"].PathID}, New: []NewFile{f("new/pm", "h9")},
		Events: []Event{{Kind: "possible_move", FileID: k["old/pm"].FileID, Path: "new/pm", OtherPath: "old/pm"}},
	})
	k = e.known()
	id := func(p string) int64 { return k[p].FileID }
	// row builds a CSV row with a file_date column and no keep_until column.
	row := func(line int, ex int64, file, date, ret string) RetentionRow {
		return RetentionRow{Line: line, ExportID: ex, FileID: id(file), FileDate: date, HasFileDate: true, Retention: ret}
	}

	// Give dated a retention first, so the export carries it.
	ex0, _ := e.export(today, now.Add(-time.Hour))
	if res, err := e.s.UpdateRetention(e.ctx, []RetentionRow{row(2, ex0, "dated", "2020-05-01", "10y")},
		now.Add(-time.Hour), today, "first.csv", false, nil); err != nil || res.Set != 1 {
		t.Fatalf("first update = %+v, %v", res, err)
	}

	exportID, items := e.export(today, now)
	if len(items) != 11 {
		t.Fatalf("exported %d rows, want 11 (every present file)", len(items))
	}
	if v := rawQueryString(t, e.db, `SELECT file_date || ' ' || retention || ' ' || keep_until FROM export_rows
		WHERE export_id = `+itoa(exportID)+` AND file_id = `+itoa(id("dated"))); v != "2020-05-01 10y 2030-05-01" {
		t.Errorf("export row = %q, want the values it was exported with", v)
	}

	// After the export: edited changes, flagged becomes unreadable, gone goes missing.
	e.apply(Changes{Updates: []FileUpdate{{FileID: id("edited"), Size: 2, MtimeNS: 2, Hash: "h3b"}},
		Unreadable: []int64{id("flagged")}, Close: []int64{k["gone"].PathID}})

	typed := row(13, exportID, "typed", "", "2027-01-01")
	typed.HasKeepUntil, typed.KeepUntil = true, "2027-06-30" // a date typed in the display column
	noFileDateColumn := RetentionRow{Line: 14, ExportID: exportID, FileID: id("dated"), Retention: "10y"}
	rows := []RetentionRow{
		row(2, exportID, "keep", "", "2027-11-11"),
		row(3, exportID, "edited", "", "2027-11-11"),
		row(4, exportID, "flagged", "", "2027-11-11"), // unreadable: a retention is fine
		row(5, exportID, "new/pm", "", "forever"),
		row(6, exportID+1, "keep", "", "2027-11-11"),
		{Line: 7, ExportID: exportID, FileID: 9999, Retention: "2027-11-11"},
		row(8, exportID, "past", "2015-03-14", "7y"), // ends 2022: accepted, now past its retention
		row(9, exportID, "untouched", "", ""),        // blank, as exported
		row(10, exportID, "gone", "", "2027-01-01"),  // missing since the export
		row(11, exportID, "tax", "", "2015-06-30"),   // a past date: accepted
		row(12, exportID, "nodate", "", "7y"),        // a period with nothing to count from
		typed,
		noFileDateColumn, // no file_date column: the exported one is kept, so nothing changed
	}

	before, err := e.s.RowCounts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := e.s.UpdateRetention(e.ctx, rows, now, today, "dry.csv", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := e.s.RowCounts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(before, after) {
		t.Errorf("dry run wrote: before %v after %v", before, after)
	}

	res, err := e.s.UpdateRetention(e.ctx, rows, now, today, "retention.csv", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Set != dry.Set || len(res.NowPast) != len(dry.NowPast) || len(res.Refusals) != len(dry.Refusals) {
		t.Errorf("dry run %+v differs from the real one %+v", dry, res)
	}
	if res.Set != 5 || len(res.NowPast) != 2 || res.Unchanged != 2 || res.Cleared != 0 {
		t.Errorf("result = %+v, want 5 set (keep, flagged, new/pm, past, tax), 2 now past, 2 unchanged", res)
	}
	wantRefused := map[int]string{
		3: "changed since export", 6: "not in this register", 7: "not in export", 10: "no longer present",
		12: "retention 7y needs a file_date", 13: `keep_until was changed to "2027-06-30"`,
	}
	if len(res.Refusals) != len(wantRefused) {
		t.Errorf("refusals = %+v", res.Refusals)
	}
	for _, r := range res.Refusals {
		if w, ok := wantRefused[r.Line]; !ok || !strings.Contains(r.Reason, w) {
			t.Errorf("line %d refused with %q, want %q", r.Line, r.Reason, w)
		}
		if r.Line == 3 && r.Path != filepath.Join(e.root.Path, "edited") {
			t.Errorf("refusal path = %q", r.Path)
		}
	}

	// flagged has its retention, but it was flagged unreadable after the export: the retention covers what the export
	// showed, so it stays unreadable until a later export's update.
	wantStatus := map[string]Status{"keep": StatusKept, "edited": StatusUndated, "flagged": StatusUnreadable,
		"untouched": StatusUndated, "new/pm": StatusForever, "past": StatusExpired, "dated": StatusKept,
		"tax": StatusExpired, "nodate": StatusUndated, "typed": StatusUndated}
	if got := e.statuses(today); !maps.Equal(got, wantStatus) {
		t.Errorf("statuses after update = %v, want %v", got, wantStatus)
	}
	if len(res.NowPast) == 2 && (res.NowPast[0] != Past{Path: filepath.Join(e.root.Path, "past"), Retention: "7y",
		KeepUntil: "2022-03-14"} || res.NowPast[1].Path != filepath.Join(e.root.Path, "tax")) {
		t.Errorf("now past = %+v", res.NowPast)
	}
	stored := func(file string) string {
		return rawQueryString(t, e.db, `SELECT coalesce(file_date, '-') || ' ' || coalesce(retention, '-') || ' ' ||
			coalesce(keep_until, '-') FROM files WHERE id = `+itoa(id(file)))
	}
	if v := stored("past"); v != "2015-03-14 7y 2022-03-14" {
		t.Errorf("past = %q", v)
	}
	if v := rawQueryString(t, e.db, `SELECT file_date || ' ' || retention || ' ' || keep_until FROM reviews
		WHERE file_id = `+itoa(id("past"))); v != "2015-03-14 7y 2022-03-14" {
		t.Errorf("history of past = %q", v)
	}

	// The same rows again: nothing to do. An edited row from the same export replaces the retention, and a new
	// file_date works keep_until out again.
	res, err = e.s.UpdateRetention(e.ctx, rows[:1], now, today, "retention.csv", false, nil)
	if err != nil || res.Set != 0 || res.Unchanged != 1 {
		t.Errorf("same rows again = %+v, %v; want 1 unchanged", res, err)
	}
	res, err = e.s.UpdateRetention(e.ctx, []RetentionRow{row(2, exportID, "keep", "", "2028-11-11"),
		row(8, exportID, "past", "2024-03-31", "7y")}, now.Add(time.Minute), today, "retention.csv", false, nil)
	if err != nil || res.Set != 2 || len(res.NowPast) != 0 {
		t.Errorf("re-edited rows = %+v, %v; want both set, none past", res, err)
	}
	if v := stored("past"); v != "2024-03-31 7y 2031-03-31" {
		t.Errorf("past after a new file_date = %q", v)
	}

	// A file_date the export suggested from the name, left untouched, is stored only with a period retention.
	if _, err := e.s.db.ExecContext(e.ctx, `UPDATE export_rows SET file_date = '2019-01-01' WHERE export_id = ?
		AND file_id IN (?, ?)`, exportID, id("nodate"), id("untouched")); err != nil {
		t.Fatal(err)
	}
	res, err = e.s.UpdateRetention(e.ctx, []RetentionRow{row(2, exportID, "nodate", "2019-01-01", "forever"),
		row(3, exportID, "untouched", "2019-01-01", "7y")}, now.Add(2*time.Minute), today, "s.csv", false, nil)
	if err != nil || res.Set != 2 {
		t.Errorf("suggested file_dates = %+v, %v", res, err)
	}
	if v := stored("nodate"); v != "- forever forever" {
		t.Errorf("suggestion with forever: %q, want no file_date stored", v)
	}
	if v := stored("untouched"); v != "2019-01-01 7y 2026-01-01" {
		t.Errorf("suggestion with a period: %q, want it stored and counted from", v)
	}

	// A blank retention where one is stored is a clear: only when confirmed.
	clear := []RetentionRow{row(2, exportID, "dated", "2020-05-01", "")}
	var asked []Clear
	res, err = e.s.UpdateRetention(e.ctx, clear, now, today, "c.csv", false, func(cs []Clear) bool {
		asked = cs
		return false
	})
	if err != nil || res.ClearsSkipped != 1 || res.Cleared != 0 {
		t.Errorf("declined clear = %+v, %v", res, err)
	}
	if len(asked) != 1 || asked[0].Retention != "10y" || asked[0].KeepUntil != "2030-05-01" ||
		asked[0].Path != filepath.Join(e.root.Path, "dated") {
		t.Errorf("asked about %+v", asked)
	}
	if res, err = e.s.UpdateRetention(e.ctx, clear, now, today, "c.csv", false, nil); err != nil || res.ClearsSkipped != 1 {
		t.Errorf("clear with no one to ask = %+v, %v; want it skipped", res, err)
	}
	if res, err = e.s.UpdateRetention(e.ctx, clear, now, today, "c.csv", true, func([]Clear) bool {
		t.Error("asked in a dry run")
		return true
	}); err != nil || res.Cleared != 1 {
		t.Errorf("dry-run clear = %+v, %v; want 1 counted", res, err)
	}
	if got := e.statuses(today)["dated"]; got != StatusKept {
		t.Errorf("dated after declined clears: %q, want kept", got)
	}
	res, err = e.s.UpdateRetention(e.ctx, clear, now, today, "c.csv", false, func([]Clear) bool { return true })
	if err != nil || res.Cleared != 1 {
		t.Errorf("confirmed clear = %+v, %v", res, err)
	}
	if got, v := e.statuses(today)["dated"], stored("dated"); got != StatusUndated || v != "2020-05-01 - -" {
		t.Errorf("dated after a confirmed clear: %q, %q; want undated, keeping its file_date", got, v)
	}

	// A newer export's retention wins; the older export can no longer override it.
	later := now.Add(time.Hour)
	export2, _ := e.export(today, later)
	if _, err := e.s.UpdateRetention(e.ctx, []RetentionRow{row(2, export2, "untouched", "2019-01-01", "2028-01-01")},
		later, today, "second.csv", false, nil); err != nil {
		t.Fatal(err)
	}
	res, err = e.s.UpdateRetention(e.ctx, []RetentionRow{row(2, exportID, "untouched", "2019-01-01", "2029-01-01")},
		later.Add(time.Hour), today, "retention.csv", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "changed after export") {
		t.Errorf("stale update = %+v", res)
	}

	// forever: never past, never a candidate, even once edited.
	e.apply(Changes{Updates: []FileUpdate{{FileID: id("new/pm"), Size: 9, MtimeNS: 9, Hash: "h9b"}}})
	if got := e.statuses("9999-12-31")["new/pm"]; got != StatusForever {
		t.Errorf("forever file, edited, in 9999: %q, want forever", got)
	}

	// On its date, keep is a deletion candidate; once edited, it needs a new retention instead.
	if got := e.statuses("2028-11-11")["keep"]; got != StatusExpired {
		t.Errorf("keep on its date: %q, want expired", got)
	}
	e.apply(Changes{Updates: []FileUpdate{{FileID: id("keep"), Size: 9, MtimeNS: 9, Hash: "h1b"}}})
	if got := e.statuses("2028-11-11")["keep"]; got != StatusChanged {
		t.Errorf("keep edited, on its date: %q, want changed", got)
	}
	if got := e.statuses(today)["keep"]; got != StatusKept {
		t.Errorf("keep edited, before its date: %q, want still kept", got)
	}
}

func TestKeepUntil(t *testing.T) {
	tests := []struct {
		date, retention, want string
		wantErr               bool
	}{
		{"2023-03-14", "7y", "2030-03-14", false},
		{"2023-03-14", "18m", "2024-09-14", false},
		{"2023-03-14", "2w", "2023-03-28", false},
		{"2023-03-14", "30d", "2023-04-13", false},
		{"2026-01-31", "1m", "2026-02-28", false}, // stops on the month's last day
		{"2028-01-31", "1m", "2028-02-29", false},
		{"2028-02-29", "1y", "2029-02-28", false},
		{"2023-03-14", "forever", "forever", false},
		{"", "forever", "forever", false},
		{"2023-03-14", "2030-01-01", "2030-01-01", false}, // a fixed date ignores file_date
		{"", "2015-01-01", "2015-01-01", false},
		{"2023-03-14", "", "", false},
		{"", "7y", "", true},
	}
	for _, tc := range tests {
		got, err := KeepUntil(tc.date, tc.retention)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("KeepUntil(%q, %q) = %q, %v; want %q, error %v", tc.date, tc.retention, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestRecordExportRollsBackOnWriteError(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "h1")}})
	q, err := e.s.Files(e.ctx, "2026-11-11", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	if _, err := e.s.RecordExport(e.ctx, "/x.csv", time.Now(), q, func(int64) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write error", err)
	}
	counts, err := e.s.RowCounts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["exports"] != 0 || counts["export_rows"] != 0 {
		t.Errorf("a failed export left rows: %v", counts)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
