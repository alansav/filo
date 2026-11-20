package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
)

// Shown is everything the register holds about one file, for filo show (docs/design.md, Showing and setting a
// file's details): what Filo observed and what the person decided.
type Shown struct {
	FileItem             // RootPath and Path are its current path, or its last one; Status is set only while Present
	RootID         int64 // the root of that path
	Present        bool  // it has a current path
	DeletedUTC     string
	GoneScan       int64  // not present: the scan that closed its last path (a deletion run's record too)
	GoneUTC        string // when that scan ran
	CtimeNS        int64  // 0 = not known
	Ino            uint64 // 0 = not known
	LinkTarget     string // symlinks
	LinkBroken     bool
	FirstSeenScan  int64
	FirstSeenUTC   string
	ReviewedHash   string // the content its retention was set on; "" = none
	ReviewedBy     string // the command that last set or cleared its retention (review_batches.command); "" = none
	RuleDir        string // the rule that set its policy (RuleID != 0): its directory, relative to the root
	RulePattern    string // and its pattern
	Paths          []ShownPath
	LastScanUTC    string // present: when the latest finished scan covering its directory finished; "" = none
	Always         bool   // its content is always: never a duplicate
	RecordSet      int64  // the set of records it is one of, on its current content; 0 = none
	Excluded       bool   // its path is under an excluded directory, so it is not one of the copies
	Copies         []DupCopy
	OneRecordSet   bool // its copies, and it, are all records of its set
	CopiesInGroups bool // its content is a duplicate group
}

// ShownPath is one of a file's paths, with the scans it was seen there in.
type ShownPath struct {
	RootID           int64
	RootPath, Path   string
	FromScan, ToScan int64  // ToScan 0 = its current path
	FromUTC, ToUTC   string // when those scans ran
}

// AbsPath is the path in full.
func (p ShownPath) AbsPath() string { return absPath(p.RootPath, p.Path) }

// ErrNotInRegister means no file has ever been registered at a path.
var ErrNotInRegister = errors.New("not in the register")

// scanTime is when a scan ran, in SQL on alias a: its finish, or its start for one that never finished.
func scanTime(a string) string { return `coalesce(` + a + `.finished_utc, ` + a + `.started_utc, '')` }

