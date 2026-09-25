package torge

import (
	"encoding/base32"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge/correlation"
)

// ---- Request ID ----

// RequestIDConfig configures request ID handling.
type RequestIDConfig struct {
	// Header carries the ID in requests and responses (default X-Request-ID).
	Header string
	// Generator creates new IDs (default: 26 random base32 characters).
	Generator func() string
	// TrustIncoming decides whether an incoming ID is accepted. By default,
	// IDs are accepted only from trusted proxies, so clients cannot inject
	// arbitrary values into logs.
	TrustIncoming func(c *Context) bool
}

// RequestID assigns every request an ID: a valid, trusted incoming ID is
// reused, otherwise one is generated. The ID is attached to the Context, to
// c.Context() (see correlation.RequestID), to logs, to error responses and to
// the response header. It is a no-op if an ID was already assigned.
//
//go:noinline
func RequestID(cfgs ...RequestIDConfig) Middleware {
	var cfg RequestIDConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	header := http.CanonicalHeaderKey(cfg.Header)
	if header == "" {
		header = "X-Request-Id"
	}
	gen := cfg.Generator
	if gen == nil {
		gen = newRequestID
	}
	trust := cfg.TrustIncoming
	if trust == nil {
		trust = (*Context).IsFromTrustedProxy
	}
	return func(next Handler) Handler {
		return func(c *Context) error {
			if c.requestID[0] != "" {
				return next(c)
			}
			id := c.req.Header.Get(header)
			if id == "" || !validRequestID(id) || !trust(c) {
				id = gen()
			}
			c.requestID[0] = id
			// Capacity 1: Header.Add copies instead of writing into the Context.
			c.res.Header()[header] = c.requestID[:1:1]
			c.pending |= pendingRequestID // attached to the context on first use
			return next(c)
		}
	}
}

func validRequestID(id string) bool {
	if len(id) > 128 {
		return false
	}
	for i := range len(id) {
		ch := id[i]
		if !('a' <= ch && ch <= 'z' || 'A' <= ch && ch <= 'Z' || '0' <= ch && ch <= '9' || strings.IndexByte("-_.:/+=", ch) >= 0) {
			return false
		}
	}
	return true
}

// ---- Recovery ----

// RecoveryConfig configures panic recovery.
type RecoveryConfig struct {
	// StackSize is the maximum stack trace size captured (default 8 KiB).
	StackSize int
	// DisableStack skips stack capture.
	DisableStack bool
	// OnPanic is called after the panic is logged, for example to report it
	// to an error tracker.
	OnPanic func(c *Context, recovered any, stack []byte)
}

// Recovery converts panics into 500 errors: the panic is recovered, logged
// with its stack trace and request metadata, and a controlled response is
// rendered, so one request can never crash the server. http.ErrAbortHandler
// is re-panicked to preserve its standard meaning.
//
//go:noinline
func Recovery(cfgs ...RecoveryConfig) Middleware {
	var cfg RecoveryConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.StackSize <= 0 {
		cfg.StackSize = 8 << 10
	}
	return func(next Handler) Handler {
		return func(c *Context) (err error) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				var stack []byte
				if !cfg.DisableStack {
					stack = make([]byte, cfg.StackSize)
					stack = stack[:runtime.Stack(stack, false)]
				}
				attrs := []slog.Attr{
					slog.Any("panic", rec),
					slog.String("method", c.req.Method),
					slog.String("path", c.req.URL.Path),
					slog.String("route", c.RoutePattern()),
				}
				if stack != nil {
					attrs = append(attrs, slog.String("stack", string(stack)))
				}
				c.Logger().LogAttrs(c.Context(), slog.LevelError, "panic recovered", attrs...)
				if cfg.OnPanic != nil {
					cfg.OnPanic(c, rec, stack)
				}
				perr, ok := rec.(error)
				if !ok {
					perr = fmt.Errorf("%v", rec)
				}
				err = Internal(CodeInternal, "Internal server error").
					Wrap(fmt.Errorf("panic: %w", perr)).
					WithMeta("panic", fmt.Sprint(rec))
			}()
			return next(c)
		}
	}
}

// ---- Access log ----

// AccessLogConfig configures request logging.
type AccessLogConfig struct {
	// SkipPaths are exact paths that are not logged. When nil, the health
	// endpoints are skipped.
	SkipPaths []string
	// Skip decides per request whether to skip logging.
	Skip func(c *Context) bool
	// Attrs adds custom attributes.
	Attrs func(c *Context) []slog.Attr
}

