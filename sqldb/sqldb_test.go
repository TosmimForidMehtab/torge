package sqldb_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/TosmimForidMehtab/torge/sqldb"
)

// recordingDriver is a minimal driver that records transaction calls.
type recordingDriver struct {
	mu     sync.Mutex
	events []string
}

func (d *recordingDriver) record(e string) {
	d.mu.Lock()
	d.events = append(d.events, e)
	d.mu.Unlock()
}

func (d *recordingDriver) log() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.events, ",")
}

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &conn{d: d}, nil }

type conn struct{ d *recordingDriver }

func (c *conn) Prepare(q string) (driver.Stmt, error) { return &stmt{c: c, q: q}, nil }
func (c *conn) Close() error                          { return nil }
func (c *conn) Begin() (driver.Tx, error)             { c.d.record("begin"); return &tx{c: c}, nil }

type tx struct{ c *conn }

func (t *tx) Commit() error   { t.c.d.record("commit"); return nil }
func (t *tx) Rollback() error { t.c.d.record("rollback"); return nil }

type stmt struct {
	c *conn
	q string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }
func (s *stmt) Exec([]driver.Value) (driver.Result, error) {
	s.c.d.record("exec:" + s.q)
	return driver.RowsAffected(1), nil
}
func (s *stmt) Query([]driver.Value) (driver.Rows, error) { return emptyRows{}, nil }

type emptyRows struct{}

func (emptyRows) Columns() []string         { return nil }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

var drv = &recordingDriver{}

func init() { sql.Register("recording", drv) }

func TestTransactionCommitRollbackAndNesting(t *testing.T) {
	db, err := sqldb.Open("recording", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	drv.events = nil

	err = db.Transaction(ctx, func(ctx context.Context) error {
		if _, ok := db.TxFrom(ctx); !ok {
			t.Error("transaction must be carried by the context")
		}
		if _, err := db.Conn(ctx).ExecContext(ctx, "a"); err != nil {
			return err
		}
		// Nested calls join the outer transaction.
		return db.Transaction(ctx, func(ctx context.Context) error {
			_, err := db.Conn(ctx).ExecContext(ctx, "b")
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := drv.log(); got != "begin,exec:a,exec:b,commit" {
		t.Fatalf("got %s", got)
	}

	drv.events = nil
	boom := errors.New("boom")
	if err := db.Transaction(ctx, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if got := drv.log(); got != "begin,rollback" {
		t.Fatalf("got %s", got)
	}

	drv.events = nil
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic must be re-raised")
			}
		}()
		_ = db.Transaction(ctx, func(context.Context) error { panic("x") })
	}()
	if got := drv.log(); got != "begin,rollback" {
		t.Fatalf("got %s", got)
	}

	if _, ok := db.Conn(ctx).(*sql.DB); !ok {
		t.Fatal("outside a transaction Conn must return the pool")
	}
	if db.Stats().MaxOpenConnections != 25 {
		t.Fatalf("pool defaults not applied: %+v", db.Stats())
	}
}
