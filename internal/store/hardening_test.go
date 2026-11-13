package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSchemaIsNotTrusted(t *testing.T) {
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	var v int
	if err := s.db.QueryRow(`PRAGMA trusted_schema`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("trusted_schema = %d, want 0", v)
	}
}

func TestLockSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no flock on windows")
	}
	db := tempDB(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(target, db+".lock"); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(context.Background(), Options{DBPath: db}); err == nil {
		s.Close()
		t.Fatal("Open followed a symlinked lock file")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the symlink's target was created")
	}
}

func TestSnapshotDirChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes and symlinks differ on windows")
	}
	ctx := context.Background()
	write := func(t *testing.T, s *Store) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('test', 'x')
			ON CONFLICT (key) DO UPDATE SET value = value || 'x'`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a symlink in its place is refused", func(t *testing.T) {
		db := tempDB(t)
		target := t.TempDir()
		if err := os.Symlink(target, SnapshotDir(db)); err != nil {
			t.Fatal(err)
		}
		s := mustOpen(t, Options{DBPath: db})
		write(t, s)
		if err := s.Close(); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("Close = %v, want a refused snapshot directory", err)
		}
		if entries, _ := os.ReadDir(target); len(entries) > 0 {
			t.Errorf("snapshots written through the symlink: %v", entries)
		}
	})

	t.Run("an open directory is made private", func(t *testing.T) {
		db := tempDB(t)
		if err := os.Mkdir(SnapshotDir(db), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(SnapshotDir(db), 0o755); err != nil { // whatever the umask
			t.Fatal(err)
		}
		mustClose(t, mustOpen(t, Options{DBPath: db}))
		if fi, err := os.Stat(SnapshotDir(db)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("snapshot directory mode %v, %v; want 0700", fi.Mode().Perm(), err)
		}
	})

	t.Run("a snapshot that cannot be written shows in the report", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permissions do not hold back root")
		}
		db := tempDB(t)
		mustClose(t, mustOpen(t, Options{DBPath: db}))
		if err := os.Chmod(SnapshotDir(db), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(SnapshotDir(db), 0o700) })
		s := mustOpen(t, Options{DBPath: db})
		write(t, s)
		if err := s.Close(); err == nil {
			t.Fatal("Close succeeded with an unwritable snapshot directory")
		}
		s = mustOpen(t, Options{DBPath: db})
		defer mustClose(t, s)
		info, err := s.Snapshots(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !info.Latest.Before(info.LatestWrite) {
			t.Errorf("latest snapshot %v, latest write %v: the failed snapshot does not show", info.Latest,
				info.LatestWrite)
		}
	})
}

func TestSnapshotBeforeUpgradeOncePerVersion(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if err := os.Mkdir(SnapshotDir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(SnapshotDir(db), "filo-v5-before-upgrade-20260101T000000.000Z.db.tmp")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := snapshotBeforeUpgrade(ctx, s.db, db, 5, time.Now().Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var copies []string
	for _, n := range snapshotNames(t, db) {
		if strings.HasPrefix(n, "filo-v5-before-upgrade-") {
			copies = append(copies, n)
		}
	}
	if len(copies) != 1 || !strings.HasSuffix(copies[0], "Z.db") {
		t.Errorf("before-upgrade copies = %v, want one (and no stale temporary file)", copies)
	}
}

func TestKeepFutureSnapshots(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 10, 0, 0, 0, time.UTC) }
	future := day(11, 20)
	have := []time.Time{future, day(8, 1), day(8, 2), day(8, 3), day(8, 4)}
	kept := keep(have, now, time.UTC)
	var got []time.Time
	for _, h := range have {
		if kept[h] {
			got = append(got, h)
		}
	}
	want := []time.Time{future, day(8, 2), day(8, 3), day(8, 4)}
	if !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("kept %v, want %v: a future snapshot is kept but takes none of the newest three places", got, want)
	}
}
