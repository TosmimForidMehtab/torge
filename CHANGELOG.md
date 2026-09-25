# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).
The core module and each contrib module are versioned independently; contrib
entries are marked with their module.

## [Unreleased]

### Added

- Release workflow: pushing a version tag publishes a GitHub release with
  prebuilt `torge` CLI binaries for Linux, macOS and Windows.

### Changed

- `auth.BasicUsers` and `auth.StaticKeys` compare credentials using
  HMAC-SHA256 tags under a random per-verifier key instead of unkeyed SHA-256
  digests. Comparison stays constant-time and length-hiding; no behavior
  change for callers.

### Fixed

- `torge version` printed `dev` when installed with `go install`; it now
  prints the installed module version.

## [0.1.1] - 2026-09-25

### Changed

- `torgetest.Server` now serves with the application's own server
  configuration (timeouts, header limits), matching `App.Listen`.
- Internal code modernized with newer standard library helpers
  (`slices.Backward`, `maps.Copy`, `strings.CutPrefix`); no behavior change.

### Fixed

- `torge generate` reported "go.mod has no module directive" when reading
  `go.mod` failed; it now reports the read error.

### Added

- Continuous integration on Linux, macOS and Windows, including PostgreSQL
  and MongoDB integration tests.

## [0.1.0] - 2026-09-25

Initial release.

### Added

- Core framework: application lifecycle with graceful shutdown, segment-trie
  router with groups, deterministic middleware pipeline, request context,
  structured errors, panic recovery, request IDs, structured access logs,
  security headers, body limits and health endpoints.
- Typed handlers with request binding, validation and generated OpenAPI 3.1
  documentation with Swagger UI or Scalar.
- Dependency container with constructor injection, scopes and startup
  diagnostics; modules; route introspection.
- Configuration from environment variables with `config.Secret`,
  `config.Database`, `config.Redis`, and Node-style `.env` loading.
- Packages for caching, background jobs, HTTP client, `database/sql`,
  authentication, sessions, rate limiting, idempotency, webhooks, file
  uploads and official middleware (CORS, compression, timeouts, CSRF, static
  files, response caching).
- `torgetest` testing toolkit and the optional `torge` CLI.
- Contrib modules `contrib/otel`, `contrib/redis`, `contrib/jwt`,
  `contrib/pgx` and `contrib/mongo`, each at v0.1.0.

[Unreleased]: https://github.com/TosmimForidMehtab/torge/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/TosmimForidMehtab/torge/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/TosmimForidMehtab/torge/releases/tag/v0.1.0
