# Benchmarks

Correctness and clarity come first; benchmarks exist to keep the hot path
honest, not to win microbenchmarks. Torge deliberately does **not** pool
request contexts (see [concurrency.md](concurrency.md)), so request data can
never leak between requests. That costs one small allocation per request,
which the rest of the hot path is designed around.

## Reproducing

```sh
go test -run '^$' -bench . -benchmem                   # framework hot paths
(cd benchmarks && go test -run '^$' -bench . -benchmem) # vs net/http, Gin, Echo
```

Sample results: Go 1.25, Windows, 8 logical CPUs. Absolute numbers vary by
machine and run-to-run noise is about ±5%; compare relative values.

## Against other frameworks

In-process through `http.Handler`, averaged over 5 runs.

**Realistic JSON API**: `POST /users/:id` with a JSON body, validation
(`required`, `min`/`max`, `email`, `gte`/`lte`) and a JSON
response. Gin and Echo use `go-playground/validator`, their usual pairing.

| Framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| **Torge** | **~4,430** | **728** | **11** |
| Gin | ~7,050 | 1,294 | 18 |
| Echo | ~8,260 | 1,277 | 19 |

**Routers alone**: the same 10-route table and handler (read a path
parameter, write small JSON), no logging or other middleware.

| Framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Gin | ~750 | 72 | 3 |
| Torge (`WithoutDefaults`) | ~790 | 280 | **2** |
| Echo | ~880 | 88 | 3 |
| `net/http.ServeMux` + `encoding/json` | ~1,360 | 152 | 5 |

The gap to Gin is the per-request `Context` allocation that Torge keeps
instead of pooling.

**Production setups**: one log line per request to a discard sink, plus
panic recovery.

| Framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Echo + Logger + Recover + RequestID | ~3,150 | 572 | 14 |
| Torge defaults | ~3,630 | 440 | **4** |
| Gin + Logger + Recovery | ~3,810 | 320 | 13 |

Torge's defaults also do work the others do not: structured `slog` access
logs with more fields (route, client IP, user ID, error code), request IDs
propagated into `context.Context` for downstream logs, HTTP calls and jobs,
security headers and health endpoints. About half of the default pipeline's
time is `log/slog` formatting the access-log record; with a level that
filters out access logs, that cost disappears.

## What was optimized

- No per-request allocation in routing: parameter values use an inline
  buffer, and static children are scanned linearly (faster than map
  hashing for the few children most nodes have).
- Common `Content-Type` values are shared, and `Content-Length` is left to
  `net/http` for bodies that fit its buffer (clients still receive it).
- JSON responses are encoded straight into the response writer, with no
  intermediate buffer.
- Rarely used context state (user, logger, request-scoped values, cleanup
  hooks) is allocated only on first use. Request IDs and the user are
  attached to the request's `context.Context` only when something asks for
  it, so the per-request context stays at 256 bytes.
- Request IDs use the runtime's per-thread generator instead of a system
  call, and the response header reuses the ID's storage. Access logs skip
  `slog`'s caller lookup and check the level before building anything.
- JSON bodies are read into one buffer sized from `Content-Length` and
  decoded with `json.Unmarshal`. The body limit is enforced by a limiter
  embedded in the context, not a separate `http.MaxBytesReader`, whenever
  the length is declared.
- `Content-Type` checks parse the common forms without allocating, and fall
  back to `mime.ParseMediaType` for anything unusual.
- Validation builds field paths only when a rule fails, and the `email` rule
  checks ordinary addresses without allocating. Unusual addresses (quoted,
  bracketed, non-ASCII) still go through `net/mail`, so results are
  identical.

## Hot paths (core module)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Routing, static path | ~230 | 256 | 1 |
| Routing, 3 path parameters (15-route table) | ~280 | 256 | 1 |
| Routing, wildcard | ~250 | 256 | 1 |
| `net/http.ServeMux`, same routes | ~815 | 112 | 3 |
| 5 pass-through middleware | ~195 | 256 | 1 |
| JSON response | ~1,080 | 320 | 2 |
| Error response (structured JSON) | ~1,235 | 344 | 3 |
| Singleton lookup via `torge.Dep` | ~270 | 256 | 1 |
| Full default pipeline | ~2,980 | 416 | 3 |

The single allocation in the routing benchmarks is the per-request
`Context`. The typed-handler benchmark (~9.6 µs) is not listed: over 80% of
its allocations come from building the request with `httptest.NewRequest`.
Fiber is omitted from comparisons because it is built on fasthttp, so an
`http.Handler` comparison would not be like-for-like.
