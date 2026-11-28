package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Synced directories, the devices sharing them and their journals, as this register knows them (docs/design.md,
// Sharing decisions between devices). Package share reads and writes the journals on the disk; this is the
// register's side.

// The states of a synced directory on this device.
const (
	SyncedJoined  = "joined"  // this device shares decisions there
	SyncedIgnored = "ignored" // a mark the person chose not to join: not reported again
)

// ErrSyncedExists is a synced directory (by its ID) this register has already.
var ErrSyncedExists = errors.New("this synced directory is known already")

// SyncedDir is a synced directory on this device: its ID from the mark, where it is, and the mark's line.
type SyncedDir struct {
	ID       int64
	SyncID   string
	RootID   int64
	RootPath string
	Dir      string // relative to the root, '/'-separated; "" = the root itself
	State    string
	Mark     []byte // the mark's line, without its newline
	AddedUTC string
}

// Abs is the synced directory's absolute path.
func (d SyncedDir) Abs() string { return AbsPath(d.RootPath, d.Dir) }

// Contains reports whether rel (relative to root rootID) is in the synced directory, or is it.
func (d SyncedDir) Contains(rootID int64, rel string) bool {
	return rootID == d.RootID && (d.Dir == "" || rel == d.Dir || strings.HasPrefix(rel, d.Dir+"/"))
}

// FoundMark is a mark a scan saw that this device has neither joined nor ignored, or a .filo with no readable mark
// (SyncID "", Problem says why).
type FoundMark struct {
	RootID   int64
	RootPath string
	Dir      string
	SyncID   string
	Problem  string
	FoundUTC string
}

// Abs is the absolute path of the directory holding the mark.
func (m FoundMark) Abs() string { return AbsPath(m.RootPath, m.Dir) }

// KnownDevice is a device met in a journal: its key and name from its line 1, and whether this device trusts it.
type KnownDevice struct {
	Dev          string
	Key          string // its public key, base64
	Name         string
	CreatedUTC   string
	TrustedUTC   string // "" = not trusted
	VouchedBy    string // the device whose trust line made it trusted; "" = trusted at a join, or not trusted
	RetiredSeq   int64  // > 0: retired; its lines after this one are refused
	RetiredBy    string
	SnoozedUntil string // the question whether to retire it is not asked again before this time
}

// Trusted reports whether its lines are accepted.
func (d KnownDevice) Trusted() bool { return d.TrustedUTC != "" }

// JournalState is what this register has read of one device's journal in one synced directory (its own too).
type JournalState struct {
	Dev     string
	Seq     int64  // the last line accepted (0: none)
	Offset  int64  // the byte just after it in the journal file
	Last    []byte // that line
	Problem string // why reading stopped, if it did
	SeenUTC string // when its status file says that device last ran there
	Applied map[string]int64
}

// JournalUpdate is what reading a journal (or writing this device's) added: the new lines, in order, and the state
// after them.
type JournalUpdate struct {
	Dev     string
	Lines   []JournalLine
	Seq     int64
	Offset  int64
	Problem string
	SeenUTC string
	Applied map[string]int64
}

// JournalLine is one line as the register keeps it.
type JournalLine struct {
	Seq int64
	Raw []byte
}

// SyncedChange is one change to the register's side of sharing, made in one transaction: a synced directory added
// (Add, with SyncID set) or one already known (SyncedID), the devices to write, and the journals read or written.
type SyncedChange struct {
	Add      *SyncedDir
	SyncedID int64
	Devices  []KnownDevice
	Journals []JournalUpdate
}

// Empty reports whether the change writes nothing.
func (c SyncedChange) Empty() bool {
	return c.Add == nil && len(c.Devices) == 0 && len(c.Journals) == 0
}

// ApplySynced writes c and returns the synced directory's ID. Lines already kept are not written again; a line
// kept with other bytes under the same number is refused (only a copied register could do that).
func (s *Store) ApplySynced(ctx context.Context, c SyncedChange) (int64, error) {
	if c.Empty() {
		return c.SyncedID, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id := c.SyncedID
	if c.Add != nil {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM synced_dirs WHERE sync_id = ?`,
			c.Add.SyncID).Scan(&n); err != nil {
			return 0, err
		}
		if n > 0 {
			return 0, ErrSyncedExists
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO synced_dirs (sync_id, root_id, dir, state, mark, added_utc)
			VALUES (?, ?, ?, ?, ?, ?)`, c.Add.SyncID, c.Add.RootID, c.Add.Dir, c.Add.State, c.Add.Mark, c.Add.AddedUTC)
		if err != nil {
			return 0, fmt.Errorf("add the synced directory: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM found_marks WHERE root_id = ? AND dir = ?`, c.Add.RootID,
			c.Add.Dir); err != nil {
			return 0, err
		}
	}
	for _, d := range c.Devices {
		if _, err := tx.ExecContext(ctx, `INSERT INTO devices (dev, key, name, created_utc, trusted_utc, vouched_by,
			retired_seq, retired_by, snoozed_until) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dev) DO UPDATE SET key = excluded.key, name = excluded.name,
			created_utc = excluded.created_utc, trusted_utc = excluded.trusted_utc, vouched_by = excluded.vouched_by,
			retired_seq = excluded.retired_seq, retired_by = excluded.retired_by,
			snoozed_until = excluded.snoozed_until`,
			d.Dev, d.Key, d.Name, nullString(d.CreatedUTC), nullString(d.TrustedUTC), nullString(d.VouchedBy),
			nullInt(d.RetiredSeq), nullString(d.RetiredBy), nullString(d.SnoozedUntil)); err != nil {
			return 0, fmt.Errorf("write device %s: %w", d.Dev, err)
		}
	}
	for _, j := range c.Journals {
		for _, l := range j.Lines {
			var have []byte
			err := tx.QueryRowContext(ctx, `SELECT raw FROM journal_lines WHERE synced_id = ? AND dev = ? AND seq = ?`,
				id, j.Dev, l.Seq).Scan(&have)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := tx.ExecContext(ctx, `INSERT INTO journal_lines (synced_id, dev, seq, raw) VALUES (?, ?, ?, ?)`,
					id, j.Dev, l.Seq, l.Raw); err != nil {
					return 0, err
				}
			case err != nil:
				return 0, err
			case !slices.Equal(have, l.Raw):
				return 0, fmt.Errorf("line %d of %s's journal is kept already with other bytes", l.Seq, j.Dev)
			}
		}
		applied, err := json.Marshal(j.Applied)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO journals (synced_id, dev, seq, byte_offset, problem, seen_utc,
			applied) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (synced_id, dev) DO UPDATE SET seq = excluded.seq,
			byte_offset = excluded.byte_offset, problem = excluded.problem, seen_utc = excluded.seen_utc,
			applied = excluded.applied`, id, j.Dev, j.Seq, j.Offset, nullString(j.Problem), nullString(j.SeenUTC),
			string(applied)); err != nil {
			return 0, err
		}
	}
	if err := s.commit(tx); err != nil {
		return 0, err
	}
	return id, nil
}

// SyncedDirs returns the synced directories this register knows, joined and ignored, by path.
func (s *Store) SyncedDirs(ctx context.Context) ([]SyncedDir, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.sync_id, d.root_id, r.path, d.dir, d.state, d.mark, d.added_utc
		FROM synced_dirs d JOIN roots r ON r.id = d.root_id ORDER BY r.path, d.dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncedDir
	for rows.Next() {
		var d SyncedDir
		if err := rows.Scan(&d.ID, &d.SyncID, &d.RootID, &d.RootPath, &d.Dir, &d.State, &d.Mark, &d.AddedUTC); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RemoveSynced forgets a synced directory: this device leaves it (or no longer ignores its mark). Its journals and
// the lines kept from them go with it; nothing on the disk changes.
func (s *Store) RemoveSynced(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM journal_lines WHERE synced_id = ?`, `DELETE FROM journals WHERE synced_id = ?`,
		`DELETE FROM synced_dirs WHERE id = ?`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return s.commit(tx)
}

// MoveSynced records that a synced directory is now at dir of root rootID (the person confirmed it moved there).
func (s *Store) MoveSynced(ctx context.Context, id, rootID int64, dir string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE synced_dirs SET root_id = ?, dir = ? WHERE id = ?`, rootID, dir, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM found_marks WHERE root_id = ? AND dir = ?`, rootID, dir); err != nil {
		return err
	}
	return s.commit(tx)
}

// FoundMarks returns the marks the scans have found that this device has neither joined nor ignored, by path.
func (s *Store) FoundMarks(ctx context.Context) ([]FoundMark, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.root_id, r.path, m.dir, coalesce(m.sync_id, ''), coalesce(m.problem, ''),
		m.found_utc FROM found_marks m JOIN roots r ON r.id = m.root_id ORDER BY r.path, m.dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoundMark
	for rows.Next() {
		var m FoundMark
		if err := rows.Scan(&m.RootID, &m.RootPath, &m.Dir, &m.SyncID, &m.Problem, &m.FoundUTC); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetFoundMarks records the marks a scan of root rootID found under scope ("" = the whole root): those it found
// are kept or added, those under scope it no longer found are dropped. It writes only if something differs, so a
// scan that found the same marks again is no change (and takes no snapshot). It reports whether it wrote.
func (s *Store) SetFoundMarks(ctx context.Context, rootID int64, scope string, marks []FoundMark, now string) (bool, error) {
	have, err := s.FoundMarks(ctx)
	if err != nil {
		return false, err
	}
	in := func(dir string) bool { return scope == "" || dir == scope || strings.HasPrefix(dir, scope+"/") }
	old := map[string]FoundMark{}
	for _, m := range have {
		if m.RootID == rootID && in(m.Dir) {
			old[m.Dir] = m
		}
	}
	var add []FoundMark
	keep := map[string]bool{}
	for _, m := range marks {
		keep[m.Dir] = true
		if o, ok := old[m.Dir]; ok && o.SyncID == m.SyncID && o.Problem == m.Problem {
			continue
		}
		add = append(add, m)
	}
	var drop []string
	for dir := range old {
		if !keep[dir] {
			drop = append(drop, dir)
		}
	}
	if len(add) == 0 && len(drop) == 0 {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, dir := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM found_marks WHERE root_id = ? AND dir = ?`, rootID, dir); err != nil {
			return false, err
		}
	}
	for _, m := range add {
		if _, err := tx.ExecContext(ctx, `INSERT INTO found_marks (root_id, dir, sync_id, problem, found_utc)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT (root_id, dir) DO UPDATE SET sync_id = excluded.sync_id,
			problem = excluded.problem, found_utc = excluded.found_utc`,
			rootID, m.Dir, nullString(m.SyncID), nullString(m.Problem), now); err != nil {
			return false, err
		}
	}
	return true, s.commit(tx)
}

// KnownDevices returns every device met in a journal, by name.
func (s *Store) KnownDevices(ctx context.Context) ([]KnownDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT dev, key, name, coalesce(created_utc, ''), coalesce(trusted_utc, ''),
		coalesce(vouched_by, ''), coalesce(retired_seq, 0), coalesce(retired_by, ''), coalesce(snoozed_until, '')
		FROM devices ORDER BY name, dev`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnownDevice
	for rows.Next() {
		var d KnownDevice
		if err := rows.Scan(&d.Dev, &d.Key, &d.Name, &d.CreatedUTC, &d.TrustedUTC, &d.VouchedBy, &d.RetiredSeq,
			&d.RetiredBy, &d.SnoozedUntil); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Journals returns what this register has read of each journal of synced directory id, by device.
func (s *Store) Journals(ctx context.Context, id int64) ([]JournalState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.dev, j.seq, j.byte_offset, coalesce(j.problem, ''),
		coalesce(j.seen_utc, ''), coalesce(j.applied, ''), l.raw
		FROM journals j LEFT JOIN journal_lines l ON l.synced_id = j.synced_id AND l.dev = j.dev AND l.seq = j.seq
		WHERE j.synced_id = ? ORDER BY j.dev`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalState
	for rows.Next() {
		var j JournalState
		var applied string
		if err := rows.Scan(&j.Dev, &j.Seq, &j.Offset, &j.Problem, &j.SeenUTC, &applied, &j.Last); err != nil {
			return nil, err
		}
		if applied != "" && applied != "null" {
			if err := json.Unmarshal([]byte(applied), &j.Applied); err != nil {
				return nil, fmt.Errorf("journal of %s: %w", j.Dev, err)
			}
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JournalLines returns the lines kept of device dev's journal in synced directory id, in order: what writing the
// journal back puts in it.
func (s *Store) JournalLines(ctx context.Context, id int64, dev string) ([]JournalLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, raw FROM journal_lines WHERE synced_id = ? AND dev = ? ORDER BY seq`,
		id, dev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalLine
	for rows.Next() {
		var l JournalLine
		if err := rows.Scan(&l.Seq, &l.Raw); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
