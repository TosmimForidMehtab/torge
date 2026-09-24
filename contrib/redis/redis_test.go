package torgeredis_test

import (
	"testing"

	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/config"
	torgeredis "github.com/TosmimForidMehtab/torge/contrib/redis"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/ratelimit"
)

// Compile-time contract checks. Behavioral tests (which need miniredis) live
// in the contrib/tests module so miniredis never becomes a requirement of
// this module.
var (
	_ cache.Store       = (*torgeredis.Store)(nil)
	_ cache.Adder       = (*torgeredis.Store)(nil)
	_ ratelimit.Limiter = (*torgeredis.Limiter)(nil)
	_ jobs.Queue        = (*torgeredis.Queue)(nil)
)

func TestOptionsFromConfig(t *testing.T) {
	var cfg struct {
		Redis config.Redis `envPrefix:"REDIS_"`
	}
	err := config.Load(&cfg, config.WithMap(map[string]string{
		"REDIS_HOST": "cache.internal", "REDIS_PORT": "6380", "REDIS_USERNAME": "app",
		"REDIS_PASSWORD": "p@ss:w/rd?#", "REDIS_DB": "3", "REDIS_TLS": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := torgeredis.Options(cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Addr != "cache.internal:6380" || opts.Username != "app" || opts.Password != "p@ss:w/rd?#" ||
		opts.DB != 3 || opts.TLSConfig == nil {
		t.Fatalf("parsed %+v", opts)
	}

	fromURL, err := torgeredis.Options(config.Redis{URL: "redis://:secret@localhost:6379/1"})
	if err != nil || fromURL.Password != "secret" || fromURL.DB != 1 || fromURL.TLSConfig != nil {
		t.Fatalf("URL form: %+v %v", fromURL, err)
	}
}
