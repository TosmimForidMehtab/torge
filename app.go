// Package torge is a batteries-included backend framework for Go that stays
// close to the standard library.
//
//	app := torge.New()
//	app.GET("/", func(c *torge.Context) error {
//	    return c.JSON(200, map[string]string{"message": "hello"})
//	})
//	log.Fatal(app.Listen(":8080"))
//
// # Request pipeline
//
// Every request flows through a fixed, documented sequence of stages:
//
//	request ID -> tracing -> access log -> error boundary -> recovery ->
//	security -> health endpoints -> global middleware (App.Use) ->
//	routing -> group middleware -> route middleware -> handler
//
// Built-in stages can be configured or disabled with Options; App.Pipeline
// lists the stages actually installed. Errors returned by handlers propagate
// outward; the error boundary renders them once, and the stages outside it
// (logging, tracing) observe both the rendered status and the original error.
//
// # Lifecycle
//
// An App moves through Building -> Starting -> Running -> Stopping -> Stopped.
// Registration (routes, middleware, providers, hooks) happens while Building.
// Start validates everything and reports all problems at once as Diagnostics,
// builds dependencies, runs start hooks in registration order and then marks
// the app ready. Shutdown stops accepting requests, waits for in-flight
// requests, then runs stop hooks in reverse order.
package torge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/TosmimForidMehtab/torge/correlation"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/validate"
)

// rootGroup lets App embed its root Group without the field name shadowing
// the Group method.
type rootGroup = Group

// State is the lifecycle state of an App.
type State int32

// Lifecycle states.
const (
	StateBuilding State = iota
	StateStarting
	StateRunning
	StateStopping
	StateStopped
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateBuilding:
		return "building"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return fmt.Sprintf("State(%d)", int32(s))
	}
}

// App is a Torge application. Create one with New.
//
// Registration methods are safe to call from multiple goroutines but are meant
// to run during setup; after Start, the route table and pipeline are
// immutable and requests are served without locks.
type App struct {
	*rootGroup

	opts   Options
	logger *slog.Logger
	// accessHandler writes access logs through the unwrapped handler; the
	// access log adds correlation attributes itself, in one batch.
	accessHandler slog.Handler
	proxies       []netip.Prefix

	mu            sync.Mutex
	state         atomic.Int32
	frozen        bool
	router        *router
	routes        []*route
	named         map[string]*route
	diags         Diagnostics
	hooks         []Hook
	started       []Hook
	container     *container
	invokes       []invocation
	modules       map[string]bool
	currentModule string
	health        *healthRegistry
	services      map[string]any
	openapi       *openAPIState
	jobs          *jobs.Manager
	jobsImplicit  bool
	databases     []Database
	pipeline      atomic.Pointer[Handler]
	stages        []string

	server *http.Server
	// stopping is canceled when shutdown begins; long-lived streams watch it.
	stopping    context.Context
	stopStreams context.CancelFunc
	stopOnce    sync.Once
	stopErr     error
	done        chan struct{}
	stopRequest chan struct{}
}

// New creates an application with production-safe defaults: request IDs,
// structured access logs, panic recovery, security headers, body limits,
// server timeouts and health endpoints.
func New(opts ...Option) *App {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	o.Server.withDefaults()
	if !o.exposeExplicit {
		o.ExposeErrors = o.Env == Development
	}
	if o.ErrorHandler == nil {
		o.ErrorHandler = DefaultErrorHandler
	}
	if o.Serializer == nil {
		o.Serializer = JSONSerializer{}
	}
	if o.Validator == nil {
		o.Validator = validate.Default
	}
	a := &App{
		opts:        o,
		router:      newRouter(),
		named:       make(map[string]*route),
		modules:     make(map[string]bool),
		services:    make(map[string]any),
		container:   newContainer(),
		done:        make(chan struct{}),
		stopRequest: make(chan struct{}),
	}
	a.rootGroup = &Group{app: a}
	a.stopping, a.stopStreams = context.WithCancel(context.Background())
	a.SetLogger(o.Logger)
	a.health = newHealthRegistry(a)
	if o.envErr != nil {
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidConfig, What: o.envErr.Error(),
			Why: "the environment decides error exposure, logging format and safety checks",
			Fix: "set TORGE_ENV to development, test or production",
		})
	}
	for _, p := range o.TrustedProxies {
		prefix, err := parsePrefix(p)
		if err != nil {
			a.addDiagnostic(&Diagnostic{
				Code: DiagInvalidConfig, What: fmt.Sprintf("invalid trusted proxy %q", p),
				Why: "trusted proxies decide which forwarding headers are believed",
				Fix: "use an IP address (10.0.0.1) or CIDR (10.0.0.0/8)",
			})
			continue
		}
		a.proxies = append(a.proxies, prefix)
	}
	return a
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()), nil
}

