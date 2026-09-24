package config_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge/config"
)

func TestRedisFromParts(t *testing.T) {
	var cfg struct {
		Redis config.Redis `envPrefix:"REDIS_"`
	}
	if err := config.Load(&cfg, config.WithMap(map[string]string{
		"REDIS_HOST": "cache", "REDIS_PASSWORD": "p@ss/word", "REDIS_DB": "2",
	})); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Redis.ConnectionURL(), "redis://:p%40ss%2Fword@cache:6379/2"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if cfg.Redis.Addr() != "cache:6379" {
		t.Fatalf("Addr = %s", cfg.Redis.Addr())
	}
	if s := fmt.Sprintf("%+v", cfg); strings.Contains(s, "p@ss") {
		t.Fatalf("password leaked: %s", s)
	}
}

func TestRedisURLWinsAndIsValidated(t *testing.T) {
	r := config.Redis{URL: "rediss://default:x@cache.example.com:6380/0", Host: "ignored"}
	if r.ConnectionURL() != string(r.URL) || r.Addr() != "cache.example.com:6380" {
		t.Fatalf("URL must take precedence: %s %s", r.ConnectionURL(), r.Addr())
	}
	var cfg struct {
		Redis config.Redis `envPrefix:"REDIS_"`
	}
	err := config.Load(&cfg, config.WithMap(map[string]string{"REDIS_URL": "http://cache", "REDIS_DB": "-1"}))
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL (Redis.URL): must be a redis://") ||
		!strings.Contains(err.Error(), "REDIS_DB (Redis.DB): must be at least 0") {
		t.Fatalf("got %v", err)
	}
	var fresh struct {
		Redis config.Redis `envPrefix:"REDIS_"`
	}
	err = config.Load(&fresh, config.WithMap(nil))
	if err == nil || !strings.Contains(err.Error(), "REDIS_URL (Redis.URL): is required: set the URL, or the host") {
		t.Fatalf("a declared, non-pointer Redis section must be required: %v", err)
	}
}

// AppConfig is a typical application configuration: required settings,
// application-specific secrets, and optional infrastructure.
type AppConfig struct {
	Addr          string           `env:"ADDR" default:":8080"`
	StripeKey     config.Secret    `env:"STRIPE_KEY"`
	SendGridKey   config.Secret    `env:"SENDGRID_KEY,required"`
	FeatureSearch bool             `env:"FEATURE_SEARCH"`
	DB            *config.Database `envPrefix:"DB_"`
	Redis         *config.Redis    `envPrefix:"REDIS_"`
}

func TestOptionalInfrastructureAndCustomSecrets(t *testing.T) {
	var cfg AppConfig
	if err := config.Load(&cfg, config.WithMap(map[string]string{"SENDGRID_KEY": "SG.real-key"})); err != nil {
		t.Fatalf("optional database and Redis must not be required: %v", err)
	}
	if cfg.DB != nil || cfg.Redis != nil || cfg.Addr != ":8080" {
		t.Fatalf("unexpected %+v", cfg)
	}
	if cfg.SendGridKey.Value() != "SG.real-key" || cfg.StripeKey.Value() != "" {
		t.Fatal("secrets must be readable with Value")
	}
	j, _ := json.Marshal(cfg)
	if strings.Contains(fmt.Sprint(cfg)+string(j), "SG.real-key") {
		t.Fatal("custom secrets must be redacted when printed or marshaled")
	}

	if err := config.Load(&cfg, config.WithMap(map[string]string{
		"SENDGRID_KEY": "k", "REDIS_URL": "redis://localhost:6379/0",
	})); err != nil || cfg.Redis == nil || cfg.DB != nil {
		t.Fatalf("setting REDIS_URL must enable only the Redis section: %+v %v", cfg, err)
	}
}
