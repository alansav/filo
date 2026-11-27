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
func inBatches[T any](values []T, fn func(placeholders string, args []any) error) error {
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

// execer is a querier that also writes: a *sql.Tx, or a *stmtCache over one.
type execer interface {
	querier
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// stmtCache runs queries in a transaction, preparing each distinct query once. A run that repeats the same few
// queries for each of thousands of rows (a combined CSV) otherwise has SQLite compile them again for every row, a third
// of its time. The statements belong to the transaction and are closed with it.
type stmtCache struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func newStmtCache(tx *sql.Tx) *stmtCache { return &stmtCache{tx: tx, stmts: map[string]*sql.Stmt{}} }

func (c *stmtCache) prepare(ctx context.Context, q string) (*sql.Stmt, error) {
	if st, ok := c.stmts[q]; ok {
		return st, nil
	}
	st, err := c.tx.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	c.stmts[q] = st
	return st, nil
}

func (c *stmtCache) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	st, err := c.prepare(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.QueryContext(ctx, args...)
}

// QueryRowContext reports a query that cannot be prepared through the row, as the transaction's own would: there is
// no other way to make a *sql.Row carry an error, so the query is run on the transaction, uncached. A query that is
// wrong fails there the same way; one whose prepare failed for a passing reason may succeed there.
func (c *stmtCache) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	st, err := c.prepare(ctx, q)
	if err != nil {
		return c.tx.QueryRowContext(ctx, q, args...)
	}
	return st.QueryRowContext(ctx, args...)
}

func (c *stmtCache) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	st, err := c.prepare(ctx, q)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(ctx, args...)
}
