package torge

import (
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
)

// Handler handles a request. Returning a non-nil error hands it to the error
// handler, which renders a response unless one was already written.
type Handler func(c *Context) error

// Middleware wraps a Handler. Code before calling next runs on the way in,
// code after it runs on the way out; not calling next short-circuits the
// request.
//
//	func Timing(next torge.Handler) torge.Handler {
//	    return func(c *torge.Context) error {
//	        start := time.Now()
//	        err := next(c)
//	        c.Logger().Info("took", "duration", time.Since(start))
//	        return err
//	    }
//	}
//
// A Middleware can be passed directly as a route or group option.
type Middleware func(next Handler) Handler

// applyRoute lets a Middleware be used as a RouteOption.
func (m Middleware) applyRoute(r *routeConfig) { r.middleware = append(r.middleware, m) }

// Chain composes middleware so that mws[0] is the outermost.
func Chain(h Handler, mws ...Middleware) Handler {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

// WrapHandler adapts a standard http.Handler. Use it to mount existing
// net/http components without rewriting them.
func WrapHandler(h http.Handler) Handler {
	return func(c *Context) error {
		h.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}

// WrapFunc adapts a standard http.HandlerFunc.
func WrapFunc(f http.HandlerFunc) Handler { return WrapHandler(f) }

// WrapMiddleware adapts standard func(http.Handler) http.Handler middleware.
// Request and response-writer changes made by the standard middleware are
// visible to the rest of the chain, and errors returned by the rest of the
// chain propagate back normally.
func WrapMiddleware(m func(http.Handler) http.Handler) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) error {
			var err error
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c.req = r
				restore := c.swapWriter(w)
				defer restore()
				err = next(c)
				if err != nil && !c.Response().Written() {
					// Render while the standard middleware's writer (for
					// example a compressor) is still in place.
					c.HandleError(err)
				}
			})
			m(inner).ServeHTTP(c.Response(), c.req)
			return err
		}
	}
}

// funcName returns a readable name for a function value, used by route
// introspection.
func funcName(fn any) string {
	v := reflect.ValueOf(fn)
	if !v.IsValid() || v.Kind() != reflect.Func || v.IsNil() {
		return ""
	}
	rf := runtime.FuncForPC(v.Pointer())
	if rf == nil {
		return ""
	}
	name := rf.Name()
	// Strip closure and method-value suffixes: pkg.Factory.func1 -> pkg.Factory.
	closure := false
	for {
		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			break
		}
		last := name[i+1:]
		if strings.HasPrefix(last, "func") || isDigits(last) {
			name, closure = name[:i], true
			continue
		}
		break
	}
	name = strings.TrimSuffix(name, "-fm")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	// A closure created by an inlined factory is named after the caller
	// (callerpkg.Caller.Factory), so its package is unknown; report just the
	// factory name.
	if parts := strings.Split(name, "."); closure && len(parts) > 2 && !strings.HasPrefix(parts[len(parts)-2], "(") {
		name = parts[len(parts)-1]
	}
	return name
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
