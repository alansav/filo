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
	reviewed := func(it QueueItem) QueueItem { it.ReviewedUTC = "2026-11-01T00:00:00Z"; return it }
	file := QueueItem{Kind: "file", Hash: "h"}
	tests := []struct {
		name string
		row  queueRow
		want Reason
	}{
		{"never reviewed, no date", queueRow{item: file}, ReasonUndated},
		{"date in the future", queueRow{item: QueueItem{Kind: "file", Hash: "h", ReviewDate: "2026-11-12"}}, ""},
		{"date today is due", queueRow{item: QueueItem{Kind: "file", Hash: "h", ReviewDate: today}}, ReasonDue},
		{"date passed is due", queueRow{item: QueueItem{Kind: "file", Hash: "h", ReviewDate: "2025-01-01"}}, ReasonDue},
		{"marked delete waits for disposal",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", Action: ActionDelete}), reviewedHash: hash("h")}, ""},
		{"reviewed, same content, date ahead",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", ReviewDate: "2027-01-01"}), reviewedHash: hash("h")}, ""},
		{"edited after a delete decision",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h2", Action: ActionDelete}), reviewedHash: hash("h")},
			ReasonChanged},
		{"reviewed without a hash, now read",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", ReviewDate: "2027-01-01"})}, ReasonChanged},
		{"reviewed, now has no hash",
			queueRow{item: reviewed(QueueItem{Kind: "file", ReviewDate: "2027-01-01"}), reviewedHash: hash("h")},
			ReasonChanged},
		{"symlinks have no content to compare",
			queueRow{item: reviewed(QueueItem{Kind: "symlink", ReviewDate: "2027-01-01"})}, ""},
		{"unreadable, never reviewed", queueRow{item: QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3}},
			ReasonUnreadable},
		{"unreadable, bulk-dated only",
			queueRow{item: QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3, ReviewDate: "2027-01-01"}},
			ReasonUnreadable},
		{"unreadable, reviewed before it was flagged",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3, ReviewDate: "2027-01-01"}),
				reviewedAfter: 2, reviewedHash: hash("h")}, ReasonUnreadable},
		{"unreadable, reviewed since it was flagged",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3, ReviewDate: "2027-01-01"}),
				reviewedAfter: 3, reviewedHash: hash("h")}, ""},
		{"unreadable and never read, reviewed since flagged: stale hash is not a change",
			queueRow{item: reviewed(QueueItem{Kind: "file", UnreadableSince: 3, ReviewDate: "2027-01-01"}),
				reviewedAfter: 4}, ""},
		{"unreadable, reviewed since flagged, date due",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3, ReviewDate: today}),
				reviewedAfter: 3, reviewedHash: hash("h")}, ReasonDue},
		{"possible move, never reviewed", queueRow{item: file, possibleMoveIn: 5}, ReasonPossibleMove},
		{"possible move, bulk-dated only",
			queueRow{item: QueueItem{Kind: "file", Hash: "h", ReviewDate: "2027-01-01"}, possibleMoveIn: 5},
			ReasonPossibleMove},
		{"possible move, reviewed since",
			queueRow{item: reviewed(QueueItem{Kind: "file", Hash: "h", ReviewDate: "2027-01-01"}), reviewedAfter: 5,
				reviewedHash: hash("h"), possibleMoveIn: 5}, ""},
		{"unreadable outranks a possible move",
			queueRow{item: QueueItem{Kind: "file", Hash: "h", UnreadableSince: 3}, possibleMoveIn: 5}, ReasonUnreadable},
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

// queue returns path -> reason for the whole register.
func (e *reviewEnv) queue(today string) map[string]Reason {
	e.t.Helper()
	q, err := e.s.Queue(e.ctx, today, QueueFilter{})
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]Reason{}
	for _, it := range q {
		m[it.Path] = it.Reason
	}
	return m
}

