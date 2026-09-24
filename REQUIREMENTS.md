# Torge — Go Backend Framework Requirements Specification

**Working name:** Torge  
**Status:** Initial requirements  
**Target:** Production-grade Go backend framework

## 1. Product Vision

Build a batteries-included backend framework for Go that combines:

- Express-like simplicity
- Go's performance and explicitness
- Huma/FastAPI-style typed API contracts and validation
- Spring Boot-style application lifecycle and conventions
- Hono-like composability and developer experience

The framework must **not** merely be another HTTP router.

It should provide a coherent runtime for:

- HTTP server
- Routing
- Request/response lifecycle
- Middleware
- Error handling
- Validation
- Configuration
- Dependency management
- Logging
- Database lifecycle
- Cache integration
- External services
- Background jobs
- Authentication/authorization primitives
- Health/readiness checks
- Observability
- Graceful shutdown
- Testing utilities
- OpenAPI
- Security defaults

### Core principles

1. Explicit over magical
2. Type-safe where practical
3. Standard-library friendly
4. Composable
5. Production-safe by default
6. Excellent developer experience
7. Minimal unnecessary abstraction
8. Infrastructure must remain replaceable

---

# 2. Go-Native Design

The framework must not attempt to turn Go into Java/Spring.

Developers should still understand and be able to use:

- `context.Context`
- `error`
- `http.Request`
- `http.ResponseWriter`
- structs
- interfaces
- goroutines
- channels

The framework should simplify backend development without hiding Go.

### No mandatory ORM

Do not implement an ORM.

Support existing database ecosystems through integrations such as:

- `database/sql`
- `pgx`
- GORM
- Ent
- Bun
- SQLX

The framework should own database lifecycle, health checks, transactions, and instrumentation—not SQL abstraction.

### Replaceable infrastructure

Do not permanently couple applications to a specific:

- database
- cache
- queue
- logger
- tracing backend
- authentication provider
- external service

Prefer adapters and interfaces.

---

# 3. Application Runtime

Provide a central application abstraction.

Example:

```go
app := Torge.New()

app.GET("/users", ListUsers)

app.Listen(":8080")
```

The application manages:

- HTTP server
- router
- middleware
- dependencies
- lifecycle
- configuration
- shutdown
- modules
- observability
- application-level errors

---

# 4. Request Lifecycle

The request lifecycle must be deterministic and documented.

Conceptually:

```text
Incoming Request
       |
       v
Request ID
       |
       v
Tracing
       |
       v
Security
       |
       v
Global Middleware
       |
       v
Route Matching
       |
       v
Route Middleware
       |
       v
Authentication
       |
       v
Validation
       |
       v
Handler
       |
       v
Service
       |
       v
Database / External Services
       |
       v
Response
       |
       v
Post-processing Middleware
       |
       v
Logging / Metrics / Tracing
       |
       v
Response
```

Middleware ordering must be deterministic.

Developers must be able to determine exactly which middleware executes and in what order.

---

# 5. Context

Provide a request context abstraction.

Example:

```go
func GetUser(c *Torge.Context) error {
    id := c.Param("id")

    user, err := service.GetUser(c.Context(), id)
    if err != nil {
        return err
    }

    return c.JSON(200, user)
}
```

The context should conveniently expose:

- path parameters
- query parameters
- headers
- cookies
- request body
- JSON
- response status
- request metadata
- request ID
- authenticated identity
- logger
- configuration
- dependency access

However, `Context` must not become an uncontrolled global service locator.

---

# 6. `context.Context` Correctness

The framework must preserve normal Go context semantics.

Request cancellation must propagate correctly.

Deadlines must propagate.

Database and external service calls should receive:

```go
c.Context()
```

rather than an artificial context that hides standard Go behavior.

The framework must clearly document context lifetime.

Avoid unsafe request-context pooling semantics that can cause data to be reused after the request lifecycle.

The safe approach should be the obvious approach.

---

# 7. Routing

Support:

- GET
- POST
- PUT
- PATCH
- DELETE
- HEAD
- OPTIONS
- arbitrary/custom HTTP methods
- path parameters
- wildcards
- route groups
- nested groups
- route-level middleware
- group-level middleware
- named routes where practical

Example:

```go
api := app.Group("/api/v1")

api.Use(Auth())

users := api.Group("/users")

users.GET("/:id", GetUser)
users.POST("/", CreateUser)
```

Routing must remain simple and fast.

---

# 8. Middleware

Support:

- global middleware
- group middleware
- route middleware
- short-circuiting
- request modification
- response modification
- pre-processing
- post-processing
- error propagation

