// Package torgepgx adapts a pgx connection pool to Torge's database
// lifecycle (startup ping, readiness check, graceful close) and provides
// context-carried transactions. SQL stays plain pgx.
//
//	db, err := torgepgx.Open(ctx, cfg.DatabaseURL.Value())
//	app.Database(db)
//
//	err = app.Transaction(ctx, func(ctx context.Context) error {
//	    _, err := db.Conn(ctx).Exec(ctx, `UPDATE accounts SET ...`)
//	    return err
//	})
//
// For query tracing, set a pgx QueryTracer (for example otelpgx) on the pool
// config passed to OpenConfig.
package torgepgx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the query surface shared by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)

// DB wraps a *pgxpool.Pool.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates a pool from a connection string. It does not wait for a
// connection; the application pings it at startup.
func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("torgepgx: parse config: %w", err)
	}
	return OpenConfig(ctx, cfg)
}

// OpenConfig creates a pool from a parsed configuration.
func OpenConfig(ctx context.Context, cfg *pgxpool.Config) (*DB, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("torgepgx: create pool: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Wrap adapts an existing pool.
func Wrap(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

// Pool returns the underlying pool.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Ping implements torge.Database.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// Close implements torge.Database.
func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

type txKey struct{ db *DB }

// TxFrom returns the transaction carried by ctx, if any.
func (d *DB) TxFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{d}).(pgx.Tx)
	return tx, ok
}

// Conn returns the transaction carried by ctx, or the pool.
func (d *DB) Conn(ctx context.Context) Querier {
	if tx, ok := d.TxFrom(ctx); ok {
		return tx
	}
	return d.pool
}

// Transaction implements torge.Transactor: fn runs in a transaction carried
// by its context, committed on nil and rolled back on error or panic. Nested
// calls join the outer transaction.
func (d *DB) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return d.TransactionWith(ctx, pgx.TxOptions{}, fn)
}

// TransactionWith is Transaction with explicit options.
func (d *DB) TransactionWith(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error {
	if _, ok := d.TxFrom(ctx); ok {
		return fn(ctx)
	}
	tx, err := d.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("torgepgx: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if err := fn(context.WithValue(ctx, txKey{d}, tx)); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("torgepgx: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("torgepgx: commit: %w", err)
	}
	return nil
}
