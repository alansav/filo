package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// File decisions shared between devices (docs/design.md, Sharing decisions between devices): every file decision
// line of a synced directory, read from a journal or written by this device, by what it decides, and whether it is
// still in effect; and the shared IDs of the files they are about.

// decisionsSQL is the shared file decisions, added in schema version 16. shared_files maps a file's shared ID in a
// synced directory to the file it is here (file_id NULL: not matched yet). decisions holds every file decision line:
// fid and key say what it decides (key 'date', 'retention', or 'tag:' and the tag's key and folded value); hash and
// path are the file's when it was decided; value is the decision (” for none: a date or retention cleared, a tag
// removed); decided_utc the line's time (shown only); current is 1 while no line read replaces it (two current lines of one key with different values are a
// conflict); applied is 1 once it was applied to the file here, or needs no applying (this device's own). replaced
// is the lines named as replaced by a line read before they were read themselves.
const decisionsSQL = `
CREATE TABLE shared_files (
    synced_id INTEGER NOT NULL REFERENCES synced_dirs(id),
    fid TEXT NOT NULL,
    file_id INTEGER REFERENCES files(id),
    PRIMARY KEY (synced_id, fid)
);
CREATE UNIQUE INDEX ux_shared_files_file ON shared_files(synced_id, file_id) WHERE file_id IS NOT NULL;

CREATE TABLE decisions (
    synced_id INTEGER NOT NULL REFERENCES synced_dirs(id),
    dev TEXT NOT NULL,
    seq INTEGER NOT NULL,
    fid TEXT NOT NULL,
    key TEXT NOT NULL,
    hash TEXT,
    path TEXT,
    value TEXT NOT NULL DEFAULT '',
    decided_utc TEXT,
    current INTEGER NOT NULL DEFAULT 1,
    applied INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (synced_id, dev, seq)
);
CREATE INDEX ix_decisions_key ON decisions(synced_id, fid, key) WHERE current = 1;

CREATE TABLE replaced (
    synced_id INTEGER NOT NULL REFERENCES synced_dirs(id),
    dev TEXT NOT NULL,
    seq INTEGER NOT NULL,
    PRIMARY KEY (synced_id, dev, seq)
);
`

// Decision is one file decision line as the register keeps it.
type Decision struct {
	SyncedID int64
	Dev      string
	Seq      int64
	FID      string
	Key      string
	Hash     string
	Path     string
	Value    string
	Decided  string // the line's time, shown only
	Current  bool
	Applied  bool
}

// LineRef names a line: its device and number.
type LineRef struct {
	Dev string
	Seq int64
}

// ErrNoDecision is a line that is not a decision kept.
var ErrNoDecision = errors.New("no such decision")

// CurrentDecisions returns the decisions in effect for fid's key in synced directory id: one, or several (a
// conflict, or equal values merged), or none.
func (s *Store) CurrentDecisions(ctx context.Context, id int64, fid, key string) ([]Decision, error) {
	return currentDecisions(ctx, s.db, id, fid, key)
}

