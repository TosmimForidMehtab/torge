# AGENTS.md — Torge

Guidance for AI coding agents working in this repo. Human contribution rules
live in `CONTRIBUTING.md`; security reports per `SECURITY.md`.

## What this repo is

Batteries-included Go backend framework. Core `torge` package is **stdlib-only**
— never add a third-party import to non-test core files. Integrations live in
`contrib/*` (one Go module each); `examples/` and `benchmarks/` are separate
modules; `contrib/tests` holds tests needing test-only deps (OTel SDK,
miniredis). See `go.work` and `README.md`.

## Key conventions

- Handlers see stdlib types (`context.Context`, `*http.Request`); framework
  adds the pipeline around them. Request stages:
  request ID → tracing → access log → error boundary → recovery → security →
  health → global middleware → routing → group/route middleware → handler.
- Routing: `app.GET(...)`, groups, `:param` / trailing `*wildcard` segments.
  Duplicate/conflicting routes are startup errors — keep them deterministic.
- Contracts are tag-driven: `path`/`query`/`header`/`cookie`/`Body` for
  binding, `validate` for rules, `doc`/`example`/`default`/`format` for
  OpenAPI. Binding runs after body decode; params never overwritten by body.
- Errors: `*torge.Error` renders `{error:{code...}}`; anything else is an
  opaque 500. Keep default JSON rendering byte-compatible.
- Public API is additive-only unless explicitly approved. Mirror existing
  shapes (`RouteOption`, `TypedHandler[I,O]`, `Module.Register`) instead of
  inventing parallel ones.

## Verification (run before finishing)

```sh
go test -race ./...                         # core module, from repo root
(cd contrib/<name> && go test -race ./...)  # each touched contrib module
(cd examples && go test ./...)              # regression when core changes
```

`_test.go` files never ship in applications — keep test-only deps out of
non-test imports. Whole relevant test file/package must pass unmodified;
never narrow runs with `-k`/`-run` excludes or skip failing tests covering
your change.

## Active work

- Roadmap tracker: `.agents/plans/2026-09-26-api-surface-roadmap.md`
- Check it before starting a track; update checkboxes as tracks land.
- No `TODO`/`FIXME` markers in-tree (kept at zero); use the tracker + issues.
