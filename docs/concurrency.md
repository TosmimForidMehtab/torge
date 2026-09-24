# Request lifecycle, context lifetime and concurrency

Torge is designed so that the safe approach is the obvious one. This page
documents exactly what runs when, how long each object lives, and what is
safe to share.

## Request lifecycle

Every request passes through the same fixed sequence. `app.Pipeline()`
returns the stages actually installed.

```text
incoming request
  │
  ├─ request-id      assign or accept (trusted proxies only) an ID; set X-Request-Id
  ├─ tracing         optional (WithTracing): continue trace context, start span
  ├─ access-log      measure; after the response, log one structured record
  ├─ error-boundary  render errors returned from inner stages, exactly once
  ├─ recovery        convert panics into 500 errors with stack logging
  ├─ security        host validation, security headers
  ├─ health          /live /ready /health answered here (before user middleware)
  ├─ global middleware (App.Use, in order)
  ├─ router          match; 404 / 405 / OPTIONS / trailing-slash redirect; body limit
  ├─ group middleware (outermost group first, in Use order)
  ├─ route middleware (in the order given)
  ├─ binding + validation (typed handlers)
  └─ handler → services → database / external calls
  │
  └─ response flows back out through the same stages in reverse
```

Errors propagate outward like return values. The error boundary renders the
first unhandled error; stages outside it (access log, tracing) still receive
the original error, so logs and spans record the real cause while clients see
only the public part.

## Context lifetime

| Object | Lifetime | Rules |
|---|---|---|
| `*torge.Context` | One request. Created per request, **never pooled or reused**. | Valid until the handler chain returns. Do not store it or use it from goroutines that outlive the request. |
| `c.Context()` (`context.Context`) | The request. Canceled when the client disconnects or the server shuts down; carries deadlines from middleware. | Pass it to every database and service call. For work that must outlive the request, derive with `context.WithoutCancel(c.Context())` (keeps values such as the request ID, drops cancellation) or dispatch a job. |
| `*torge.ResponseWriter` | One request. | Not usable after the handler returns. |
| Request-scoped dependencies | One request. | Built on first use by `torge.Dep`; `io.Closer`s are closed after the response. |
| Singletons | The application. | Built once at startup; must be safe for concurrent use. |

Because contexts are never pooled, a goroutine that mistakenly keeps a
`*torge.Context` can at worst see a finished request — never another
request's data.

```go
// Wrong: the goroutine outlives the request.
go audit(c)

// Right: copy what you need; decide on cancellation explicitly.
id, user := c.RequestID(), c.User()
ctx := context.WithoutCancel(c.Context())
go audit(ctx, id, user)

// Better for durable work: a background job (retries, shutdown handling).
jobs.Dispatch(c.Context(), "audit", payload)
```

## Goroutine safety

| Type | Safe for concurrent use? |
|---|---|
| `*torge.App` | Registration methods are locked but meant for setup. After `Start`, routing and the pipeline are immutable and read without locks. |
| `*torge.Context` | No (like `http.ResponseWriter`). Use it from the handler's goroutine. |
| `cache.Memory`, `ratelimit.Memory`, `jobs.Manager`, `jobs.MemoryQueue`, `httpclient.Client`, `sqldb.DB`, `validate.Validator`, `openapi.Generator`* | Yes (*`openapi.Generator` is not; it is used only at startup). |
| `session.Session` | No; request-scoped. |

## Pooled objects

Torge pools only buffers whose contents never outlive a call:

- the JSON encoding buffer used by `c.JSON` (returned after the bytes are
  written; buffers larger than 64 KiB are not pooled);
- gzip writers in `middleware.Compress` (reset before reuse).

No request-scoped state is pooled.

## Shared dependencies

Singletons are shared by every request. Keep them immutable after
construction or protect mutable state with synchronization. If a dependency
is inherently per-request (a unit of work, a tenant-scoped client), register
it with `torge.RequestScoped()`; startup fails if a singleton depends on a
request-scoped value.

## Mutable configuration

Options and loaded configuration are read at construction and treated as
immutable. To change configuration, restart the process (orchestrators do
this on deploy). Values that must change at runtime (feature flags, tenant
settings) belong in a service with its own synchronization.

## Middleware state

A middleware factory (`func Foo(cfg) torge.Middleware`) runs once at setup;
the returned closure runs concurrently for many requests. Keep per-request
data in local variables or `c.Set`, never in variables captured from the
factory. Captured state must be read-only or synchronized (as the rate
limiter and response cache do).
