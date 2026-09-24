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

Same 10-route table and the same handler (read a path parameter, write
small JSON), in-process through `http.Handler`.

**Routers alone** (no logging or other middleware):

| Framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Gin | ~780 | 72 | 3 |
| Echo | ~880 | 88 | 3 |
| Torge (`WithoutDefaults`) | ~920 | 264 | **2** |
| `net/http.ServeMux` + `encoding/json` | ~1,380 | 152 | 5 |

**Production setups** (one log line per request to a discard sink, plus
panic recovery):

| Framework | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Echo + Logger + Recover + RequestID | ~3,550 | 491 | 11 |
| Gin + Logger + Recovery | ~3,920 | 320 | 13 |
| Torge defaults | ~4,320 | 825 | **8** |

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
- Rarely used context state (user, logger, request-scoped values, cleanup
  hooks) is allocated only on first use, keeping the per-request context at
  240 bytes.
- Request IDs use the runtime's per-thread generator instead of a system
  call; access logs skip `slog`'s caller lookup and check the level before
  building anything.

## Hot paths (core module)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Routing, static path | ~220 | 240 | 1 |
| Routing, 3 path parameters (15-route table) | ~305 | 240 | 1 |
| Routing, wildcard | ~235 | 240 | 1 |
| `net/http.ServeMux`, same routes | ~790 | 112 | 3 |
| 5 pass-through middleware | ~225 | 240 | 1 |
| JSON response | ~1,190 | 304 | 2 |
| Error response (structured JSON) | ~1,325 | 328 | 3 |
| Singleton lookup via `torge.Dep` | ~250 | 240 | 1 |
| Full default pipeline | ~3,530 | 800 | 7 |

The single allocation in the routing benchmarks is the per-request
`Context`. Fiber is omitted from comparisons because it is built on
fasthttp, so an `http.Handler` comparison would not be like-for-like.
