// Package httpclient provides an HTTP client for calling external services
// with production defaults: timeouts, connection pooling, conservative
// retries with exponential backoff, request ID propagation, structured errors
// and optional logging.
//
// The client is a plain *http.Client underneath; HTTP returns it for SDKs that
// accept one, and every feature is implemented as an http.RoundTripper layer,
// so tracing (for example otelhttp from contrib/otel) plugs in with
// Config.Middleware.
//
// Retries are conservative: only idempotent methods (GET, HEAD, OPTIONS,
// TRACE, PUT, DELETE) and requests carrying an Idempotency-Key header are
// retried, only on network errors and 429/502/503/504, and only when the body
// can be replayed.
package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge/correlation"
)

// RetryPolicy configures retries.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first
	// (default 3). Set to 1 to disable retries.
	MaxAttempts int
	// InitialBackoff is the delay before the first retry (default 100ms).
	InitialBackoff time.Duration
	// MaxBackoff caps the delay between attempts (default 2s). A Retry-After
	// longer than this ends retrying and returns the response.
	MaxBackoff time.Duration
	// RetryStatuses are retried status codes (default 429, 502, 503, 504).
	RetryStatuses []int
	// RetryNonIdempotent also retries POST and PATCH requests without an
	// Idempotency-Key. Enable only if the server deduplicates requests.
	RetryNonIdempotent bool
	// ShouldRetry, if set, replaces the status and error classification.
	// Method idempotency and body replayability are still enforced.
	ShouldRetry func(res *http.Response, err error) bool
}

// Config configures a Client.
type Config struct {
	// BaseURL is prepended to relative paths passed to NewRequest and the
	// JSON helpers.
	BaseURL string
	// Timeout bounds a whole call including retries (default 30s).
	Timeout time.Duration
	// Retry configures retries.
	Retry RetryPolicy
	// Transport is the innermost transport. The default is a pooled
	// transport tuned for service-to-service calls.
	Transport http.RoundTripper
	// Middleware wraps the transport for every attempt; the first entry is
	// outermost. Use it for tracing and metrics.
	Middleware []func(http.RoundTripper) http.RoundTripper
	// Logger logs failed attempts at Warn and every attempt at Debug. Nil
	// disables logging.
	Logger *slog.Logger
	// RequestIDHeader propagates the request ID from the context (default
	// X-Request-ID). Set to "-" to disable.
	RequestIDHeader string
	// UserAgent sets the User-Agent header when requests have none.
	UserAgent string
	// Header is added to every request that does not set the same header.
	Header http.Header
	// MaxErrorBody bounds the response body captured in StatusError
	// (default 4 KiB).
	MaxErrorBody int64
}

// Client calls external HTTP services.
type Client struct {
	http *http.Client
	cfg  Config
	base *url.URL
}

// New returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxErrorBody <= 0 {
		cfg.MaxErrorBody = 4 << 10
	}
	if cfg.RequestIDHeader == "" {
		cfg.RequestIDHeader = "X-Request-ID"
	}
	r := &cfg.Retry
	if r.MaxAttempts <= 0 {
		r.MaxAttempts = 3
	}
	if r.InitialBackoff <= 0 {
		r.InitialBackoff = 100 * time.Millisecond
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = 2 * time.Second
	}
	if r.RetryStatuses == nil {
		r.RetryStatuses = []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	}
	c := &Client{cfg: cfg}
	if cfg.BaseURL != "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("httpclient: invalid BaseURL %q", cfg.BaseURL)
		}
		c.base = u
	}
	var rt http.RoundTripper = cfg.Transport
	if rt == nil {
		rt = DefaultTransport()
	}
	for _, v := range slices.Backward(cfg.Middleware) {
		rt = v(rt)
	}
	if cfg.Logger != nil {
		rt = &loggingTransport{next: rt, log: cfg.Logger}
	}
	rt = &headerTransport{next: rt, cfg: &c.cfg}
	rt = &retryTransport{next: rt, policy: cfg.Retry, log: cfg.Logger}
	c.http = &http.Client{Transport: rt, Timeout: cfg.Timeout}
	return c, nil
}