func (e *reviewEnv) export(today string, now time.Time) (int64, []QueueItem) {
	e.t.Helper()
	q, err := e.s.Queue(e.ctx, today, QueueFilter{})
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

func TestQueueReasons(t *testing.T) {
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

	q, err := e.s.Queue(e.ctx, "2026-11-11", QueueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]QueueItem{}
	for _, it := range q {
		got[it.Path] = it
	}
	want := map[string]Reason{"docs/a": ReasonUnreadable, "docs/sub/b": ReasonUndated, "docsx/c": ReasonUndated,
		"new/same": ReasonPossibleMove, "📷 Pictures/🏖️ beach.jpg": ReasonUndated, "📷 Pictures🎉/party.jpg": ReasonUndated,
		"📷 Pictures-x/y.jpg": ReasonUndated}
	if len(got) != len(want) {
		t.Fatalf("queue = %+v, want %v", q, want)
	}
	for p, r := range want {
		if got[p].Reason != r {
			t.Errorf("%s: reason %q, want %q", p, got[p].Reason, r)
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
	q, err = e.s.Queue(e.ctx, "2026-11-11", QueueFilter{RootID: e.root.ID, Scope: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range q {
		paths = append(paths, it.Path)
	}
	if !slices.Equal(paths, []string{"docs/a", "docs/sub/b"}) {
		t.Errorf("scoped queue = %v", paths)
	}

	// An emoji folder: its siblings that share the prefix ("📷 Pictures🎉", "📷 Pictures-x") are outside it.
	q, err = e.s.Queue(e.ctx, "2026-11-11", QueueFilter{RootID: e.root.ID, Scope: "📷 Pictures"})
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].Path != "📷 Pictures/🏖️ beach.jpg" || q[0].AbsPath() != filepath.Join(e.root.Path, "📷 Pictures", "🏖️ beach.jpg") {
		t.Errorf("emoji-scoped queue = %+v", q)
	}
}

func TestImportDecisions(t *testing.T) {
	e := newReviewEnv(t)
	const today = "2026-11-11"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("keep", "h1"), f("del", "h2"), f("edited", "h3"), f("flagged", "h4"),
		f("floor", "h5"), f("again", "h6"), f("untouched", "h7"), f("old/pm", "h8")}})
	k := e.known()
	e.apply(Changes{
		Close: []int64{k["old/pm"].PathID}, New: []NewFile{f("new/pm", "h9")},
		Events: []Event{{Kind: "possible_move", FileID: k["old/pm"].FileID, Path: "new/pm", OtherPath: "old/pm"}},
	})
	k = e.known()
	exportID, items := e.export(today, now)
	if len(items) != 8 {
		t.Fatalf("exported %d rows, want 8", len(items))
	}

	// After the export: edited changes, flagged becomes unreadable.
	ed := k["edited"]
	e.apply(Changes{Updates: []FileUpdate{{FileID: ed.FileID, Size: 2, MtimeNS: 2, Hash: "h3b"}},
		Unreadable: []int64{k["flagged"].FileID}})

	id := func(p string) int64 { return k[p].FileID }
	ds := []Decision{
		{Line: 2, ExportID: exportID, FileID: id("keep"), ReviewDate: "2027-11-11"},
		{Line: 3, ExportID: exportID, FileID: id("del"), Action: ActionDelete},
		{Line: 4, ExportID: exportID, FileID: id("edited"), ReviewDate: "2027-11-11"},
		{Line: 5, ExportID: exportID, FileID: id("flagged"), Action: ActionDelete},
		{Line: 6, ExportID: exportID, FileID: id("floor"), Action: ActionDelete, RetainUntil: "2030-01-01"},
		{Line: 7, ExportID: exportID, FileID: id("new/pm"), Action: ActionDelete},
		{Line: 8, ExportID: exportID + 1, FileID: id("keep"), ReviewDate: "2027-11-11"},
		{Line: 9, ExportID: exportID, FileID: 9999, ReviewDate: "2027-11-11"},
		{Line: 10, ExportID: exportID, FileID: id("again"), ReviewDate: "2027-11-11", RetainUntil: "2033-12-31"},
	}

	before, err := e.s.RowCounts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := e.s.ApplyDecisions(e.ctx, ds, now, today, "dry.csv", true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := e.s.RowCounts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after["reviews"] != before["reviews"] || after["review_batches"] != before["review_batches"] {
		t.Errorf("dry run wrote reviews: before %v after %v", before, after)
	}

	res, err := e.s.ApplyDecisions(e.ctx, ds, now, today, "review.csv", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dates != dry.Dates || res.Deletes != dry.Deletes || len(res.Refusals) != len(dry.Refusals) {
		t.Errorf("dry run %+v differs from the real one %+v", dry, res)
	}
	if res.Dates != 2 || res.Deletes != 1 || res.Already != 0 {
		t.Errorf("result = %+v, want 2 dates (keep, again) and 1 delete (del)", res)
	}
	wantRefused := map[int]string{
		4: "changed since export", 5: "flagged unreadable", 6: "retained until 2030-01-01", 7: "possible move",
		8: "not in this register", 9: "not in export",
	}
	if len(res.Refusals) != len(wantRefused) {
		t.Errorf("refusals = %+v", res.Refusals)
	}
	for _, r := range res.Refusals {
		if w, ok := wantRefused[r.Line]; !ok || !strings.Contains(r.Reason, w) {
			t.Errorf("line %d refused with %q, want %q", r.Line, r.Reason, w)
		}
	}
	for _, r := range res.Refusals {
		if r.Line == 4 && r.Path != filepath.Join(e.root.Path, "edited") {
			t.Errorf("refusal path = %q", r.Path)
		}
	}

	// The queue now: keep, again and del are decided; the rest are still queued.
	wantQueue := map[string]Reason{"edited": ReasonUndated, "flagged": ReasonUnreadable, "floor": ReasonUndated,
		"untouched": ReasonUndated, "new/pm": ReasonPossibleMove}
	if got := e.queue(today); !maps.Equal(got, wantQueue) {
		t.Errorf("queue after import = %v, want %v", got, wantQueue)
	}
	if v := rawQueryString(t, e.db, `SELECT retain_until FROM files WHERE id = `+itoa(id("again"))); v != "2033-12-31" {
		t.Errorf("retain_until = %q", v)
	}

	// Importing the same file again is a no-op, and changes nothing.
	res, err = e.s.ApplyDecisions(e.ctx, ds[:3], now, today, "review.csv", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Already != 2 || res.Dates != 0 || res.Deletes != 0 {
		t.Errorf("re-import = %+v, want 2 already imported", res)
	}

	// A newer export's decision wins; the older export can no longer override it.
	later := now.Add(time.Hour)
	export2, _ := e.export(today, later)
	if _, err := e.s.ApplyDecisions(e.ctx, []Decision{{Line: 2, ExportID: export2, FileID: id("floor"),
		ReviewDate: "2028-01-01"}}, later, today, "second.csv", false); err != nil {
		t.Fatal(err)
	}
	res, err = e.s.ApplyDecisions(e.ctx, []Decision{{Line: 2, ExportID: exportID, FileID: id("floor"),
		ReviewDate: "2029-01-01"}}, later.Add(time.Hour), today, "review.csv", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refusals) != 1 || !strings.Contains(res.Refusals[0].Reason, "reviewed again since export") {
		t.Errorf("stale import = %+v", res)
	}

	// After the review date passes, keep is due again.
	if got := e.queue("2027-11-11")["keep"]; got != ReasonDue {
		t.Errorf("keep on its review date: %q, want due", got)
	}
	// Editing a file marked for deletion brings it back.
	kd := e.known()["del"]
	e.apply(Changes{Updates: []FileUpdate{{FileID: kd.FileID, Size: 9, MtimeNS: 9, Hash: "h2b"}}})
	if got := e.queue(today)["del"]; got != ReasonChanged {
		t.Errorf("del after an edit: %q, want changed", got)
	}
}

func TestSetReviewDate(t *testing.T) {
	e := newReviewEnv(t)
	const today = "2026-11-11"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	e.apply(Changes{New: []NewFile{f("photos/a", "h1"), f("photos/2019/b", "h2"), f("photos/c", "h3"),
		f("photosx/d", "h4"), f("other/e", "h5")}})
	k := e.known()
	e.apply(Changes{Unreadable: []int64{k["photos/c"].FileID}})

	// Already decided files are never overwritten.
	exportID, _ := e.export(today, now)
	if _, err := e.s.ApplyDecisions(e.ctx, []Decision{{Line: 2, ExportID: exportID, FileID: k["photos/a"].FileID,
		ReviewDate: "2030-01-01"}}, now, today, "r.csv", false); err != nil {
		t.Fatal(err)
	}

	scope := QueueFilter{RootID: e.root.ID, Scope: "photos"}
	n, err := e.s.SetReviewDate(e.ctx, scope, "2027-06-30", now, "photos 2027-06-30", true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("dry run counts %d, want 2 (photos/2019/b, photos/c)", n)
	}
	if got := e.queue(today); got["photos/2019/b"] != ReasonUndated {
		t.Errorf("dry run changed the queue: %v", got)
	}

	if n, err = e.s.SetReviewDate(e.ctx, scope, "2027-06-30", now, "photos 2027-06-30", false); err != nil || n != 2 {
		t.Fatalf("SetReviewDate = %d, %v; want 2", n, err)
	}
	want := map[string]Reason{"photos/c": ReasonUnreadable, "photosx/d": ReasonUndated, "other/e": ReasonUndated}
	if got := e.queue(today); !maps.Equal(got, want) {
		t.Errorf("queue = %v, want %v (an unreadable file stays queued after a bulk date)", got, want)
	}
	if v := rawQueryString(t, e.db, `SELECT review_date FROM files WHERE id = `+itoa(k["photos/a"].FileID)); v != "2030-01-01" {
		t.Errorf("photos/a review_date = %s, want its reviewed date kept", v)
	}
	if v := rawQueryString(t, e.db, `SELECT count(*) FROM reviews r JOIN review_batches b ON b.id = r.batch_id
		WHERE b.command = 'set-date'`); v != "2" {
		t.Errorf("set-date history rows = %s, want 2", v)
	}
	// Running it again finds nothing left to date.
	if n, err = e.s.SetReviewDate(e.ctx, scope, "2028-01-01", now, "again", false); err != nil || n != 0 {
		t.Errorf("second SetReviewDate = %d, %v; want 0", n, err)
	}
}

func TestRecordExportRollsBackOnWriteError(t *testing.T) {
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a", "h1")}})
	q, err := e.s.Queue(e.ctx, "2026-11-11", QueueFilter{})
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
