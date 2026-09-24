# Production defaults, diagnostics and shutdown

## Defaults

`torge.New()` is production-ready without configuration.

| Setting | Default | Change with |
|---|---|---|
| Environment | `production` unless `TORGE_ENV`/`APP_ENV` says otherwise | `WithEnv` |
| Read header timeout | 10s | `WithServer` |
| Read timeout | 30s | `WithServer` |
| Write timeout | 30s (lifted per request for `Stream`/`SSE`) | `WithServer` |
| Idle timeout | 120s | `WithServer` |
| Max header size | 1 MiB | `WithServer` |
| Request body limit | 4 MiB (per route: `torge.BodyLimit`) | `WithBodyLimit` |
| Startup timeout | 60s | `WithServer` |
| Graceful shutdown timeout | 30s | `WithServer` |
| Readiness drain delay | 0 (use 5–10s behind load balancers) | `ServerConfig.DrainDelay` |
| Panic recovery | on | `WithRecovery` |
| Request IDs | on; incoming IDs trusted only from trusted proxies | `WithRequestID` |
| Structured access logs | on; JSON in production | `WithAccessLog`, `WithLogger` |
| Security headers | nosniff, frame deny, strict referrer policy | `WithSecurity` |
| Health endpoints | `/live`, `/ready`, `/health` | `WithHealth` |
| Internal error details in responses | off (on only in development; refused in production) | `WithExposeErrors` |
| Forwarded headers (`X-Forwarded-For`/`-Proto`) | ignored unless from a trusted proxy | `WithTrustedProxies` |

`WithoutDefaults()` removes the optional stages (request IDs, access log,
recovery, security headers, health) but keeps timeouts and body limits.

## Providing configuration in production

Torge reads configuration from **process environment variables**
(`config.Load`), exactly like `process.env` in Node. Anything that sets them
works without code changes. `config.LoadDotEnv()` (or
`import _ "github.com/TosmimForidMehtab/torge/config/autoload"`) behaves like Node's
`dotenv`: it fills in variables from a `.env` file **if one exists**, in every
environment, and never overrides variables that are already set. The same
binary therefore runs unchanged on a laptop, in a container and on a VPS.

| Deployment | How values arrive | What to do |
|---|---|---|
| Render, Heroku, Railway, Fly.io, Cloud Run, Kubernetes, ECS | The platform's env manager injects env vars (and `PORT`) | Add the variables in the dashboard or manifest. Listen with `app.Listen("")`. |
| Render "Secret Files" | Mounted as files (Render documents `/etc/secrets/<name>`) | Set `TORGE_ENV_FILE=/etc/secrets/.env`, or use env vars instead. |
| Secret managers (Doppler, Infisical, 1Password, Vault Agent) | Their runner or agent injects env vars (`doppler run -- ./app`) | Start the app through the runner. |
| Docker / Compose | `-e`, `--env-file prod.env` or `env_file:` | Nothing: Docker sets real env vars. `.env` is excluded from images by the generated `.dockerignore`. |
| VPS with systemd | `EnvironmentFile=/etc/myapp/app.env` | Nothing: systemd sets real env vars. Keep the file `chmod 600`. |
| VPS with a `.env` beside the binary (pm2, supervisor, `nohup`, systemd) | `LoadDotEnv` finds it | Nothing: `.env` is found next to the executable even when started from another directory. |

**Port.** Platforms tell the app which port to use through `PORT` (Render
uses 10000 by default). `app.Listen("")` listens on `:$PORT`, or `:8080`
when `PORT` is unset, like `app.listen(process.env.PORT || 8080)` in
Express. An explicit address (`app.Listen(":9000")`, or `ADDR` in the
generated config) always wins.

**Precedence**, highest first:

1. Real environment variables (platform, Docker, systemd, your shell).
2. Files named by `TORGE_ENV_FILE` (comma-separated), which then replace the
   default locations and must exist.
3. `.env` in the working directory.
4. `.env` next to the executable.

Values not provided anywhere fall back to `default` tags; required values
still missing stop the process before it accepts traffic, each listed by
variable name.

```ini
# /etc/systemd/system/myapp.service
[Service]
ExecStart=/opt/myapp/myapp
EnvironmentFile=/etc/myapp/app.env   # or put .env next to /opt/myapp/myapp
Environment=TORGE_ENV=production
User=myapp
Restart=on-failure
```

`torge new` generates a multi-stage `Dockerfile` (static binary,
distroless non-root image) and a `.dockerignore` that keeps `.env` out of
images, so containers always get configuration from the platform.

