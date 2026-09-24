// Package platform holds cross-cutting setup: configuration and shared
// infrastructure.
package platform

import (
	"time"

	"github.com/TosmimForidMehtab/torge/config"
)

// Config is loaded from environment variables at startup; invalid or missing
// values stop the process before it accepts traffic.
type Config struct {
	Addr           string        `env:"ADDR"` // empty: ":$PORT" or ":8080"
	APITokens      []string      `env:"API_TOKENS,required,secret"`
	WebhookSecret  config.Secret `env:"WEBHOOK_SECRET,required"`
	RateLimit      int           `env:"RATE_LIMIT_PER_MINUTE" default:"120" validate:"min=1"`
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" default:"10s" validate:"min=1s,max=5m"`
	AllowedOrigins []string      `env:"ALLOWED_ORIGINS" default:"http://localhost:3000"`
	TrustedProxies []string      `env:"TRUSTED_PROXIES"`
}