// DefaultTransport returns a pooled transport with conservative timeouts.
func DefaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 30 * time.Second
	t.ExpectContinueTimeout = time.Second
	t.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return t
}

// HTTP returns the underlying *http.Client, with retries, request ID
// propagation and logging, for SDKs that accept an *http.Client.
func (c *Client) HTTP() *http.Client { return c.http }

// Do sends req. Non-2xx responses are returned as responses, not errors; use
// the JSON helpers or CheckResponse for structured errors.
func (c *Client) Do(req *http.Request) (*http.Response, error) { return c.http.Do(req) }

// NewRequest builds a request for path (resolved against BaseURL). A non-nil
// body is encoded as JSON; []byte and io.Reader bodies are sent as is. Bodies
// are buffered so they can be replayed on retries.
func (c *Client) NewRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	target := path
	if c.base != nil {
		ref, err := url.Parse(path)
		if err != nil {
			return nil, fmt.Errorf("httpclient: invalid path %q: %w", path, err)
		}
		target = c.base.ResolveReference(ref).String()
	}
	var rdr io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rdr = bytes.NewReader(b)
	case string:
		rdr = strings.NewReader(b)
	case io.Reader:
		data, err := io.ReadAll(b)
		if err != nil {
			return nil, fmt.Errorf("httpclient: read body: %w", err)
		}
		rdr = bytes.NewReader(data)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("httpclient: encode body: %w", err)
		}
		rdr = bytes.NewReader(data)
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// DoJSON sends in (if non-nil) as JSON and decodes a 2xx JSON response into
// out (if non-nil). Non-2xx responses return a *StatusError.
func (c *Client) DoJSON(ctx context.Context, method, path string, in, out any) error {
	req, err := c.NewRequest(ctx, method, path, in)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: %s %s: %w", method, redact(req.URL), err)
	}
	defer res.Body.Close()
	if err := c.CheckResponse(res); err != nil {
		return err
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("httpclient: decode %s %s response: %w", method, redact(req.URL), err)
	}
	return nil
}

// GetJSON issues a GET and decodes the JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, out)
}

// PostJSON issues a POST with a JSON body and decodes the response into out.
func (c *Client) PostJSON(ctx context.Context, path string, in, out any) error {
	return c.DoJSON(ctx, http.MethodPost, path, in, out)
}

// PutJSON issues a PUT with a JSON body and decodes the response into out.
func (c *Client) PutJSON(ctx context.Context, path string, in, out any) error {
	return c.DoJSON(ctx, http.MethodPut, path, in, out)
}

// DeleteJSON issues a DELETE and decodes the response into out.
func (c *Client) DeleteJSON(ctx context.Context, path string, out any) error {
	return c.DoJSON(ctx, http.MethodDelete, path, nil, out)
}

// StatusError is returned for non-2xx responses by the JSON helpers.
type StatusError struct {
	Method     string
	URL        string
	StatusCode int
	Header     http.Header
	// Body holds the beginning of the response body (bounded by
	// MaxErrorBody).
	Body []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("httpclient: %s %s: unexpected status %d", e.Method, e.URL, e.StatusCode)
}

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == code
}

// CheckResponse returns a *StatusError for non-2xx responses. The body is read
// (up to MaxErrorBody) but not closed.
func (c *Client) CheckResponse(res *http.Response) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, c.cfg.MaxErrorBody))
	return &StatusError{
		Method:     res.Request.Method,
		URL:        redact(res.Request.URL),
		StatusCode: res.StatusCode,
		Header:     res.Header,
		Body:       body,
	}
}

// redact removes credentials and the query string, which often carries API
// keys, from URLs placed in logs and errors.
func redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.Fragment = ""
	return c.String()
}

// ---- Transports ----

