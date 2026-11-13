package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestKeep(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	daily := func(from, to string) []string { // 10:00 each day, inclusive
		var out []string
		for d := at(from + " 10:00"); !d.After(at(to + " 10:00")); d = d.AddDate(0, 0, 1) {
			out = append(out, d.Format("2006-01-02 15:04"))
		}
		return out
	}
	tests := []struct {
		name string
		now  string
		have []string
		keep []string
	}{
		{"none", "2026-11-13 12:00", nil, nil},
		{"everything from the last 24 hours, across midnight", "2026-11-13 12:00",
			[]string{"2026-11-12 13:00", "2026-11-12 23:30", "2026-11-13 00:10", "2026-11-13 01:00", "2026-11-13 02:00",
				"2026-11-13 11:00"},
			[]string{"2026-11-12 13:00", "2026-11-12 23:30", "2026-11-13 00:10", "2026-11-13 01:00", "2026-11-13 02:00",
				"2026-11-13 11:00"}},
		{"older than a day: the newest of the day", "2026-11-13 12:00",
			[]string{"2026-11-12 08:00", "2026-11-12 10:00", "2026-11-13 09:00", "2026-11-13 10:00", "2026-11-13 11:00"},
			[]string{"2026-11-12 10:00", "2026-11-13 09:00", "2026-11-13 10:00", "2026-11-13 11:00"}},
		// 11-12 to 11-06 are the seven days; week 45 (11-02 to 11-08) is covered by them; then the newest of weeks
		// 44, 43 and 42; 10-13 is over 31 days old.
		{"seven days, then weeks, up to a month", "2026-11-13 12:00", daily("2026-10-10", "2026-11-13"),
			append(daily("2026-11-06", "2026-11-13"), "2026-11-01 10:00", "2026-10-25 10:00", "2026-10-18 10:00")},
		{"nothing older than a month, except the newest three", "2026-11-13 12:00",
			[]string{"2026-08-01 10:00", "2026-08-02 10:00", "2026-08-03 10:00", "2026-08-04 10:00"},
			[]string{"2026-08-02 10:00", "2026-08-03 10:00", "2026-08-04 10:00"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var have []time.Time
			for _, s := range tc.have {
				have = append(have, at(s))
			}
			kept := keep(have, at(tc.now), loc)
			var got []string
			for _, h := range have {
				if kept[h] {
					got = append(got, h.In(loc).Format("2006-01-02 15:04"))
				}
			}
			slices.Sort(got)
			want := slices.Clone(tc.keep)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("kept %v, want %v", got, want)
			}
		})
	}
}

func TestKeepMany(t *testing.T) {
	// Two years of four writes a day: the last day's four, seven days and the weeks of a month.
	var have []time.Time
	start := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC)
	for d := range 730 {
		for h := range 4 {
			have = append(have, start.AddDate(0, 0, d).Add(time.Duration(h)*time.Hour))
		}
	}
	now := have[len(have)-1].Add(time.Hour)
	if n := len(keep(have, now, time.UTC)); n < 4+snapDays || n > 4+snapDays+5 {
		t.Errorf("kept %d", n)
	}
}

func TestSnapName(t *testing.T) {
	now := time.Date(2026, 11, 13, 14, 2, 3, 456_000_000, time.UTC)
	name := snapName(now)
	if name != "filo-20261113T140203.456Z.db" {
		t.Errorf("name = %s", name)
	}
	if got, ok := snapTime(name); !ok || !got.Equal(now) {
		t.Errorf("snapTime(%s) = %v, %v", name, got, ok)
	}
	for _, other := range []string{"filo-v5-before-upgrade-20261113T140203.456Z.db", name + ".tmp", "notes.txt",
		"filo-20261113T140203Z.db", "filo-20261313T140203.456Z.db"} {
		if _, ok := snapTime(other); ok {
			t.Errorf("snapTime(%s) matched", other)
		}
	}
}

