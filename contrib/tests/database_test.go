package tests

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/TosmimForidMehtab/torge/config"
	"github.com/TosmimForidMehtab/torge/sqldb"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

// The password deliberately contains every character that breaks naive
// connection-string building.
const trickyPassword = `p@ss:w/rd?#%&=`

func loadParts(t *testing.T, params string) config.Database {
	t.Helper()
	var cfg struct {
		DB config.Database `envPrefix:"DB_"`
	}
	err := config.Load(&cfg, config.WithMap(map[string]string{
		"DB_HOST": "db.internal", "DB_PORT": "6543", "DB_USER": "app",
		"DB_PASSWORD": trickyPassword, "DB_NAME": "shop", "DB_PARAMS": params,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.DB
}

func TestPostgresDSNParsesWithPgx(t *testing.T) {
	db := loadParts(t, "application_name=torge")
	cfg, err := pgx.ParseConfig(db.PostgresDSN())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "db.internal" || cfg.Port != 6543 || cfg.User != "app" || cfg.Password != trickyPassword ||
		cfg.Database != "shop" || cfg.RuntimeParams["application_name"] != "torge" {
		t.Fatalf("pgx parsed %+v", cfg.Config)
	}
}

func TestMySQLDSNParsesWithDriver(t *testing.T) {
	db := loadParts(t, "autocommit=true")
	cfg, err := mysql.ParseDSN(db.MySQLDSN())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "db.internal:6543" || cfg.User != "app" || cfg.Passwd != trickyPassword ||
		cfg.DBName != "shop" || !cfg.ParseTime || cfg.Params["autocommit"] != "true" {
		t.Fatalf("mysql parsed %+v", cfg)
	}

	// The mysql:// URL form converts to the same result.
	var fromURL struct {
		DB config.Database `envPrefix:"DB_"`
	}
	if err := config.Load(&fromURL, config.WithMap(map[string]string{"DB_URL": "mysql://app:p%40ss@localhost:3306/shop"})); err != nil {
		t.Fatal(err)
	}
	cfg, err = mysql.ParseDSN(fromURL.DB.MySQLDSN())
	if err != nil || cfg.Passwd != "p@ss" || cfg.Addr != "localhost:3306" || cfg.DBName != "shop" {
		t.Fatalf("mysql URL form: %+v %v", cfg, err)
	}
}

func TestMongoURIParsesWithDriver(t *testing.T) {
	db := loadParts(t, "authSource=admin")
	opts := options.Client().ApplyURI(db.MongoURI())
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if opts.Auth == nil || opts.Auth.Username != "app" || opts.Auth.Password != trickyPassword ||
		opts.Auth.AuthSource != "admin" || opts.Hosts[0] != "db.internal:6543" {
		t.Fatalf("mongo parsed %+v %+v", opts.Hosts, opts.Auth)
	}
	if db.Database() != "shop" {
		t.Fatalf("database name %q", db.Database())
	}
}

// TestSQLiteEndToEnd runs a real SQLite database through config, sqldb and
// the application lifecycle, including transaction commit and rollback.
func TestSQLiteEndToEnd(t *testing.T) {
	var cfg struct {
		DB config.Database `envPrefix:"DB_"`
	}
	path := filepath.Join(t.TempDir(), "app.db")
	if err := config.Load(&cfg, config.WithMap(map[string]string{
		"DB_NAME": path, "DB_PARAMS": "_pragma=foreign_keys(1)",
	})); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open("sqlite", cfg.DB.SQLiteDSN(), sqldb.Options{MaxOpenConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	app := torgetest.NewApp(t)
	app.Database(db)
	torgetest.Start(t, app)

	ctx := context.Background()
	if _, err := db.Conn(ctx).ExecContext(ctx, `CREATE TABLE items (name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := db.Conn(ctx).QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	boom := errors.New("boom")
	err = app.Transaction(ctx, func(ctx context.Context) error {
		if _, err := db.Conn(ctx).ExecContext(ctx, `INSERT INTO items VALUES ('a')`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != 0 {
		t.Fatalf("rollback: err=%v rows=%d", err, count())
	}
	if err := app.Transaction(ctx, func(ctx context.Context) error {
		_, err := db.Conn(ctx).ExecContext(ctx, `INSERT INTO items VALUES ('b')`)
		return err
	}); err != nil || count() != 1 {
		t.Fatalf("commit: err=%v rows=%d", err, count())
	}
	torgetest.New(t, app).GET("/ready").Do().ExpectStatus(200).ExpectJSONPath("status", "up")
}
