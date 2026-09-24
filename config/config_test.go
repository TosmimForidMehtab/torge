package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/config"
)

type DB struct {
	URL      config.Secret `env:"URL,required"`
	MaxConns int           `env:"MAX_CONNS" default:"10" validate:"min=1"`
}

type Config struct {
	Port     int           `env:"PORT" default:"8080" validate:"min=1,max=65535"`
	Debug    bool          `env:"DEBUG"`
	Timeout  time.Duration `env:"TIMEOUT" default:"5s"`
	Origins  []string      `env:"ORIGINS"`
	Ratio    float64       `env:"RATIO" default:"0.5"`
	APIKey   string        `env:"API_KEY,secret"`
	Endpoint url.URL       `env:"ENDPOINT" default:"https://api.example.com"`
	DB       DB            `envPrefix:"DB_"`
	Optional *DB           `envPrefix:"REPLICA_"`
}

func TestLoad(t *testing.T) {
	var cfg Config
	err := config.Load(&cfg, config.WithPrefix("APP_"), config.WithMap(map[string]string{
		"APP_PORT":              "9090",
		"APP_DEBUG":             "true",
		"APP_ORIGINS":           "https://a.com, https://b.com",
		"APP_API_KEY":           "k-123",
		"APP_DB_URL":            "postgres://user:pass@db/app",
		"APP_REPLICA_URL":       "postgres://replica",
		"APP_REPLICA_MAX_CONNS": "3",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9090 || !cfg.Debug || cfg.Timeout != 5*time.Second || len(cfg.Origins) != 2 ||
		cfg.Ratio != 0.5 || cfg.DB.URL.Value() != "postgres://user:pass@db/app" || cfg.DB.MaxConns != 10 ||
		cfg.Endpoint.Host != "api.example.com" || cfg.Optional == nil || cfg.Optional.MaxConns != 3 {
		t.Fatalf("unexpected config %+v", cfg)
	}
}

func TestLoadReportsAllProblems(t *testing.T) {
	var cfg Config
	err := config.Load(&cfg, config.WithMap(map[string]string{
		"PORT": "http", "TIMEOUT": "soon", "REPLICA_URL": "x",
	}))
	var cerr *config.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *config.Error, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		`PORT (Port): invalid integer "http"`,
		`TIMEOUT (Timeout): invalid duration "soon"`,
		"DB_URL (DB.URL): required but not set",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestValidationRulesUseVariableNames(t *testing.T) {
	var cfg Config
	err := config.Load(&cfg, config.WithMap(map[string]string{"PORT": "70000", "DB_URL": "x"}))
	if err == nil || !strings.Contains(err.Error(), "PORT (Port): must be at most 65535") {
		t.Fatalf("got %v", err)
	}
}

func TestSecretsAreNeverPrinted(t *testing.T) {
	s := config.Secret("hunter2")
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("cfg", "secret", s)
	j, _ := json.Marshal(struct{ S config.Secret }{s})
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %s %q %+v %#v", s, s, s, s, s), string(j), logBuf.String()} {
		if strings.Contains(out, "hunter2") {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	if s.Value() != "hunter2" {
		t.Fatal("Value must return the secret")
	}

	cfg := Config{APIKey: "plain-secret", DB: DB{URL: "postgres://pw"}}
	for _, e := range config.Redacted(&cfg) {
		if strings.Contains(e.Value, "secret") || strings.Contains(e.Value, "pw") {
			t.Fatalf("Redacted leaked %s=%s", e.Var, e.Value)
		}
	}
}

func TestParseEnv(t *testing.T) {
	for in, want := range map[string]config.Env{"prod": config.Production, "Dev": config.Development, "testing": config.Test} {
		if got, err := config.ParseEnv(in); err != nil || got != want {
			t.Errorf("ParseEnv(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := config.ParseEnv("staging"); err == nil {
		t.Fatal("unknown environments must be rejected")
	}
	t.Setenv("TORGE_ENV", "")
	t.Setenv("APP_ENV", "")
	if env, err := config.CurrentEnv(); env != config.Production || err != nil {
		t.Fatal("the default environment must be production")
	}
	t.Setenv("TORGE_ENV", "bogus")
	if env, err := config.CurrentEnv(); env != config.Production || err == nil {
		t.Fatal("an invalid environment must fall back to production with an error")
	}
}