Example:

```go
app.Use(
    Logger(),
    Recovery(),
    RequestID(),
)
```

Middleware should have a minimal implementation surface.

Conceptually:

```go
func MyMiddleware(next Torge.Handler) Torge.Handler {
    return func(c *Torge.Context) error {
        // before

        err := next(c)

        // after

        return err
    }
}
```

Do not require complicated middleware interfaces for ordinary middleware.

---

# 9. Error System

Provide structured application errors.

Example:

```go
return Torge.NotFound(
    "USER_NOT_FOUND",
    "User does not exist",
)
```

Errors must support:

- HTTP status
- machine-readable code
- human-readable message
- underlying error
- metadata
- request ID
- internal/public distinction
- error wrapping
- error classification

Example response:

```json
{
  "error": {
    "code": "USER_NOT_FOUND",
    "message": "User does not exist",
    "request_id": "req_123"
  }
}
```

Internal errors must never accidentally expose:

- stack traces
- SQL errors
- credentials
- secrets
- filesystem paths
- internal infrastructure information

Development mode may expose additional diagnostic information.

---

# 10. Panic Recovery

Recover panics at the framework boundary.

The framework must:

1. Recover the panic
2. Capture diagnostic information
3. Log it
4. Attach request metadata
5. Return a controlled response
6. Prevent one request from crashing the entire server

Behavior must be configurable.

---

# 11. Request Validation

Provide first-class validation for:

- JSON bodies
- query parameters
- path parameters
- headers
- cookies
- custom validation
- nested structures
- arrays
- required fields
- formats

Example:

```go
type CreateUserRequest struct {
    Name  string `json:"name" validate:"required,min=3"`
    Email string `json:"email" validate:"required,email"`
}
```

Invalid requests should automatically produce consistent validation responses.

Avoid requiring developers to manually write repetitive validation boilerplate.

---

# 12. Serialization

Provide convenient response helpers:

```go
c.JSON(...)
c.String(...)
c.Bytes(...)
c.Status(...)
c.Header(...)
c.NoContent(...)
```

JSON behavior should use standard Go mechanisms unless a measurable reason justifies another implementation.

Custom serializers should be pluggable.

---

# 13. API Contracts and OpenAPI

Provide first-class API documentation.

The framework should generate OpenAPI documentation from route definitions and typed request/response models.

Support:

- schemas
- request bodies
- parameters
- responses
- validation metadata
- authentication schemes
- tags
- descriptions
- examples

Expose configurable endpoints such as:

```text
/openapi.json
/docs
```

Avoid requiring developers to manually duplicate API definitions.

Typed API contracts, validation, and OpenAPI should be integrated rather than separate systems.

---

# 14. Dependency Management

Provide lightweight dependency registration.

Example:

```go
app.Provide(NewUserService)
app.Provide(NewAuthService)
```

Dependencies should be resolvable through constructors.

Support:

- singleton/application-scoped dependencies
- request-scoped dependencies where necessary
- constructor dependencies
- lifecycle hooks

Avoid excessive reflection.

Explicit registration is preferred.

Dependency resolution should detect:

- missing dependencies
- duplicate registrations where invalid
- dependency cycles
- invalid scopes

---

# 15. Application Modules

Support modular applications.

Example:

```go
app.Register(
    UserModule{},
    AuthModule{},
    PaymentModule{},
)
```

A module may register:

- routes
- middleware
- dependencies
- configuration
- lifecycle hooks
- health checks
- background jobs
- OpenAPI metadata

Modules should provide a scalable answer to organizing large applications without forcing MVC, Clean Architecture, or another rigid architecture.

---

# 16. Configuration

Support:

- environment variables
- defaults
- typed values
- required values
- validation
- development/test/production environments

Example:

```go
type Config struct {
    Port        int    `env:"PORT" default:"8080"`
    DatabaseURL string `env:"DATABASE_URL,required"`
}
```

Invalid configuration should fail before the server accepts traffic.

Secrets must never be logged.

Configuration should be injectable into modules and services.

---

# 17. Logging

Integrate with structured logging.

Prefer Go's modern standard logging ecosystem where appropriate.

Request logs should support:

- request ID
- trace ID
- method
- route
- path
- status
- duration
- user ID
- error

Support:

- log levels
- structured fields
- JSON output
- text output
- request correlation
- custom logger implementations

The framework must not force a proprietary logging system.

---

# 18. Observability

Make observability a first-class concern.

Prefer OpenTelemetry instead of inventing a proprietary tracing system.