// ShowFile returns the file registered at path in a root: the one there now, or else the last one that was (a file
// gone missing, or deleted by Filo), with its whole record. today (YYYY-MM-DD) decides its status. ErrNotInRegister
// if no file was ever registered at path.
func (s *Store) ShowFile(ctx context.Context, rootID int64, rel, today string) (Shown, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT file_id FROM paths WHERE root_id = ? AND path = ?
		ORDER BY valid_to_scan IS NOT NULL, coalesce(valid_to_scan, 0) DESC, id DESC LIMIT 1`, rootID, rel).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Shown{}, ErrNotInRegister
	}
	if err != nil {
		return Shown{}, fmt.Errorf("find %s: %w", rel, err)
	}
	return s.showFile(ctx, id, today)
}

// showFile reads the record of one file by its id.
func (s *Store) showFile(ctx context.Context, id int64, today string) (Shown, error) {
	var sh Shown
	it := &sh.FileItem
	var ino int64
	err := s.db.QueryRowContext(ctx, `
		SELECT f.id, f.kind, f.size, f.mtime_ns, f.ctime_ns, coalesce(f.hash, ''), coalesce(f.inode, 0),
		       coalesce(f.link_target, ''), f.link_broken, coalesce(f.file_date, ''), `+retentionColumns+`,
		       coalesce(f.keep_until, ''), coalesce(f.reviewed_utc, ''), coalesce(f.reviewed_hash, ''),
		       f.rule_dated_utc IS NOT NULL, coalesce(f.rule_id, 0), coalesce(ru.dir, ''), coalesce(ru.pattern, ''),
		       coalesce(f.deleted_utc, ''), f.first_seen_scan, `+scanTime("fs")+`,
		       coalesce(f.unreadable_since_scan, 0), coalesce(`+scanTime("us")+`, ''),
		       coalesce((SELECT b.command FROM reviews rv JOIN review_batches b ON b.id = rv.batch_id
		                 WHERE rv.file_id = f.id ORDER BY rv.id DESC LIMIT 1), ''),
		       coalesce(dr.set_id, 0), ah.hash IS NOT NULL
		FROM files f`+policyJoin+`
		LEFT JOIN rules ru ON ru.id = f.rule_id
		LEFT JOIN scans fs ON fs.id = f.first_seen_scan
		LEFT JOIN scans us ON us.id = f.unreadable_since_scan
		LEFT JOIN duplicate_records dr ON dr.file_id = f.id AND dr.hash = f.hash
		LEFT JOIN always_hashes ah ON ah.hash = f.hash
		WHERE f.id = ?`, id).Scan(&it.FileID, &it.Kind, &it.Size, &it.MtimeNS, &sh.CtimeNS, &it.Hash, &ino,
		&sh.LinkTarget, &sh.LinkBroken, &it.FileDate, &it.Retention, &it.Policy, &it.PolicyValue, &it.KeepUntil,
		&it.ReviewedUTC, &sh.ReviewedHash, &it.RuleDated, &it.RuleID, &sh.RuleDir, &sh.RulePattern, &sh.DeletedUTC,
		&sh.FirstSeenScan, &sh.FirstSeenUTC, &it.UnreadableSince, &it.UnreadableSinceUTC, &sh.ReviewedBy,
		&sh.RecordSet, &sh.Always)
	if err != nil {
		return Shown{}, fmt.Errorf("read file %d: %w", id, err)
	}
	sh.Ino = uint64(ino)

	sh.Paths, err = queryAll(ctx, s.db, func(rows *sql.Rows) (ShownPath, error) {
		var p ShownPath
		err := rows.Scan(&p.RootID, &p.RootPath, &p.Path, &p.FromScan, &p.ToScan, &p.FromUTC, &p.ToUTC)
		return p, err
	}, `SELECT p.root_id, r.path, p.path, p.valid_from_scan, coalesce(p.valid_to_scan, 0), `+scanTime("sf")+`,
		       coalesce(`+scanTime("st")+`, '')
		FROM paths p JOIN roots r ON r.id = p.root_id
		LEFT JOIN scans sf ON sf.id = p.valid_from_scan
		LEFT JOIN scans st ON st.id = p.valid_to_scan
		WHERE p.file_id = ?
		ORDER BY p.valid_to_scan IS NOT NULL, coalesce(p.valid_to_scan, 0) DESC, p.valid_from_scan DESC, p.id DESC`,
		id)
	if err != nil {
		return Shown{}, fmt.Errorf("read the paths of file %d: %w", id, err)
	}
	if len(sh.Paths) == 0 {
		return Shown{}, fmt.Errorf("file %d has no path", id)
	}
	last := sh.Paths[0]
	it.RootPath, it.Path, sh.RootID = last.RootPath, last.Path, last.RootID
	sh.Present = last.ToScan == 0
	if sh.Present {
		items, err := s.Files(ctx, today, Filter{FileID: id})
		if err != nil {
			return Shown{}, err
		}
		if len(items) != 1 {
			return Shown{}, fmt.Errorf("file %d: %d current paths", id, len(items))
		}
		sh.FileItem = items[0]
		dir := path.Dir(sh.Path)
		if dir == "." {
			dir = ""
		}
		if sh.LastScanUTC, err = s.LastScanCovering(ctx, sh.RootID, dir); err != nil {
			return Shown{}, err
		}
	} else {
		sh.GoneScan, sh.GoneUTC = last.ToScan, last.ToUTC
	}

	excluded, err := exclusions(ctx, s.db)
	if err != nil {
		return Shown{}, err
	}
	sh.Excluded = sh.Present && excluded.covers(sh.RootID, sh.Path)
	if sh.Kind == "file" && sh.Hash != "" && sh.Size > 0 {
		contents, err := loadContents(ctx, s.db, `SELECT ?`, sh.Hash)
		if err != nil {
			return Shown{}, err
		}
		if len(contents) == 1 {
			c := contents[0]
			sh.CopiesInGroups = c.IsGroup()
			sh.OneRecordSet = c.OneRecordSet()
			abs := sh.AbsPath()
			for _, cp := range c.Copies {
				if sh.Present && (cp.FileID == id || cp.AbsPath() == abs || slices.Contains(cp.Links, abs)) {
					continue // the file itself, or the hard links it is one of
				}
				sh.Copies = append(sh.Copies, cp)
			}
		}
	}
	return sh, nil
}
