package torgeredis

import (
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/TosmimForidMehtab/torge/config"
)

// Options converts Redis settings loaded with config.Load (URL or
// host/port/credentials) into go-redis client options:
//
//	opts, err := torgeredis.Options(cfg.Redis)
//	if err != nil { ... }
//	app.Cache(torgeredis.NewStore(redis.NewClient(opts)))
func Options(cfg config.Redis) (*redis.Options, error) {
	opts, err := redis.ParseURL(cfg.ConnectionURL())
	if err != nil {
		// The URL may contain a password, so it is not included.
		return nil, fmt.Errorf("torgeredis: invalid Redis settings: %w", err)
	}
	return opts, nil
}
