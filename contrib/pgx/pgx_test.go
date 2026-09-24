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
	if _, err := db.Conn(ctx).Exec(ctx, `CREATE TEMP TABLE IF NOT EXISTS torge_t (v int)`); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = db.Transaction(ctx, func(ctx context.Context) error {
		if _, err := db.Conn(ctx).Exec(ctx, `INSERT INTO torge_t VALUES (1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	if err := db.Conn(ctx).QueryRow(ctx, `SELECT count(*) FROM torge_t`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback failed: n=%d err=%v", n, err)
	}
}
