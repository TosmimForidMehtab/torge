// Package torgetest provides fast, deterministic, in-process testing for Torge
// applications: test apps, request builders, response assertions, middleware
// testing and real test servers.
//
//	func TestCreateUser(t *testing.T) {
//	    app := torgetest.NewApp(t)
//	    app.Register(users.Module{})
//	    app.Replace(func() users.Store { return fakeStore{} })
//
//	    tc := torgetest.New(t, app)
//	    tc.POST("/users").JSON(map[string]any{"name": "Ada"}).Do().
//	        ExpectStatus(201).
//	        ExpectJSONPath("name", "Ada")
//	}
package torgetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

// NewApp returns an App configured for tests (see NewAppOptions).
// Additional options override the test defaults.
func NewApp(t testing.TB, opts ...torge.Option) *torge.App {
	t.Helper()
	return torge.New(append(NewAppOptions(t), opts...)...)
}

// NewAppOptions returns the options NewApp uses: the Test environment,
// internal error details in responses, and a logger writing to t.Log so
// output appears only for failing tests. Pass them to application
// constructors that build their own App.
func NewAppOptions(t testing.TB) []torge.Option {
	return []torge.Option{
		torge.WithEnv(torge.Test),
		torge.WithExposeErrors(true),
		torge.WithLogger(slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// Start starts app if it has not started yet and registers a cleanup that
// shuts it down when the test ends.
func Start(t testing.TB, app *torge.App) {
	t.Helper()
	if app.State() != torge.StateBuilding {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("torgetest: app failed to start:\n%v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			t.Errorf("torgetest: shutdown: %v", err)
		}
	})
}

// Client sends requests to an application in-process, without a network.
type Client struct {
	t       testing.TB
	handler http.Handler
	header  http.Header
}

// New starts app (see Start) and returns a client for it.
func New(t testing.TB, app *torge.App) *Client {
	t.Helper()
	Start(t, app)
	return &Client{t: t, handler: app, header: make(http.Header)}
}

// ForHandler returns a client for any http.Handler.
func ForHandler(t testing.TB, h http.Handler) *Client {
	return &Client{t: t, handler: h, header: make(http.Header)}
}

// WithHeader returns a copy of the client that sends header on every request.
func (c *Client) WithHeader(key, value string) *Client {
	cp := *c
	cp.header = c.header.Clone()
	cp.header.Set(key, value)
	return &cp
}

// WithBearerToken returns a copy of the client that authenticates every
// request with a bearer token.
func (c *Client) WithBearerToken(token string) *Client {
	return c.WithHeader("Authorization", "Bearer "+token)
}

// Request starts building a request.
func (c *Client) Request(method, path string) *Request {
	return &Request{c: c, method: method, path: path, header: c.header.Clone(), query: url.Values{}}
}

// GET starts a GET request.
func (c *Client) GET(path string) *Request { return c.Request(http.MethodGet, path) }

// POST starts a POST request.
func (c *Client) POST(path string) *Request { return c.Request(http.MethodPost, path) }

// PUT starts a PUT request.
func (c *Client) PUT(path string) *Request { return c.Request(http.MethodPut, path) }

// PATCH starts a PATCH request.
func (c *Client) PATCH(path string) *Request { return c.Request(http.MethodPatch, path) }

// DELETE starts a DELETE request.
func (c *Client) DELETE(path string) *Request { return c.Request(http.MethodDelete, path) }

// HEAD starts a HEAD request.
func (c *Client) HEAD(path string) *Request { return c.Request(http.MethodHead, path) }

// OPTIONS starts an OPTIONS request.
func (c *Client) OPTIONS(path string) *Request { return c.Request(http.MethodOptions, path) }

// Request is a request under construction.
type Request struct {
	c          *Client
	method     string
	path       string
	header     http.Header
	query      url.Values
	body       io.Reader
	remoteAddr string
	ctx        context.Context
}

// Header sets a request header.
func (r *Request) Header(key, value string) *Request { r.header.Set(key, value); return r }

// Query adds a query parameter.
func (r *Request) Query(key, value string) *Request { r.query.Add(key, value); return r }

// Cookie adds a cookie.
func (r *Request) Cookie(ck *http.Cookie) *Request {
	r.header.Add("Cookie", ck.String())
	return r
}

// BearerToken sets an Authorization bearer token.
func (r *Request) BearerToken(token string) *Request {
	return r.Header("Authorization", "Bearer "+token)
}

// BasicAuth sets HTTP Basic credentials.
func (r *Request) BasicAuth(username, password string) *Request {
	req := http.Request{Header: http.Header{}}
	req.SetBasicAuth(username, password)
	return r.Header("Authorization", req.Header.Get("Authorization"))
}

// RemoteAddr sets the client address (default 192.0.2.1:1234).
func (r *Request) RemoteAddr(addr string) *Request { r.remoteAddr = addr; return r }

// WithContext sets the request context, for example to test cancellation.
func (r *Request) WithContext(ctx context.Context) *Request { r.ctx = ctx; return r }

// JSON encodes v as the request body and sets Content-Type.
func (r *Request) JSON(v any) *Request {
	r.c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		r.c.t.Fatalf("torgetest: encode JSON body: %v", err)
	}
	r.body = bytes.NewReader(b)
	r.header.Set("Content-Type", "application/json")
	return r
}

// Body sets a raw body and content type.
func (r *Request) Body(body io.Reader, contentType string) *Request {
	r.body = body
	if contentType != "" {
		r.header.Set("Content-Type", contentType)
	}
	return r
}

// Text sets a plain-text body.
func (r *Request) Text(s string) *Request {
	return r.Body(strings.NewReader(s), "text/plain; charset=utf-8")
}

// Form sets a URL-encoded form body.
func (r *Request) Form(values url.Values) *Request {
	return r.Body(strings.NewReader(values.Encode()), "application/x-www-form-urlencoded")
}

// Build returns the *http.Request.
func (r *Request) Build() *http.Request {
	target := r.path
	if len(r.query) > 0 {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + r.query.Encode()
	}
	req := httptest.NewRequest(r.method, target, r.body)
	if r.ctx != nil {
		req = req.WithContext(r.ctx)
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	if r.remoteAddr != "" {
		req.RemoteAddr = r.remoteAddr
	}
	return req
}

// Do sends the request and returns the recorded response.
func (r *Request) Do() *Response {
	r.c.t.Helper()
	rec := httptest.NewRecorder()
	r.c.handler.ServeHTTP(rec, r.Build())
	return newResponse(r.c.t, rec)
}

func newResponse(t testing.TB, rec *httptest.ResponseRecorder) *Response {
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return &Response{t: t, Status: res.StatusCode, Header: res.Header, Body: body, Result: res}
}

// Response is a recorded response with assertion helpers. Assertions report
// failures with t.Errorf and return the response for chaining.
type Response struct {
	t      testing.TB
	Status int
	Header http.Header
	Body   []byte
	Result *http.Response
}

func (r *Response) String() string {
	body := string(r.Body)
	if len(body) > 2048 {
		body = body[:2048] + "..."
	}
	return fmt.Sprintf("HTTP %d\n%s", r.Status, body)
}

// ExpectStatus asserts the status code.
func (r *Response) ExpectStatus(code int) *Response {
	r.t.Helper()
	if r.Status != code {
		r.t.Errorf("expected status %d, got %d\nbody: %s", code, r.Status, r.Body)
	}
	return r
}

// ExpectHeader asserts a response header value.
func (r *Response) ExpectHeader(key, value string) *Response {
	r.t.Helper()
	if got := r.Header.Get(key); got != value {
		r.t.Errorf("expected header %s=%q, got %q", key, value, got)
	}
	return r
}

// ExpectHeaderPresent asserts that a response header is set.
func (r *Response) ExpectHeaderPresent(key string) *Response {
	r.t.Helper()
	if r.Header.Get(key) == "" {
		r.t.Errorf("expected header %s to be present", key)
	}
	return r
}

// ExpectBody asserts the exact body.
func (r *Response) ExpectBody(body string) *Response {
	r.t.Helper()
	if string(r.Body) != body {
		r.t.Errorf("expected body %q, got %q", body, r.Body)
	}
	return r
}

// ExpectBodyContains asserts that the body contains s.
func (r *Response) ExpectBodyContains(s string) *Response {
	r.t.Helper()
	if !strings.Contains(string(r.Body), s) {
		r.t.Errorf("expected body to contain %q, got %s", s, r.Body)
	}
	return r
}

// ExpectJSON asserts that the body is JSON equal to expected (compared after
// normalizing both through encoding/json, so field order does not matter).
func (r *Response) ExpectJSON(expected any) *Response {
	r.t.Helper()
	var want, got any
	wantBytes, err := json.Marshal(expected)
	if err != nil {
		r.t.Fatalf("torgetest: encode expected JSON: %v", err)
	}
	_ = json.Unmarshal(wantBytes, &want)
	if err := json.Unmarshal(r.Body, &got); err != nil {
		r.t.Errorf("response is not JSON: %v\nbody: %s", err, r.Body)
		return r
	}
	if !reflect.DeepEqual(want, got) {
		r.t.Errorf("JSON mismatch\nexpected: %s\ngot:      %s", wantBytes, r.Body)
	}
	return r
}

// ExpectJSONPath asserts the value at a dot-separated path (for example
// "error.code" or "items.0.id") in the JSON body.
func (r *Response) ExpectJSONPath(path string, expected any) *Response {
	r.t.Helper()
	got, ok := r.JSONPath(path)
	if !ok {
		r.t.Errorf("JSON path %q not found in %s", path, r.Body)
		return r
	}
	var want any
	b, _ := json.Marshal(expected)
	_ = json.Unmarshal(b, &want)
	if !reflect.DeepEqual(want, got) {
		r.t.Errorf("JSON path %q: expected %v, got %v", path, want, got)
	}
	return r
}

// JSONPath returns the value at a dot-separated path in the JSON body.
func (r *Response) JSONPath(path string) (any, bool) {
	var cur any
	if err := json.Unmarshal(r.Body, &cur); err != nil {
		return nil, false
	}
	for part := range strings.SplitSeq(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			var i int
			if _, err := fmt.Sscanf(part, "%d", &i); err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// ExpectErrorCode asserts the machine-readable code of a Torge error response.
func (r *Response) ExpectErrorCode(code string) *Response {
	r.t.Helper()
	return r.ExpectJSONPath("error.code", code)
}

// DecodeJSON decodes the body into v, failing the test on error.
func (r *Response) DecodeJSON(v any) *Response {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decode JSON response: %v\nbody: %s", err, r.Body)
	}
	return r
}

// Decode decodes a JSON response body into a T.
func Decode[T any](r *Response) T {
	r.t.Helper()
	var v T
	r.DecodeJSON(&v)
	return v
}

// Server starts app and serves it on a real loopback listener, for tests that
// need a network (HTTP clients, WebSockets). The server is closed at the end
// of the test.
func Server(t testing.TB, app *torge.App) *httptest.Server {
	t.Helper()
	Start(t, app)
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

// Handle runs h (for example a middleware chain built with torge.Chain) for
// req with a Context attached to app, without routing. It is the simplest way
// to unit-test middleware.
func Handle(t testing.TB, app *torge.App, h torge.Handler, req *http.Request) (*Response, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	c := app.NewContext(rec, req)
	err := h(c)
	if err != nil {
		c.HandleError(err)
	}
	return newResponse(t, rec), err
}
