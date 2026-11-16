package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ExportedCopy is one copy of a duplicates export as it was exported (its group, path and content), with its state
// in the register now: what filo delete FILE.csv checks against the disk (docs/design.md, Duplicates).
type ExportedCopy struct {
	// Candidate is the copy at its exported path: RootID, RootPath, PathID and Path as exported; size, modification
	// time, inode, hash and retention as the register has them now. ReviewedHash is the content exported, which the
	// check at the moment of deleting re-hashes against.
	Candidate
	Group      int64
	Deleted    string // when filo deleted it, "" if it did not
	PathOpen   bool   // the exported path is still the file's current path in the register
	OtherPath  string // another current path of the file, absolute, "" if none: a scan saw it move
	Unreadable bool   // the last scan could not read it
	RecordSet  int64  // the set of records it is one of, on its current content; 0 for none
	Always     bool   // its current content is always
}

// ExportedCopies returns, for each of the given exports that is a duplicates export, its copies ordered by group and
// path. An export that is not one is left out.
func (s *Store) ExportedCopies(ctx context.Context, exportIDs []int64) (map[int64][]ExportedCopy, error) {
	out := map[int64][]ExportedCopy{}
	if len(exportIDs) == 0 {
		return out, nil
	}
	args := []any{ExportDuplicates}
	for _, id := range exportIDs {
		args = append(args, id)
	}
	// Another current path of the file, the same one each time: the first by root and path.
	const other = `FROM paths p2 JOIN roots r2 ON r2.id = p2.root_id
		WHERE p2.file_id = f.id AND p2.valid_to_scan IS NULL AND p2.id != p.id ORDER BY r2.path, p2.path LIMIT 1`
	rows, err := queryAll(ctx, s.db, func(rows *sql.Rows) (struct {
		export int64
		c      ExportedCopy
	}, error) {
		var r struct {
			export int64
			c      ExportedCopy
		}
		c := &r.c
		var ino int64
		var otherRoot, otherRel string
		err := rows.Scan(&r.export, &c.Group, &c.FileID, &c.RootID, &c.RootPath, &c.PathID, &c.Path, &c.PathOpen,
			&c.Kind, &c.Size, &c.MtimeNS, &c.CtimeNS, &c.Hash, &ino, &c.FileDate, &c.Retention, &c.Policy, &c.PolicyValue, &c.KeepUntil,
			&c.ReviewedHash, &c.Deleted, &c.Unreadable, &c.RecordSet, &c.Always, &otherRoot, &otherRel)
		c.Ino = uint64(ino)
		if otherRoot != "" {
			c.OtherPath = absPath(otherRoot, otherRel)
		}
		return r, err
	}, `
		SELECT er.export_id, er.dup_group, f.id, r.id, r.path, p.id, p.path, p.valid_to_scan IS NULL, f.kind,
		       coalesce(f.size, 0), coalesce(f.mtime_ns, 0), coalesce(f.ctime_ns, 0), coalesce(f.hash, ''),
		       coalesce(f.inode, 0), coalesce(f.file_date, ''), `+retentionColumns+`,
		       coalesce(f.keep_until, ''), coalesce(er.hash, ''), coalesce(f.deleted_utc, ''),
		       f.unreadable_since_scan IS NOT NULL, coalesce(dr.set_id, 0), ah.hash IS NOT NULL,
		       coalesce((SELECT r2.path `+other+`), ''), coalesce((SELECT p2.path `+other+`), '')
		FROM export_rows er
		JOIN exports e ON e.id = er.export_id
		JOIN files f ON f.id = er.file_id`+policyJoin+`
		JOIN paths p ON p.id = er.path_id
		JOIN roots r ON r.id = p.root_id
		LEFT JOIN duplicate_records dr ON dr.file_id = f.id AND dr.hash = f.hash
		LEFT JOIN always_hashes ah ON ah.hash = f.hash
		WHERE e.kind = ? AND er.dup_group IS NOT NULL
		  AND er.export_id IN (?`+strings.Repeat(", ?", len(exportIDs)-1)+`)
		ORDER BY er.export_id, er.dup_group, r.path, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("read exports: %w", err)
	}
	for _, r := range rows {
		out[r.export] = append(out[r.export], r.c)
	}
	return out, nil
}

// RecordCopy is a copy to store as one of a set of records: the file, on the content checked.
type RecordCopy struct {
	FileID int64
	Hash   string
}

// RecordSet stores copies as one new set of records (docs/design.md, Duplicates): copies kept on purpose, each with
// its own retention, so the content is no duplicate group while they are its only copies. A copy already in a set
// moves to the new one. A record holds while the file's content is the hash stored, so an edited record leaves its
// set by itself.
func (s *Store) RecordSet(ctx context.Context, copies []RecordCopy, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var set int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(set_id), 0) + 1 FROM duplicate_records`).
		Scan(&set); err != nil {
		return fmt.Errorf("record a set of records: %w", err)
	}
	for _, c := range copies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO duplicate_records (file_id, hash, set_id, added_utc)
			VALUES (?, ?, ?, ?) ON CONFLICT (file_id) DO UPDATE SET hash = excluded.hash, set_id = excluded.set_id,
			added_utc = excluded.added_utc`, c.FileID, c.Hash, set, utc(now)); err != nil {
			return fmt.Errorf("record a set of records: %w", err)
		}
	}
	return s.commit(tx)
}

// AddAlways stores a content as always: it belongs everywhere, so its copies are never a duplicate group.
func (s *Store) AddAlways(ctx context.Context, hash string, now time.Time) error {
	if _, err := s.exec(ctx, `INSERT INTO always_hashes (hash, added_utc) VALUES (?, ?) ON CONFLICT (hash) DO NOTHING`,
		hash, utc(now)); err != nil {
		return fmt.Errorf("record a content always: %w", err)
	}
	return nil
}