func defaultLogger(env Env) *slog.Logger {
	switch env {
	case Development:
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	case Test:
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	default:
		return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
}

// Env returns the deployment environment.
func (a *App) Env() Env { return a.opts.Env }

// Options returns a copy of the effective options.
func (a *App) Options() Options { return a.opts }

// Logger returns the application logger. Records logged with a request
// context carry request_id and trace_id attributes.
func (a *App) Logger() *slog.Logger { return a.logger }

// SetLogger replaces the application logger. Any slog handler works; it is
// wrapped so that request correlation attributes are added automatically. A
// nil logger selects the environment's default.
func (a *App) SetLogger(l *slog.Logger) {
	if l == nil {
		l = defaultLogger(a.opts.Env)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	h := l.Handler()
	if ch, ok := h.(*correlation.Handler); ok {
		h = ch.Unwrap()
	}
	a.logger = slog.New(correlation.NewHandler(h))
	a.accessHandler = h
}

// State returns the current lifecycle state.
func (a *App) State() State { return State(a.state.Load()) }

// Done is closed once the application has fully stopped.
func (a *App) Done() <-chan struct{} { return a.done }

func (a *App) addDiagnostic(d *Diagnostic) {
	a.diags = append(a.diags, d)
}

// mustBeRegistering panics with a diagnostic if the route table is frozen.
// Callers hold a.mu.
func (a *App) mustBeRegistering(loc, what string) {
	if a.frozen {
		panic(&Diagnostic{
			Code:  DiagLateRegistration,
			What:  fmt.Sprintf("%s registered after the application started", what),
			Where: loc,
			Why:   "the route table and middleware pipeline are immutable once serving begins, so the registration would be silently ignored or race with requests",
			Fix:   "register routes and middleware before calling Start/Listen, or inside a module or Invoke function",
		})
	}
}

func (a *App) addRoute(g *Group, method, path string, h Handler, opts []RouteOption, loc string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	full := joinPaths(g.prefix, path)
	a.mustBeRegistering(loc, "route "+method+" "+full)
	if h == nil {
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidHandler, What: fmt.Sprintf("nil handler for %s %s", method, full), Where: loc,
			Why: "the route would panic on every request", Fix: "pass a non-nil handler function",
		})
		return
	}
	if !validMethod(method) {
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidRoute, What: fmt.Sprintf("invalid HTTP method %q", method), Where: loc,
			Why: "methods must be HTTP tokens", Fix: "use a method such as GET or a custom token without spaces",
		})
		return
	}
	r := &route{method: method, path: full, handler: h, group: g, opts: opts, module: a.currentModule, location: loc}
	for _, o := range opts {
		t, ok := o.(typedOption)
		if !ok {
			continue
		}
		r.handlerFn = t.name
		if t.planErr != nil {
			a.addDiagnostic(&Diagnostic{
				Code: DiagInvalidHandler, What: t.planErr.Error(), Where: loc,
				Why: "the typed handler's input cannot be bound from requests",
				Fix: "use supported parameter types (strings, numbers, booleans, time values, TextUnmarshaler) or slices of them",
			})
		}
	}
	existing, err := a.router.insert(r)
	if err != nil {
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidRoute, What: err.Error(), Where: loc,
			Why: "the route pattern cannot be matched reliably",
			Fix: "use /literal, /:param and a trailing /*wildcard segments only",
		})
		return
	}
	if existing != nil {
		a.addDiagnostic(&Diagnostic{
			Code:  DiagDuplicateRoute,
			What:  fmt.Sprintf("%s %s conflicts with %s %s", method, full, existing.method, existing.path),
			Where: loc + " (first registered at " + existing.location + ")",
			Why:   "two handlers for the same request shape make routing ambiguous",
			Fix:   "remove one registration or change its path; parameter names do not make paths distinct",
		})
		return
	}
	a.routes = append(a.routes, r)
}

func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for i := range len(m) {
		c := m[i]
		if c <= ' ' || c >= 0x7f || strings.IndexByte("()<>@,;:\\\"/[]?={}", c) >= 0 {
			return false
		}
	}
	return true
}