func currentDecisions(ctx context.Context, q querier, id int64, fid, key string) ([]Decision, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+decisionCols+` FROM decisions WHERE synced_id = ? AND fid = ? AND
		key = ? AND current = 1 ORDER BY dev, seq`, id, fid, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// decisionCols are the columns scanDecision reads.
const decisionCols = `synced_id, dev, seq, fid, key, coalesce(hash, ''), coalesce(path, ''), value,
	coalesce(decided_utc, ''), current, applied`

func scanDecision(rows *sql.Rows) (Decision, error) {
	var d Decision
	err := rows.Scan(&d.SyncedID, &d.Dev, &d.Seq, &d.FID, &d.Key, &d.Hash, &d.Path, &d.Value, &d.Decided, &d.Current,
		&d.Applied)
	return d, err
}

// addDecisions keeps decision lines in tx: each replaces the current decisions it names (or marks them replaced
// ahead, if not read yet); one named replaced ahead is kept as not current. A line kept already is left as it is.
func addDecisions(ctx context.Context, tx *sql.Tx, ds []Decision, replaces [][]LineRef) error {
	for i, d := range ds {
		var ahead int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM replaced WHERE synced_id = ? AND dev = ? AND seq = ?`,
			d.SyncedID, d.Dev, d.Seq).Scan(&ahead); err != nil {
			return err
		}
		current := ahead == 0
		if _, err := tx.ExecContext(ctx, `INSERT INTO decisions (synced_id, dev, seq, fid, key, hash, path, value,
			decided_utc, current, applied) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			d.SyncedID, d.Dev, d.Seq, d.FID, d.Key, nullString(d.Hash), nullString(d.Path), d.Value,
			nullString(d.Decided), current, d.Applied); err != nil {
			return fmt.Errorf("keep decision %s:%d: %w", d.Dev, d.Seq, err)
		}
		if i < len(replaces) {
			for _, r := range replaces[i] {
				res, err := tx.ExecContext(ctx, `UPDATE decisions SET current = 0 WHERE synced_id = ? AND dev = ? AND
					seq = ?`, d.SyncedID, r.Dev, r.Seq)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 0 {
					if _, err := tx.ExecContext(ctx, `INSERT INTO replaced (synced_id, dev, seq) VALUES (?, ?, ?)
						ON CONFLICT DO NOTHING`, d.SyncedID, r.Dev, r.Seq); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// MarkApplied records that the decisions named were applied to the file here (or need no applying).
func (s *Store) MarkApplied(ctx context.Context, id int64, refs []LineRef) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `UPDATE decisions SET applied = 1 WHERE synced_id = ? AND dev = ? AND seq = ?
			AND applied = 0`, id, r.Dev, r.Seq); err != nil {
			return err
		}
	}
	return s.commit(tx)
}

// Unapplied returns the current decisions of synced directory id not applied yet (waiting for their file, or its
// content), by file and key.
func (s *Store) Unapplied(ctx context.Context, id int64) ([]Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionCols+` FROM decisions WHERE synced_id = ? AND current = 1
		AND applied = 0 ORDER BY fid, key, dev, seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// conflictValue is what two current decisions must differ in to conflict: the value, but for a tag only whether it is
// added or removed (two devices adding it, spelled in other cases, agree).
const conflictValue = `CASE WHEN key LIKE 'tag:%' THEN value != '' ELSE value END`

// Conflicts returns the (fid, key) of synced directory id with current decisions of different values.
func (s *Store) Conflicts(ctx context.Context, id int64) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT fid, key FROM decisions WHERE synced_id = ? AND current = 1
		GROUP BY fid, key HAVING count(DISTINCT `+conflictValue+`) > 1 ORDER BY fid, key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// SharedFile returns the file a shared ID is here (0: not matched yet), and whether the ID is known.
func (s *Store) SharedFile(ctx context.Context, id int64, fid string) (int64, bool, error) {
	var file sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT file_id FROM shared_files WHERE synced_id = ? AND fid = ?`, id,
		fid).Scan(&file)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return file.Int64, err == nil, err
}

// SharedID returns a file's shared ID in synced directory id ("" if it has none).
func (s *Store) SharedID(ctx context.Context, id, fileID int64) (string, error) {
	var fid string
	err := s.db.QueryRowContext(ctx, `SELECT fid FROM shared_files WHERE synced_id = ? AND file_id = ?`, id,
		fileID).Scan(&fid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fid, err
}

// insertCursors sets the publish cursors past every review and tag change in tx's register (none in a new one).
func insertCursors(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []struct{ key, max string }{
		{metaPublishReviews, `SELECT coalesce(max(id), 0) FROM reviews`},
		{metaPublishTags, `SELECT coalesce(max(id), 0) FROM tag_changes`},
		{metaPublishPolicies, `SELECT coalesce(max(id), 0) FROM policy_changes`},
		{metaPublishEvents, `SELECT coalesce(max(id), 0) FROM events`},
	} {
		var n int64
		if err := tx.QueryRowContext(ctx, q.max).Scan(&n); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)`, q.key,
			fmt.Sprint(n)); err != nil {
			return err
		}
	}
	return nil
}

// CommandJournal is the command recorded for a change that applied another device's decision: never written to
// this device's journal again.
const CommandJournal = "journal"

// ReviewEvent is a retention or file_date decision made on this device, from the review history: the values after
// it, and the content it was made on.
type ReviewEvent struct {
	ID        int64
	FileID    int64
	FileDate  string
	Retention string // a policy's name, or the file's own retention; "" = none
	Hash      string
}

// ReviewsSince returns the decisions recorded after review after, but those that applied a journal's (batch command
// CommandJournal), in order.
func (s *Store) ReviewsSince(ctx context.Context, after int64) ([]ReviewEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.file_id, coalesce(r.file_date, ''), coalesce(r.retention, ''),
		coalesce(r.hash, '') FROM reviews r JOIN review_batches b ON b.id = r.batch_id
		WHERE r.id > ? AND b.command != ? ORDER BY r.id`, after, CommandJournal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewEvent
	for rows.Next() {
		var e ReviewEvent
		if err := rows.Scan(&e.ID, &e.FileID, &e.FileDate, &e.Retention, &e.Hash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Latest returns the newest review and tag change: how far the cursors go when nothing is left to publish.
func (s *Store) Latest(ctx context.Context) (PublishCursors, error) {
	var c PublishCursors
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM reviews`).Scan(&c.Reviews); err != nil {
		return c, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM tag_changes`).Scan(&c.TagChanges); err != nil {
		return c, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM policy_changes`).Scan(&c.PolicyChanges); err != nil {
		return c, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM events`).Scan(&c.Events)
	return c, err
}

// TagEvent is a tag added or removed on this device.
type TagEvent struct {
	ID     int64
	FileID int64
	Added  bool
	Key    string
	Value  string // as spelled
}

// TagChangesSince returns the tag changes after after, but those that applied a journal's, in order.
func (s *Store) TagChangesSince(ctx context.Context, after int64) ([]TagEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_id, kind = 'add', key, value FROM tag_changes
		WHERE id > ? AND command != ? ORDER BY id`, after, CommandJournal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagEvent
	for rows.Next() {
		var e TagEvent
		if err := rows.Scan(&e.ID, &e.FileID, &e.Added, &e.Key, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FilePlace is where a file is now, and its content.
type FilePlace struct {
	FileID int64
	RootID int64
	Path   string
	Hash   string
}

// FilePlaces returns where each file is now (a file not present is left out).
func (s *Store) FilePlaces(ctx context.Context, ids []int64) (map[int64]FilePlace, error) {
	out := map[int64]FilePlace{}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		var p FilePlace
		err := s.db.QueryRowContext(ctx, `SELECT f.id, p.root_id, p.path, coalesce(f.hash, '') FROM files f
			JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL WHERE f.id = ? AND f.deleted_utc IS NULL`,
			id).Scan(&p.FileID, &p.RootID, &p.Path, &p.Hash)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}

// MatchFile finds the one file a decision is about in synced directory sd: the one present with content hash at
// path (relative to sd's top), else the one present with hash under sd, else, of several with hash, the one that was
// once at path. With no hash (a tag on a file not hashed yet) only the one file present at path matches. It returns
// 0 when there is none, or more than one: matching never guesses.
func (s *Store) MatchFile(ctx context.Context, sd SyncedDir, hash, path string) (int64, error) {
	full := path
	if sd.Dir != "" {
		full = sd.Dir + "/" + path
	}
	ids := func(q string, a ...any) ([]int64, error) {
		rows, err := s.db.QueryContext(ctx, q, a...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
	if hash == "" {
		at, err := ids(`SELECT f.id FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
			WHERE f.deleted_utc IS NULL AND p.root_id = ? AND p.path = ?`, sd.RootID, full)
		return first(at), err
	}
	present := `SELECT DISTINCT f.id FROM files f JOIN paths p ON p.file_id = f.id AND p.valid_to_scan IS NULL
		WHERE f.deleted_utc IS NULL AND f.hash = ? AND p.root_id = ?`
	at, err := ids(present+` AND p.path = ?`, hash, sd.RootID, full)
	if err != nil || len(at) == 1 {
		return first(at), err
	}
	q, args := present, []any{hash, sd.RootID}
	if sd.Dir != "" {
		q += ` AND (p.path = ? OR substr(p.path, 1, ?) = ?)`
		args = append(args, sd.Dir, len(sd.Dir)+1, sd.Dir+"/")
	}
	in, err := ids(q, args...)
	if err != nil || len(in) <= 1 {
		return first(in), err
	}
	once, err := ids(present+` AND f.id IN (SELECT file_id FROM paths WHERE root_id = ? AND path = ?)`, hash,
		sd.RootID, sd.RootID, full)
	if err != nil || len(once) != 1 {
		return 0, err
	}
	return once[0], nil
}

func first(ids []int64) int64 {
	if len(ids) == 1 {
		return ids[0]
	}
	return 0
}

// FileDecisions returns the current decisions about a file, in every synced directory it has a shared ID in.
func (s *Store) FileDecisions(ctx context.Context, fileID int64) ([]Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionCols+` FROM decisions d WHERE current = 1 AND EXISTS (
		SELECT 1 FROM shared_files sf WHERE sf.synced_id = d.synced_id AND sf.fid = d.fid AND sf.file_id = ?)
		ORDER BY key, dev, seq`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ConflictedFiles returns the files with a conflict on what they are decided (their date, their retention, or a
// tag), with the keys in conflict. A file whose date or retention is in conflict is never a delete candidate.
func (s *Store) ConflictedFiles(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sf.file_id, d.key FROM decisions d JOIN shared_files sf
		ON sf.synced_id = d.synced_id AND sf.fid = d.fid WHERE d.current = 1 AND sf.file_id IS NOT NULL
		GROUP BY sf.file_id, d.key HAVING count(DISTINCT `+conflictValue+`) > 1 ORDER BY sf.file_id, d.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = append(out[id], key)
	}
	return out, rows.Err()
}

// FileValues is what a file is decided now: its file_date, its retention (its policy's name, or its own), that
// policy's value, and its content; and whether it is present (has a current path) or was deleted by Filo.
type FileValues struct {
	FileDate, Retention, PolicyValue, Hash string
	Policy                                 bool // Retention names a policy
	Present, Deleted                       bool
}

// Values returns file fileID's values (false: no such file in the register).
func (s *Store) Values(ctx context.Context, fileID int64) (FileValues, bool, error) {
	var v FileValues
	var policy sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT coalesce(f.file_date, ''), coalesce(f.retention, ''), p.name,
		coalesce(p.value, ''), coalesce(f.hash, ''), f.deleted_utc IS NOT NULL,
		EXISTS (SELECT 1 FROM paths x WHERE x.file_id = f.id AND x.valid_to_scan IS NULL)
		FROM files f LEFT JOIN policies p ON p.id = f.policy_id WHERE f.id = ?`, fileID).Scan(&v.FileDate,
		&v.Retention, &policy, &v.PolicyValue, &v.Hash, &v.Deleted, &v.Present)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if policy.Valid {
		v.Retention, v.Policy = policy.String, true
	}
	return v, true, nil
}

// PolicyEvent is a change of policy made on this device, from the policy history: kind 'add', 'set', 'rename',
// 'move' or 'remove', the policy's name, its old value and its new one (for rename and move, the policy moved to).
type PolicyEvent struct {
	ID       int64
	Kind     string
	Policy   string
	Old, New string
}

// PolicyChangesSince returns the policy changes after after, but those that applied a journal's, in order.
func (s *Store) PolicyChangesSince(ctx context.Context, after int64) ([]PolicyEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, policy, coalesce(old_value, ''), coalesce(new_value, '')
		FROM policy_changes WHERE id > ? AND coalesce(command, '') != ? ORDER BY id`, after, CommandJournal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PolicyEvent
	for rows.Next() {
		var e PolicyEvent
		if err := rows.Scan(&e.ID, &e.Kind, &e.Policy, &e.Old, &e.New); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkPolicyChangesJournal records that the policy changes after after applied another device's decisions: they are
// never written to this device's journal again.
func (s *Store) MarkPolicyChangesJournal(ctx context.Context, after int64) error {
	_, err := s.exec(ctx, `UPDATE policy_changes SET command = ? WHERE id > ? AND command IS NULL`, CommandJournal,
		after)
	return err
}

// SettingsFID is the file ID the decisions that are not about one file are kept under: policies, rules, directories
// excluded from duplicates, contents always kept, sets of records, tag indexes (docs/design.md, Sharing decisions
// between devices). It is no file's: a file's shared ID is 26 characters.
const SettingsFID = "-"

// CurrentByKey returns the current decisions of synced directory id kept under fid, by key.
func (s *Store) CurrentByKey(ctx context.Context, id int64, fid string) (map[string][]Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionCols+` FROM decisions WHERE synced_id = ? AND fid = ? AND
		current = 1 ORDER BY key, dev, seq`, id, fid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Decision{}
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out[d.Key] = append(out[d.Key], d)
	}
	return out, rows.Err()
}

// AlwaysHashes returns the contents marked always, every one (docs/design.md, Duplicates).
func (s *Store) AlwaysHashes(ctx context.Context) ([]string, error) {
	return queryAll(ctx, s.db, func(rows *sql.Rows) (string, error) {
		var h string
		return h, rows.Scan(&h)
	}, `SELECT hash FROM always_hashes ORDER BY hash`)
}

// RecordMember is a file kept as one of a set of records, while its content is still the one marked.
type RecordMember struct {
	Set    int64
	FileID int64
	Hash   string
}

// RecordMembers returns every file kept as a record of a set, while its content is the one it was marked on.
func (s *Store) RecordMembers(ctx context.Context) ([]RecordMember, error) {
	return queryAll(ctx, s.db, func(rows *sql.Rows) (RecordMember, error) {
		var m RecordMember
		return m, rows.Scan(&m.Set, &m.FileID, &m.Hash)
	}, `SELECT r.set_id, r.file_id, r.hash FROM duplicate_records r JOIN files f ON f.id = r.file_id
		WHERE f.hash = r.hash AND f.deleted_utc IS NULL ORDER BY r.set_id, r.file_id`)
}

// Seen returns the setting values this device last wrote or applied for synced directory id (0: the policies), by
// key.
func (s *Store) Seen(ctx context.Context, id int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings_seen WHERE synced_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSeen records the value this device wrote or applied for a setting of synced directory id (0: a policy). It
// writes only when the value changes.
func (s *Store) SetSeen(ctx context.Context, id int64, key, value string) error {
	var have string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings_seen WHERE synced_id = ? AND key = ?`, id,
		key).Scan(&have)
	if err == nil && have == value {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO settings_seen (synced_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT (synced_id, key) DO UPDATE SET value = excluded.value`, id, key, value)
	return err
}

// FileEvent is a scan's or a delete run's event about one file: moved (renamed, moved, returned), changed (its
// content edited), or deleted by Filo.
type FileEvent struct {
	ID     int64
	ScanID int64
	Kind   string
	FileID int64
	Path   string // where it is (moved: to), relative to its root
}

// FileEventsSince returns the moved, changed and deleted events after event after, in order.
func (s *Store) FileEventsSince(ctx context.Context, after int64) ([]FileEvent, error) {
	return queryAll(ctx, s.db, func(rows *sql.Rows) (FileEvent, error) {
		var e FileEvent
		return e, rows.Scan(&e.ID, &e.ScanID, &e.Kind, &e.FileID, &e.Path)
	}, `SELECT id, scan_id, kind, file_id, coalesce(path, '') FROM events WHERE id > ? AND file_id IS NOT NULL
		AND kind IN ('moved', 'changed', ?) ORDER BY id`, after, EventDeleted)
}

// SeenSince reports whether a scan finished after scan scanID covered the file's root and found it unchanged: an
// edit seen on two scans in a row, so not a file still being written.
func (s *Store) SeenSince(ctx context.Context, fileID, scanID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM scans sc JOIN paths p ON p.file_id = ? AND
		p.valid_to_scan IS NULL AND p.root_id = sc.root_id
		WHERE sc.id > ? AND sc.kind = 'scan' AND sc.finished_utc IS NOT NULL
		AND (sc.scope = '' OR p.path = sc.scope OR substr(p.path, 1, length(sc.scope) + 1) = sc.scope || '/')
		AND NOT EXISTS (SELECT 1 FROM events e WHERE e.scan_id = sc.id AND e.file_id = ? AND e.kind = 'changed')`,
		fileID, scanID, fileID).Scan(&n)
	return n > 0, err
}

// RemapShared makes shared ID fid of synced directory id the file fileID, which has none (another device's scan
// found the file moved or edited where this one saw a missing file and a new one), and makes its current decisions
// apply again, to it. It reports whether it did.
func (s *Store) RemapShared(ctx context.Context, id int64, fid string, fileID int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var other int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM shared_files WHERE synced_id = ? AND file_id = ?`, id,
		fileID).Scan(&other); err != nil || other > 0 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO shared_files (synced_id, fid, file_id) VALUES (?, ?, ?)
		ON CONFLICT (synced_id, fid) DO UPDATE SET file_id = excluded.file_id`, id, fid, fileID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE decisions SET applied = 0 WHERE synced_id = ? AND fid = ? AND
		current = 1 AND key NOT LIKE 'fact:%'`, id, fid); err != nil {
		return false, err
	}
	return true, s.commit(tx)
}

// MarkDeletedElsewhere records that a file missing here was deleted by Filo on another device (named in detail):
// its row is closed as deleted, so it is never "missing", and a copy restored later is a new file. A file present,
// or deleted already, is left alone; it reports whether it marked it.
func (s *Store) MarkDeletedElsewhere(ctx context.Context, fileID int64, now time.Time) (bool, error) {
	res, err := s.exec(ctx, `UPDATE files SET deleted_utc = ? WHERE id = ? AND deleted_utc IS NULL AND NOT EXISTS (
		SELECT 1 FROM paths WHERE file_id = ? AND valid_to_scan IS NULL)`, utc(now), fileID, fileID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
