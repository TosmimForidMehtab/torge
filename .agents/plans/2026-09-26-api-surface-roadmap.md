# API Surface Roadmap — execution tracker

Approved plan (API surface focus, broad roadmap) delivered inline and approved
in session; this file tracks execution. Full plan body lives in session history.
Status updated as each track lands.

## Tracks

- [x] 1. List ergonomics — standard `Page`/`Sort`/`Filter` bindable structs,
  cursor + offset pagination envelopes, `Link` headers.
  Landed as `page.go` (`PageParams`, `Page[T]`, `CursorParams`, `CursorPage[T]`,
  `ParseSort`, `Search`, `Context.SetPageLinks`) + `page_test.go`, stdlib-only.
- [ ] 2. Response contracts — RFC 9457 problem-details rendering option, typed
  `Result[T]` / `Page[T]` envelopes, `Accept` content negotiation.
  Surfaces: `errors.go`, `response.go`, `context.go`.
  (Partial head start: `Accepts`/`Format`/`Is` + 406 landed in `express.go`.)
- [x] Express req/res parity — `Send`, `Format`, `Accepts`, `Is`, `Location`,
  `Links`, `Type`, `ClearCookie`, `Hostname`, `Secure`, `XHR`,
  `CodeNotAcceptable`/`NotAcceptable`. Landed as `express.go` + `express_test.go`.
- [x] 2. Response contracts — `ProblemBody`/`NewProblem`/`ProblemErrorHandler`
  (`application/problem+json`, opt-in via `WithErrorHandler`), `Result[T]`
  envelope with `NewResult`/`WithMeta`. Landed in `errors.go`, `result.go` +
  `result_test.go`. Default envelope byte-compatible.
- [x] 3. Binding gaps — `Strict` marker (unknown JSON fields rejected,
  undeclared query params 422), `query:"tag,comma"` splitting, `layout` tag
  for time.Time params, `BindFormValues` for multipart form fields.
  Landed in `internal/binding/binding.go`, `bind.go`, `strict_test.go` + guide.
- [x] 4. OpenAPI completeness — `Discriminator` + oneOf via SchemaProvider,
  `RequestContentType` (multipart via `format:"binary"`), `RequestExample` /
  `ResponseExample` (schema-preserving merge), `Scopes` merging into Security.
  Landed in `openapi/types.go`, `route.go`, `openapi_gen.go` + `openapi_extra_test.go`.
- [x] 5. Versioning story — `Group.Version` (`/api/<v>` + tag), `Sunset`
  option (docs deprecated + Deprecation/Sunset headers, group-compatible),
  `middleware.APIVersion`/`RequestVersion` (Accept-Version, latest default),
  `RouteInfo.Deprecated`. Landed in `group.go`, `route.go`,
  `middleware/version.go` + tests.
- [x] 6. Realtime ergonomics — `realtime.Hub` (topics, per-topic replay,
  Last-Event-ID resume, non-blocking publish, `Serve` bridge to `c.SSE()`).
  WS stays bring-your-own via hijack. Landed as `realtime/` + tests.
- [x] 7. Auth/policy expressiveness — `auth.Policy` + `All`/`Any`/`Not`
  combinators, `OwnerIs`/`RequireOwnerID`, composing with `auth.Require`
  (403 FORBIDDEN on denial). Landed in `auth/policy.go` + tests.
- [x] 8. Testing + DX — `torgetest.FetchOpenAPI`/`AssertOpenAPIGolden`
  (`TORGE_UPDATE_GOLDEN=1` refresh), `torge routes --check golden.json`
  drift gate. Landed in `torgetest/openapi.go`, `cmd/torge/main.go` + tests.
  (`generate client` stub left as follow-up: output shape undecided.)

## Ground rules (from approved plan)

- Additive-only public API; core module stays stdlib-only.
- Mirror existing conventions: `RouteOption`, tag-driven binding
  (`path`/`query`/`header`/`cookie`/`Body` + `validate`), OpenAPI derived from
  the same structs, `Body[T]`/`Returns[T]` fallback for untyped handlers.
- Highest-risk slice: error-rendering changes (Track 2) — prove byte-level
  backward compatibility for default JSON consumers.

## Validation per track

- `go test -race ./...` (core) + touched module suites.
- New `*_test.go` per feature; OpenAPI golden files where schemas change.
- `cd examples && go test ./...` as regression.
- Manual: `/openapi.json` + `/docs` render; `torge routes` output.

## Design references

- REST: resource-oriented routes, correct method semantics, plural nouns,
  stateless requests, correct status codes, versioning from day one,
  always-paginate collections, OpenAPI docs.
- Pagination/filter pattern: `page`/`page_size` params with bounds,
  `PaginatedResponse{items, total, page, page_size, pages}` + `has_next`/`has_prev`.
- Errors: consistent envelope/status codes (2xx/4xx/5xx), structured
  validation details per field.