// Routes returns every registered route in registration order.
func (a *App) Routes() []RouteInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]RouteInfo, len(a.routes))
	for i, r := range a.routes {
		out[i] = r.info()
	}
	return out
}

// Pipeline returns the names of the stages every request passes through, in
// order, including global middleware. Available after Start.
func (a *App) Pipeline() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.stages...)
}

// URL builds the path of a named route from name/value parameter pairs:
//
//	app.URL("user.get", "id", "42") // "/users/42"
func (a *App) URL(name string, params ...string) (string, error) {
	a.mu.Lock()
	r := a.named[name]
	if r == nil {
		for _, rt := range a.routes {
			if rt.resolve().name == name {
				r = rt
				break
			}
		}
	}
	a.mu.Unlock()
	if r == nil {
		return "", fmt.Errorf("torge: no route named %q", name)
	}
	return r.buildURL(params)
}

// Validate runs the startup checks that do not require starting components
// and returns every problem found, or nil.
func (a *App) Validate() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	diags := a.collectStaticDiagnostics()
	if len(diags) > 0 {
		return diags
	}
	return nil
}

func (a *App) collectStaticDiagnostics() Diagnostics {
	diags := append(Diagnostics(nil), a.diags...)
	if a.opts.Env == Production && a.opts.ExposeErrors {
		diags = append(diags, &Diagnostic{
			Code: DiagUnsafeProduction, What: "ExposeErrors is enabled in production",
			Why: "internal error messages can leak SQL, file paths, hostnames or secrets to clients",
			Fix: "remove WithExposeErrors(true) or run with TORGE_ENV=development",
		})
	}
	diags = append(diags, routeNameDiagnostics(a.routes, nil)...)
	if a.opts.Server.ShutdownTimeout < 0 || a.opts.Server.DrainDelay < 0 {
		diags = append(diags, &Diagnostic{
			Code: DiagInvalidConfig, What: "negative shutdown timeout or drain delay",
			Why: "shutdown would stop immediately and drop in-flight requests",
			Fix: "use a positive duration such as 30s",
		})
	}
	return diags
}

// Start validates the application, builds dependencies, runs invocations,
// freezes the route table and runs start hooks. It fails with Diagnostics
// describing every problem found. Start is called by Listen and Serve; call it
// directly when serving through your own http.Server or in tests.
func (a *App) Start(ctx context.Context) error {
	if !a.state.CompareAndSwap(int32(StateBuilding), int32(StateStarting)) {
		return &Diagnostic{
			Code: DiagInvalidState, What: fmt.Sprintf("Start called while the application is %s", a.State()),
			Why: "an application can be started only once",
			Fix: "create a new App for each run (tests: one App per test)",
		}
	}
	if err := a.start(ctx); err != nil {
		a.state.Store(int32(StateFailed))
		a.stopOnce.Do(func() { close(a.done) })
		return err
	}
	a.state.Store(int32(StateRunning))
	return nil
}

func (a *App) start(ctx context.Context) error {
	a.mu.Lock()
	diags := a.collectStaticDiagnostics()
	a.mu.Unlock()
	if len(diags) > 0 {
		return diags
	}

	a.finalizeInfrastructure()

	// Dependencies and invocations may register routes and hooks, so the
	// lock is not held while they run.
	if err := a.container.build(ctx, a); err != nil {
		return err
	}
	if err := a.runInvokes(ctx); err != nil {
		return err
	}
	a.registerOpenAPIRoutes()

	a.mu.Lock()
	a.freeze()
	a.buildOpenAPI()
	diags = a.diags
	a.mu.Unlock()
	if len(diags) > 0 {
		return diags
	}
	a.buildPipeline()

	hooks := append(append([]Hook(nil), a.hooks...), a.container.hooks...)
	if a.jobs != nil {
		hooks = append(hooks, Hook{Name: "jobs", OnStart: a.jobs.Start, OnStop: a.jobs.Stop})
	}
	for _, h := range hooks {
		if h.OnStart != nil {
			if err := h.OnStart(ctx); err != nil {
				a.logger.Error("start hook failed; stopping started components", "hook", h.Name, "error", err)
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.opts.Server.ShutdownTimeout)
				stopErr := a.runStopHooks(stopCtx)
				cancel()
				d := &Diagnostic{
					Code: DiagStartFailed, What: fmt.Sprintf("start hook %q failed: %v", h.Name, err),
					Why: "a required component could not start, so the application must not accept traffic",
					Fix: "check the component's configuration and that its dependencies are reachable",
				}
				if errors.Is(err, errDatabaseUnreachable) {
					d.Code = DiagDatabaseUnreachable
					d.Fix = "check the database address, credentials and network access, and that the server is running"
				}
				return errors.Join(d, err, stopErr)
			}
		}
		a.started = append(a.started, h)
	}
	a.logger.Info("application started", "env", string(a.opts.Env), "routes", len(a.routes))
	return nil
}

