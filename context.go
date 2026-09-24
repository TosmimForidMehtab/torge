package torge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
)

// Context is the per-request context passed to handlers and middleware.
//
// # Lifetime
//
// A Context is created for exactly one request and is never pooled or reused.
// It is valid only until the handler chain returns. Do not retain it, and do
// not use it from goroutines that outlive the request; pass c.Context() and
// copies of the values you need instead. Like http.ResponseWriter, a Context
// is not safe for concurrent use.
//
// # Cancellation
//
// c.Context() is the request's context.Context. It is canceled when the client
// disconnects or the server shuts down, and carries any deadline set by
// middleware such as Timeout. Pass it to database and service calls.
type Context struct {
	req    *http.Request
	res    *ResponseWriter
	resBuf ResponseWriter
	app    *App
	rt     *route
	vals   []string
	// valsBuf backs vals for routes with few parameters, avoiding an
	// allocation per request.
	valsBuf [4]string

	requestID string
	query     url.Values
	realIP    string

	// x holds rarely used state, allocated on first use, which keeps the
	// per-request allocation small.
	x *contextExtras

	accessLogged bool
	handled      bool
}

type contextExtras struct {
	user      Principal
	logger    *slog.Logger
	loggerCtx context.Context
	values    map[any]any
	scoped    map[reflect.Type]reflect.Value
	cleanups  []func()
}

func (c *Context) extras() *contextExtras {
	if c.x == nil {
		c.x = &contextExtras{}
	}
	return c.x
}

// Principal is an authenticated identity. Applications define their own
// principal types; auth.User is a ready-made implementation.
type Principal interface {
	// ID returns a stable identifier for the principal, for logs and
	// per-user limits.
	ID() string
}

// NewContext creates a Context for w and r outside the normal request
// pipeline. It is intended for tests and adapters.
func (a *App) NewContext(w http.ResponseWriter, r *http.Request) *Context {
	return &Context{req: r, res: newResponseWriter(w), app: a}
}

// Request returns the current *http.Request.
func (c *Context) Request() *http.Request { return c.req }

// SetRequest replaces the request, for middleware that needs to modify it.
func (c *Context) SetRequest(r *http.Request) { c.req = r }

// Response returns the response writer.
func (c *Context) Response() *ResponseWriter { return c.res }

// swapWriter makes c write through w (which usually wraps the current writer)
// and returns a function restoring the previous writer.
func (c *Context) swapWriter(w http.ResponseWriter) (restore func()) {
	prev := c.res
	if rw, ok := w.(*ResponseWriter); ok {
		c.res = rw
	} else {
		c.res = newResponseWriter(w)
	}
	return func() { c.res = prev }
}

// SetResponseWriter makes subsequent writes go through w, which should wrap
// the current Response(). It returns a function that restores the previous
// writer; call it before the middleware returns. Compression and response
// capture middleware use it.
func (c *Context) SetResponseWriter(w http.ResponseWriter) (restore func()) {
	return c.swapWriter(w)
}

// Context returns the request's context.Context.
func (c *Context) Context() context.Context { return c.req.Context() }

// SetContext replaces the request's context. Middleware uses it to attach
// deadlines or values; ctx must derive from c.Context().
func (c *Context) SetContext(ctx context.Context) { c.req = c.req.WithContext(ctx) }

// Method returns the request method.
func (c *Context) Method() string { return c.req.Method }

// Path returns the request URL path.
func (c *Context) Path() string { return c.req.URL.Path }

// RoutePattern returns the matched route pattern such as "/users/:id", or ""
// when no route matched.
func (c *Context) RoutePattern() string {
	if c.rt == nil {
		return ""
	}
	return c.rt.path
}

// RouteName returns the matched route's name, or "".
func (c *Context) RouteName() string {
	if c.rt == nil || c.rt.cfg == nil {
		return ""
	}
	return c.rt.cfg.name
}

// Param returns the value of a path parameter, or "".
func (c *Context) Param(name string) string {
	if c.rt == nil {
		return ""
	}
	for i, n := range c.rt.paramNames {
		if n == name {
			return c.vals[i]
		}
	}
	return ""
}

// Param is a path parameter.
type Param struct {
	Name  string
	Value string
}

// Params returns all path parameters in pattern order.
func (c *Context) Params() []Param {
	if c.rt == nil {
		return nil
	}
	ps := make([]Param, len(c.rt.paramNames))
	for i, n := range c.rt.paramNames {
		ps[i] = Param{Name: n, Value: c.vals[i]}
	}
	return ps
}

// Query returns the first value of a query parameter, or "".
func (c *Context) Query(name string) string { return c.QueryValues().Get(name) }

// QueryDefault returns a query parameter or def if absent or empty.
func (c *Context) QueryDefault(name, def string) string {
	if v := c.Query(name); v != "" {
		return v
	}
	return def
}