func snapshotNames(t *testing.T, db string) []string {
	t.Helper()
	entries, err := os.ReadDir(SnapshotDir(db))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestSnapshotOnClose(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	dir := SnapshotDir(db)

	// Creating the register is a write: one snapshot.
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	names := snapshotNames(t, db)
	if len(names) != 1 || !strings.HasPrefix(names[0], "filo-") {
		t.Fatalf("snapshots after create = %v", names)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, names[0]): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, want)
		}
	}

	// A run that only reads takes none.
	s := mustOpen(t, Options{DBPath: db})
	if _, err := s.Roots(ctx); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	if n := len(snapshotNames(t, db)); n != 1 {
		t.Errorf("%d snapshots after a read-only run, want 1", n)
	}

	// A write takes one holding it; a temporary file left by an interrupted run is removed, other files are not.
	stale := filepath.Join(dir, snapName(time.Now().Add(-time.Hour))+tmpSuffix)
	keepers := []string{filepath.Join(dir, "notes.txt"), filepath.Join(dir, "filo-v5-before-upgrade-x.db")}
	for _, p := range append(keepers, stale) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s = mustOpen(t, Options{DBPath: db})
	if _, err := s.exec(ctx, `INSERT INTO meta (key, value) VALUES ('test', 'written')`); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale temporary snapshot still there")
	}
	for _, p := range keepers {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	var latest string
	for _, n := range snapshotNames(t, db) {
		if _, ok := snapTime(n); ok && n > latest {
			latest = n
		}
	}
	if v := rawQueryString(t, filepath.Join(dir, latest), `SELECT value FROM meta WHERE key = 'test'`); v != "written" {
		t.Errorf("latest snapshot holds %q, want the write", v)
	}
	info, err := func() (SnapshotInfo, error) {
		s := mustOpen(t, Options{DBPath: db})
		defer mustClose(t, s)
		return s.Snapshots(ctx, time.Now())
	}()
	if err != nil || info.Count != 2 || info.PreUpgrade != 1 {
		t.Errorf("Snapshots = %+v, %v; want 2 rotated and 1 from before an upgrade", info, err)
	}
}

func TestSnapshotFailureKeepsTheWrite(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	// The snapshot directory's place is taken by a file, so no snapshot can be written.
	if err := os.RemoveAll(SnapshotDir(db)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SnapshotDir(db), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := mustOpen(t, Options{DBPath: db})
	if _, err := s.exec(ctx, `INSERT INTO meta (key, value) VALUES ('test', 'written')`); err != nil {
		t.Fatal(err)
	}
	err := s.Close()
	if err == nil || !strings.Contains(err.Error(), "register snapshot failed") {
		t.Fatalf("Close = %v, want a snapshot failure", err)
	}
	if v := rawQueryString(t, db, `SELECT value FROM meta WHERE key = 'test'`); v != "written" {
		t.Errorf("the write was lost: %q", v)
	}
	// The lock was released despite the failure.
	mustClose(t, mustOpen(t, Options{DBPath: db}))
}

// TestNoSnapshotForARolledBackRun: a run whose only write was rolled back (a failed or interrupted scan) changed
// nothing, so it takes no snapshot and records no write.
func TestNoSnapshotForARolledBackRun(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	before := snapshotNames(t, db)
	s := mustOpen(t, Options{DBPath: db})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('test', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	if after := snapshotNames(t, db); !slices.Equal(after, before) {
		t.Errorf("snapshots %v after a rolled-back run, want %v", after, before)
	}
}

// TestWritesGoThroughTheHelpers guards the rule that every write in this package goes through commit or exec, so a
// run that changes the register always gets its snapshot. Creating and upgrading the database (store.go) and Close's
// own record of the write time (snapshot.go) are the exceptions.
func TestWritesGoThroughTheHelpers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if f != "store.go" && strings.Contains(src, ".Commit()") {
			t.Errorf("%s commits a transaction itself; use s.commit(tx)", f)
		}
		if n := strings.Count(src, "s.db.ExecContext("); n > 0 && f != "store.go" && !(f == "snapshot.go" && n == 1) {
			t.Errorf("%s writes with s.db.ExecContext; use s.exec", f)
		}
	}
}

func TestSnapshotsCountsFutureApart(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if err := os.Mkdir(SnapshotDir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour)} {
		if err := os.WriteFile(filepath.Join(SnapshotDir(db), snapName(at)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := s.Snapshots(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if info.Count != 0 || info.Future != 2 || !info.Latest.IsZero() {
		t.Errorf("Snapshots = %+v; want none current and two in the future", info)
	}
}

func TestPruneReportsRemovalErrors(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	gone := now.AddDate(0, -3, 0) // old enough to be pruned, but its file is not there
	have := []time.Time{gone, now.Add(-3 * time.Hour), now.Add(-2 * time.Hour), now.Add(-time.Hour)}
	if err := prune(t.TempDir(), have, now, time.UTC); err == nil {
		t.Error("prune returned no error for a snapshot it could not remove")
	}
}