// freeze resolves route options and composes handlers. Callers hold a.mu.
func (a *App) freeze() {
	a.frozen = true
	for _, r := range a.routes {
		r.cfg = r.resolve()
	}
	for _, d := range routeNameDiagnostics(a.routes, a.named) {
		a.addDiagnostic(d)
	}
	for _, r := range a.routes {
		if c, ok := a.opts.Validator.(interface{ Compile(reflect.Type) error }); ok {
			for _, t := range [...]reflect.Type{r.cfg.doc.input, r.cfg.doc.body} {
				if t == nil {
					continue
				}
				if err := c.Compile(t); err != nil {
					a.addDiagnostic(&Diagnostic{
						Code: DiagInvalidHandler, What: err.Error(), Where: r.location,
						Why: "invalid validation tags would fail every request", Fix: "fix the validate tag on the named field",
					})
				}
			}
		}
		r.composed = Chain(r.handler, r.cfg.middleware...)
	}
}

func (a *App) buildPipeline() {
	type stage struct {
		name string
		mw   Middleware
	}
	var stages []stage
	o := a.opts
	if o.RequestID != nil {
		stages = append(stages, stage{"request-id", RequestID(*o.RequestID)})
	}
	if o.Tracing != nil {
		stages = append(stages, stage{"tracing", o.Tracing})
	}
	if o.AccessLog != nil {
		cfg := *o.AccessLog
		if cfg.SkipPaths == nil && o.Health != nil {
			cfg.SkipPaths = a.health.paths()
		}
		stages = append(stages, stage{"access-log", Logger(cfg)})
	}
	stages = append(stages, stage{"error-boundary", errorBoundary})
	if o.Recovery != nil {
		stages = append(stages, stage{"recovery", Recovery(*o.Recovery)})
	}
	if o.Security != nil {
		stages = append(stages, stage{"security", SecurityHeaders(*o.Security)})
	}
	if o.Health != nil {
		stages = append(stages, stage{"health", a.health.middleware})
	}
	for _, m := range a.rootGroup.mws {
		stages = append(stages, stage{funcName(m), m})
	}
	mws := make([]Middleware, len(stages))
	names := make([]string, len(stages), len(stages)+1)
	for i, s := range stages {
		mws[i], names[i] = s.mw, s.name
	}
	h := Chain(a.dispatch, mws...)
	a.mu.Lock()
	a.stages = append(names, "router")
	a.mu.Unlock()
	a.pipeline.Store(&h)
}

// routeNameDiagnostics reports route names used more than once. When named is
// non-nil it is filled with the name index.
func routeNameDiagnostics(routes []*route, named map[string]*route) Diagnostics {
	var diags Diagnostics
	seen := make(map[string]*route)
	for _, r := range routes {
		cfg := r.cfg
		if cfg == nil {
			cfg = r.resolve()
		}
		name := cfg.name
		if name == "" {
			continue
		}
		if prev, dup := seen[name]; dup {
			diags = append(diags, &Diagnostic{
				Code:  DiagDuplicateRouteName,
				What:  fmt.Sprintf("route name %q is used by %s %s and %s %s", name, prev.method, prev.path, r.method, r.path),
				Where: r.location, Why: "URL building by name would be ambiguous", Fix: "give each route a unique name",
			})
			continue
		}
		seen[name] = r
		if named != nil {
			named[name] = r
		}
	}
	return diags
}

func errorBoundary(next Handler) Handler {
	return func(c *Context) error {
		err := next(c)
		if err != nil {
			c.HandleError(err)
		}
		return err
	}
}

// ServeHTTP implements http.Handler, so an App can be used with any server,
// httptest, or mounted inside another router. The app must be started.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := a.pipeline.Load()
	if p == nil {
		a.logger.Error("request received before Start; call app.Start(ctx) or use Listen/Serve", "method", r.Method, "path", r.URL.Path)
		http.Error(w, "service not started", http.StatusServiceUnavailable)
		return
	}
	c := &Context{req: r, app: a}
	c.resBuf.ResponseWriter = w
	c.res = &c.resBuf
	c.vals = c.valsBuf[:0]
	if err := (*p)(c); err != nil {
		c.HandleError(err)
	}
	c.finish()
}

