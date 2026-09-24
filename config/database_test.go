package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge/config"
)

type dbConfig struct {
	DB      config.Database  `envPrefix:"DB_"`
	Replica *config.Database `envPrefix:"REPLICA_"`
}

func loadDB(t *testing.T, env map[string]string) (dbConfig, error) {
	t.Helper()
	var cfg dbConfig
	err := config.Load(&cfg, config.WithMap(env))
	return cfg, err
}

func TestDatabaseFromParts(t *testing.T) {
	cfg, err := loadDB(t, map[string]string{
		"DB_HOST": "db.internal", "DB_PORT": "5433", "DB_USER": "app",
		"DB_PASSWORD": "p@ss:w/rd?#", "DB_NAME": "shop", "DB_PARAMS": "sslmode=require",
	})
	if err != nil {
		t.Fatal(err)
	}
	db := cfg.DB
	if got, want := db.PostgresDSN(), "postgres://app:p%40ss%3Aw%2Frd%3F%23@db.internal:5433/shop?sslmode=require"; got != want {
		t.Errorf("PostgresDSN\n got %s\nwant %s", got, want)
	}
	if got, want := db.MySQLDSN(), "app:p@ss:w/rd?#@tcp(db.internal:5433)/shop?parseTime=true&sslmode=require"; got != want {
		t.Errorf("MySQLDSN\n got %s\nwant %s", got, want)
	}
	if got, want := db.MongoURI(), "mongodb://app:p%40ss%3Aw%2Frd%3F%23@db.internal:5433/?sslmode=require"; got != want {
		t.Errorf("MongoURI\n got %s\nwant %s", got, want)
	}
	if db.Database() != "shop" {
		t.Errorf("Database() = %q", db.Database())
	}
	if cfg.Replica != nil {
		t.Error("an unset optional database section must stay nil")
	}
	if s := fmt.Sprintf("%v %+v", db, cfg); strings.Contains(s, "p@ss") {
		t.Fatalf("password leaked when printing: %s", s)
	}
}

func TestDatabaseURLWins(t *testing.T) {
	cfg, err := loadDB(t, map[string]string{
		"DB_URL":  "postgres://u:secret@primary:5432/app?sslmode=disable",
		"DB_HOST": "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB.PostgresDSN() != "postgres://u:secret@primary:5432/app?sslmode=disable" || cfg.DB.Database() != "app" {
		t.Fatalf("URL must take precedence: %s", cfg.DB.PostgresDSN())
	}
}

func TestMySQLURLConversion(t *testing.T) {
	db := config.Database{URL: "mysql://root:p%40ss@localhost:3306/shop?charset=utf8mb4&parseTime=false"}
	if got, want := db.MySQLDSN(), "root:p@ss@tcp(localhost:3306)/shop?charset=utf8mb4&parseTime=false"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	native := config.Database{URL: "root@tcp(localhost)/shop"}
	if native.MySQLDSN() != "root@tcp(localhost)/shop" {
		t.Fatal("native DSNs must pass through unchanged")
	}
}

func TestSQLite(t *testing.T) {
	for _, tc := range []struct {
		db   config.Database
		want string
	}{
		{config.Database{Name: "data/app.db", Params: "_pragma=foreign_keys(1)"}, "data/app.db?_pragma=foreign_keys(1)"},
		{config.Database{URL: "sqlite://app.db"}, "app.db"},
		{config.Database{URL: "file::memory:?cache=shared"}, "file::memory:?cache=shared"},
	} {
		if got := tc.db.SQLiteDSN(); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	if _, err := loadDB(t, map[string]string{"DB_NAME": "app.db"}); err != nil {
		t.Fatalf("a name alone is valid for SQLite: %v", err)
	}
}

func TestDatabaseValidation(t *testing.T) {
	_, err := loadDB(t, map[string]string{"DB_PORT": "70000", "DB_PARAMS": "a=%zz"})
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		"DB_URL (DB.URL): is required: set the URL, or the host and database name",
		"DB_PORT (DB.Port): must be at most 65535",
		"DB_PARAMS (DB.Params): must be in key=value&key=value form",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	// A partially configured optional section is validated too.
	if _, err := loadDB(t, map[string]string{"DB_HOST": "x", "REPLICA_USER": "ro"}); err == nil ||
		!strings.Contains(err.Error(), "REPLICA_URL") {
		t.Fatalf("got %v", err)
	}
}

func TestIPv6Hosts(t *testing.T) {
	db := config.Database{Host: "::1", Port: 5432, Name: "app"}
	if got := db.PostgresDSN(); got != "postgres://[::1]:5432/app" {
		t.Fatalf("got %s", got)
	}
	db.Port = 0
	if got := db.PostgresDSN(); got != "postgres://[::1]/app" {
		t.Fatalf("got %s", got)
	}
}
