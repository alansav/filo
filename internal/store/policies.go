package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Named retention policies (docs/design.md, Lifespan, Named retention policies): a retention written once and named,
// so changing it changes every file using it. A file on a policy has files.policy_id set and no retention of its own;
// its keep_until is worked out from the policy's value and stored, so the statuses and filo delete read it as before.

var (
	// ErrPolicyName means a name that cannot be a policy's.
	ErrPolicyName = errors.New("a policy name is letters, digits, - and _, starting with a letter, and not forever")
	// ErrNoPolicy means there is no policy of that name.
	ErrNoPolicy = errors.New("no such policy (filo policies lists them)")
	// ErrPolicyExists means a policy of that name exists already.
	ErrPolicyExists = errors.New("a policy of that name exists already")
	// ErrPolicyInUse means a policy that present files use, which is not removed.
	ErrPolicyInUse = errors.New("policy in use")
)

// PolicyName checks a typed policy name and returns it as stored, in lower case: letters, digits, - and _, starting
// with a letter (so it is never mistaken for a period or a date), at most 64 characters, and not forever.
func PolicyName(s string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if name == "" || len(name) > 64 || name == Forever || name[0] < 'a' || name[0] > 'z' {
		return "", fmt.Errorf("%q: %w", s, ErrPolicyName)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", fmt.Errorf("%q: %w", s, ErrPolicyName)
		}
	}
	return name, nil
}

// checkPolicyValue checks a policy's value as stored: "" (none yet), a period in its stored form, Forever, or a real
// date. The CLI reads what was typed (retention.ParseRetention) before it gets here.
func checkPolicyValue(v string) error {
	if v == "" || v == Forever {
		return nil
	}
	if p, ok := NormalPeriod(v); ok && p == v {
		return nil
	}
	if t, err := time.Parse(DateLayout, v); err == nil && t.Format(DateLayout) == v {
		return nil
	}
	return fmt.Errorf("%q is not a policy value: a period such as 7y, a date YYYY-MM-DD, or forever", v)
}

// Policy is a named retention policy, with the present files using it.
type Policy struct {
	ID         int64
	Name       string
	Value      string // a period, a date or Forever; "" = none yet
	Files      int64  // present files using it
	NoFileDate int64  // of those, files without a file_date while the value is a period
	ChangedUTC string
}

const policiesQuery = `
	SELECT pol.id, pol.name, coalesce(pol.value, ''), pol.changed_utc,
	       (SELECT count(*) FROM files f WHERE f.policy_id = pol.id AND f.deleted_utc IS NULL
	          AND EXISTS (SELECT 1 FROM paths p WHERE p.file_id = f.id AND p.valid_to_scan IS NULL)),
	       (SELECT count(*) FROM files f WHERE f.policy_id = pol.id AND f.deleted_utc IS NULL AND f.file_date IS NULL
	          AND EXISTS (SELECT 1 FROM paths p WHERE p.file_id = f.id AND p.valid_to_scan IS NULL))
	FROM policies pol`

func scanPolicy(rows *sql.Rows) (Policy, error) {
	var p Policy
	err := rows.Scan(&p.ID, &p.Name, &p.Value, &p.ChangedUTC, &p.Files, &p.NoFileDate)
	if !IsPeriod(p.Value) {
		p.NoFileDate = 0
	}
	return p, err
}

// Policies returns every policy, by name.
func (s *Store) Policies(ctx context.Context) ([]Policy, error) {
	ps, err := queryAll(ctx, s.db, scanPolicy, policiesQuery+` ORDER BY pol.name`)
	if err != nil {
		return nil, fmt.Errorf("read policies: %w", err)
	}
	return ps, nil
}

// Policy returns the policy of that name (any case), or ErrNoPolicy.
func (s *Store) Policy(ctx context.Context, name string) (Policy, error) {
	n, err := PolicyName(name)
	if err != nil {
		return Policy{}, err
	}
	ps, err := queryAll(ctx, s.db, scanPolicy, policiesQuery+` WHERE pol.name = ?`, n)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy %s: %w", n, err)
	}
	if len(ps) == 0 {
		return Policy{}, fmt.Errorf("%s: %w", n, ErrNoPolicy)
	}
	return ps[0], nil
}

// AddPolicy creates a policy, with a value or none yet.
func (s *Store) AddPolicy(ctx context.Context, name, value string, now time.Time) error {
	n, err := PolicyName(name)
	if err != nil {
		return err
	}
	if err := checkPolicyValue(value); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addPolicy(ctx, tx, n, value, now); err != nil {
		return err
	}
	return s.commit(tx)
}