type headerTransport struct {
	next http.RoundTripper
	cfg  *Config
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var add http.Header
	if h := t.cfg.RequestIDHeader; h != "-" && req.Header.Get(h) == "" {
		if id := correlation.RequestID(req.Context()); id != "" {
			add = http.Header{h: {id}}
		}
	}
	if t.cfg.UserAgent != "" && req.Header.Get("User-Agent") == "" {
		if add == nil {
			add = http.Header{}
		}
		add.Set("User-Agent", t.cfg.UserAgent)
	}
	for k, v := range t.cfg.Header {
		if req.Header.Get(k) == "" {
			if add == nil {
				add = http.Header{}
			}
			add[k] = v
		}
	}
	if add != nil {
		// RoundTrippers must not modify the caller's request.
		req = req.Clone(req.Context())
		for k, v := range add {
			req.Header[http.CanonicalHeaderKey(k)] = v
		}
	}
	return t.next.RoundTrip(req)
}

type loggingTransport struct {
	next http.RoundTripper
	log  *slog.Logger
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := t.next.RoundTrip(req)
	attrs := []slog.Attr{
		slog.String("method", req.Method),
		slog.String("url", redact(req.URL)),
		slog.Duration("duration", time.Since(start)),
	}
	level := slog.LevelDebug
	if err != nil {
		level = slog.LevelWarn
		attrs = append(attrs, slog.String("error", err.Error()))
	} else {
		attrs = append(attrs, slog.Int("status", res.StatusCode))
		if res.StatusCode >= 500 {
			level = slog.LevelWarn
		}
	}
	t.log.LogAttrs(req.Context(), level, "outgoing request", attrs...)
	return res, err
}

type retryTransport struct {
	next   http.RoundTripper
	policy RetryPolicy
	log    *slog.Logger
}

var idempotentMethods = []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete}

func (t *retryTransport) canRetry(req *http.Request) bool {
	if t.policy.MaxAttempts <= 1 {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false // the body cannot be replayed
	}
	return slices.Contains(idempotentMethods, req.Method) ||
		req.Header.Get("Idempotency-Key") != "" || t.policy.RetryNonIdempotent
}

func (t *retryTransport) shouldRetry(req *http.Request, res *http.Response, err error) bool {
	if req.Context().Err() != nil {
		return false
	}
	if t.policy.ShouldRetry != nil {
		return t.policy.ShouldRetry(res, err)
	}
	if err != nil {
		var certErr *tls.CertificateVerificationError
		var unknownAuthority x509.UnknownAuthorityError
		var hostErr x509.HostnameError
		return !errors.As(err, &certErr) && !errors.As(err, &unknownAuthority) && !errors.As(err, &hostErr)
	}
	return slices.Contains(t.policy.RetryStatuses, res.StatusCode)
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.canRetry(req) {
		return t.next.RoundTrip(req)
	}
	attemptReq := req
	for attempt := 1; ; attempt++ {
		res, err := t.next.RoundTrip(attemptReq)
		if attempt >= t.policy.MaxAttempts || !t.shouldRetry(req, res, err) {
			return res, err
		}
		delay := t.backoff(attempt)
		if res != nil {
			if ra, ok := retryAfter(res.Header.Get("Retry-After")); ok {
				if ra > t.policy.MaxBackoff {
					return res, nil // the server asked for a longer pause than we wait
				}
				delay = max(delay, ra)
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
		}
		if t.log != nil {
			status := 0
			if res != nil {
				status = res.StatusCode
			}
			t.log.LogAttrs(req.Context(), slog.LevelDebug, "retrying outgoing request",
				slog.String("method", req.Method), slog.String("url", redact(req.URL)),
				slog.Int("attempt", attempt), slog.Int("status", status), slog.Duration("delay", delay))
		}
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
		attemptReq = req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("httpclient: replay body: %w", err)
			}
			attemptReq.Body = body
		}
	}
}

// backoff returns the full-jitter exponential delay for a retry.
func (t *retryTransport) backoff(attempt int) time.Duration {
	d := t.policy.InitialBackoff << (attempt - 1)
	if d <= 0 || d > t.policy.MaxBackoff {
		d = t.policy.MaxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}