Automatically instrument where possible:

- HTTP requests
- database operations through supported adapters
- external HTTP calls through the framework client
- background jobs
- important lifecycle events

Provide metrics for:

- request count
- request duration
- active requests
- status codes
- errors

Allow custom application metrics.

---

# 19. Request ID and Correlation

Every request should receive a request ID.

Behavior:

1. Accept a trusted incoming request ID according to configuration
2. Generate one if absent
3. Attach it to request context
4. Return it in the response
5. Include it in logs
6. Include it in structured errors

Support trace ID propagation when tracing is enabled.

---

# 20. Database Integration

Provide database lifecycle management.

Support:

- connection configuration
- connection creation
- startup validation
- health checks
- graceful shutdown
- instrumentation
- transaction helpers

Example:

```go
app.Database(db)
```

Transaction helper:

```go
err := app.Transaction(ctx, func(tx Torge.Tx) error {
    // transactional work
    return nil
})
```

Remain ORM-agnostic.

Do not implement another ORM.

---

# 21. Cache Integration

Support pluggable caching.

Potential integrations:

- Redis
- Valkey
- in-memory cache

Capabilities:

- get
- set
- delete
- TTL
- invalidation
- namespacing

Redis must not be mandatory.

Distributed deployments must not accidentally rely on process-local state for features requiring shared state.

---

# 22. External Services

Provide a consistent mechanism for registering external services.

Example:

```go
app.Service("stripe", stripeClient)
app.Service("s3", s3Client)
app.Service("github", githubClient)
```

Services should be injectable into modules and handlers where appropriate.

Do not build proprietary implementations of third-party APIs unless there is a compelling reason.

---

# 23. HTTP Client

Provide an optional framework HTTP client with:

- context support
- timeout
- connection pooling
- retries
- exponential backoff
- structured errors
- request ID propagation
- tracing
- logging

Retries must be conservative.

Never blindly retry non-idempotent requests.

Retry policy must be configurable.

---

# 24. Authentication

Provide authentication primitives rather than forcing one authentication strategy.

Support integrations for:

- JWT
- API keys
- sessions
- OAuth/OIDC
- custom authentication providers

Expose an authenticated principal through the request context.

Example:

```go
app.Use(Auth())

func Handler(c *Torge.Context) error {
    user := c.User()

    // ...
}
```

Authorization policies should remain application-specific.

---

# 25. Security Defaults

Provide secure defaults for:

- request body limits
- header limits
- server timeouts
- security headers
- CORS
- trusted proxy configuration
- panic recovery
- request IDs
- host validation
- rate limiting hooks

Dangerous production behavior should require explicit configuration.

Avoid insecure defaults that developers are expected to discover later.

---

# 26. Rate Limiting

Provide rate limiting middleware.

Support:

- in-memory limiter
- Redis/Valkey-backed limiter
- IP-based limits
- route-based limits
- user-based limits
- configurable windows
- burst capacity

Distributed deployments must use shared state when required.

---

# 27. Idempotency

Provide first-class idempotency support for APIs where duplicate requests can be dangerous.

Example:

```http
Idempotency-Key: abc123
```

Support:

- key extraction
- request fingerprinting
- response persistence
- TTL
- conflict detection
- distributed storage

Useful for:

- payments
- orders
- registrations
- webhooks
- other non-idempotent operations

---

# 28. Background Jobs

Provide a common abstraction for background jobs.

Example:

```go
app.Job("send-email", SendEmail{})
```

Dispatch:

```go
jobs.Dispatch(ctx, "send-email", payload)
```

Support adapters rather than implementing proprietary queue infrastructure.

Potential integrations:

- Redis queues
- PostgreSQL-backed queues
- SQS
- RabbitMQ
- NATS

The framework should handle job lifecycle, observability, retries, and graceful shutdown where applicable.

---

# 29. Lifecycle Management

Explicitly manage:

```text
startup
running
shutdown
```

Components may implement lifecycle hooks.

Conceptually:

```go
type Lifecycle interface {
    Start(context.Context) error
    Stop(context.Context) error
}
```

Startup and shutdown order must be deterministic.

Shutdown must:

1. Stop accepting new requests
2. Allow active requests to finish
3. Stop background workers
4. Close dependencies
5. Flush logs/telemetry where possible
6. Exit cleanly

Shutdown must have a configurable timeout.

---

# 30. Health and Readiness

Provide configurable health endpoints such as:

```text
/health
/live
/ready
```

Applications should be able to register checks:

```go
app.HealthCheck("database", dbCheck)
app.HealthCheck("redis", redisCheck)
```

