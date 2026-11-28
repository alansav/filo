package store

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

func TestMigrateFromV15SetsTheCursorsPastEverything(t *testing.T) {
	ctx := context.Background()
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	db := tempDB(t)
	e := &reviewEnv{t: t, ctx: ctx, s: mustOpen(t, Options{DBPath: db}), db: db}
	r, err := e.s.AddRoot(ctx, tree(t))
	if err != nil {
		t.Fatal(err)
	}
	e.root = r
	e.apply(Changes{New: []NewFile{f("a.txt", "ha")}})
	id := e.known()["a.txt"].FileID
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if _, err := e.s.SetFiles(ctx, []int64{id}, SetValues{Retention: "forever", SetRetention: true}, now,
		"2026-11-28", SetOptions{}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, e.s)
	rawExec(t, db, dropDecisions, `UPDATE meta SET value = '15' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	cur, err := s.Cursors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest, _ := s.Latest(ctx); cur != latest || cur.Reviews == 0 {
		t.Fatalf("cursors = %+v, want past everything (%+v)", cur, latest)
	}
	if evs, _ := s.ReviewsSince(ctx, cur.Reviews); len(evs) != 0 {
		t.Fatalf("decisions left to publish after the upgrade: %+v", evs)
	}
}

func TestDecisionsReplaceAndConflict(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	id, err := s.ApplySynced(ctx, SyncedChange{Add: &SyncedDir{SyncID: "s", RootID: root.ID, Dir: "", State: SyncedJoined,
		AddedUTC: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	dec := func(dev string, seq int64, value string) Decision {
		return Decision{Dev: dev, Seq: seq, FID: "f", Key: "retention", Value: value, Current: true}
	}
	// laptop:5 says 7y; desktop:3 says 10y, not having seen it: a conflict.
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Decisions: []Decision{dec("laptop", 5, "7y"),
		dec("desktop", 3, "10y")}, Replaces: [][]LineRef{nil, nil}}); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.CurrentDecisions(ctx, id, "f", "retention")
	if c, _ := s.Conflicts(ctx, id); len(cur) != 2 || len(c) != 1 {
		t.Fatalf("current %+v, conflicts %v", cur, c)
	}
	// tablet:9 replaces both: resolved.
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Decisions: []Decision{dec("tablet", 9, "8y")},
		Replaces: [][]LineRef{{{"laptop", 5}, {"desktop", 3}}}}); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.CurrentDecisions(ctx, id, "f", "retention")
	if c, _ := s.Conflicts(ctx, id); len(cur) != 1 || cur[0].Value != "8y" || len(c) != 0 {
		t.Fatalf("after resolving: current %+v, conflicts %v", cur, c)
	}
	// A line replacing one not read yet: when that one arrives it is not current.
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Decisions: []Decision{dec("tablet", 10, "9y")},
		Replaces: [][]LineRef{{{"tablet", 9}, {"phone", 2}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Decisions: []Decision{dec("phone", 2, "1d")},
		Replaces: [][]LineRef{nil}}); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.CurrentDecisions(ctx, id, "f", "retention")
	if len(cur) != 1 || cur[0].Value != "9y" {
		t.Fatalf("a line replaced ahead became current: %+v", cur)
	}
	// Equal values from two devices: two current, no conflict.
	if _, err := s.ApplySynced(ctx, SyncedChange{SyncedID: id, Decisions: []Decision{
		{Dev: "a", Seq: 1, FID: "g", Key: "date", Value: "2026-01-01"},
		{Dev: "b", Seq: 1, FID: "g", Key: "date", Value: "2026-01-01"}}, Replaces: [][]LineRef{nil, nil}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Conflicts(ctx, id); len(c) != 0 {
		t.Fatalf("equal values are a conflict: %v", c)
	}
	un, _ := s.Unapplied(ctx, id)
	if len(un) != 3 { // tablet:10, a:1, b:1
		t.Fatalf("unapplied = %+v", un)
	}
	if err := s.MarkApplied(ctx, id, []LineRef{{"tablet", 10}}); err != nil {
		t.Fatal(err)
	}
	if un, _ := s.Unapplied(ctx, id); len(un) != 2 {
		t.Fatalf("unapplied after marking = %+v", un)
	}
}

func TestPublishMovesTheCursorsOnce(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	id, _ := s.ApplySynced(ctx, SyncedChange{Add: &SyncedDir{SyncID: "s", RootID: root.ID, State: SyncedJoined,
		AddedUTC: "t"}})
	err := s.Publish(ctx, []SyncedChange{{SyncedID: id, Journals: []JournalUpdate{{Dev: "me",
		Lines: []JournalLine{{1, []byte("l1")}}, Seq: 1, Offset: 3}}, Decisions: []Decision{{Dev: "me", Seq: 1, FID: "f",
		Key: "date", Value: "2026-01-01", Applied: true}}, Replaces: [][]LineRef{nil}, Shared: map[string]int64{"f": 0}}},
		PublishCursors{Reviews: 7, TagChanges: 3, PolicyChanges: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Cursors(ctx); cur != (PublishCursors{7, 3, 2}) {
		t.Fatalf("cursors = %+v", cur)
	}
	if _, known, _ := s.SharedFile(ctx, id, "f"); !known {
		t.Fatal("the shared ID was not recorded")
	}
	if cur, _ := s.CurrentDecisions(ctx, id, "f", "date"); len(cur) != 1 || !cur[0].Applied {
		t.Fatalf("own decision: %+v", cur)
	}
}

func TestMatchFile(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("Sync/bills/a.pdf", "same"), f("Sync/copy/a.pdf", "same"), f("Sync/b.pdf", "hb"),
		f("elsewhere/b.pdf", "hb")}})
	k := e.known()
	sd := SyncedDir{RootID: e.root.ID, Dir: "Sync"}
	tests := []struct {
		name, hash, path, want string
	}{
		{"hash at its path", "same", "bills/a.pdf", "Sync/bills/a.pdf"},
		{"hash alone, one under the synced directory", "hb", "old/b.pdf", "Sync/b.pdf"},
		{"two copies, neither at the path", "same", "x/a.pdf", ""},
		{"no such content", "none", "a", ""},
		{"no hash: the one file at the path", "", "b.pdf", "Sync/b.pdf"},
		{"no hash, nothing at the path", "", "none.pdf", ""},
	}
	for _, tt := range tests {
		got, err := e.s.MatchFile(ctx, sd, tt.hash, tt.path)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if tt.want != "" {
			want = k[tt.want].FileID
		}
		if got != want {
			t.Errorf("%s: MatchFile = %d, want %d", tt.name, got, want)
		}
	}
	// bills/a.pdf moved to bills/moved.pdf: of two copies, the one once at the path is the one.
	e.apply(Changes{Close: []int64{k["Sync/bills/a.pdf"].PathID},
		Open: []OpenPath{{FileID: k["Sync/bills/a.pdf"].FileID, Path: "Sync/bills/moved.pdf"}}})
	if got, _ := e.s.MatchFile(ctx, sd, "same", "bills/a.pdf"); got != k["Sync/bills/a.pdf"].FileID {
		t.Errorf("a moved copy: MatchFile = %d, want %d", got, k["Sync/bills/a.pdf"].FileID)
	}
}

func TestJournalChangesAreNotPublished(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a.txt", "ha")}})
	cur, _ := e.s.Cursors(ctx)
	id := e.known()["a.txt"].FileID
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if _, err := e.s.SetFiles(ctx, []int64{id}, SetValues{Retention: "7y", SetRetention: true, FileDate: "2026-01-01",
		SetFileDate: true}, now, "2026-11-28", SetOptions{Command: CommandJournal}); err != nil {
		t.Fatal(err)
	}
	tag, _ := tags.Parse("sender:EDF")
	if changed, err := e.s.JournalTag(ctx, id, tag, true, now); err != nil || !changed {
		t.Fatalf("JournalTag: %v, %v", changed, err)
	}
	if evs, _ := e.s.ReviewsSince(ctx, cur.Reviews); len(evs) != 0 {
		t.Fatalf("a journal's retention is offered for publishing: %+v", evs)
	}
	if evs, _ := e.s.TagChangesSince(ctx, cur.TagChanges); len(evs) != 0 {
		t.Fatalf("a journal's tag is offered for publishing: %+v", evs)
	}
	if _, err := e.s.SetFiles(ctx, []int64{id}, SetValues{Retention: "10y", SetRetention: true}, now, "2026-11-28",
		SetOptions{Confirm: func(SetResult) bool { return true }}); err != nil {
		t.Fatal(err)
	}
	if evs, _ := e.s.ReviewsSince(ctx, cur.Reviews); len(evs) != 1 || evs[0].Retention != "10y" {
		t.Fatalf("a decision made here: %+v", evs)
	}
	if changed, _ := e.s.JournalTag(ctx, id, tag, false, now); !changed {
		t.Fatal("JournalTag did not remove the tag")
	}
}

func TestValues(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("a.txt", "ha"), f("b.txt", "hb")}})
	k := e.known()
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if _, err := e.s.SetFiles(ctx, []int64{k["a.txt"].FileID}, SetValues{Retention: "bills", SetRetention: true,
		FileDate: "2026-01-01", SetFileDate: true}, now, "2026-11-28", SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetPolicy(ctx, "bills", "7y", now); err != nil {
		t.Fatal(err)
	}
	v, ok, err := e.s.Values(ctx, k["a.txt"].FileID)
	if err != nil || !ok || v.Retention != "bills" || !v.Policy || v.PolicyValue != "7y" || v.FileDate != "2026-01-01" ||
		v.Hash != "ha" {
		t.Fatalf("Values = %+v, %v, %v", v, ok, err)
	}
	if !v.Present || v.Deleted {
		t.Fatalf("a present file: %+v", v)
	}
	if v, ok, _ := e.s.Values(ctx, k["b.txt"].FileID); !ok || v.Retention != "" || v.Policy {
		t.Fatalf("Values of an undecided file = %+v, %v", v, ok)
	}
	if _, ok, _ := e.s.Values(ctx, 9999); ok {
		t.Fatal("Values of no file")
	}
}

func TestConflictKeepsAFileFromDeletion(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t)
	e.apply(Changes{New: []NewFile{f("Sync/old.pdf", "ho")}})
	id := e.known()["Sync/old.pdf"].FileID
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if _, err := e.s.SetFiles(ctx, []int64{id}, SetValues{Retention: "2020-01-01", SetRetention: true}, now,
		"2026-11-28", SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.s.Candidates(ctx, "2026-11-28", Filter{}); len(c) != 1 {
		t.Fatalf("candidates before the conflict: %d", len(c))
	}
	sid, err := e.s.ApplySynced(ctx, SyncedChange{Add: &SyncedDir{SyncID: "s", RootID: e.root.ID, Dir: "Sync",
		State: SyncedJoined, AddedUTC: "t"}, Shared: map[string]int64{"f": id}, Decisions: []Decision{
		{Dev: "a", Seq: 1, FID: "f", Key: "retention", Value: "2020-01-01"},
		{Dev: "b", Seq: 1, FID: "f", Key: "retention", Value: "forever"},
		{Dev: "a", Seq: 2, FID: "f", Key: "tag:x:y", Value: "x:y"},
		{Dev: "b", Seq: 2, FID: "f", Key: "tag:x:y", Value: ""}}, Replaces: [][]LineRef{nil, nil, nil, nil}})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := e.s.Candidates(ctx, "2026-11-28", Filter{}); len(c) != 0 {
		t.Fatalf("a file whose retention is in conflict is a candidate: %d", len(c))
	}
	items, _ := e.s.FilesItems(ctx, "2026-11-28", Filter{})
	if len(items) != 1 || items[0].Conflict != "retention tags" {
		t.Fatalf("conflict cell = %q", items[0].Conflict)
	}
	// One tag added on two devices, spelled in other cases: they agree.
	if _, err := e.s.ApplySynced(ctx, SyncedChange{SyncedID: sid, Decisions: []Decision{
		{Dev: "a", Seq: 3, FID: "f", Key: "tag:sender:edf", Value: "sender:EDF"},
		{Dev: "b", Seq: 3, FID: "f", Key: "tag:sender:edf", Value: "sender:edf"}}, Replaces: [][]LineRef{nil, nil}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.s.ConflictedFiles(ctx); len(c[id]) != 2 {
		t.Fatalf("a tag spelled in two cases is a conflict: %v", c[id])
	}
}

func TestPolicyChangesForPublishing(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	cur, _ := s.Cursors(ctx)
	if err := s.AddPolicy(ctx, "bills", "7y", now); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Latest(ctx)
	if _, err := s.SetPolicy(ctx, "bills", "10y", now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPolicyChangesJournal(ctx, before.PolicyChanges); err != nil {
		t.Fatal(err)
	}
	evs, err := s.PolicyChangesSince(ctx, cur.PolicyChanges)
	if err != nil || len(evs) != 1 || evs[0].Kind != "add" || evs[0].Policy != "bills" || evs[0].New != "7y" {
		t.Fatalf("policy changes = %+v, %v (the journal's set left out)", evs, err)
	}
}

func TestIndexWriter(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	ix, err := s.AddTagIndex(ctx, root.ID, "", "index", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ix.Writer != "" {
		t.Fatalf("a new index's writer = %q, want this device", ix.Writer)
	}
	if err := s.SetIndexWriter(ctx, ix.ID, "laptop-id"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TagIndex(ctx, ix.ID); got.Writer != "laptop-id" {
		t.Fatalf("writer = %q", got.Writer)
	}
}

func TestCurrentByKey(t *testing.T) {
	ctx := context.Background()
	s, root := syncedTestStore(t)
	id, _ := s.ApplySynced(ctx, SyncedChange{Add: &SyncedDir{SyncID: "s", RootID: root.ID, State: SyncedJoined,
		AddedUTC: "t"}, Decisions: []Decision{
		{Dev: "a", Seq: 1, FID: SettingsFID, Key: "policy:bills", Value: "7y"},
		{Dev: "b", Seq: 1, FID: SettingsFID, Key: "policy:bills", Value: "10y"},
		{Dev: "a", Seq: 2, FID: SettingsFID, Key: "always:h", Value: "1"},
		{Dev: "a", Seq: 3, FID: "other", Key: "date", Value: "2026-01-01"}}, Replaces: [][]LineRef{nil, nil, nil, nil}})
	got, err := s.CurrentByKey(ctx, id, SettingsFID)
	if err != nil || len(got) != 2 || len(got["policy:bills"]) != 2 || len(got["always:h"]) != 1 {
		t.Fatalf("CurrentByKey = %+v, %v", got, err)
	}
}
