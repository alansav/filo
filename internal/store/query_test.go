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
