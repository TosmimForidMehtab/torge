// Package sqldb adapts database/sql to Torge's database lifecycle: pooled
// connections with production defaults, health checks, graceful close and
// context-carried transactions. It does not abstract SQL; use *sql.DB, sqlx,
// or any query builder on top.
//
//	db, err := sqldb.Open("pgx", cfg.DatabaseURL.Value())
//	app.Database(db)
//
//	err := db.Transaction(ctx, func(ctx context.Context) error {
//	    if _, err := db.Conn(ctx).ExecContext(ctx, `UPDATE ...`); err != nil {
//	        return err
//	    }
//	    return orders.Create(ctx, order) // also uses db.Conn(ctx)
//	})
//
// Instrumentation is left to driver wrappers such as otelsql, which work
// unchanged because sqldb only manages the *sql.DB.
package sqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Options configures the connection pool. Zero values use the defaults.
type Options struct {
	// MaxOpenConns bounds open connections (default 25).
	MaxOpenConns int
	// MaxIdleConns bounds idle connections (default 25).
	MaxIdleConns int
	// ConnMaxLifetime recycles connections periodically (default 30m).
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime closes idle connections (default 5m).
	ConnMaxIdleTime time.Duration
}

// DB wraps a *sql.DB.
type DB struct {
	db *sql.DB
}

// Querier is the query surface shared by *sql.DB, *sql.Tx and *sql.Conn.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
)

// Open opens a database and configures its pool. It does not connect; the
// application pings it at startup.
func Open(driver, dsn string, opts ...Options) (*DB, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("sqldb: open %s: %w", driver, err)
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	configure(db, o)
	return &DB{db: db}, nil
}

// Wrap adapts an existing *sql.DB without changing its pool settings.
func Wrap(db *sql.DB) *DB { return &DB{db: db} }

func configure(db *sql.DB, o Options) {
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 25
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = o.MaxOpenConns
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = 30 * time.Minute
	}
	if o.ConnMaxIdleTime <= 0 {
		o.ConnMaxIdleTime = 5 * time.Minute
	}
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxIdleConns)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	db.SetConnMaxIdleTime(o.ConnMaxIdleTime)
}

// SQL returns the underlying *sql.DB.
func (d *DB) SQL() *sql.DB { return d.db }

// Ping verifies connectivity. It implements torge.Database.
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Close closes the pool. It implements torge.Database.
func (d *DB) Close() error { return d.db.Close() }

// Stats returns pool statistics.
func (d *DB) Stats() sql.DBStats { return d.db.Stats() }

type txKey struct{ db *DB }

// TxFrom returns the transaction of d carried by ctx, if any.
func (d *DB) TxFrom(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(txKey{d}).(*sql.Tx)
	return tx, ok
}

// Conn returns the transaction carried by ctx, or the pool when there is
// none. Repositories call it so they participate in transactions without
// knowing about them.
func (d *DB) Conn(ctx context.Context) Querier {
	if tx, ok := d.TxFrom(ctx); ok {
		return tx
	}
	return d.db
}

// Transaction runs fn in a transaction carried by the context passed to fn.
// It commits when fn returns nil and rolls back when fn returns an error or
// panics (the panic is re-raised). A nested call joins the outer transaction.
// It implements torge.Transactor.
func (d *DB) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return d.TransactionWith(ctx, nil, fn)
}

// TransactionWith is Transaction with explicit options (isolation level,
// read-only).
func (d *DB) TransactionWith(ctx context.Context, opts *sql.TxOptions, fn func(ctx context.Context) error) (err error) {
	if _, ok := d.TxFrom(ctx); ok {
		return fn(ctx)
	}
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("sqldb: begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(context.WithValue(ctx, txKey{d}, tx)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("sqldb: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqldb: commit: %w", err)
	}
	return nil
}
