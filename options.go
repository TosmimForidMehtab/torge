package torge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/TosmimForidMehtab/torge/config"
	"github.com/TosmimForidMehtab/torge/validate"
)

// Env is the deployment environment.
type Env = config.Env

// Deployment environments.
const (
	Development = config.Development
	Test        = config.Test
	Production  = config.Production
)

// ServerConfig configures the HTTP server. Zero fields use production-safe
// defaults.
type ServerConfig struct {
	// ReadHeaderTimeout bounds reading request headers (default 10s). It
	// protects against slowloris attacks.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds reading the entire request (default 30s).
	ReadTimeout time.Duration
	// WriteTimeout bounds writing the response (default 30s). Streaming
	// helpers (Stream, SSE) lift it for their request.
	WriteTimeout time.Duration
	// IdleTimeout bounds keep-alive idle time (default 120s).
	IdleTimeout time.Duration
	// MaxHeaderBytes bounds request header size (default 1 MiB).
	MaxHeaderBytes int
	// StartTimeout bounds Start when called by Listen or Serve (default 60s).
	StartTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown (default 30s).
	ShutdownTimeout time.Duration
	// DrainDelay is how long the application keeps serving after readiness
	// turns unhealthy at shutdown, so load balancers stop routing to it
	// (default 0; typically 5-10s on Kubernetes).
	DrainDelay time.Duration
}

