// Package app wires the production example together. Keeping construction
// in a function (rather than in main) lets tests build the real application
// with test options and fakes.
package app

import (
	"context"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/accounts"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/payments"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/platform"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/middleware"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/ratelimit"
)

// New builds the application. Infrastructure is chosen here and nowhere
// else: swap cache.NewMemory for contrib/redis, or the in-memory job queue
// for a durable adapter, without touching the feature modules.
func New(cfg *platform.Config, opts ...torge.Option) *torge.App {
	opts = append([]torge.Option{
		torge.WithTrustedProxies(cfg.TrustedProxies...),
		torge.WithSecurity(&torge.SecurityConfig{HSTSMaxAge: 365 * 24 * time.Hour}),
	}, opts...)
	app := torge.New(opts...)
	app.Config(cfg)

	store := cache.NewMemory()
	app.Cache(store)
	app.Jobs(jobs.NewManager(jobs.Options{Queue: jobs.NewMemoryQueue(0), Logger: app.Logger()}))

	app.OpenAPI(torge.OpenAPIConfig{
		Info: openapi.Info{Title: "Payments API", Version: "1.0.0", Description: "Torge production example"},
		SecuritySchemes: map[string]*openapi.SecurityScheme{
			"apiToken": openapi.BearerAuth("opaque"),
		},
	})

	// Global middleware runs for every request, before routing.
	app.Use(
		middleware.CORS(middleware.CORSConfig{AllowOrigins: cfg.AllowedOrigins, AllowCredentials: true}),
		middleware.Compress(),
	)

	// The authenticated API surface, shared with modules through the
	// container.
	tokens := make(map[string]torge.Principal, len(cfg.APITokens))
	for i, t := range cfg.APITokens {
		tokens[t] = &auth.User{Subject: "client-" + string(rune('a'+i)), Roles: []string{"client"}}
	}
	api := app.Group("/v1",
		middleware.Timeout(cfg.RequestTimeout),
		auth.Required(auth.Bearer(func(ctx context.Context, token string) (torge.Principal, error) {
			return auth.StaticKeys(tokens)(ctx, token)
		})),
		ratelimit.Middleware(ratelimit.Config{
			Limiter: ratelimit.NewMemory(ratelimit.PerMinute(cfg.RateLimit)),
			Key:     ratelimit.ByUser,
		}),
	)
	app.Supply(api)

	app.Register(accounts.Module{}, payments.Module{})
	return app
}
