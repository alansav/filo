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
	snapSkew   = time.Minute         // a snapshot further ahead of now than this is dated in the future
)

// metaLastWrite records when a run last changed the register, written by Close just before its snapshot.
const metaLastWrite = "last_write_utc"

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

// writeSnapshot writes a consistent copy of db to dir/name. The temporary file is created empty and private first, so
// the register is never readable by others while it is copied (VACUUM INTO accepts an empty file and would
// otherwise create it with the umask's mode). VACUUM INTO does not flush what it writes, so the copy is synced before
// it is renamed into place, and the directory after, so a power cut cannot leave a torn newest snapshot once older
// ones are pruned. An existing file of the final name is never replaced.
func writeSnapshot(ctx context.Context, db *sql.DB, dir, name string) error {
	final := filepath.Join(dir, name)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("%s already exists", final)
	}
	tmp := final + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("flush %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// snapshotDir makes sure dir is a directory of the user's own, private, creating it if need be. A symlink or a file in
// its place, or a directory someone else owns (possible with --db in a shared directory such as /tmp), is refused:
// snapshots hold every file name, and pruning removes files there.
func snapshotDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.Mkdir(dir, 0o700)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink or a file is in its place)", dir)
	}
	if !ownedByUser(fi) {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// openSnapshotDir makes sure the snapshot directory is fit to use (see snapshotDir) and lists it once for the
// callers: it removes the temporary files an interrupted snapshot left behind, rotated or taken before an upgrade
// (the caller holds the register's lock, so no other run is writing one), and returns the other entries.
func openSnapshotDir(dir string) ([]fs.DirEntry, error) {
	if err := snapshotDir(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, snapPrefix) && strings.HasSuffix(n, ".db"+tmpSuffix) && e.Type().IsRegular() {
			os.Remove(filepath.Join(dir, n))
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// errPrune marks a failure to remove old snapshots, after the new one was written.
var errPrune = errors.New("pruning old snapshots failed (the new snapshot was written)")

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir flushes a directory's entries (the rename). Best effort: not every platform can sync a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// Snapshot writes a snapshot of the register taken at now, then prunes the older ones by the rotation. Nothing is
// pruned unless the new snapshot was written.
func (s *Store) Snapshot(ctx context.Context, now time.Time) error {
	dir := SnapshotDir(s.dbPath)
	entries, err := openSnapshotDir(dir) // one listing serves the clean-up and the rotation
	if err != nil {
		return err
	}
	var times []time.Time
	for _, e := range entries {
		if t, ok := snapTime(e.Name()); ok && e.Type().IsRegular() {
			times = append(times, t)
		}
	}
	name := snapName(now)
	if err := writeSnapshot(ctx, s.db, dir, name); err != nil {
		return err
	}
	t, _ := snapTime(name)
	if err := prune(dir, append(times, t), now, time.Local); err != nil {
		return fmt.Errorf("%w: %w", errPrune, err)
	}
	return nil
}

// prune removes the rotated snapshots (at times, in dir) the rotation no longer keeps.
func prune(dir string, times []time.Time, now time.Time, loc *time.Location) error {
	var errs []error
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
// Nothing else older than snapMaxAge is kept. A snapshot dated in the future (the clock was ahead when it was taken)
// is kept, since it cannot be placed, but it takes no place among the newest: it holds an older state than its date.
func keep(times []time.Time, now time.Time, loc *time.Location) map[time.Time]bool {
	kept := map[time.Time]bool{}
	var ts []time.Time
	for _, t := range times {
		if t.After(now.Add(snapSkew)) {
			kept[t] = true
		} else {
			ts = append(ts, t)
		}
	}
	slices.SortFunc(ts, func(a, b time.Time) int { return b.Compare(a) }) // newest first
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
	Count       int       // rotated snapshots, not counting those dated in the future
	Latest      time.Time // the newest not dated in the future; zero if none
	Future      int       // dated in the future: the clock was ahead when they were taken
	PreUpgrade  int       // snapshots taken before a schema upgrade, never rotated away
	LatestWrite time.Time // when a run last changed the register; zero if not known
	Problem     string    // why the directory could not be read; "" if it could (or does not exist yet)
}

// Snapshots describes the register's snapshots as of now.
func (s *Store) Snapshots(ctx context.Context, now time.Time) (SnapshotInfo, error) {
	info := SnapshotInfo{Dir: SnapshotDir(s.dbPath)}
	entries, err := os.ReadDir(info.Dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		info.Problem = ErrDetail(err) // the report says so; it is not a reason to fail the report
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if t, ok := snapTime(e.Name()); ok {
			if t.After(now.Add(snapSkew)) {
				info.Future++
				continue
			}
			info.Count++
			if t.After(info.Latest) {
				info.Latest = t
			}
		} else if strings.Contains(e.Name(), "-before-upgrade-") && strings.HasSuffix(e.Name(), ".db") {
			info.PreUpgrade++
		}
	}
	var latest string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaLastWrite).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return info, fmt.Errorf("latest write: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, latest); err == nil {
		info.LatestWrite = t
	}
	return info, nil
}

// recordWrite stores when this run changed the register, at the precision of snapshot names, so the snapshot taken
// next (with the same time) is never older than it.
func (s *Store) recordWrite(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, metaLastWrite,
		now.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano))
	return err
}

// ErrDetail is an error without the path it names, for messages that show the path already.
func ErrDetail(err error) string {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

// snapshotBeforeUpgrade keeps a copy of a database about to be upgraded from version v. It is never rotated away.
// One copy per version is enough: if an upgrade keeps failing (an hourly timer opening an old database), no further
// copies pile up.
func snapshotBeforeUpgrade(ctx context.Context, db *sql.DB, dbPath string, v int, now time.Time) error {
	dir := SnapshotDir(dbPath)
	prefix := fmt.Sprintf("filo-v%d-before-upgrade-", v)
	entries, err := openSnapshotDir(dir)
	if err != nil {
		return fmt.Errorf("keep a copy before upgrading it: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), snapSuffix) && e.Type().IsRegular() {
			return nil
		}
	}
	if err := writeSnapshot(ctx, db, dir, prefix+now.UTC().Format(snapLayout)+snapSuffix); err != nil {
		return fmt.Errorf("keep a copy before upgrading it: %w", err)
	}
	return nil
}
