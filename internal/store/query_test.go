package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestInBatches(t *testing.T) {
	for _, n := range []int{0, 1, 500, 501, 1234} {
		values := make([]string, n)
		for i := range values {
			values[i] = strconv.Itoa(i)
		}
		var sizes []int
		var seen []string
		err := inBatches(values, func(in string, args []any) error {
			if strings.Count(in, "?") != len(args) {
				t.Errorf("n=%d: placeholders %q for %d args", n, in, len(args))
			}
			sizes = append(sizes, len(args))
			for _, a := range args {
				seen = append(seen, a.(string))
			}
			return nil
		})
		if err != nil || strings.Join(seen, ",") != strings.Join(values, ",") {
			t.Errorf("n=%d: err %v, values passed %d", n, err, len(seen))
		}
		for _, sz := range sizes {
			if sz < 1 || sz > 500 {
				t.Errorf("n=%d: a batch of %d", n, sz)
			}
		}
	}
	stop := errors.New("stop")
	calls := 0
	err := inBatches(make([]string, 1200), func(string, []any) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("an error from fn: err %v after %d calls, want stop after 1", err, calls)
	}
}

func TestQueryAll(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	got, err := queryAll(ctx, s.db, func(rows *sql.Rows) (int, error) {
		var n int
		err := rows.Scan(&n)
		return n, err
	}, `SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT ? ORDER BY 1`, 3)
	if err != nil || len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("queryAll = %v, %v; want [1 2 3]", got, err)
	}
	bad := errors.New("bad row")
	if _, err := queryAll(ctx, s.db, func(*sql.Rows) (int, error) { return 0, bad }, `SELECT 1`); !errors.Is(err, bad) {
		t.Errorf("a scan error: %v, want it returned", err)
	}
	if _, err := queryAll(ctx, s.db, func(*sql.Rows) (int, error) { return 0, nil }, `SELECT nope FROM`); err == nil {
		t.Error("a broken query returned no error")
	}
}

// TestStmtCache: each distinct query is prepared once and gives what the transaction gives, and a query that cannot
// be prepared fails as it would on the transaction.
func TestStmtCache(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c := newStmtCache(tx)
	if _, err := c.ExecContext(ctx, `CREATE TEMP TABLE kv (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := c.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES (?, ?)`, i, "v"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		var v string
		if err := c.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, i).Scan(&v); err != nil || v != "v"+strconv.Itoa(i) {
			t.Errorf("row %d: %q, %v", i, v, err)
		}
	}
	vs, err := queryAll(ctx, c, func(rs *sql.Rows) (string, error) {
		var v string
		return v, rs.Scan(&v)
	}, `SELECT v FROM kv ORDER BY k`)
	if err != nil || strings.Join(vs, ",") != "v0,v1,v2" {
		t.Errorf("queryAll = %v, %v", vs, err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM kv`).Scan(&n); err != nil || n != 3 {
		t.Errorf("the transaction sees %d rows, %v; want the cache's 3", n, err)
	}
	if len(c.stmts) != 4 {
		t.Errorf("%d statements prepared; want 4, one per distinct query", len(c.stmts))
	}
	bad := `SELECT nope FROM kv`
	if err := c.QueryRowContext(ctx, bad).Scan(&n); err == nil {
		t.Error("QueryRowContext of a bad query: no error")
	}
	if _, err := c.QueryContext(ctx, bad); err == nil {
		t.Error("QueryContext of a bad query: no error")
	}
	if _, err := c.ExecContext(ctx, `DELETE FROM nope`); err == nil {
		t.Error("ExecContext of a bad query: no error")
	}
	if len(c.stmts) != 4 {
		t.Errorf("%d statements after failed ones; want 4", len(c.stmts))
	}
}
