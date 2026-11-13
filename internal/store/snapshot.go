package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Snapshots of the register (docs/design.md, Register snapshots). After every run that wrote to the register, a
// consistent copy is written with VACUUM INTO next to the database, in <db>.snapshots/, and older copies are pruned
// by a rotation like Borg's. VACUUM INTO is used rather than copying the file because the database is in WAL mode:
// filo.db alone can miss commits still in the -wal file, or catch a page being written.

const (
	snapPrefix = "filo-"
	snapSuffix = "Z.db"
	snapLayout = "20060102T150405.000" // UTC; the name ends in Z
	tmpSuffix  = ".tmp"

	// Rotation (see keep). Snapshots are a short-term safety net: the long-term copies are the backups of them.
	snapRecent = 24 * time.Hour      // every snapshot this recent is kept
	snapMaxAge = 31 * 24 * time.Hour // nothing older is kept, except the newest snapNewest
	snapNewest = 3                   // kept whatever their age, so a write after a long gap keeps the state before it
	snapDays   = 7                   // then the newest of each of this many days with snapshots
)

// SnapshotDir is where the snapshots of the database at dbPath are kept.
func SnapshotDir(dbPath string) string { return dbPath + ".snapshots" }

func snapName(t time.Time) string { return snapPrefix + t.UTC().Format(snapLayout) + snapSuffix }

// snapTime reads the time from a rotated snapshot's name. Only names filo writes match: a pre-upgrade snapshot, a
// temporary file or anything else in the directory does not, and so is never pruned.
func snapTime(name string) (time.Time, bool) {
	s, ok := strings.CutPrefix(name, snapPrefix)
	if !ok {
		return time.Time{}, false
	}
	if s, ok = strings.CutSuffix(s, snapSuffix); !ok {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(snapLayout, s, time.UTC)
	if err != nil || snapName(t) != name {
		return time.Time{}, false
	}
	return t, true
}

// writeSnapshot writes a consistent copy of db to dir/name: VACUUM INTO a temporary file, made private, then renamed,
// so nothing ever sees a half-written snapshot. An existing file of that name is never replaced.
func writeSnapshot(ctx context.Context, db *sql.DB, dir, name string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("%s already exists", final)
	}
	tmp := final + tmpSuffix
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Snapshot writes a snapshot of the register taken at now, then prunes the older ones by the rotation. Nothing is
// pruned unless the new snapshot was written.
func (s *Store) Snapshot(ctx context.Context, now time.Time) error {
	dir := SnapshotDir(s.dbPath)
	if err := writeSnapshot(ctx, s.db, dir, snapName(now)); err != nil {
		return err
	}
	return prune(dir, now, time.Local)
}

// prune removes the rotated snapshots the rotation no longer keeps, and temporary files left by an interrupted run
// (the caller holds the lock, so no other run is writing one).
func prune(dir string, now time.Time, loc *time.Location) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var times []time.Time
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if base, isTmp := strings.CutSuffix(name, tmpSuffix); isTmp {
			if _, ok := snapTime(base); ok && e.Type().IsRegular() {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if t, ok := snapTime(name); ok && e.Type().IsRegular() {
			times = append(times, t)
		}
	}
	kept := keep(times, now, loc)
	for _, t := range times {
		if !kept[t] {
			if err := os.Remove(filepath.Join(dir, snapName(t))); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// keep returns the snapshot times the rotation keeps, as of now:
//   - every snapshot from the last snapRecent (a rolling 24 hours, so working past midnight loses nothing);
//   - the newest snapNewest, whatever their age;
//   - up to snapMaxAge old: the newest of each of the snapDays most recent days that have older snapshots, then the
//     newest of each ISO week (local time).
//
// Nothing else older than snapMaxAge is kept.
func keep(times []time.Time, now time.Time, loc *time.Location) map[time.Time]bool {
	ts := slices.Clone(times)
	slices.SortFunc(ts, func(a, b time.Time) int { return b.Compare(a) }) // newest first
	kept := map[time.Time]bool{}
	for i, t := range ts {
		if i < snapNewest || now.Sub(t) < snapRecent {
			kept[t] = true
		}
	}
	day := func(t time.Time) string { return t.In(loc).Format("2006-01-02") }
	week := func(t time.Time) string {
		y, w := t.In(loc).ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	}
	days, weeks := map[string]bool{}, map[string]bool{}
	for _, t := range ts {
		age := now.Sub(t)
		if age < snapRecent {
			continue
		}
		if age > snapMaxAge {
			break
		}
		d, w := day(t), week(t)
		switch {
		case days[d] || (len(days) >= snapDays && weeks[w]):
			// not the newest of its day, or a later day already stands for its week
		case len(days) < snapDays:
			days[d], weeks[w] = true, true
			kept[t] = true
		default:
			weeks[w] = true
			kept[t] = true
		}
	}
	return kept
}

// SnapshotInfo is what the report shows about the snapshots.
type SnapshotInfo struct {
	Dir         string
	Count       int       // rotated snapshots
	Latest      time.Time // zero if none
	PreUpgrade  int       // snapshots taken before a schema upgrade, never rotated away
	LatestWrite time.Time // the latest write the register records (scan, export, update, deletion); zero if none
	Problem     string    // why the directory could not be read; "" if it could (or does not exist yet)
}

// Snapshots describes the register's snapshots.
func (s *Store) Snapshots(ctx context.Context) (SnapshotInfo, error) {
	info := SnapshotInfo{Dir: SnapshotDir(s.dbPath)}
	entries, err := os.ReadDir(info.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		info.Problem = errText(err) // the report says so; it is not a reason to fail the report
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if t, ok := snapTime(e.Name()); ok {
			info.Count++
			if t.After(info.Latest) {
				info.Latest = t
			}
		} else if strings.Contains(e.Name(), "-before-upgrade-") && strings.HasSuffix(e.Name(), ".db") {
			info.PreUpgrade++
		}
	}
	var latest sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT max(t) FROM (
		SELECT max(coalesce(finished_utc, started_utc)) AS t FROM scans
		UNION ALL SELECT max(created_utc) FROM exports
		UNION ALL SELECT max(created_utc) FROM review_batches
		UNION ALL SELECT max(deleted_utc) FROM files)`).Scan(&latest); err != nil {
		return info, fmt.Errorf("latest write: %w", err)
	}
	if latest.Valid {
		if t, err := time.Parse(time.RFC3339Nano, latest.String); err == nil {
			info.LatestWrite = t.Truncate(time.Millisecond) // the precision of snapshot names
		}
	}
	return info, nil
}

func errText(err error) string {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

// wrote reports whether this connection changed anything since it was opened. The store has a single connection,
// so this covers the whole run.
func (s *Store) wrote(ctx context.Context) (bool, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// snapshotBeforeUpgrade keeps a copy of a database about to be upgraded from version v. It is never rotated away.
func snapshotBeforeUpgrade(ctx context.Context, db *sql.DB, dbPath string, v int, now time.Time) error {
	name := fmt.Sprintf("filo-v%d-before-upgrade-%s%s", v, now.UTC().Format(snapLayout), snapSuffix)
	if err := writeSnapshot(ctx, db, SnapshotDir(dbPath), name); err != nil {
		return fmt.Errorf("keep a copy before upgrading it: %w", err)
	}
	return nil
}
