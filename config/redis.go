package config

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/TosmimForidMehtab/torge/validate"
)

// Redis holds Redis or Valkey connection settings given either as a URL or
// as separate parts. Embed it with an envPrefix:
//
//	type Config struct {
//	    Redis config.Redis `envPrefix:"REDIS_"`
//	}
//
// reads REDIS_URL, or REDIS_HOST, REDIS_PORT, REDIS_USERNAME,
// REDIS_PASSWORD, REDIS_DB and REDIS_TLS. When URL is set it wins and the
// parts are ignored. Use a pointer field (*config.Redis) to make Redis
// optional.
//
// With go-redis, contrib/redis converts it directly:
//
//	opts, err := torgeredis.Options(cfg.Redis)
//	client := redis.NewClient(opts)
type Redis struct {
	// URL is a redis://, rediss:// (TLS) or unix:// URL, for example
	// rediss://default:secret@cache.example.com:6380/0.
	URL Secret `env:"URL"`
	// Host is the server host name or IP address.
	Host string `env:"HOST"`
	// Port is the server port (default 6379).
	Port int `env:"PORT" validate:"min=0,max=65535"`
	// Username is the ACL user (Redis 6+); empty uses the default user.
	Username string `env:"USERNAME"`
	// Password authenticates the connection.
	Password Secret `env:"PASSWORD"`
	// DB selects the logical database.
	DB int `env:"DB" validate:"min=0"`
	// TLS connects with TLS (the rediss scheme).
	TLS bool `env:"TLS"`
}

// Validate implements validate.Validatable: either URL or Host must be set,
// and URL must use a Redis scheme.
func (r Redis) Validate() error {
	switch {
	case r.URL != "":
		u, err := url.Parse(string(r.URL))
		if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss" && u.Scheme != "unix") {
			return validate.Errors{{Field: "URL", Rule: "url", Message: "must be a redis://, rediss:// or unix:// URL"}}
		}
	case r.Host == "":
		return validate.Errors{{Field: "URL", Rule: "required", Message: "is required: set the URL, or the host"}}
	}
	return nil
}

// ConnectionURL returns a redis:// or rediss:// URL (the URL if set,
// otherwise one built from the parts with correct escaping). It contains the
// password: never log it.
func (r Redis) ConnectionURL() string {
	if r.URL != "" {
		return string(r.URL)
	}
	scheme := "redis"
	if r.TLS {
		scheme = "rediss"
	}
	port := r.Port
	if port == 0 {
		port = 6379
	}
	u := &url.URL{Scheme: scheme, Host: hostPort(r.Host, port), Path: "/" + strconv.Itoa(r.DB)}
	switch {
	case r.Password != "":
		u.User = url.UserPassword(r.Username, r.Password.Value())
	case r.Username != "":
		u.User = url.User(r.Username)
	}
	return u.String()
}

// Addr returns host:port (from URL or parts), for clients configured by
// address.
func (r Redis) Addr() string {
	if r.URL != "" {
		if u, err := url.Parse(string(r.URL)); err == nil && u.Scheme != "unix" {
			if u.Port() == "" {
				return hostPort(u.Hostname(), 6379)
			}
			return u.Host
		}
		return strings.TrimPrefix(string(r.URL), "unix://")
	}
	port := r.Port
	if port == 0 {
		port = 6379
	}
	return hostPort(r.Host, port)
}