func (s *ServerConfig) withDefaults() {
	setDefault(&s.ReadHeaderTimeout, 10*time.Second)
	setDefault(&s.ReadTimeout, 30*time.Second)
	setDefault(&s.WriteTimeout, 30*time.Second)
	setDefault(&s.IdleTimeout, 120*time.Second)
	setDefault(&s.StartTimeout, 60*time.Second)
	setDefault(&s.ShutdownTimeout, 30*time.Second)
	setDefault(&s.MaxHeaderBytes, 1<<20)
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// DefaultBodyLimit is the default maximum request body size (4 MiB).
const DefaultBodyLimit = 4 << 20

// Options configures an App. Use the With* functions with New.
type Options struct {
	// Env is the deployment environment. Defaults to config.CurrentEnv(),
	// which is Production unless TORGE_ENV or APP_ENV says otherwise.
	Env Env
	// Logger is the application logger. Defaults to JSON on stdout in
	// production and text in development.
	Logger *slog.Logger
	// Server configures timeouts and limits.
	Server ServerConfig
	// BodyLimit is the default maximum request body size. Negative disables.
	BodyLimit int64
	// ErrorHandler renders errors. Defaults to DefaultErrorHandler.
	ErrorHandler ErrorHandler
	// Validator validates bound requests. Defaults to validate.Default.
	Validator Validator
	// Serializer encodes and decodes bodies. Defaults to JSON.
	Serializer Serializer
	// ExposeErrors adds internal error details to responses. It defaults to
	// true only in development and is refused in production.
	ExposeErrors bool
	// TrustedProxies lists CIDRs or IPs of reverse proxies whose forwarding
	// headers (X-Forwarded-For, X-Forwarded-Proto, X-Request-ID) are trusted.
	TrustedProxies []string
	// RedirectTrailingSlash redirects /path/ to /path (and vice versa) when
	// only the other form is registered. Default true.
	RedirectTrailingSlash bool

	// Built-in pipeline stages. A nil pointer disables the stage.
	RequestID *RequestIDConfig
	Tracing   Middleware
	AccessLog *AccessLogConfig
	Recovery  *RecoveryConfig
	Security  *SecurityConfig
	Health    *HealthConfig

	envErr         error
	exposeExplicit bool
}

// Option configures an App.
type Option func(*Options)

// WithEnv sets the deployment environment.
func WithEnv(env Env) Option { return func(o *Options) { o.Env = env } }

// WithLogger sets the application logger.
func WithLogger(l *slog.Logger) Option { return func(o *Options) { o.Logger = l } }

// WithServer sets server timeouts and limits.
func WithServer(cfg ServerConfig) Option { return func(o *Options) { o.Server = cfg } }

// WithBodyLimit sets the default request body limit in bytes.
func WithBodyLimit(n int64) Option { return func(o *Options) { o.BodyLimit = n } }

// WithErrorHandler replaces the error renderer.
func WithErrorHandler(h ErrorHandler) Option { return func(o *Options) { o.ErrorHandler = h } }

// WithValidator replaces the request validator.
func WithValidator(v Validator) Option { return func(o *Options) { o.Validator = v } }

// WithSerializer replaces the body serializer.
func WithSerializer(s Serializer) Option { return func(o *Options) { o.Serializer = s } }

// WithExposeErrors controls whether internal error details are included in
// responses. Enabling it in production makes Start fail.
func WithExposeErrors(expose bool) Option {
	return func(o *Options) { o.ExposeErrors, o.exposeExplicit = expose, true }
}

// WithTrustedProxies sets the trusted reverse proxies (IPs or CIDRs).
func WithTrustedProxies(proxies ...string) Option {
	return func(o *Options) { o.TrustedProxies = proxies }
}

// WithRedirectTrailingSlash toggles trailing-slash redirects.
func WithRedirectTrailingSlash(enabled bool) Option {
	return func(o *Options) { o.RedirectTrailingSlash = enabled }
}

// WithRequestID configures request IDs. Pass nil to disable.
func WithRequestID(cfg *RequestIDConfig) Option { return func(o *Options) { o.RequestID = cfg } }

// WithTracing installs tracing middleware (for example from contrib/otel) in
// the pipeline slot right after request IDs, so every later stage is traced.
func WithTracing(m Middleware) Option { return func(o *Options) { o.Tracing = m } }

// WithAccessLog configures request logging. Pass nil to disable.
func WithAccessLog(cfg *AccessLogConfig) Option { return func(o *Options) { o.AccessLog = cfg } }

// WithRecovery configures panic recovery. Pass nil to disable.
func WithRecovery(cfg *RecoveryConfig) Option { return func(o *Options) { o.Recovery = cfg } }

// WithSecurity configures security headers and host validation. Pass nil to
// disable.
func WithSecurity(cfg *SecurityConfig) Option { return func(o *Options) { o.Security = cfg } }

// WithHealth configures health endpoints. Pass nil to disable.
func WithHealth(cfg *HealthConfig) Option { return func(o *Options) { o.Health = cfg } }

// WithoutDefaults disables every optional built-in stage (request IDs,
// access log, recovery, security headers, health endpoints). Server timeouts
// and body limits stay in place. Use it to compose the pipeline by hand.
func WithoutDefaults() Option {
	return func(o *Options) {
		o.RequestID, o.AccessLog, o.Recovery, o.Security, o.Health = nil, nil, nil, nil, nil
	}
}

func defaultOptions() Options {
	env, err := config.CurrentEnv()
	return Options{
		Env:                   env,
		envErr:                err,
		RedirectTrailingSlash: true,
		BodyLimit:             DefaultBodyLimit,
		RequestID:             &RequestIDConfig{},
		AccessLog:             &AccessLogConfig{},
		Recovery:              &RecoveryConfig{},
		Security:              &SecurityConfig{},
		Health:                &HealthConfig{},
	}
}

// Validator validates decoded request values.
type Validator interface {
	Validate(v any) error
}

var _ Validator = (*validate.Validator)(nil)

// Serializer encodes response bodies and decodes request bodies.
type Serializer interface {
	ContentType() string
	Encode(w io.Writer, v any) error
	Decode(r io.Reader, v any) error
}

// JSONSerializer is the default Serializer, built on encoding/json.
type JSONSerializer struct {
	// DisallowUnknownFields rejects request bodies with unknown fields.
	DisallowUnknownFields bool
}

// ContentType implements Serializer.
func (JSONSerializer) ContentType() string { return "application/json; charset=utf-8" }

// Encode implements Serializer.
func (JSONSerializer) Encode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

// Decode implements Serializer.
func (s JSONSerializer) Decode(r io.Reader, v any) error {
	return s.decode(r, -1, v)
}

// decode reads the whole body (sizeHint is its expected length, or -1) and
// decodes a single JSON value from it. A body that is empty or only
// whitespace yields io.EOF; anything after the value is rejected.
func (s JSONSerializer) decode(r io.Reader, sizeHint int64, v any) error {
	b, err := readAll(r, sizeHint)
	if err != nil {
		return err
	}
	if len(bytes.TrimLeft(b, " \t\r\n")) == 0 {
		return io.EOF
	}
	if !s.DisallowUnknownFields {
		return json.Unmarshal(b, v)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if len(bytes.TrimLeft(b[dec.InputOffset():], " \t\r\n")) != 0 {
		return errTrailingData
	}
	return nil
}

// maxPrealloc caps the buffer allocated up front from a declared length.
const maxPrealloc = 1 << 20

// readAll is io.ReadAll with the first buffer sized from sizeHint, so a body
// of the declared length is read with a single allocation.
func readAll(r io.Reader, sizeHint int64) ([]byte, error) {
	size := 512
	if sizeHint > 0 && sizeHint < maxPrealloc {
		size = int(sizeHint) + 1 // one spare byte to observe io.EOF
	}
	b := make([]byte, 0, size)
	for {
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			if err == io.EOF {
				return b, nil
			}
			return b, err
		}
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
	}
}

// errTrailingData reports a request body with data after the first value.
var errTrailingData = errors.New("unexpected data after the JSON value")