Distinguish between:

- process is alive
- application is ready to receive traffic

Health checks must have timeouts and must not hang indefinitely.

---

# 31. Testing

Testing must be a first-class experience.

Provide:

- test applications
- test servers
- HTTP request builders
- JSON request helpers
- response assertions
- middleware testing
- dependency replacement
- test configuration
- fake service registration

Conceptually:

```go
app := Torge.TestApp()

res := app.Test().
    POST("/users").
    JSON(payload).
    Do()

res.ExpectStatus(201)
```

Tests must be fast and deterministic.

Testing APIs should not require running the full application externally.

---

# 32. Webhooks

Provide webhook primitives supporting:

- signature verification
- raw request body preservation
- replay protection
- idempotency
- timestamp validation
- structured errors

Webhook processing should integrate with request IDs, logging, tracing, and error handling.

---

# 33. File Uploads

Support:

- multipart uploads
- configurable size limits
- streaming
- multiple files
- metadata
- external storage adapters

Never load arbitrarily large files into memory by default.

---

# 34. Streaming

Support:

- streaming responses
- Server-Sent Events
- WebSocket integration
- large response streaming
- request body streaming

Streaming APIs must respect request cancellation.

---

# 35. Official Middleware

Provide optional official middleware for:

- Recovery
- Logger
- RequestID
- CORS
- SecurityHeaders
- Compression
- Timeout
- RateLimit
- BodyLimit
- Authentication
- BasicAuth
- Sessions
- Cache
- CSRF
- StaticFiles
- Metrics
- Tracing

Middleware must remain independently replaceable.

---

# 36. Standard Library Compatibility

Make it easy to integrate existing `net/http` code.

Support adapters for:

```go
http.Handler
http.HandlerFunc
```

Existing Go HTTP components should be reusable without rewriting them.

Do not make framework adoption require abandoning the standard library ecosystem.

---

# 37. Performance

Performance matters, but benchmark theater is forbidden.

Benchmark:

- routing
- middleware
- JSON handling
- request creation
- response creation
- error handling
- dependency resolution

Compare where meaningful against:

- `net/http`
- Gin
- Echo
- Fiber
- Huma where applicable

Avoid reflection or abstraction in hot paths when it causes meaningful overhead.

However:

> Developer productivity and correctness are more important than winning an artificial microbenchmark by 2%.

Do not sacrifice correctness to chase benchmark numbers.

---

# 38. Concurrency Safety

The framework must be safe for concurrent production workloads.

Clearly document:

- request context lifetime
- pooled objects
- goroutine safety
- shared dependency safety
- mutable configuration
- middleware state

Request-specific mutable state must never leak between requests.

---

# 39. Production Defaults

A production-oriented application should have sensible defaults for:

- read timeout
- write timeout
- idle timeout
- request body limit
- header limit
- graceful shutdown timeout
- panic recovery
- request IDs
- structured logging
- health endpoints

Developers should not need to discover dozens of hidden security settings before deployment.

---

# 40. Developer Experience

The framework should minimize boilerplate.

A basic application should be approximately:

```go
package main

import "github.com/.../Torge"

func main() {
    app := Torge.New()

    app.GET("/", func(c *Torge.Context) error {
        return c.JSON(200, map[string]string{
            "message": "hello",
        })
    })

    app.Listen(":8080")
}
```

A large application should retain the same fundamental programming model.

---

# 41. CLI

A CLI should eventually provide:

```bash
Torge new my-api
Torge dev
Torge generate module users
Torge generate resource users
Torge generate middleware auth
Torge routes
Torge doctor
Torge version
```

Potential future commands:

```bash
Torge migrate
Torge db
Torge test
Torge build
```

The CLI must remain optional.

The framework itself must work without the CLI.

---

# 42. Developer Diagnostics

Provide actionable diagnostics for:

- unreachable database
- duplicate route
- middleware registration after startup
- missing required configuration
- dependency cycle
- duplicate dependency
- invalid CORS configuration
- unsafe production configuration
- failed health check
- invalid lifecycle state

Diagnostics should explain:

1. What happened
2. Where it happened
3. Why it matters
4. How to fix it

---

# 43. Route Introspection

Expose registered routes programmatically.

Example:

```go
app.Routes()
```

Route information should include:

- method
- path
- handler
- middleware
- module

This should power:

```bash
Torge routes
```

and development diagnostics.

---

# 44. Configuration Validation

Validate configuration before accepting traffic.

Examples:

