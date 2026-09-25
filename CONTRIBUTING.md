# Contributing to Torge

Thanks for your interest in improving Torge. Bug reports, documentation
fixes, tests and code are all welcome.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
To report a security vulnerability, follow [SECURITY.md](SECURITY.md)
instead of opening a public issue.

## Before you start

- **Bugs:** search the [issues](https://github.com/TosmimForidMehtab/torge/issues)
  first; if none matches, open one with a minimal reproduction.
- **Features and larger changes:** open an issue to discuss the design
  before writing code. Torge deliberately stays small (see
  [Design principles](#design-principles)), so agreeing on the approach first
  saves everyone time.
- **Small fixes** (typos, docs, obvious bugs) can go straight to a pull
  request.

## Development setup

Requirements: **Go 1.25 or newer** and Git. Docker is only needed to run the
database integration tests.

```sh
git clone https://github.com/TosmimForidMehtab/torge.git
cd torge
go test ./...
```

The repository is a Go workspace (`go.work`) with several modules:

| Directory | Module | Published |
|---|---|---|
| `.` | core framework (`github.com/TosmimForidMehtab/torge`) | yes |
| `contrib/otel`, `contrib/redis`, `contrib/jwt`, `contrib/pgx`, `contrib/mongo` | optional integrations | yes |
| `contrib/tests` | contrib tests needing test-only dependencies | no |
| `examples` | example applications | no |
| `benchmarks` | comparisons with other frameworks | no |

Thanks to the workspace, every module builds against your local copy of the
core, so a change to the core and a contrib module can be made together.

## Running checks

CI runs the following; please run them before opening a pull request.

**bash / zsh (Linux, macOS, Git Bash):**

```sh
# Tests for every module (drop -race on Windows)
for dir in $(go list -m -f '{{.Dir}}'); do (cd "$dir" && go test -race ./...); done

# Formatting, vet and staticcheck
gofmt -l .
for dir in $(go list -m -f '{{.Dir}}'); do (cd "$dir" && go vet ./...); done
go install honnef.co/go/tools/cmd/staticcheck@v0.7.0
for dir in $(go list -m -f '{{.Dir}}'); do (cd "$dir" && staticcheck ./...); done

# go.mod files must be tidy
for dir in $(go list -m -f '{{.Dir}}'); do (cd "$dir" && GOWORK=off go mod tidy -diff); done
```

**PowerShell:**

```powershell
go list -m -f '{{.Dir}}' | ForEach-Object { Push-Location $_; go test ./...; Pop-Location }
```

**Database integration tests** run against real servers when these variables
are set (CI sets them):

```sh
docker run -d --name pg -e POSTGRES_PASSWORD=postgres -p 5432:5432 postgres:17
export TORGE_TEST_POSTGRES_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'
(cd contrib/pgx && go test -run TestTransactions -v ./...)

docker run -d --name mongo -p 27017:27017 mongo:8 --replSet rs0 --bind_ip_all
docker exec mongo mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"localhost:27017"}]})'
export TORGE_TEST_MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0'
(cd contrib/mongo && go test -run TestTransactions -v ./...)
```

## Design principles

Changes are evaluated against these principles (see the
[guide](docs/guide.md) and [docs](docs/) for details):

1. **The core module has no third-party dependencies.** Anything that needs
   one belongs in a new or existing `contrib/` module. Pull requests adding a
   `require` to the root `go.mod` will not be accepted.
2. **Explicit over magical.** No hidden goroutines, global state or
   reflection on the hot path. Handlers keep working with `context.Context`,
   `error` and the standard `net/http` types.
3. **Production-safe by default.** New settings must default to the safe
   choice; unsafe behavior requires explicit configuration and, where it is
   dangerous in production, is refused at startup.
4. **Correctness over benchmarks.** Request contexts are never pooled. A
   performance change must come with benchmark numbers
   (`go test -run '^$' -bench . -benchmem`) and must not trade away
   correctness.
5. **Orchestrate the ecosystem, don't replace it.** Integrate existing
   libraries through small interfaces instead of reimplementing them.
6. **Public APIs are hard to change.** Keep new exported identifiers to the
   minimum needed, and document every one.

## Making changes

- **Tests are required** for bug fixes (a test that fails without the fix)
  and for new behavior, including failure paths. Tests must be deterministic
  and pass with `-race`.
- **Document** exported identifiers with Go doc comments, and update
  `docs/` and `README.md` when behavior visible to users changes.
- **Changelog:** add a line under `Unreleased` in
  [CHANGELOG.md](CHANGELOG.md) for any user-visible change.
- **New contrib module:** create `contrib/<name>` with its own `go.mod`
  requiring the latest core release, add it to `go.work` and to the published
  module lists in `.github/workflows/ci.yml` and `.github/dependabot.yml`,
  and keep test-only dependencies in `contrib/tests`.
- Keep each pull request focused on one change.

## Commit messages and pull requests

- Write commit subjects in the imperative mood, under about 72 characters:
  `router: support wildcards at the root`.
- Prefix with the affected area where it helps: `config:`, `contrib/redis:`,
  `docs:`, `ci:`.
- Explain *why* in the body when it is not obvious.
- Fill in the pull request template, link related issues (`Fixes #123`) and
  make sure CI is green.

## Releasing (maintainers)

Each module is versioned independently with [semantic versioning](https://semver.org).

Pushing a tag runs the [release workflow](.github/workflows/release.yml),
which validates the tag, tests the tagged module, publishes the GitHub
release (with prebuilt CLI binaries for core releases) and notifies the Go
module proxy.

1. Move the `Unreleased` entries in [CHANGELOG.md](CHANGELOG.md) under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading and commit. Core releases fail without
   this section, since it becomes the release notes.
2. Tag the core and push the tag:
   `git tag -a v0.X.Y -m "Torge v0.X.Y" && git push origin v0.X.Y`.
3. If a contrib module needs the new core, bump its requirement
   (`cd contrib/<name> && GOWORK=off go get github.com/TosmimForidMehtab/torge@v0.X.Y && go mod tidy`),
   commit and push.
4. Tag changed contrib modules with their directory as a prefix and push the
   tag: `git tag -a contrib/<name>/v0.X.Y -m "contrib/<name> v0.X.Y" && git push origin contrib/<name>/v0.X.Y`.
   Their release notes are generated from commits since the previous release
   of the same module.

A tag with a suffix such as `v0.3.0-rc.1` is published as a pre-release.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
