# Torge

[![CI](https://github.com/TosmimForidMehtab/torge/actions/workflows/ci.yml/badge.svg)](https://github.com/TosmimForidMehtab/torge/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/TosmimForidMehtab/torge.svg)](https://pkg.go.dev/github.com/TosmimForidMehtab/torge)

A batteries-included backend framework for Go that makes serious backend
development coherent **without hiding Go**. Handlers still see
`context.Context`, `error`, `*http.Request` and `http.ResponseWriter`; the
framework adds the production runtime around them.

- **Express-like simplicity** — `app.GET("/", handler)` and `app.Listen("")` (listens on `$PORT`, like `process.env.PORT || 8080`).
- **Typed contracts** — one struct declares binding, validation and the OpenAPI schema.
- **Unified lifecycle** — config → dependencies → start hooks → serve → graceful shutdown, with startup diagnostics that say what, where, why and how to fix.
- **Production-safe defaults** — timeouts, body limits, request IDs, structured access logs, panic recovery, security headers, health endpoints. The environment defaults to *production* when unset.
- **Replaceable infrastructure** — databases, caches, queues, tracing and auth plug in through small interfaces. The core module has **zero third-party dependencies**.

```go
package main

import (
	"log"

	"github.com/TosmimForidMehtab/torge"
)

func main() {
	app := torge.New()

	app.GET("/", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"message": "hello"})
	})

	if err := app.Listen(""); err != nil { // ":$PORT", or ":8080" when PORT is unset
		log.Fatal(err)
	}
}
```

Requires Go 1.26+.

```sh
go get github.com/TosmimForidMehtab/torge
go install github.com/TosmimForidMehtab/torge/cmd/torge@latest   # optional CLI
```

## A realistic endpoint

```go
type CreatePayment struct {
	Body struct {
		AccountID string `json:"account_id" validate:"required"`
		Amount    int64  `json:"amount"     validate:"gt=0,lte=1000000" doc:"Amount in cents"`
		Currency  string `json:"currency"   validate:"oneof=EUR USD GBP"`
	}
}

func (s *Payments) Create(c *torge.Context, in *CreatePayment) (*Payment, error) {
	acct, err := s.accounts.Get(c.Context(), in.Body.AccountID) // cancellation propagates
	if err != nil {
		return nil, err // *torge.Error renders as {"error":{"code":...}}; anything else as an opaque 500
	}
	return s.charge(c.Context(), acct, in.Body.Amount, in.Body.Currency)
}

// In a module:
torge.Post(api, "/payments", payments.Create,
	torge.Summary("Create a payment"),
	torge.Status(http.StatusCreated),
	idempotency.Middleware(idempotency.Config{Store: idempotency.NewStore(store), Required: true}),
)
```

Invalid input never reaches the handler: it gets a consistent `422` listing
every failing field. The same struct produces the OpenAPI request schema with
its constraints at `/openapi.json`, with interactive docs at `/docs`.

## A serious application

```go
var cfg Config
if err := config.Load(&cfg); err != nil { // typed env config, all problems reported at once
	log.Fatal(err)
}

app := torge.New(torge.WithTracing(torgeotel.Tracing()))
app.Config(&cfg)
app.Database(db)                   // pinged at startup, readiness check, closed last
app.Cache(torgeredis.NewStore(rdb))
app.Use(middleware.CORS(corsCfg), torgeotel.Metrics())
app.Register(AuthModule{}, UserModule{}, PaymentModule{})

log.Fatal(app.Listen(cfg.Addr))  // SIGTERM → drain → stop jobs → close deps → flush telemetry
```

The full, tested version lives in [`examples/production`](examples/production).

## What's included

| Concern | Package | Notes |
|---|---|---|
| App, routing, groups, middleware, context, errors, lifecycle, DI, modules, health, OpenAPI | `torge` | Core. Stdlib only. |
| Validation | `validate` | Tag rules, nested/dive, custom rules, `Validatable`. |
| Configuration | `config` | Env vars, defaults, required, validation, `Secret` type. |
| OpenAPI 3.1 types & schema generation | `openapi` | Driven by `json`, `validate`, `doc`, `example` tags. |
| Official middleware | `middleware` | CORS, Compress, Timeout, CSRF, Static, response Cache. |
| Authentication | `auth` | Bearer, API keys, Basic, policies (`RequireRoles`, `RequireScopes`). |
| Sessions | `session` | Server-side, fixation-safe, saved before headers. |
| Rate limiting | `ratelimit` | GCRA; by IP, user, route; memory limiter. |
| Idempotency | `idempotency` | Keys, fingerprints, replay, conflict detection. |
| Background jobs | `jobs` | Retries, backoff, dead letters, graceful stop; memory queue. |
| Cache | `cache` | Store contract, namespaces, LRU+TTL memory store, typed load-through. |
| HTTP client | `httpclient` | Timeouts, pooling, conservative retries, request ID propagation. |
| `database/sql` | `sqldb` | Pool defaults, context-carried transactions. |
| Webhooks | `webhook` | HMAC, Stripe, GitHub, Standard Webhooks; replay protection. |
| Uploads | `upload` | Streaming multipart, limits, sniffing, storage adapters. |
| Testing | `torgetest` | In-process client, assertions, test servers, middleware harness. |
| CLI | `cmd/torge` | `new`, `dev`, `generate`, `routes`, `doctor`, plugins. Optional. |

Optional integrations are **separate modules**, so an HTTP-only application
never downloads them:

| Module | Provides |
|---|---|
| `contrib/otel` | Tracing and metrics middleware, client transport, job spans, shutdown flush (OpenTelemetry API). |
| `contrib/redis` | Shared cache store, distributed GCRA limiter, reliable job queue (go-redis; Redis or Valkey). |
| `contrib/jwt` | Bearer token verifier (golang-jwt) with mandatory algorithm allow-list. |
| `contrib/pgx` | pgx pool lifecycle and context-carried transactions. |
| `contrib/mongo` | MongoDB (official v2 driver) lifecycle and session-based transactions. |

### Staying lean

- The core module has **no dependencies** beyond the Go standard library.
- `_test.go` files are never compiled into applications. Tests that need
  test-only libraries (the OpenTelemetry SDK, miniredis) live in the separate
  `contrib/tests` module, so those libraries never appear in the dependency
  graph of applications using a contrib module.
- `examples/` and `benchmarks/` are separate modules, excluded from the
  framework's download.

## Testing

```go
func TestCreateUser(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Register(users.Module{})
	app.Replace(func() users.Store { return fakeStore{} }) // swap a dependency

	tc := torgetest.New(t, app)
	tc.POST("/users").JSON(map[string]string{"name": "Ada"}).Do().
		ExpectStatus(201).
		ExpectJSONPath("name", "Ada")
}
```

No network, no external processes; the whole suite runs with `-race`.

## Documentation

- [Guide](docs/guide.md) — every feature with examples.
- [Request lifecycle, context lifetime and concurrency](docs/concurrency.md).
- [Production defaults, diagnostics and shutdown](docs/production.md).
- [Benchmarks](docs/benchmarks.md) — methodology and results.

## Development

The repository is a Go workspace (`go.work`): the core module, one module
per contrib integration, `contrib/tests` (contrib tests with test-only
dependencies), `examples` and `benchmarks`.

```sh
go test -race ./...                         # core module
(cd contrib/redis && go test -race ./...)   # each contrib module
(cd contrib/tests && go test -race ./...)   # OpenTelemetry and Redis behavior
(cd examples && go test ./...)
(cd benchmarks && go test -bench . -benchmem)
```

Database integration tests run against real servers when configured:
`TORGE_TEST_POSTGRES_URL` for `contrib/pgx`, `TORGE_TEST_MONGO_URI` (a
replica set) for `contrib/mongo`.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) and
the [Code of Conduct](CODE_OF_CONDUCT.md). Report security issues privately as
described in [SECURITY.md](SECURITY.md). Release notes are in
[CHANGELOG.md](CHANGELOG.md).

## License

Torge is released under the [MIT License](LICENSE).
