# Torge Guide

This guide walks through every part of Torge. Each section is independent;
skim the headings and jump to what you need. For lifetime and concurrency
rules see [concurrency.md](concurrency.md); for production defaults and
diagnostics see [production.md](production.md).

- [Application](#application)
- [Routing](#routing)
- [Middleware](#middleware)
- [The request context](#the-request-context)
- [Errors](#errors)
- [Binding and validation](#binding-and-validation)
- [Typed handlers and OpenAPI](#typed-handlers-and-openapi)
- [Dependencies and modules](#dependencies-and-modules)
- [Configuration](#configuration)
- [Logging and observability](#logging-and-observability)
- [Databases](#databases)
- [Cache](#cache)
- [External services and the HTTP client](#external-services-and-the-http-client)
- [Authentication and authorization](#authentication-and-authorization)
- [Security middleware](#security-middleware)
- [Rate limiting](#rate-limiting)
- [Idempotency](#idempotency)
- [Background jobs](#background-jobs)
- [Health and readiness](#health-and-readiness)
- [Webhooks](#webhooks)
- [File uploads](#file-uploads)
- [Streaming, SSE and WebSockets](#streaming-sse-and-websockets)
- [Testing](#testing)
- [Standard library compatibility](#standard-library-compatibility)
- [CLI and extensions](#cli-and-extensions)

---

## Application

`torge.New(options...)` creates an application with production defaults.
Options configure the built-in pipeline and server:

```go
app := torge.New(
	torge.WithEnv(torge.Production),            // default: TORGE_ENV / APP_ENV, else production
	torge.WithLogger(logger),                   // any *slog.Logger
	torge.WithServer(torge.ServerConfig{ShutdownTimeout: 20 * time.Second, DrainDelay: 5 * time.Second}),
	torge.WithBodyLimit(8 << 20),
	torge.WithTrustedProxies("10.0.0.0/8"),
	torge.WithTracing(torgeotel.Tracing()),
)
```

| Method | Purpose |
|---|---|
| `Listen(addr)` / `ListenTLS(addr, cert, key)` | Start, serve, stop gracefully on SIGINT/SIGTERM. Returns `nil` after a clean shutdown. |
| `Serve(listener)` | Same with your own listener. |
| `Start(ctx)` / `Shutdown(ctx)` | Drive the lifecycle yourself (tests, custom servers). |
| `Server()` | A configured `*http.Server` if you manage serving. |
| `ServeHTTP` | An `App` is an `http.Handler` once started. |
| `State()`, `Done()` | Lifecycle state and a channel closed when stopped. |
| `Routes()`, `Pipeline()`, `PrintRoutes(w)` | Introspection. |
| `Validate()` | Run static startup checks without starting. |

Lifecycle states: **Building → Starting → Running → Stopping → Stopped**
(or **Failed**). Everything is registered while Building. `Start`:

1. reports all static problems at once (duplicate routes, invalid patterns, unsafe production settings, invalid config, ...);
2. validates the dependency graph and constructs singletons;
3. runs `Invoke` functions (which may register routes);
4. freezes the route table and builds the OpenAPI document;
5. runs start hooks in registration order, then container-managed components, then job workers.

If any start hook fails, already-started components are stopped in reverse
order and `Start` returns a diagnostic.

### Lifecycle hooks

```go
app.OnStart("warm-cache", func(ctx context.Context) error { return warm(ctx) })
app.OnStop("flush", func(ctx context.Context) error { return flush(ctx) })
app.Manage("scheduler", scheduler)          // anything with Start(ctx)/Stop(ctx)
app.AddHook(torge.Hook{Name: "x", OnStart: start, OnStop: stop})
```

Stop hooks run in **reverse order of successful starts**, after the HTTP
server has drained. See [production.md](production.md#graceful-shutdown).

## Routing

```go
app.GET("/users", listUsers)
app.POST("/users", createUser)
app.Handle("PURGE", "/cache/*key", purge)   // any method token
app.Any("/proxy/*path", proxy)              // all standard methods

api := app.Group("/api/v1", auth.Required(verifier), torge.Tags("v1"))
users := api.Group("/users")
users.GET("/:id", getUser, torge.Name("user.get"))
users.POST("", createUser)                   // "/api/v1/users"
```

- `:name` matches one segment; `*name` (last segment only) matches the rest, including slashes.
- Static segments win over parameters, parameters over wildcards, with backtracking: `/users/me` and `/users/:id` coexist.
- Parameter names do not distinguish routes: `/users/:id` and `/users/:uid` conflict and are reported at startup.
- `HEAD` falls back to `GET`. Unregistered `OPTIONS` requests get `204` with `Allow`. Wrong methods get `405` with `Allow`.
- `/users/` redirects to `/users` (and vice versa) when only the other form exists (`WithRedirectTrailingSlash(false)` disables).
- `app.URL("user.get", "id", "42")` builds `/api/v1/users/42`.

Route options: `Name`, `BodyLimit`, `Summary`, `Description`, `OperationID`,
`Tags`, `Deprecated`, `Hidden`, `Security`, `Public`, `Status`, `Body[T]`,
`Returns[T]`, `Responds`. Any `torge.Middleware` is also a route option.

## Middleware

Middleware is a plain function:

```go
func Timing(next torge.Handler) torge.Handler {
	return func(c *torge.Context) error {
		start := time.Now()
		err := next(c)                      // not calling next short-circuits
		c.Logger().Info("timing", "took", time.Since(start))
		return err                          // errors propagate outward
	}
}
```

Where it runs:

| Registered with | Runs |
|---|---|
| `app.Use(...)` | For every request, **before routing** (so it sees 404s and CORS preflights). |
| `group.Use(...)` or `app.Group(prefix, mw)` | For the group's routes, after routing. |
| `app.GET(path, h, mw)` | For that route only. |

Order is deterministic: global in `Use` order, then groups from outermost to
innermost, then route middleware. **Group middleware applies to every route of
the group regardless of whether `Use` was called before or after the route was
registered.** `app.Pipeline()` lists the full chain after `Start`.

To change the response, set headers before calling `next`, register a
`c.Response().Before(fn)` hook (runs just before headers are sent), or wrap
the writer with `c.SetResponseWriter(w)` (see `middleware.Compress`).

## The request context

```go
func GetUser(c *torge.Context) error {
	id := c.Param("id")
	user, err := users.Get(c.Context(), id)   // pass the context.Context down
	if err != nil {
		return err
	}
	return c.JSON(200, user)
}
```

| Request | Response | Metadata |
|---|---|---|
| `Param`, `Params` | `JSON`, `String`, `HTML`, `Bytes` | `RequestID` |
| `Query`, `QueryDefault`, `QueryInt`, `QueryValues` | `Status`, `NoContent`, `Redirect` | `User`, `SetUser` |
| `GetHeader`, `Cookie` | `Header`, `SetCookie` | `Logger` |
| `Body`, `ReadBody`, `Bind`, `BindJSON` | `File`, `FileFS`, `Attachment` | `RoutePattern`, `RouteName` |
| `Request`, `Method`, `Path` | `Stream`, `SSE`, `Flush` | `RealIP`, `Scheme`, `Env` |

`c.Context()` is the request's `context.Context`: canceled when the client
disconnects or the server shuts down, carrying deadlines from middleware such
as `middleware.Timeout`. `c.Set`/`c.Get` pass request-scoped values between
middleware. `torge.UserFrom(ctx)` reads the principal in code that only has a
`context.Context`.

A `Context` lives exactly as long as the request and is never pooled; see
[concurrency.md](concurrency.md).

## Errors

Return errors; the framework renders them once, at the error boundary:

```go
var ErrUserNotFound = torge.NotFound("USER_NOT_FOUND", "User does not exist")

return ErrUserNotFound.Wrap(err).WithMeta("user_id", id)
```

```json
{"error": {"code": "USER_NOT_FOUND", "message": "User does not exist", "request_id": "K7N..."}}
```

- **Public**: `Status`, `Code`, `Message`, `Details`. **Internal** (logged only): `Err` (the cause) and `Meta`.
- Any error that is not a `*torge.Error` becomes an opaque `500 INTERNAL_ERROR`, so SQL errors, paths and hostnames never leak.
- Classification: validation errors → `422` with field details; `*http.MaxBytesError` → `413`; `context.DeadlineExceeded` → `504`; `context.Canceled` → `499`.
- `errors.Is(err, ErrUserNotFound)` matches wrapped copies (same status and code). `With*` methods return copies, so sentinels are safe to share.
- `WithExposeErrors(true)` (default only in development) adds `error.debug` with the internal message. Enabling it in production fails `Start`.
- Replace the renderer with `WithErrorHandler`; `torge.AsError` and `torge.StatusOf` help classify.

Panics are recovered by the recovery stage, logged with stack and request
metadata, and rendered as `500`. Configure with `WithRecovery(&torge.RecoveryConfig{OnPanic: report})`.

## Binding and validation

```go
type SearchInput struct {
	Org     string        `path:"org"`
	Query   string        `query:"q"      validate:"max=100"`
	Page    int           `query:"page"   default:"1" validate:"min=1"`
	Tags    []string      `query:"tag"`
	Since   *time.Time    `query:"since"`
	Trace   string        `header:"X-Trace"`
	Session string        `cookie:"session"`
	Body    SearchFilters // JSON body
}

var in SearchInput
if err := c.Bind(&in); err != nil {
	return err // 400 malformed JSON, 413 too large, 415 wrong type, 422 invalid fields
}
```

- The body decodes into a field named `Body`, or into the struct itself if there is none. Parameters are bound **after** the body, so a body can never overwrite a path parameter.
- Parameter types: strings, numbers, booleans, `time.Duration`, `time.Time` and anything implementing `encoding.TextUnmarshaler`, plus pointers and slices of them. Repeated parameters fill slices; `query:"tag,comma"` additionally splits single values on commas.
- `time.Time` parameters parse RFC 3339 by default; `layout:"2006-01-02"` selects another Go reference layout.
- Embed `torge.Strict` to reject unknown JSON fields and undeclared query parameters (a `422` naming them). Multipart form fields pair with `upload.Stream`: bind its returned values with `c.BindFormValues(&in, values)`.
- `c.BindJSON(&v)` decodes and validates a required body only.

Validation rules (`validate` tag): `required`, `omitempty`, `min`, `max`,
`len`, `gt`, `gte`, `lt`, `lte`, `eq`, `ne`, `oneof`, `email`, `url`, `uuid`,
`alpha`, `alphanum`, `numeric`, `lowercase`, `uppercase`, `ip`, `ipv4`,
`ipv6`, `contains`, `excludes`, `startswith`, `endswith`, and `dive` to apply
the following rules to slice or map elements. `pattern:"^[A-Z]+$"` adds a
regular expression. Size rules mean characters for strings, items for
collections and values for numbers (durations accept `max=1m`).

Nested structs, pointers, slices and maps are validated recursively. For
cross-field rules implement `Validate() error` (return `validate.FieldError`
or `validate.Errors` to point at fields). Register custom rules with
`validate.Default.RegisterRule`, or plug in another validator with
`WithValidator`. Invalid tags are reported at startup for typed handlers.

## Typed handlers and OpenAPI

A typed handler declares its contract with types; binding, validation,
encoding and documentation follow from it:

```go
type GetUser struct {
	ID string `path:"id" validate:"uuid" doc:"User ID"`
}

torge.Get(api, "/users/:id", func(c *torge.Context, in *GetUser) (*User, error) {
	return users.Find(c.Context(), in.ID)
}, torge.Summary("Get a user"), torge.Returns[torge.ErrorBody](404, "Not found"))

torge.Post(api, "/users", svc.Create, torge.Status(http.StatusCreated))
torge.Delete(api, "/users/:id", svc.Delete) // return (nil, nil) for 204
```

Enable the document and UI:

```go
app.OpenAPI(torge.OpenAPIConfig{
	Info:            openapi.Info{Title: "Payments API", Version: "1.0.0"},
	SecuritySchemes: map[string]*openapi.SecurityScheme{"bearer": openapi.BearerAuth("JWT")},
	UI:              openapi.Scalar,             // or openapi.SwaggerUI (default)
	RouteOptions:    []torge.RouteOption{auth.Required(docsAuth)}, // keep docs private
})
```

The OpenAPI 3.1 document at `/openapi.json` includes parameters (with
constraints from `validate` tags), request bodies, response schemas, tags,
security and a shared error schema. Field tags `doc`, `example`, `default`,
`format`, `deprecated`, `readonly` and `writeonly` enrich schemas; types can
implement `openapi.SchemaProvider` for full control. Untyped handlers can be
documented with `torge.Body[T]()` and `torge.Returns[T](status, desc)`.

## Dependencies and modules

Register constructors; dependencies are their parameters:

```go
app.Provide(NewUserStore)                  // func(*sqldb.DB) (UserStore, error)
app.Provide(NewUserService)                // func(UserStore, *slog.Logger) *UserService
app.Supply(stripeClient)                   // a ready-made value
torge.SupplyAs[Clock](app, realClock{})    // a value under an interface type
```

At `Start` the graph is validated — **missing dependencies, duplicates,
cycles and scope violations** are all reported with their registration site —
and singletons are constructed. Constructed values implementing
`Start/Stop` are started and stopped with the app; constructed `io.Closer`s
are closed at shutdown. Supplied values are never managed.

Built-in injectables: `context.Context`, `*slog.Logger`, `torge.Env`, and
(request-scoped providers only) `*torge.Context`.

```go
app.Provide(NewUnitOfWork, torge.RequestScoped()) // one per request, closed after it
uow, err := torge.Dep[*UnitOfWork](c)
svc := torge.MustResolve[*UserService](app)      // after Start
```

`Invoke` runs code with resolved dependencies during startup — the natural
place to register routes that need services:

```go
type UserModule struct{}

func (UserModule) Register(app *torge.App) error {
	app.Provide(NewUserStore)
	app.Provide(NewUserService)
	app.Invoke(func(svc *UserService) {
		g := app.Group("/users", torge.Tags("users"))
		torge.Get(g, "/:id", svc.Get)
		torge.Post(g, "", svc.Create, torge.Status(201))
	})
	return nil
}

app.Register(AuthModule{}, UserModule{}, PaymentModule{})
```

Modules may register anything: routes, middleware, providers, hooks, health
checks, jobs, services and OpenAPI metadata (`AddSecurityScheme`, `AddTag`).
Routes are attributed to their module in `app.Routes()`. Modules impose no
architecture — no controllers, repositories or layers are required.

In tests, `app.Replace(ctor)` and `torge.ReplaceValue[T](app, v)` swap
dependencies before start.

## Configuration

```go
type Config struct {
	Addr        string        `env:"ADDR"` // empty: ":$PORT" or ":8080"
	DatabaseURL config.Secret `env:"DATABASE_URL,required"`
	Timeout     time.Duration `env:"TIMEOUT" default:"5s" validate:"min=1s,max=1m"`
	Origins     []string      `env:"ALLOWED_ORIGINS"`
	Redis       *RedisConfig  `envPrefix:"REDIS_"` // optional section
}

var cfg Config
if err := config.Load(&cfg); err != nil { // lists every problem with its variable name
	log.Fatal(err)
}
app.Config(&cfg) // validated again, then injectable into modules
```

Supported types: strings, booleans, integers, floats, durations, `url.URL`,
`TextUnmarshaler`s, pointers and comma-separated slices (`sep` tag to
change). Nested structs share variables with an `envPrefix`; pointer sections
stay nil unless one of their variables is set. `config.WithPrefix`,
`config.WithMap` (tests) and `config.WithLookup` adjust the source.

**Nothing is required unless you declare it.** A hello-world app needs no
environment variables; a plain field is required, a pointer field is
optional. Declare your own secrets the same way as any other setting:

```go
type Config struct {
	SendGridKey config.Secret    `env:"SENDGRID_KEY,required"` // your own secret
	StripeKey   config.Secret    `env:"STRIPE_KEY"`           // optional secret
	DB          *config.Database `envPrefix:"DB_"`            // optional database
	Redis       *config.Redis    `envPrefix:"REDIS_"`         // optional Redis
}

mailer := sendgrid.New(cfg.SendGridKey.Value()) // Value() returns the real string
```

`config.Redis` accepts `REDIS_URL` (`redis://`, `rediss://` for TLS, or
`unix://`) **or** `REDIS_HOST`, `REDIS_PORT` (default 6379),
`REDIS_USERNAME`, `REDIS_PASSWORD`, `REDIS_DB` and `REDIS_TLS`; the URL wins
when both are set. With go-redis:

```go
if cfg.Redis != nil {
	opts, err := torgeredis.Options(*cfg.Redis) // contrib/redis
	if err != nil {
		log.Fatal(err)
	}
	app.Cache(torgeredis.NewStore(redis.NewClient(opts)))
}
```

`cfg.Redis.ConnectionURL()` and `Addr()` serve other clients.
`config.Database` works the same way for SQL databases and MongoDB (see
[Databases](#databases)).

### .env files

`.env` handling works like Node's `dotenv`, in every environment:

```go
import _ "github.com/TosmimForidMehtab/torge/config/autoload" // like require("dotenv/config")
```

or explicitly:

```go
func main() {
	if err := config.LoadDotEnv(); err != nil { // reads ./.env if present
		log.Fatal(err)
	}
	var cfg Config
	if err := config.Load(&cfg); err != nil {
		log.Fatal(err)
	}
	app := torge.New() // sees TORGE_ENV from .env
	...
}
```

```sh
# .env — git-ignored; commit a .env.example instead
TORGE_ENV=development
DB_URL=postgres://app:secret@localhost:5432/shop?sslmode=disable
REDIS_URL=redis://localhost:6379/0
SENDGRID_KEY="SG.dev-key"
API_BASE=http://localhost:9000
WEBHOOK_URL=${API_BASE}/hooks          # ${VAR} expands earlier values
PRIVATE_KEY="-----BEGIN KEY-----
...multi-line values in double quotes...
-----END KEY-----"
```

- Real environment variables **always win**; `.env` only fills in unset ones. Values injected by Render, Heroku, Kubernetes, Docker or systemd therefore always take precedence.
- `.env` is looked up in the working directory, then next to the executable, so a binary started from another directory (systemd, pm2) still finds the `.env` beside it.
- A missing file is not an error, so containers and platforms without one work unchanged.
- `LoadDotEnv(".env.local", ".env")` loads several files; the first file to set a variable wins.
- `TORGE_ENV_FILE=/etc/secrets/.env` names the files to load instead (for example a platform-mounted secret file); they must exist. See [production.md](production.md#providing-configuration-in-production).
- Syntax: `KEY=value`, optional `export`, `#` comments, `'literal'` values, `"escaped\n"` and multi-line double-quoted values, `${VAR}` expansion (write `\${` for a literal). Parse errors report the file and line.

`torge new` generates `main.go` with this call, a `.env.example`, a
git-ignored `.env`, and a `Dockerfile` whose `.dockerignore` keeps `.env` out
of images.

`config.Secret` values print as `[REDACTED]` through `fmt`, JSON and `slog`;
`config.Redacted(cfg)` lists configuration safely for logs. The environment
comes from `TORGE_ENV` (or `APP_ENV`) and **defaults to production**.

## Logging and observability

Torge logs with `log/slog`. The default handler is JSON in production and
text in development; pass any handler with `WithLogger` or `SetLogger`.
Handlers are wrapped so records logged with a request context carry
`request_id` and `trace_id`:

```go
c.Logger().Info("charged", "amount", amt)          // bound to the request
logger.InfoContext(ctx, "deep in a service")       // same attributes via ctx
```

The access log writes one record per request with method, route, path,
status, duration, bytes, client IP, user ID, error and error code (Error level
for 5xx, Warn for 4xx). Configure with `WithAccessLog(&torge.AccessLogConfig{...})`.

Request IDs are generated for every request, returned in `X-Request-Id`,
included in logs, errors and outgoing requests. Incoming IDs are accepted only
from trusted proxies (`RequestIDConfig.TrustIncoming` overrides).

OpenTelemetry lives in `contrib/otel`:

```go
app := torge.New(torge.WithTracing(torgeotel.Tracing()))   // server spans, W3C propagation
app.Use(torgeotel.Metrics())                                // duration, active requests, errors
torgeotel.FlushOnShutdown(app, tracerProvider, meterProvider)
httpclient.New(httpclient.Config{Middleware: []func(http.RoundTripper) http.RoundTripper{torgeotel.Transport}})
jobs.NewManager(jobs.Options{Middleware: []jobs.Middleware{torgeotel.Jobs()}})
```

Custom metrics use the OpenTelemetry meter API directly.

## Databases

Torge manages database lifecycle, not SQL:

| Database | Adapter | Driver (your choice, not bundled) |
|---|---|---|
| PostgreSQL | `contrib/pgx`, or `sqldb` | `github.com/jackc/pgx/v5` (`"pgx"` via `pgx/v5/stdlib`) |
| MySQL / MariaDB | `sqldb` | `github.com/go-sql-driver/mysql` |
| SQLite | `sqldb` | `modernc.org/sqlite` (pure Go) or `github.com/mattn/go-sqlite3` (cgo) |
| MongoDB | `contrib/mongo` | official driver v2 (included by the adapter) |
| Anything else | implement `Ping(ctx)` and `Close()` | — |

Connection settings come from `config.Database`, which accepts **either a
URL or separate parts**:

```go
type Config struct {
	DB config.Database `envPrefix:"DB_"` // DB_URL, or DB_HOST DB_PORT DB_USER DB_PASSWORD DB_NAME DB_PARAMS
}
```

```sh
DB_URL=postgres://app:secret@db:5432/shop?sslmode=require
# or
DB_HOST=db DB_PORT=5432 DB_USER=app DB_PASSWORD='p@ss/word' DB_NAME=shop DB_PARAMS=sslmode=require
```

If `DB_URL` is set it wins. Loading fails (naming the variables) when
neither the URL nor a host/database name is given, and `URL`/`Password` are
`config.Secret`s, so the config prints redacted. The builders escape
passwords correctly for each driver:

```go
db, err := sqldb.Open("pgx", cfg.DB.PostgresDSN())        // or torgepgx.Open(ctx, cfg.DB.PostgresDSN())
db, err := sqldb.Open("mysql", cfg.DB.MySQLDSN())         // accepts mysql:// URLs; adds parseTime=true
db, err := sqldb.Open("sqlite", cfg.DB.SQLiteDSN())       // DB_NAME=data/app.db
db, err := torgemongo.Open(cfg.DB.MongoURI(), cfg.DB.Database())
app.Database(db)
```

Use `*config.Database` for an optional database (for example a read
replica): it stays nil unless one of its variables is set. Use
`envPrefix:"DATABASE_"` to read the common `DATABASE_URL`. The DSN strings
contain the password — never log them.

`app.Database` pings at startup (failing with `DATABASE_UNREACHABLE`), adds
a readiness check, supplies the handle to the container and closes it at
shutdown. Any type with `Ping(ctx) error` and `Close() error` works, so GORM,
Ent or Bun wrap in a few lines.

Transactions are carried by the context, so repositories join them without
knowing:

```go
err := app.Transaction(ctx, func(ctx context.Context) error {
	if err := orders.Create(ctx, o); err != nil { // uses db.Conn(ctx)
		return err                                // rollback
	}
	return stock.Reserve(ctx, o.Items)            // same transaction
})                                                // commit
```

Panics roll back and re-panic; nested calls join the outer transaction. For
query instrumentation use driver wrappers (`otelsql`, `otelpgx`, `otelmongo`).

MongoDB works the same way: driver calls made with the context passed to
`fn` join the transaction (`db.Collection("orders").InsertOne(ctx, o)`).
Mongo transactions need a replica set or sharded cluster, and the driver may
retry `fn` on transient errors, so keep non-database side effects
idempotent.

## Cache

```go
app.Cache(torgeredis.NewStore(rdb)) // or cache.NewMemory() for one instance

store := torge.MustDep[cache.Store](c)
users := cache.NewTyped[User](cache.Namespace(store, "users:"))
u, err := users.GetOrLoad(ctx, id, 5*time.Minute, func(ctx context.Context) (User, error) {
	return repo.Get(ctx, id) // concurrent misses for the same key share one load
})
_ = cache.Namespace(store, "users:").DeletePrefix(ctx, "") // invalidate a namespace
```

`cache.Store` is Get/Set/Delete/DeletePrefix with TTLs; stores implementing
`cache.Adder` (memory, Redis) also support atomic set-if-absent, used by
idempotency and replay protection. Registering a process-local store in
production logs a warning, because each instance would see different data.

## External services and the HTTP client

```go
app.Service("stripe", stripeClient)             // Lifecycle / HealthChecker honored
stripe, err := torge.ServiceAs[*stripe.Client](app, "stripe")
```

Torge does not wrap third-party SDKs. For your own HTTP integrations:

```go
client, err := httpclient.New(httpclient.Config{
	BaseURL: "https://api.partner.com",
	Timeout: 10 * time.Second,
	Retry:   httpclient.RetryPolicy{MaxAttempts: 3},
	Logger:  app.Logger(),
})
var out Invoice
err = client.PostJSON(ctx, "/invoices", in, &out)   // *httpclient.StatusError on non-2xx
sdk := partner.New(client.HTTP())                   // same behavior for SDKs
```

Retries are conservative: idempotent methods, or requests with an
`Idempotency-Key`, on network errors and 429/502/503/504 only, with jittered
exponential backoff that honors `Retry-After`. Request IDs propagate
automatically; URLs in logs and errors are stripped of credentials and query
strings.

## Authentication and authorization

```go
verifier, _ := torgejwt.New(torgejwt.Config{Key: secret, Algorithms: []string{"HS256"}, Issuer: iss, Audience: "api"})

api := app.Group("/api", auth.Required(
	auth.Bearer(verifier),
	auth.APIKey(auth.APIKeyConfig{Lookup: keys.Lookup}),
	session.Authenticator("user_id", users.LoadPrincipal),
))

api.DELETE("/users/:id", deleteUser, auth.RequireRoles("admin"))
api.POST("/reports", createReport, auth.RequireScopes("reports:write"))
api.GET("/orders/:id", getOrder, auth.Require(ownsOrder))

func handler(c *torge.Context) error {
	user, _ := torge.UserAs[*auth.User](c)
	...
}
```

Authenticators return `auth.ErrNoCredentials` to let the next one try.
`auth.Optional` allows anonymous requests but still rejects invalid
credentials. `auth.StaticKeys` and `auth.BasicUsers` compare in constant
time. Implement `auth.Authenticator` for anything else (OAuth introspection,
mTLS). Authorization policies stay in your code.

Sessions (`session` package) are server-side, stored in a `cache.Store`,
referenced by a `Secure`, `HttpOnly`, `SameSite=Lax` cookie with a random
256-bit ID, and saved automatically before headers are written. Call
`Regenerate()` at login to prevent fixation.

## Security middleware

Always on (configurable with `WithSecurity`): `X-Content-Type-Options`,
`X-Frame-Options`, `Referrer-Policy`, optional CSP and HSTS (HTTPS only),
and `AllowedHosts` host validation.

```go
app.Use(
	middleware.CORS(middleware.CORSConfig{AllowOrigins: []string{"https://app.example.com"}, AllowCredentials: true}),
	middleware.CSRF(middleware.CSRFConfig{BypassPatterns: []string{"POST /webhooks/"}}),
	middleware.Compress(),
	middleware.Static(middleware.StaticConfig{Root: os.DirFS("public"), Prefix: "/assets"}),
)
api.Use(middleware.Timeout(10 * time.Second))
api.GET("/catalog", catalog, middleware.Cache(middleware.CacheConfig{Store: store, TTL: time.Minute}))
```

- **CORS** rejects insecure configurations (`*` with credentials, malformed origins) at startup.
- **CSRF** uses the standard library's `http.CrossOriginProtection` (Fetch metadata / Origin) — no tokens needed.
- **Timeout** sets a context deadline; there is no racing goroutine, so handlers observe cancellation like any Go code.
- **Static** never lists directories and supports single-page apps.
- **Cache** stores only successful, cookie-free, non-private GET responses.

## Rate limiting

```go
limiter := torgeredis.NewLimiter(rdb, ratelimit.PerMinute(100).WithBurst(20)) // shared
// limiter := ratelimit.NewMemory(ratelimit.PerMinute(100))                  // single instance

api.Use(ratelimit.Middleware(ratelimit.Config{Limiter: limiter, Key: ratelimit.ByUser}))
app.POST("/login", login, ratelimit.Middleware(ratelimit.Config{
	Limiter: loginLimiter, Key: ratelimit.ByRoute(ratelimit.ByIP),
}))
```

Responses carry `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`
and, when limited, `429` with `Retry-After`. Limiter failures fail open (and
log) unless `FailClosed` is set.

## Idempotency

```go
app.POST("/payments", createPayment, idempotency.Middleware(idempotency.Config{
	Store:    idempotency.NewStore(redisStore),
	Required: true,
}))
```

The first request with an `Idempotency-Key` runs; its response is stored and
replayed (with `Idempotent-Replayed: true`) for retries. The same key with a
different body → `422`; while the first is running → `409`. Failures and 5xx
release the key so clients can retry. Keys are scoped per user.

## Background jobs

```go
app.Jobs(jobs.NewManager(jobs.Options{Queue: torgeredis.NewQueue(rdb, "default"), Workers: 8}))
app.Job("send-email", jobs.Typed(func(ctx context.Context, e Email) error {
	return mailer.Send(ctx, e)
}), jobs.MaxAttempts(5))

m := torge.MustDep[*jobs.Manager](c)
err := m.Dispatch(c.Context(), "send-email", Email{To: u.Email}, jobs.Delay(time.Minute))
```

Failed jobs retry with jittered exponential backoff; `jobs.Permanent(err)`
skips retries; exhausted jobs are dead-lettered and passed to `OnFailure`.
Panics are recovered. Jobs carry the dispatching request's ID for log
correlation. Workers start after all other components and stop first after
the HTTP server, waiting for in-flight jobs until the shutdown deadline.
`jobs.MemoryQueue` is for development; implement `jobs.Queue` for other
brokers (SQS, RabbitMQ, NATS, PostgreSQL).

## Health and readiness

| Endpoint | Meaning |
|---|---|
| `GET /live` | The process is alive. Runs only checks registered with `torge.Liveness()`. |
| `GET /ready` | The app should receive traffic: `503` while starting or stopping, or when a required check fails. |
| `GET /health` | Detailed report of every check with durations. |

```go
app.HealthCheck("search", searchClient.Ping, torge.CheckTimeout(2*time.Second), torge.Optional())
```

Checks run concurrently with timeouts, so probes never hang. Databases,
caches with `Ping` and services implementing `HealthChecker` register checks
automatically. Health endpoints run before global middleware (so auth never
blocks probes) and are excluded from access logs.

## Webhooks

```go
app.POST("/webhooks/stripe", handleStripe, webhook.Middleware(webhook.Config{
	Verifier: webhook.Stripe(cfg.StripeSecret.Value()),
	Replay:   store,
}))

func handleStripe(c *torge.Context) error {
	var ev stripe.Event
	if err := webhook.Decode(c, &ev); err != nil { // verified raw bytes
		return err
	}
	...
}
```

Verifiers: `webhook.HMAC` (generic, with optional timestamp and ID headers
and secret rotation), `webhook.Stripe`, `webhook.GitHub`,
`webhook.StandardWebhooks`. Timestamps outside the tolerance and bad
signatures get `401` with specific codes. With `Replay`, successfully
processed deliveries are remembered and duplicates are acknowledged with
`200` without running the handler again.

## File uploads

```go
app.POST("/avatars", func(c *torge.Context) error {
	fields, err := upload.Stream(c.Request(), upload.Options{
		MaxFileSize: 5 << 20, MaxFiles: 1, AllowedTypes: []string{"image/png", "image/jpeg"},
	}, func(f *upload.File) error {
		_, err := storage.Put(c.Context(), "avatars/"+userID+".png", f, f.Meta())
		return err
	})
	...
}, torge.BodyLimit(6<<20))
```

Parts stream straight from the request body; nothing is buffered beyond 512
bytes for content sniffing. Limits produce `413`/`415` errors; filenames are
sanitized. `upload.Disk` writes atomically inside a root directory
(`os.Root`), and `upload.Storage` adapts object stores.

## Streaming, SSE and WebSockets

```go
return c.Stream(200, "application/x-ndjson", func(w io.Writer) error {
	for row := range rows {
		if err := json.NewEncoder(w).Encode(row); err != nil { // fails once the client is gone
			return err
		}
	}
	return nil
})

sse, err := c.SSE()
for {
	select {
	case <-sse.Done():
		return nil
	case ev := <-updates:
		if err := sse.SendJSON("update", ev); err != nil {
			return err
		}
	}
}
```

Streaming lifts the server write timeout for that request only. For
WebSockets use any library that accepts `http.ResponseWriter`; hijacking
works through Torge's writer:

```go
conn, err := websocket.Accept(c.Response(), c.Request(), nil) // github.com/coder/websocket
```

## Testing

```go
app := torgetest.NewApp(t)                    // test env, logs to t.Log, debug errors
app.Register(users.Module{})
app.Replace(func() users.Store { return fake }) // dependency replacement
app.Service("stripe", fakeStripe)              // fake external services

tc := torgetest.New(t, app)                    // started now, shut down at cleanup
tc.WithBearerToken("t").POST("/users").JSON(body).Do().
	ExpectStatus(201).
	ExpectHeaderPresent("X-Request-Id").
	ExpectJSONPath("name", "Ada")

res := tc.GET("/users/1").Do()
user := torgetest.Decode[User](res)

srv := torgetest.Server(t, app)               // real listener for network tests
res, err := torgetest.Handle(t, app, torge.Chain(h, myMiddleware), req) // middleware unit tests
```

For applications built by a constructor, pass `torgetest.NewAppOptions(t)`.
Test configuration: `config.Load(&cfg, config.WithMap(map[string]string{...}))`.

## Standard library compatibility

```go
app.GET("/metrics", torge.WrapHandler(promhttp.Handler()))
app.Use(torge.WrapMiddleware(gorillaHandlers.ProxyHeaders))
app.Mount("/debug/pprof", http.DefaultServeMux)
http.ListenAndServe(":8080", app)             // after app.Start(ctx)
```

`c.Response()` supports `http.Flusher`, `http.Hijacker`, `io.ReaderFrom` and
`http.ResponseController`.

## CLI and extensions

```sh
go install github.com/TosmimForidMehtab/torge/cmd/torge@latest
torge new my-api && cd my-api && go mod tidy
torge generate resource orders     # typed CRUD module + tests
torge dev                          # rebuild and restart on changes
torge routes                       # route table (TORGE_ROUTES=json for JSON)
torge doctor
```

The CLI is optional. Unknown commands run `torge-<name>` from `PATH`, so
extensions can add commands. Extensions for the framework itself are modules
or plain packages: they can register middleware, services, routes,
configuration, hooks, health checks and metrics through the public API, with
no changes to Torge.