// QueryInt parses a query parameter as an int, returning def when absent. A
// malformed value yields a 400 error.
func (c *Context) QueryInt(name string, def int) (int, error) {
	v := c.Query(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, BadRequest(CodeBadRequest, fmt.Sprintf("query parameter %q must be an integer", name))
	}
	return n, nil
}

// QueryValues returns the parsed query string. The result is cached and must
// not be modified.
func (c *Context) QueryValues() url.Values {
	if c.query == nil {
		c.query = c.req.URL.Query()
	}
	return c.query
}

// GetHeader returns a request header.
func (c *Context) GetHeader(name string) string { return c.req.Header.Get(name) }

// Header sets a response header.
func (c *Context) Header(name, value string) { c.res.Header().Set(name, value) }

// Cookie returns a request cookie.
func (c *Context) Cookie(name string) (*http.Cookie, error) { return c.req.Cookie(name) }

// SetCookie adds a Set-Cookie response header.
func (c *Context) SetCookie(cookie *http.Cookie) { http.SetCookie(c.res, cookie) }

// Body returns the request body. It is subject to the route's body limit.
func (c *Context) Body() io.ReadCloser { return c.req.Body }

// ReadBody reads the whole request body, subject to the body limit.
func (c *Context) ReadBody() ([]byte, error) {
	if c.req.Body == nil {
		return nil, nil
	}
	return io.ReadAll(c.req.Body)
}

// RequestID returns the request ID, or "" if request IDs are disabled.
func (c *Context) RequestID() string { return c.requestID }

// User returns the authenticated principal, or nil.
func (c *Context) User() Principal {
	if c.x == nil {
		return nil
	}
	return c.x.user
}

// SetUser records the authenticated principal. Authentication middleware
// calls it; the principal is also attached to c.Context() (see UserFrom).
func (c *Context) SetUser(p Principal) {
	c.extras().user = p
	c.SetContext(context.WithValue(c.Context(), userKey{}, p))
}

type userKey struct{}

// UserFrom returns the principal attached to ctx by SetUser, or nil. Use it in
// services that receive only a context.Context.
func UserFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(userKey{}).(Principal)
	return p
}

// UserAs returns the principal as type T.
func UserAs[T Principal](c *Context) (T, bool) {
	p, ok := c.User().(T)
	return p, ok
}

// Logger returns the application logger bound to this request, so records
// carry request_id and trace_id even when logged without a context.
func (c *Context) Logger() *slog.Logger {
	if c.app == nil {
		return slog.Default()
	}
	ctx, x := c.Context(), c.extras()
	if x.logger == nil || x.loggerCtx != ctx {
		x.logger = slog.New(boundHandler{Handler: c.app.logger.Handler(), ctx: ctx})
		x.loggerCtx = ctx
	}
	return x.logger
}

// boundHandler substitutes a request context when a record is logged without
// one, so context-aware handlers can add correlation attributes.
type boundHandler struct {
	slog.Handler
	ctx context.Context
}

func (h boundHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil || ctx == context.Background() {
		ctx = h.ctx
	}
	return h.Handler.Handle(ctx, r)
}

func (h boundHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return boundHandler{Handler: h.Handler.WithAttrs(attrs), ctx: h.ctx}
}

func (h boundHandler) WithGroup(name string) slog.Handler {
	return boundHandler{Handler: h.Handler.WithGroup(name), ctx: h.ctx}
}

// Env returns the application environment.
func (c *Context) Env() Env {
	if c.app == nil {
		return Production
	}
	return c.app.opts.Env
}

// Set stores a request-scoped value for later middleware or the handler. Use
// an unexported key type to avoid collisions.
func (c *Context) Set(key, value any) {
	x := c.extras()
	if x.values == nil {
		x.values = make(map[any]any)
	}
	x.values[key] = value
}

// Get returns a value stored with Set.
func (c *Context) Get(key any) (any, bool) {
	if c.x == nil {
		return nil, false
	}
	v, ok := c.x.values[key]
	return v, ok
}

// OnDone registers fn to run after the handler chain has finished and the
// response was written. Hooks run in reverse registration order.
func (c *Context) OnDone(fn func()) {
	x := c.extras()
	x.cleanups = append(x.cleanups, fn)
}

func (c *Context) finish() {
	if c.x == nil {
		return
	}
	for i := len(c.x.cleanups) - 1; i >= 0; i-- {
		c.x.cleanups[i]()
	}
}

// HandleError renders err immediately with the application's error handler,
// unless the response was already written. Most code simply returns errors;
// middleware that must finalize the response itself can call this.
func (c *Context) HandleError(err error) {
	if err == nil || c.handled {
		return
	}
	if c.res.Written() {
		c.handled = true
		if c.app != nil {
			c.Logger().LogAttrs(c.Context(), slog.LevelDebug, "error after response was written",
				slog.String("error", err.Error()))
		}
		return
	}
	c.handled = true
	h := DefaultErrorHandler
	if c.app != nil && c.app.opts.ErrorHandler != nil {
		h = c.app.opts.ErrorHandler
	}
	h(c, err)
}