func addPolicy(ctx context.Context, tx *sql.Tx, name, value string, now time.Time) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM policies WHERE name = ?`, name).Scan(&exists); err != nil {
		return fmt.Errorf("add policy %s: %w", name, err)
	}
	if exists > 0 {
		return fmt.Errorf("%s: %w", name, ErrPolicyExists)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO policies (name, value, added_utc, changed_utc) VALUES (?, ?, ?, ?)`,
		name, nullString(value), utc(now), utc(now)); err != nil {
		return fmt.Errorf("add policy %s: %w", name, err)
	}
	return recordPolicyChange(ctx, tx, "add", name, "", value, 0, now)
}

func recordPolicyChange(ctx context.Context, tx *sql.Tx, kind, policy, old, new string, files int64,
	now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO policy_changes (kind, policy, old_value, new_value, files,
		changed_utc) VALUES (?, ?, ?, ?, ?, ?)`, kind, policy, nullString(old), nullString(new), files,
		utc(now)); err != nil {
		return fmt.Errorf("record the change of policy %s: %w", policy, err)
	}
	return nil
}

// PolicyFile is a present file a change of policy would give a new keep_until: KeepUntil is the new one.
type PolicyFile struct {
	FileItem
	OldKeepUntil string
}

// PolicyPreview is what a change of policy would do, for the question before it (docs/design.md, Changing a policy).
type PolicyPreview struct {
	From, To   Policy       // the policy now, and as it would be (To.Value the new value; for a move, the policy moved to)
	Files      int          // present files whose keep_until is worked out again
	Past       []PolicyFile // files the change puts past their date that were not already
	NoFileDate []PolicyFile // files that would newly need a file_date: the new value is a period, the old was not
}

// PreviewPolicy works out what setting the policy to value would do to its present files, today being YYYY-MM-DD.
func (s *Store) PreviewPolicy(ctx context.Context, name, value, today string) (PolicyPreview, error) {
	p, err := s.Policy(ctx, name)
	if err != nil {
		return PolicyPreview{}, err
	}
	if err := checkPolicyValue(value); err != nil {
		return PolicyPreview{}, err
	}
	to := p
	to.Value = value
	return s.preview(ctx, p, to, today)
}

// PreviewMove works out what moving the files of policy from to policy to would do: into a new name nothing changes
// but the name; into an existing policy the files take its value.
func (s *Store) PreviewMove(ctx context.Context, from, to, today string) (PolicyPreview, error) {
	p, err := s.Policy(ctx, from)
	if err != nil {
		return PolicyPreview{}, err
	}
	n, err := PolicyName(to)
	if err != nil {
		return PolicyPreview{}, err
	}
	target, err := s.Policy(ctx, n)
	switch {
	case errors.Is(err, ErrNoPolicy):
		target = p
		target.ID, target.Name, target.Files, target.NoFileDate = 0, n, 0, 0
	case err != nil:
		return PolicyPreview{}, err
	}
	return s.preview(ctx, p, target, today)
}

// preview works out the new keep_until of from's present files under to's value.
func (s *Store) preview(ctx context.Context, from, to Policy, today string) (PolicyPreview, error) {
	out := PolicyPreview{From: from, To: to}
	files, err := s.Files(ctx, today, Filter{})
	if err != nil {
		return out, err
	}
	past := func(k string) bool { return k != "" && k != Forever && k <= today }
	for _, it := range files {
		if it.Policy != from.Name {
			continue
		}
		out.Files++
		keep, _ := KeepUntil(it.FileDate, to.Value) // a period with no file_date has none
		pf := PolicyFile{FileItem: it, OldKeepUntil: it.KeepUntil}
		pf.KeepUntil = keep
		if past(keep) && !past(it.KeepUntil) {
			out.Past = append(out.Past, pf)
		}
		if it.FileDate == "" && IsPeriod(to.Value) && !IsPeriod(from.Value) {
			out.NoFileDate = append(out.NoFileDate, pf)
		}
	}
	return out, nil
}

// workOutFiles works out again the keep_until of every file on the policy (present or missing; not those filo
// deleted, whose row is their history) from its value, and returns how many it changed.
func workOutFiles(ctx context.Context, tx *sql.Tx, policyID int64, value string) (int64, error) {
	type row struct {
		id             int64
		fileDate, keep string
	}
	rows, err := queryAll(ctx, tx, func(rows *sql.Rows) (row, error) {
		var r row
		err := rows.Scan(&r.id, &r.fileDate, &r.keep)
		return r, err
	}, `SELECT id, coalesce(file_date, ''), coalesce(keep_until, '') FROM files
		WHERE policy_id = ? AND deleted_utc IS NULL`, policyID)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, r := range rows {
		keep, _ := KeepUntil(r.fileDate, value) // a period with no file_date has none: it needs a file_date
		n++
		if keep == r.keep {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE files SET keep_until = ? WHERE id = ?`, nullString(keep),
			r.id); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// SetPolicy gives a policy a value, or changes it, and works out the keep_until of every file on it again. It returns
