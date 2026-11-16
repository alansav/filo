package store

import (
	"strconv"
	"testing"
)

// Directory rules (docs/design.md, Lifespan, Directory rules): the register and store, slice 1.

// TestMigrateFromV9 is scenario G1: the upgrade creates the rules table and the files' columns, and nothing else
// changes.
func TestMigrateFromV9(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, dropRules, `UPDATE meta SET value = '9' WHERE key = 'schema_version'`,
		`INSERT INTO roots (id, path, added_utc) VALUES (1, '/r', '2026-01-01T00:00:00Z')`,
		`INSERT INTO scans (id, started_utc, root_id) VALUES (1, '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO policies (id, name, value, added_utc, changed_utc) VALUES (1, 'bills', '7y', 'x', 'x')`,
		`INSERT INTO files (id, kind, size, mtime_ns, hash, first_seen_scan, file_date, policy_id, keep_until)
			VALUES (1, 'file', 3, 1, 'a', 1, '2020-01-01', 1, '2027-01-01')`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) || SchemaVersion != 10 {
		t.Fatalf("schema_version = %s after upgrade, want 10", v)
	}
	var policy, keep string
	var rule, dated *string
	if err := s.db.QueryRow(`SELECT policy_id, keep_until, rule_id, rule_dated_utc FROM files WHERE id = 1`).
		Scan(&policy, &keep, &rule, &dated); err != nil || policy != "1" || keep != "2027-01-01" || rule != nil ||
		dated != nil {
		t.Errorf("file after upgrade: policy %s, %s, rule %v, dated %v (%v)", policy, keep, rule, dated, err)
	}
	counts, err := s.RowCounts(t.Context())
	if err != nil || counts["rules"] != 0 || counts["policies"] != 1 {
		t.Errorf("row counts after upgrade: %v, %v", counts, err)
	}
	if notes := s.UpgradeNotes(); len(notes) != 0 {
		t.Errorf("UpgradeNotes = %q, want none", notes)
	}
}