// ---- Response helpers ----

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

const maxPooledBuffer = 64 << 10

func getBuffer() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putBuffer(b *bytes.Buffer) {
	if b.Cap() <= maxPooledBuffer {
		bufPool.Put(b)
	}
}

func (c *Context) serializer() Serializer {
	if c.app != nil && c.app.opts.Serializer != nil {
		return c.app.opts.Serializer
	}
	return JSONSerializer{}
}

// JSON encodes v with the application's serializer (JSON by default) and
// writes it with the given status. Encoding happens before headers are sent,
// so an encoding failure results in a clean error response.
func (c *Context) JSON(status int, v any) error {
	if c.res.Written() {
		return errAlreadyWritten
	}
	s := c.serializer()
	buf := getBuffer()
	defer putBuffer(buf)
	if err := s.Encode(buf, v); err != nil {
		return fmt.Errorf("torge: encode response: %w", err)
	}
	return c.Bytes(status, s.ContentType(), buf.Bytes())
}

// String writes a plain-text response.
func (c *Context) String(status int, s string) error {
	return c.writeBody(status, "text/plain; charset=utf-8", nil, s)
}

// HTML writes an HTML response. The caller is responsible for escaping.
func (c *Context) HTML(status int, html string) error {
	return c.writeBody(status, "text/html; charset=utf-8", nil, html)
}

// Bytes writes b with the given status and content type.
func (c *Context) Bytes(status int, contentType string, b []byte) error {
	return c.writeBody(status, contentType, b, "")
}

func (c *Context) writeBody(status int, contentType string, b []byte, s string) error {
	if c.res.Written() {
		return errAlreadyWritten
	}
	h := c.res.Header()
	if v := sharedContentType(contentType); v != nil {
		h["Content-Type"] = v
	} else if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	n := len(b)
	if b == nil {
		n = len(s)
	}
	// net/http sets Content-Length itself for bodies that fit its write
	// buffer, so it is only set explicitly for larger bodies (avoiding
	// chunked encoding) and for HEAD, which writes no body.
	if n > autoContentLength || c.req.Method == http.MethodHead {
		h.Set("Content-Length", strconv.Itoa(n))
	}
	c.res.WriteHeader(status)
	if !bodyAllowed(status) || c.req.Method == http.MethodHead {
		return nil
	}
	var err error
	if b != nil {
		_, err = c.res.Write(b)
	} else {
		_, err = c.res.WriteString(s)
	}
	return err
}

// autoContentLength is net/http's buffer size below which the server adds
// Content-Length automatically.
const autoContentLength = 2048

// Header values for the common content types, shared across requests to
// avoid an allocation per response. Their capacity equals their length, so
// Header.Add copies instead of modifying them, and Header.Set replaces them.
var (
	jsonContentType = []string{"application/json; charset=utf-8"}
	textContentType = []string{"text/plain; charset=utf-8"}
	htmlContentType = []string{"text/html; charset=utf-8"}
)

func sharedContentType(ct string) []string {
	switch ct {
	case jsonContentType[0]:
		return jsonContentType
	case textContentType[0]:
		return textContentType
	case htmlContentType[0]:
		return htmlContentType
	}
	return nil
}

func bodyAllowed(status int) bool {
	return status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

// Status writes only a status line and headers, with no body.
func (c *Context) Status(code int) error {
	if c.res.Written() {
		return errAlreadyWritten
	}
	c.res.WriteHeader(code)
	return nil
}

// NoContent writes a response without a body, typically 204.
func (c *Context) NoContent(code int) error { return c.Status(code) }

// StatusCode returns the status written so far, or 0.
func (c *Context) StatusCode() int { return c.res.Status() }

// Redirect replies with a redirect to url. code must be a 3xx status. Never
// redirect to unvalidated user input.
func (c *Context) Redirect(code int, url string) error {
	if code < 300 || code > 399 {
		return fmt.Errorf("torge: invalid redirect status %d", code)
	}
	http.Redirect(c.res, c.req, url, code)
	return nil
}

// File serves a file from the local filesystem, handling Range and
// conditional requests.
func (c *Context) File(path string) error {
	http.ServeFile(c.res, c.req, path)
	return nil
}

// FileFS serves name from fsys.
func (c *Context) FileFS(fsys fs.FS, name string) error {
	http.ServeFileFS(c.res, c.req, fsys, name)
	return nil
}

// Attachment serves a local file as a download named filename.
func (c *Context) Attachment(path, filename string) error {
	if filename == "" {
		filename = filepath.Base(path)
	}
	c.res.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	return c.File(path)
}