// Logger logs one structured record per request after it completes, with
// method, route, path, status, duration, size, client IP, user ID and error.
// request_id and trace_id are added from the request context. Server errors
// log at Error, client errors at Warn, everything else at Info.
//
//go:noinline
func Logger(cfgs ...AccessLogConfig) Middleware {
	var cfg AccessLogConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	return func(next Handler) Handler {
		return func(c *Context) error {
			if c.accessLogged || slices.Contains(cfg.SkipPaths, c.req.URL.Path) {
				return next(c)
			}
			c.accessLogged = true
			start := time.Now()
			err := next(c)
			if cfg.Skip != nil && cfg.Skip(c) {
				return err
			}
			status := c.res.Status()
			if status == 0 {
				status = StatusOf(err)
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}
			// The raw request context: the request ID is on c, and reading
			// c.Context() would attach pending values nothing else needs.
			ctx := c.req.Context()
			h := slog.Default().Handler()
			if c.app != nil {
				h = c.app.accessHandler
			}
			// Check the level before building anything, so filtered access
			// logs cost nothing.
			if !h.Enabled(ctx, level) {
				return err
			}
			end := time.Now()
			attrs := make([]slog.Attr, 0, 14)
			// Correlation attributes are added here, in the same batch, rather
			// than by the correlation handler, so the record grows only once.
			if id := c.requestID[0]; id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}
			if id := correlation.TraceID(ctx); id != "" {
				attrs = append(attrs, slog.String("trace_id", id))
			}
			attrs = append(attrs,
				slog.String("method", c.req.Method),
				slog.String("route", c.RoutePattern()),
				slog.String("path", c.req.URL.Path),
				slog.Int("status", status),
				slog.Duration("duration", end.Sub(start)),
				slog.Int64("bytes", c.res.Size()),
				slog.String("ip", c.RealIP()),
			)
			if u := c.User(); u != nil {
				attrs = append(attrs, slog.String("user_id", u.ID()))
			}
			if err != nil {
				attrs = append(attrs, slog.String("error", err.Error()))
				if e := AsError(err); e.Code != "" {
					attrs = append(attrs, slog.String("error_code", e.Code))
				}
			}
			if cfg.Attrs != nil {
				attrs = append(attrs, cfg.Attrs(c)...)
			}
			// Build the record directly: slog.Logger would capture the
			// caller's program counter, which access logs never need.
			r := slog.NewRecord(end, level, "request", 0)
			r.AddAttrs(attrs...)
			_ = h.Handle(ctx, r)
			return err
		}
	}
}

// ---- Security headers & host validation ----

// SecurityConfig configures security headers and host validation.
type SecurityConfig struct {
	// Headers adds or overrides response headers. A header mapped to "" is
	// removed from the defaults. Defaults:
	//   X-Content-Type-Options: nosniff
	//   X-Frame-Options: DENY
	//   Referrer-Policy: strict-origin-when-cross-origin
	Headers map[string]string
	// ContentSecurityPolicy sets Content-Security-Policy when non-empty.
	ContentSecurityPolicy string
	// HSTSMaxAge enables Strict-Transport-Security on HTTPS requests.
	HSTSMaxAge time.Duration
	// HSTSIncludeSubdomains adds includeSubDomains to HSTS.
	HSTSIncludeSubdomains bool
	// AllowedHosts restricts accepted Host headers. Entries may be exact
	// hosts or "*.example.com" wildcards. Empty allows any host.
	AllowedHosts []string
}

// SecurityHeaders sets security headers on every response and rejects
// requests for hosts outside AllowedHosts with 400.
//
//go:noinline
func SecurityHeaders(cfgs ...SecurityConfig) Middleware {
	var cfg SecurityConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	if cfg.ContentSecurityPolicy != "" {
		headers["Content-Security-Policy"] = cfg.ContentSecurityPolicy
	}
	maps.Copy(headers, cfg.Headers)
	type pair struct {
		key   string
		value []string
	}
	var pairs []pair
	for k, v := range headers {
		if v != "" {
			// Full slice expression: appending never mutates the shared value.
			pairs = append(pairs, pair{http.CanonicalHeaderKey(k), []string{v}[:1:1]})
		}
	}
	var hsts []string
	if cfg.HSTSMaxAge > 0 {
		v := "max-age=" + strconv.Itoa(int(cfg.HSTSMaxAge.Seconds()))
		if cfg.HSTSIncludeSubdomains {
			v += "; includeSubDomains"
		}
		hsts = []string{v}[:1:1]
	}
	hosts := newHostMatcher(cfg.AllowedHosts)
	return func(next Handler) Handler {
		return func(c *Context) error {
			if hosts != nil && !hosts.match(c.req.Host) {
				return BadRequest(CodeInvalidHost, "Invalid host")
			}
			h := c.res.Header()
			for _, p := range pairs {
				h[p.key] = p.value
			}
			if hsts != nil && c.Scheme() == "https" {
				h["Strict-Transport-Security"] = hsts
			}
			return next(c)
		}
	}
}

type hostMatcher struct {
	exact    map[string]bool
	suffixes []string
}

func newHostMatcher(hosts []string) *hostMatcher {
	if len(hosts) == 0 {
		return nil
	}
	m := &hostMatcher{exact: make(map[string]bool)}
	for _, h := range hosts {
		h = strings.ToLower(h)
		if strings.HasPrefix(h, "*.") {
			m.suffixes = append(m.suffixes, h[1:])
		} else {
			m.exact[h] = true
		}
	}
	return m
}

func (m *hostMatcher) match(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if m.exact[host] {
		return true
	}
	for _, s := range m.suffixes {
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return true
		}
	}
	return false
}

var requestIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newRequestID returns a 128-bit random ID as 26 base32 characters. Request
// IDs correlate logs; they are not secrets, so the runtime's per-thread
// ChaCha8 generator (seeded from the OS) is used instead of crypto/rand,
// which costs a system call per request.
func newRequestID() string {
	var b [16]byte
	x, y := rand.Uint64(), rand.Uint64()
	for i := range 8 {
		b[i], b[8+i] = byte(x>>(8*i)), byte(y>>(8*i))
	}
	return requestIDEncoding.EncodeToString(b[:])
}