```text
DATABASE_URL missing
JWT secret missing
invalid port
invalid CORS origin
negative shutdown timeout
duplicate service
invalid dependency
```

Fail early and provide actionable errors.

---

# 45. Extension / Plugin System

Allow third-party extensions.

Extensions should be able to register:

- middleware
- services
- routes
- configuration
- lifecycle hooks
- health checks
- metrics
- CLI commands

Extensions must not require modifying framework core.

---

# 46. Core Must Stay Small

Separate the framework core from optional integrations.

### Core

- HTTP
- Router
- Context
- Middleware
- Errors
- Lifecycle
- Configuration
- Dependency management

### Optional integrations

- PostgreSQL
- Redis
- MongoDB
- Kafka
- S3
- JWT
- OAuth
- Stripe
- OpenTelemetry
- job queues
- email providers
- cloud services

An application using only HTTP must not be forced to install the entire ecosystem.

---

# 47. Explicitly Avoid These Mistakes

Do NOT:

- build another ORM
- build another Redis client
- build another Kafka client
- build another OpenTelemetry implementation
- build another JWT implementation
- build another logging ecosystem
- create excessive reflection-based dependency injection
- hide goroutines behind unnecessary abstractions
- replace Go's `context.Context`
- force MVC
- force Clean Architecture
- force repository/service/controller layers
- force microservices
- require Docker
- require a CLI
- require code generation
- require an external runtime
- create a proprietary configuration language

The framework should **orchestrate the ecosystem, not replace it**.

---

# 48. Competitive Differentiation

Do not attempt to compete with Gin/Echo/Fiber/Huma merely by adding more features.

Existing frameworks already solve basic routing and middleware very well.

The framework's differentiation should be:

## A. Unified application lifecycle

Understand the complete lifecycle:

```text
HTTP
 -> middleware
 -> validation
 -> dependencies
 -> handler
 -> database
 -> external services
 -> jobs
 -> observability
 -> shutdown
```

## B. Production architecture

Provide strong defaults for:

- errors
- lifecycle
- observability
- security
- configuration
- health
- graceful shutdown

## C. Type-safe developer experience

Prefer compile-time guarantees and Go's type system over runtime magic.

## D. Replaceable infrastructure

Use adapters instead of vendor lock-in.

## E. Large-application ergonomics

Provide:

- modules
- dependency management
- lifecycle management
- diagnostics
- testing
- route introspection

## F. Standard-library compatibility

Existing Go ecosystem code should remain usable.

## G. Correctness over benchmark theater

Do not introduce unsafe pooling, hidden mutable state, or confusing context behavior merely to claim extreme benchmark numbers.

---

# 49. Definition of Done for V1

V1 is complete when an engineer can build a production-style API containing:

- REST API
- routing
- middleware
- validation
- structured errors
- authentication
- database integration
- Redis/cache integration
- external API integration
- structured logging
- request IDs
- OpenTelemetry integration
- health checks
- graceful shutdown
- configuration
- testing utilities
- OpenAPI

A serious application should be expressible conceptually as:

```go
app := Torge.New()

app.Config(config)

app.Logger(logger)

app.Database(db)

app.Cache(redis)

app.Use(
    Torge.Recovery(),
    Torge.RequestID(),
    Torge.Logger(),
    Torge.Tracing(),
)

app.Register(
    AuthModule{},
    UserModule{},
    PaymentModule{},
)

app.Listen(":8080")
```

---

# 50. Quality Bar

Prioritize:

**Correctness > API design > maintainability > developer experience > raw benchmark performance**

Every major feature must include:

- unit tests
- integration tests where applicable
- concurrency tests where applicable
- failure-path tests
- documentation
- examples
- benchmarks for hot paths

Public APIs should be designed carefully because breaking Go APIs after adoption is expensive.

No feature should be added merely because another framework has it.

For every feature ask:

> Does this make production Go backend development materially simpler, safer, or more coherent without hiding how Go works?

If not, leave it out.

---

# 51. Final Objective

The framework should combine:

```text
Express
    -> simple API

FastAPI / Huma
    -> typed contracts + validation + OpenAPI

Spring Boot
    -> lifecycle + conventions + dependency management

Hono
    -> clean and composable developer experience

Go
    -> explicitness + concurrency + standard library + performance
```

But it must **not** become a Frankenstein framework.

The objective is not to cram every feature from existing ecosystems into one repository.

The objective is to identify the recurring problems in production backend development and provide one coherent, idiomatic Go abstraction for solving them.

The framework should feel like:

> **A batteries-included Go backend framework that makes serious backend development dramatically more coherent without hiding Go behind magic.**