## Deployment checklist

- Set `TORGE_ENV=production` explicitly (it is also the default).
- Set `WithTrustedProxies` to your load balancer ranges so client IPs, HTTPS
  detection and request IDs are correct.
- Set `ServerConfig.DrainDelay` so load balancers stop routing before the
  server stops accepting connections.
- Use shared state for anything that must be consistent across instances:
  `contrib/redis` for the cache, rate limits, idempotency, sessions and webhook
  replay; a durable job queue. Torge logs a `PROCESS_LOCAL_STATE` warning when
  in-memory implementations are registered in production.
- Configure `SecurityConfig.AllowedHosts` and `HSTSMaxAge` if you serve
  browsers directly.
- Wrap secrets in `config.Secret`.
- Prefer your platform's env manager or secret store for secrets; if you deploy a `.env` file, keep it readable only by the service user.
- Listen with `app.Listen("")` (or leave `ADDR` empty) so the platform's `PORT` is honored.
- Point liveness probes at `/live` and readiness probes at `/ready`.

## Diagnostics

Setup problems are reported together, before the server accepts traffic.
Each diagnostic states **what** happened, **where** (the file and line of the
registration), **why** it matters and **how to fix** it:

```text
torge: 2 problem(s) prevent startup:
torge: DUPLICATE_ROUTE: GET /users/:uid conflicts with GET /users/:id
    where: /app/internal/users/module.go:41 (first registered at /app/internal/users/module.go:38)
    why:   two handlers for the same request shape make routing ambiguous
    fix:   remove one registration or change its path; parameter names do not make paths distinct
torge: MISSING_DEPENDENCY: users.NewService (/app/internal/users/module.go:22) requires *sqldb.DB, but nothing provides it
    where: /app/internal/users/module.go:22
    why:   the value cannot be constructed
    fix:   register a constructor returning *sqldb.DB with app.Provide, or a value with app.Supply
```

| Code | Raised when |
|---|---|
| `DUPLICATE_ROUTE` | Two routes share a method and path shape. |
| `INVALID_ROUTE` | A path pattern or method is malformed. |
| `DUPLICATE_ROUTE_NAME` | Two routes share a name. |
| `INVALID_HANDLER` | Nil handlers or middleware, unbindable typed inputs, invalid validation tags, duplicate jobs. |
| `REGISTRATION_AFTER_START` | Routes, middleware, providers or hooks are added after startup (panics). |
| `INVALID_LIFECYCLE_STATE` | `Start` is called twice, or `Serve` on a stopped app. |
| `MISSING_DEPENDENCY` / `DUPLICATE_DEPENDENCY` / `DEPENDENCY_CYCLE` / `INVALID_SCOPE` / `INVALID_PROVIDER` | Dependency graph problems. |
| `INVALID_CONFIGURATION` | Invalid environment name, trusted proxies, registered configuration values, CORS or CSRF settings, negative timeouts. |
| `UNSAFE_PRODUCTION_CONFIGURATION` | Development-only behavior enabled in production. |
| `DATABASE_UNREACHABLE` | A registered database fails its startup ping. |
| `START_HOOK_FAILED` | A start hook or constructor fails. |
| `DUPLICATE_SERVICE` / `DUPLICATE_MODULE` / `MODULE_REGISTRATION_FAILED` / `INVOKE_FAILED` / `DUPLICATE_HEALTH_CHECK` | Registration problems. |
| `PROCESS_LOCAL_STATE` (warning) | In-memory shared-state implementations in production. |

Failed health checks are logged at Warn with the check name and error.
Configuration loading (`config.Load`) reports every missing, malformed or
invalid variable by name in one error.

## Graceful shutdown

On SIGINT/SIGTERM (or `app.Shutdown(ctx)`):

1. The state becomes `stopping`; `/ready` returns `503`.
2. The app waits `DrainDelay` while still serving.
3. Streaming responses (`Stream`, `SSE`) have their context canceled so they
   end promptly; the HTTP server stops accepting connections and waits for
   other in-flight requests to complete normally.
4. Stop hooks run in reverse start order: job workers stop receiving and
   finish in-flight jobs; container-managed components stop; caches and
   databases close; telemetry registered with `torgeotel.FlushOnShutdown`
   flushes last if it was registered first.
5. The state becomes `stopped` and `Listen` returns.

Everything is bounded by `ShutdownTimeout`; when it expires the server closes
remaining connections and running jobs are canceled. Errors from every step
are joined and returned.
