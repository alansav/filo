package store

import (
	"context"
	"database/sql"
	"strings"
)

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// queryAll runs query on q and returns one value per row, read by scan. It closes the rows, and returns the first
// error from the query, a scan or the rows, unwrapped: the caller says what it was reading.
func queryAll[T any](ctx context.Context, q querier, scan func(*sql.Rows) (T, error), query string,
	args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// inBatches calls fn for the values in batches of at most 500, small enough for SQLite's limit on query arguments,
// with the placeholders for an IN list ("?,?,?") and the batch as arguments.
func inBatches(values []string, fn func(placeholders string, args []any) error) error {
	const batch = 500
	for start := 0; start < len(values); start += batch {
		part := values[start:min(start+batch, len(values))]
		args := make([]any, len(part))
		for i, v := range part {
			args[i] = v
		}
		if err := fn("?"+strings.Repeat(",?", len(part)-1), args); err != nil {
			return err
		}
	}
	return nil
}