func (a *App) dispatch(c *Context) error {
	r := c.req
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	rt, vals := a.router.find(r.Method, path, c.vals)
	if rt == nil && r.Method == http.MethodHead {
		rt, vals = a.router.find(http.MethodGet, path, c.vals)
	}
	if rt == nil {
		return a.noRoute(c, path)
	}
	c.rt, c.vals = rt, vals
	limit := rt.cfg.bodyLimit
	if limit == 0 {
		limit = a.opts.BodyLimit
	}
	if limit > 0 && r.Body != nil && r.Body != http.NoBody {
		if r.ContentLength > limit {
			return AsError(&http.MaxBytesError{Limit: limit})
		}
		r.Body = http.MaxBytesReader(c.res, r.Body, limit)
	}
	return rt.composed(c)
}

func (a *App) noRoute(c *Context, path string) error {
	r := c.req
	if a.opts.RedirectTrailingSlash && path != "/" && !strings.Contains(path, "//") {
		alt := path + "/"
		if strings.HasSuffix(path, "/") {
			alt = strings.TrimSuffix(path, "/")
		}
		method := r.Method
		if method == http.MethodHead {
			method = http.MethodGet
		}
		if alt != "" && !strings.HasPrefix(alt, "//") {
			if rt, _ := a.router.find(method, alt, nil); rt != nil {
				code := http.StatusPermanentRedirect
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					code = http.StatusMovedPermanently
				}
				u := *r.URL
				u.Path, u.RawPath = alt, ""
				u.Scheme, u.Host = "", ""
				return c.Redirect(code, u.RequestURI())
			}
		}
	}
	if allowed := a.router.allowed(path); len(allowed) > 0 {
		c.Header("Allow", strings.Join(allowed, ", "))
		if r.Method == http.MethodOptions {
			return c.NoContent(http.StatusNoContent)
		}
		return NewError(http.StatusMethodNotAllowed, CodeMethodNotAllowed, "Method not allowed")
	}
	return NotFound(CodeRouteNotFound, "Route not found")
}

// Hook is a named pair of lifecycle callbacks.
type Hook struct {
	Name    string
	OnStart func(context.Context) error
	OnStop  func(context.Context) error
}

// Lifecycle is implemented by components with explicit start and stop phases.
type Lifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

// AddHook registers lifecycle callbacks. Start hooks run in registration
// order; stop hooks run in reverse order of successful starts.
func (a *App) AddHook(h Hook) {
	loc := callerLocation()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mustBeRegistering(loc, "hook "+h.Name)
	a.hooks = append(a.hooks, h)
}

// OnStart registers a start callback.
func (a *App) OnStart(name string, fn func(context.Context) error) {
	a.AddHook(Hook{Name: name, OnStart: fn})
}

// OnStop registers a stop callback.
func (a *App) OnStop(name string, fn func(context.Context) error) {
	a.AddHook(Hook{Name: name, OnStop: fn})
}

// Manage registers a Lifecycle component.
func (a *App) Manage(name string, l Lifecycle) {
	a.AddHook(Hook{Name: name, OnStart: l.Start, OnStop: l.Stop})
}