// how many files are on it. The question before it is the CLI's (PreviewPolicy).
func (s *Store) SetPolicy(ctx context.Context, name, value string, now time.Time) (int64, error) {
	p, err := s.Policy(ctx, name)
	if err != nil {
		return 0, err
	}
	if err := checkPolicyValue(value); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE policies SET value = ?, changed_utc = ? WHERE id = ?`, nullString(value),
		utc(now), p.ID); err != nil {
		return 0, fmt.Errorf("set policy %s: %w", p.Name, err)
	}
	n, err := workOutFiles(ctx, tx, p.ID, value)
	if err != nil {
		return 0, fmt.Errorf("set policy %s: %w", p.Name, err)
	}
	if value != p.Value {
		if err := recordPolicyChange(ctx, tx, "set", p.Name, p.Value, value, n, now); err != nil {
			return 0, err
		}
	}
	return n, s.commit(tx)
}

// MovePolicy moves every file of policy from to policy to: into a new name it is a rename (the value goes with it),
// into an existing policy a merge (the files take its value, and from stays, with no files). It returns how many
// files moved.
func (s *Store) MovePolicy(ctx context.Context, from, to string, now time.Time) (int64, error) {
	p, err := s.Policy(ctx, from)
	if err != nil {
		return 0, err
	}
	n, err := PolicyName(to)
	if err != nil {
		return 0, err
	}
	target, err := s.Policy(ctx, n)
	if err != nil && !errors.Is(err, ErrNoPolicy) {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var moved int64
	if target.ID == 0 { // a new name: a rename
		if _, err := tx.ExecContext(ctx, `UPDATE policies SET name = ?, changed_utc = ? WHERE id = ?`, n, utc(now),
			p.ID); err != nil {
			return 0, fmt.Errorf("rename policy %s: %w", p.Name, err)
		}
		moved = p.Files
		if err := recordPolicyChange(ctx, tx, "rename", p.Name, "", n, moved, now); err != nil {
			return 0, err
		}
		return moved, s.commit(tx)
	}
	if target.ID == p.ID {
		return 0, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE files SET policy_id = ? WHERE policy_id = ? AND deleted_utc IS NULL`,
		target.ID, p.ID)
	if err != nil {
		return 0, fmt.Errorf("move policy %s: %w", p.Name, err)
	}
	if moved, err = res.RowsAffected(); err != nil {
		return 0, err
	}
	if _, err := workOutFiles(ctx, tx, target.ID, target.Value); err != nil {
		return 0, fmt.Errorf("move policy %s: %w", p.Name, err)
	}
	if err := recordPolicyChange(ctx, tx, "move", p.Name, "", target.Name, moved, now); err != nil {
		return 0, err
	}
	return moved, s.commit(tx)
}

// RemovePolicy removes a policy that no present file uses (ErrPolicyInUse otherwise). Files filo deleted, and
// missing files, that were on it keep their keep_until with the policy's value as a retention of their own, so a
// missing file that comes back still has one.
func (s *Store) RemovePolicy(ctx context.Context, name string, now time.Time) error {
	p, err := s.Policy(ctx, name)
	if err != nil {
		return err
	}
	if p.Files > 0 {
		return fmt.Errorf("%s is used by %d files: %w", p.Name, p.Files, ErrPolicyInUse)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE files SET retention = ?, policy_id = NULL WHERE policy_id = ?`,
		nullString(p.Value), p.ID); err != nil {
		return fmt.Errorf("remove policy %s: %w", p.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM policies WHERE id = ?`, p.ID); err != nil {
		return fmt.Errorf("remove policy %s: %w", p.Name, err)
	}
	if err := recordPolicyChange(ctx, tx, "remove", p.Name, p.Value, "", 0, now); err != nil {
		return err
	}
	return s.commit(tx)
}

// PolicyFiles returns the present files on a policy, with their status, ordered by root and path.
func (s *Store) PolicyFiles(ctx context.Context, name, today string) ([]FileItem, error) {
	p, err := s.Policy(ctx, name)
	if err != nil {
		return nil, err
	}
	files, err := s.Files(ctx, today, Filter{})
	if err != nil {
		return nil, err
	}
	var out []FileItem
	for _, it := range files {
		if it.Policy == p.Name {
			out = append(out, it)
		}
	}
	return out, nil
}
