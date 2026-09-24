package torgepgx_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	torgepgx "github.com/TosmimForidMehtab/torge/contrib/pgx"
)

// Compile-time checks that DB satisfies the framework contracts.
var (
	_ torge.Database   = (*torgepgx.DB)(nil)
	_ torge.Transactor = (*torgepgx.DB)(nil)
)

func TestOpenRejectsInvalidDSN(t *testing.T) {
	if _, err := torgepgx.Open(context.Background(), "postgres://%zz"); err == nil {
		t.Fatal("expected a parse error")
	}
}

// TestTransactions runs against a real PostgreSQL when TORGE_TEST_POSTGRES_URL
// is set, for example postgres://postgres:postgres@localhost:5432/postgres.
func TestTransactions(t *testing.T) {
	dsn := os.Getenv("TORGE_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set TORGE_TEST_POSTGRES_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, err := torgepgx.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	// A regular table, because a temporary one is visible only to the pool
	// connection that created it.
	for _, stmt := range []string{`DROP TABLE IF EXISTS torge_tx_test`, `CREATE TABLE torge_tx_test (v int)`} {
		if _, err := db.Conn(ctx).Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = db.Conn(ctx).Exec(ctx, `DROP TABLE IF EXISTS torge_tx_test`) })
	count := func() int {
		var n int
		if err := db.Conn(ctx).QueryRow(ctx, `SELECT count(*) FROM torge_tx_test`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	boom := errors.New("boom")
	err = db.Transaction(ctx, func(ctx context.Context) error {
		if _, err := db.Conn(ctx).Exec(ctx, `INSERT INTO torge_tx_test VALUES (1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != 0 {
		t.Fatalf("rollback: err=%v rows=%d", err, count())
	}

	err = db.Transaction(ctx, func(ctx context.Context) error {
		if _, err := db.Conn(ctx).Exec(ctx, `INSERT INTO torge_tx_test VALUES (2)`); err != nil {
			return err
		}
		// A nested call joins the outer transaction.
		return db.Transaction(ctx, func(ctx context.Context) error {
			_, err := db.Conn(ctx).Exec(ctx, `INSERT INTO torge_tx_test VALUES (3)`)
			return err
		})
	})
	if err != nil || count() != 2 {
		t.Fatalf("commit: err=%v rows=%d", err, count())
	}
}
