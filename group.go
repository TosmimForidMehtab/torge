package torge

import (
	"net/http"
	"strings"
)

// Group is a set of routes sharing a path prefix, middleware and options.
// Groups nest. The App itself embeds the root group, whose middleware is the
// application's global middleware.
//
// Group middleware applies to every route of the group and its subgroups,
// regardless of whether Use is called before or after the routes are
// registered; within a group, middleware runs in the order it was added.
type Group struct {
	app    *App
	prefix string
	parent *Group
	mws    []Middleware
	opts   []RouteOption
}

// Router is implemented by *App and *Group. Typed handler helpers such as Get
// and Post accept a Router.
type Router interface {
	Handle(method, path string, h Handler, opts ...RouteOption)
}

var _ Router = (*Group)(nil)

// Prefix returns the full path prefix of the group.
func (g *Group) Prefix() string { return g.prefix }

// Use appends middleware. On the App this is global middleware, which runs for
// every request before routing (including requests that match no route). On a
// Group it runs for the group's routes after routing.
func (g *Group) Use(mws ...Middleware) {
	a := g.app
	loc := callerLocation()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mustBeRegistering(loc, "middleware")
	for _, m := range mws {
		if m == nil {
			a.addDiagnostic(&Diagnostic{
				Code: DiagInvalidHandler, What: "nil middleware passed to Use", Where: loc,
				Why: "a nil middleware would panic on the first request",
				Fix: "remove the nil value or construct the middleware before registering it",
			})
			continue
		}
		g.mws = append(g.mws, m)
	}
}

// Group creates a subgroup. Options (including middleware) apply to all routes
// of the subgroup.
func (g *Group) Group(prefix string, opts ...RouteOption) *Group {
	child := &Group{app: g.app, prefix: joinPaths(g.prefix, prefix), parent: g}
	for _, o := range opts {
		if m, ok := o.(Middleware); ok {
			child.mws = append(child.mws, m)
		} else if o != nil {
			child.opts = append(child.opts, o)
		}
	}
	return child
}

// Handle registers a handler for method and path. Any method token is
// accepted, including custom methods.
func (g *Group) Handle(method, path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, strings.ToUpper(method), path, h, opts, callerLocation())
}

// GET registers a GET route. HEAD requests are served by GET routes unless a
// HEAD route is registered.
func (g *Group) GET(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodGet, path, h, opts, callerLocation())
}

// POST registers a POST route.
func (g *Group) POST(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodPost, path, h, opts, callerLocation())
}

// PUT registers a PUT route.
func (g *Group) PUT(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodPut, path, h, opts, callerLocation())
}

// PATCH registers a PATCH route.
func (g *Group) PATCH(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodPatch, path, h, opts, callerLocation())
}

// DELETE registers a DELETE route.
func (g *Group) DELETE(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodDelete, path, h, opts, callerLocation())
}

// HEAD registers a HEAD route.
func (g *Group) HEAD(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodHead, path, h, opts, callerLocation())
}

// OPTIONS registers an OPTIONS route. Without one, OPTIONS requests are
// answered automatically with an Allow header.
func (g *Group) OPTIONS(path string, h Handler, opts ...RouteOption) {
	g.app.addRoute(g, http.MethodOptions, path, h, opts, callerLocation())
}

// Any registers the handler for all standard methods.
func (g *Group) Any(path string, h Handler, opts ...RouteOption) {
	loc := callerLocation()
	for _, m := range standardMethods {
		g.app.addRoute(g, m, path, h, opts, loc)
	}
}

// Mount serves a standard http.Handler under prefix for all standard methods.
// The prefix is stripped from the request path before h sees it.
func (g *Group) Mount(prefix string, h http.Handler, opts ...RouteOption) {
	full := joinPaths(g.prefix, prefix)
	stripped := http.StripPrefix(strings.TrimSuffix(full, "/"), h)
	handler := WrapHandler(stripped)
	loc := callerLocation()
	opts = append([]RouteOption{Hidden()}, opts...)
	for _, m := range standardMethods {
		g.app.addRoute(g, m, strings.TrimSuffix(prefix, "/")+"/*path", handler, opts, loc)
	}
}

// joinPaths joins a group prefix and a route path. A path of "" or "/" inside
// a prefixed group refers to the prefix itself.
func joinPaths(prefix, path string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	if path == "" || path == "/" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return prefix + path
}