func (a *App) runStopHooks(ctx context.Context) error {
	a.mu.Lock()
	started := a.started
	a.started = nil
	a.mu.Unlock()
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		h := started[i]
		if h.OnStop == nil {
			continue
		}
		if err := h.OnStop(ctx); err != nil {
			a.logger.Error("stop hook failed", "hook", h.Name, "error", err)
			errs = append(errs, fmt.Errorf("stop %s: %w", h.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Listen starts the application and serves HTTP on addr until SIGINT/SIGTERM
// or Shutdown, then shuts down gracefully. It returns nil after a clean
// shutdown.
//
// An empty addr listens on ":$PORT", the convention of Render, Heroku,
// Railway, Cloud Run and most other platforms, or ":8080" when PORT is unset:
//
//	app.Listen("") // like app.listen(process.env.PORT || 8080) in Express
func (a *App) Listen(addr string) error {
	ln, err := listen(addr)
	if err != nil {
		return err
	}
	return a.Serve(ln)
}

// DefaultPort is used by Listen("") when the PORT variable is unset.
const DefaultPort = "8080"

// ListenAddr resolves an address the way Listen does: addr itself when
// non-empty, otherwise ":$PORT", otherwise ":8080".
func ListenAddr(addr string) (string, error) {
	if addr != "" {
		return addr, nil
	}
	port := os.Getenv("PORT")
	if port == "" {
		return ":" + DefaultPort, nil
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("torge: PORT=%q is not a valid port number", port)
	}
	return ":" + port, nil
}

func listen(addr string) (net.Listener, error) {
	addr, err := ListenAddr(addr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("torge: listen on %s: %w", addr, err)
	}
	return ln, nil
}

// ListenTLS is like Listen but serves HTTPS with the given certificate files.
func (a *App) ListenTLS(addr, certFile, keyFile string) error {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("torge: load TLS certificate: %w", err)
	}
	ln, err := listen(addr)
	if err != nil {
		return err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
	return a.Serve(tls.NewListener(ln, cfg))
}

// Server returns a configured *http.Server for this app, for callers that
// manage serving themselves.
func (a *App) Server() *http.Server {
	s := a.opts.Server
	return &http.Server{
		Handler:           a,
		ReadHeaderTimeout: s.ReadHeaderTimeout,
		ReadTimeout:       s.ReadTimeout,
		WriteTimeout:      s.WriteTimeout,
		IdleTimeout:       s.IdleTimeout,
		MaxHeaderBytes:    s.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(a.logger.Handler(), slog.LevelWarn),
	}
}

// Serve starts the application if needed and serves on ln until a signal or
// Shutdown. Set TORGE_ROUTES=1 to print the route table and exit instead.
func (a *App) Serve(ln net.Listener) error {
	if os.Getenv("TORGE_ROUTES") != "" {
		ln.Close()
		return a.printRoutes(os.Stdout)
	}
	if a.State() == StateBuilding {
		ctx, cancel := context.WithTimeout(context.Background(), a.opts.Server.StartTimeout)
		err := a.Start(ctx)
		cancel()
		if err != nil {
			ln.Close()
			return err
		}
	}
	if a.State() != StateRunning {
		ln.Close()
		return &Diagnostic{Code: DiagInvalidState, What: "Serve called while the application is " + a.State().String(),
			Why: "only a running application can serve", Fix: "create a new App"}
	}
	srv := a.Server()
	a.mu.Lock()
	a.server = srv
	a.mu.Unlock()

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	a.logger.Info("server listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			<-a.done
			return a.stopErr
		}
		a.logger.Error("server failed", "error", err)
		_ = a.shutdownWithTimeout()
		return err
	case <-sigCtx.Done():
		a.logger.Info("shutdown signal received")
	case <-a.stopRequest:
	}
	err := a.shutdownWithTimeout()
	<-serveErr
	return err
}

func (a *App) shutdownWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), a.opts.Server.ShutdownTimeout)
	defer cancel()
	return a.Shutdown(ctx)
}

// Shutdown gracefully stops the application:
//
//  1. readiness reports "stopping" (and the app waits DrainDelay);
//  2. the server stops accepting connections and waits for in-flight
//     requests;
//  3. stop hooks run in reverse start order (job workers, services,
//     caches, databases, telemetry flushers).
//
// It is safe to call more than once; later calls wait for the first.
func (a *App) Shutdown(ctx context.Context) error {
	a.stopOnce.Do(func() {
		a.state.Store(int32(StateStopping))
		select {
		case <-a.stopRequest:
		default:
			close(a.stopRequest)
		}
		a.logger.Info("shutting down")
		// Streams never become idle, so end them now; ordinary requests
		// finish normally while the server drains.
		a.stopStreams()
		var errs []error
		if d := a.opts.Server.DrainDelay; d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
			}
		}
		a.mu.Lock()
		srv := a.server
		a.mu.Unlock()
		if srv != nil {
			if err := srv.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("http server: %w", err))
				_ = srv.Close()
			}
		}
		if err := a.runStopHooks(ctx); err != nil {
			errs = append(errs, err)
		}
		a.stopErr = errors.Join(errs...)
		a.state.Store(int32(StateStopped))
		if a.stopErr != nil {
			a.logger.Error("shutdown completed with errors", "error", a.stopErr)
		} else {
			a.logger.Info("shutdown complete")
		}
		close(a.done)
	})
	select {
	case <-a.done:
		return a.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
