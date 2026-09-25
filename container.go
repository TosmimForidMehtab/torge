package torge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
)

// Dependency management is deliberately small: constructors are registered
// explicitly, keyed by their return type, and the whole graph is validated
// when the application starts. Reflection is used once per constructor at
// startup (and per request only for request-scoped providers).
//
// Ownership rule: the container manages the lifecycle of values it
// constructs. A constructed value implementing Lifecycle is started and
// stopped with the application; one implementing io.Closer is closed at
// shutdown. Values passed to Supply are never managed.

// Scope controls how often a provider's constructor runs.
type Scope uint8

const (
	// SingletonScope constructs one value at startup, shared by the whole
	// application. Singleton values must be safe for concurrent use.
	SingletonScope Scope = iota
	// RequestScope constructs one value per request, on first use.
	RequestScope
)

func (s Scope) String() string {
	if s == RequestScope {
		return "request"
	}
	return "singleton"
}

// ProvideOption configures a provider.
type ProvideOption func(*provider)

// RequestScoped makes a provider construct one value per request. Its
// constructor may depend on *torge.Context. Request-scoped values that
// implement io.Closer are closed when the request finishes.
func RequestScoped() ProvideOption {
	return func(p *provider) { p.scope = RequestScope }
}

type provider struct {
	out        reflect.Type
	fn         reflect.Value
	params     []reflect.Type
	returnsErr bool
	scope      Scope
	supplied   bool
	value      reflect.Value
	name       string
	location   string
	module     string
}

func (p *provider) describe() string {
	if p.supplied {
		return fmt.Sprintf("supplied %s", p.out)
	}
	return fmt.Sprintf("%s (%s)", p.name, p.location)
}

type invocation struct {
	fn       reflect.Value
	location string
	module   string
}

type container struct {
	providers  map[reflect.Type]*provider
	order      []*provider
	singletons map[reflect.Type]reflect.Value
	hooks      []Hook
	built      bool
}

func newContainer() *container {
	return &container{
		providers:  make(map[reflect.Type]*provider),
		singletons: make(map[reflect.Type]reflect.Value),
	}
}

var (
	errorType      = reflect.TypeFor[error]()
	contextType    = reflect.TypeFor[context.Context]()
	ctxPtrType     = reflect.TypeFor[*Context]()
	loggerType     = reflect.TypeFor[*slog.Logger]()
	envType        = reflect.TypeFor[Env]()
	builtinDepType = map[reflect.Type]bool{contextType: true, ctxPtrType: true, loggerType: true, envType: true}
)

// Provide registers a constructor. The constructor is a function whose
// parameters are its dependencies and whose results are the provided value
// and, optionally, an error:
//
//	func NewUserService(db *sqldb.DB, log *slog.Logger) (*UserService, error)
//
// Besides registered types, constructors may depend on context.Context (the
// start context, or the request context for request-scoped providers),
// *slog.Logger, torge.Env and, for request-scoped providers only,
// *torge.Context. To provide an interface, return the interface type.
func (a *App) Provide(ctor any, opts ...ProvideOption) {
	a.register(ctor, opts, false, callerLocation())
}

// Replace registers a constructor that replaces any existing provider of the
// same type. It is intended for tests that swap real dependencies for fakes.
func (a *App) Replace(ctor any, opts ...ProvideOption) {
	a.register(ctor, opts, true, callerLocation())
}

// Supply registers ready-made values, keyed by their dynamic types. Supplied
// values are not lifecycle-managed.
func (a *App) Supply(values ...any) {
	loc := callerLocation()
	for _, v := range values {
		if v == nil {
			a.mu.Lock()
			a.addDiagnostic(&Diagnostic{Code: DiagInvalidProvider, What: "nil value passed to Supply", Where: loc,
				Why: "a nil value has no type to register", Fix: "use SupplyAs[T] to supply a typed nil, or remove it"})
			a.mu.Unlock()
			continue
		}
		a.supply(reflect.TypeOf(v), reflect.ValueOf(v), loc, false)
	}
}

// SupplyAs registers v under type T, typically an interface.
func SupplyAs[T any](a *App, v T) {
	a.supply(reflect.TypeFor[T](), reflect.ValueOf(&v).Elem(), callerLocation(), false)
}

// ReplaceValue registers v under type T, replacing any existing provider.
// Intended for tests.
func ReplaceValue[T any](a *App, v T) {
	a.supply(reflect.TypeFor[T](), reflect.ValueOf(&v).Elem(), callerLocation(), true)
}

func (a *App) supply(t reflect.Type, v reflect.Value, loc string, replace bool) {
	a.addProvider(&provider{out: t, supplied: true, value: v, location: loc, name: "Supply"}, replace)
}

func (a *App) register(ctor any, opts []ProvideOption, replace bool, loc string) {
	fn := reflect.ValueOf(ctor)
	ft := fn.Type()
	invalid := func(why string) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidProvider, What: fmt.Sprintf("invalid constructor %T: %s", ctor, why), Where: loc,
			Why: "constructors are how the container builds values",
			Fix: "pass a function such as func(deps...) (T, error) or func(deps...) T",
		})
	}
	if ctor == nil || ft.Kind() != reflect.Func || fn.IsNil() {
		invalid("not a function")
		return
	}
	if ft.IsVariadic() {
		invalid("variadic constructors are not supported")
		return
	}
	if ft.NumOut() == 0 || ft.NumOut() > 2 || (ft.NumOut() == 2 && ft.Out(1) != errorType) {
		invalid("must return T or (T, error)")
		return
	}
	out := ft.Out(0)
	if out == errorType || builtinDepType[out] {
		invalid(fmt.Sprintf("cannot provide built-in type %s", out))
		return
	}
	p := &provider{
		out: out, fn: fn, returnsErr: ft.NumOut() == 2,
		name: funcName(ctor), location: loc,
	}
	for i := range ft.NumIn() {
		p.params = append(p.params, ft.In(i))
	}
	for _, o := range opts {
		o(p)
	}
	a.addProvider(p, replace)
}

func (a *App) addProvider(p *provider, replace bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mustBeRegistering(p.location, "provider for "+p.out.String())
	if a.container.built {
		panic(&Diagnostic{Code: DiagLateRegistration, What: "provider for " + p.out.String() + " registered after dependencies were built",
			Where: p.location, Why: "the dependency graph is fixed at startup", Fix: "register providers before Start"})
	}
	p.module = a.currentModule
	ct := a.container
	if existing, ok := ct.providers[p.out]; ok {
		if !replace {
			a.addDiagnostic(&Diagnostic{
				Code:  DiagDuplicateDependency,
				What:  fmt.Sprintf("%s is provided twice: by %s and by %s", p.out, existing.describe(), p.describe()),
				Where: p.location,
				Why:   "the container would not know which value to inject",
				Fix:   "remove one registration, return a distinct type, or use Replace in tests",
			})
			return
		}
		for i, q := range ct.order {
			if q == existing {
				ct.order = append(ct.order[:i], ct.order[i+1:]...)
				break
			}
		}
	}
	ct.providers[p.out] = p
	ct.order = append(ct.order, p)
}

// Invoke registers a function that runs during Start, after all singletons
// are constructed and before the route table is frozen. Its parameters are
// resolved from the container; it may return an error. Modules use Invoke to
// register routes that need constructed services:
//
//	app.Invoke(func(users *UserService) {
//	    app.GET("/users/:id", users.Get)
//	})
func (a *App) Invoke(fn any) {
	loc := callerLocation()
	v := reflect.ValueOf(fn)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mustBeRegistering(loc, "Invoke")
	if fn == nil || v.Kind() != reflect.Func || v.Type().IsVariadic() ||
		v.Type().NumOut() > 1 || (v.Type().NumOut() == 1 && v.Type().Out(0) != errorType) {
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidProvider, What: fmt.Sprintf("invalid Invoke function %T", fn), Where: loc,
			Why: "Invoke functions are called with resolved dependencies", Fix: "pass a func(deps...) or func(deps...) error",
		})
		return
	}
	a.invokes = append(a.invokes, invocation{fn: v, location: loc, module: a.currentModule})
}

// build validates the graph and constructs all singletons.
func (ct *container) build(ctx context.Context, a *App) error {
	a.mu.Lock()
	ct.built = true
	a.mu.Unlock()
	if diags := ct.validate(); len(diags) > 0 {
		return diags
	}
	for _, p := range ct.order {
		if p.scope != SingletonScope {
			continue
		}
		if _, err := ct.singleton(ctx, a, p); err != nil {
			ct.closeBuilt(ctx)
			return err
		}
	}
	return nil
}

func (ct *container) validate() Diagnostics {
	var diags Diagnostics
	for _, p := range ct.order {
		for _, dep := range p.params {
			switch {
			case dep == ctxPtrType:
				if p.scope == SingletonScope {
					diags = append(diags, &Diagnostic{
						Code: DiagInvalidScope, What: fmt.Sprintf("singleton %s depends on *torge.Context", p.describe()),
						Where: p.location, Why: "a request context does not exist when singletons are built",
						Fix: "register the constructor with torge.RequestScoped(), or pass request data as method arguments",
					})
				}
				continue
			case builtinDepType[dep]:
				continue
			}
			q, ok := ct.providers[dep]
			if !ok {
				diags = append(diags, &Diagnostic{
					Code:  DiagMissingDependency,
					What:  fmt.Sprintf("%s requires %s, but nothing provides it", p.describe(), dep),
					Where: p.location,
					Why:   "the value cannot be constructed",
					Fix:   fmt.Sprintf("register a constructor returning %s with app.Provide, or a value with app.Supply", dep),
				})
				continue
			}
			if p.scope == SingletonScope && q.scope == RequestScope {
				diags = append(diags, &Diagnostic{
					Code:  DiagInvalidScope,
					What:  fmt.Sprintf("singleton %s depends on request-scoped %s", p.describe(), dep),
					Where: p.location,
					Why:   "a singleton outlives every request, so it would capture one request's value forever",
					Fix:   "make the dependent request-scoped too, or depend on a singleton factory instead",
				})
			}
		}
	}
	if cycle := ct.findCycle(); cycle != nil {
		names := make([]string, len(cycle))
		for i, p := range cycle {
			names[i] = p.out.String()
		}
		diags = append(diags, &Diagnostic{
			Code:  DiagDependencyCycle,
			What:  "dependency cycle: " + strings.Join(names, " -> "),
			Where: cycle[0].location,
			Why:   "none of the values in the cycle can be constructed first",
			Fix:   "break the cycle by extracting shared logic into a third type or passing one dependency later via a method",
		})
	}
	return diags
}

func (ct *container) findCycle() []*provider {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[*provider]int, len(ct.order))
	var stack []*provider
	var visit func(p *provider) []*provider
	visit = func(p *provider) []*provider {
		state[p] = visiting
		stack = append(stack, p)
		for _, dep := range p.params {
			q, ok := ct.providers[dep]
			if !ok {
				continue
			}
			switch state[q] {
			case visiting:
				for i, s := range stack {
					if s == q {
						return append(append([]*provider(nil), stack[i:]...), q)
					}
				}
			case unvisited:
				if c := visit(q); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[p] = done
		return nil
	}
	for _, p := range ct.order {
		if state[p] == unvisited {
			if c := visit(p); c != nil {
				return c
			}
		}
	}
	return nil
}

func (ct *container) singleton(ctx context.Context, a *App, p *provider) (reflect.Value, error) {
	if v, ok := ct.singletons[p.out]; ok {
		return v, nil
	}
	if p.supplied {
		ct.singletons[p.out] = p.value
		return p.value, nil
	}
	args := make([]reflect.Value, len(p.params))
	for i, dep := range p.params {
		switch dep {
		case contextType:
			args[i] = reflect.ValueOf(&ctx).Elem()
			continue
		case loggerType:
			args[i] = reflect.ValueOf(a.logger)
			continue
		case envType:
			args[i] = reflect.ValueOf(a.opts.Env)
			continue
		}
		v, err := ct.singleton(ctx, a, ct.providers[dep])
		if err != nil {
			return reflect.Value{}, err
		}
		args[i] = v
	}
	v, err := p.call(args)
	if err != nil {
		return reflect.Value{}, &Diagnostic{
			Code: DiagStartFailed, What: fmt.Sprintf("constructor %s failed: %v", p.name, err), Where: p.location,
			Why: "a required dependency could not be created", Fix: "check the constructor's configuration and inputs",
		}
	}
	ct.singletons[p.out] = v
	ct.manage(p, v)
	return v, nil
}

func (p *provider) call(args []reflect.Value) (reflect.Value, error) {
	out := p.fn.Call(args)
	if p.returnsErr && !out[1].IsNil() {
		return reflect.Value{}, out[1].Interface().(error)
	}
	return out[0], nil
}

func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return !v.IsValid()
}

// manage registers lifecycle hooks for a constructed singleton.
func (ct *container) manage(p *provider, v reflect.Value) {
	if isNilValue(v) {
		return
	}
	iface := v.Interface()
	name := p.out.String()
	switch x := iface.(type) {
	case Lifecycle:
		ct.hooks = append(ct.hooks, Hook{Name: name, OnStart: x.Start, OnStop: x.Stop})
	case io.Closer:
		ct.hooks = append(ct.hooks, Hook{Name: name, OnStop: func(context.Context) error { return x.Close() }})
	}
}

// closeBuilt closes constructed closers when construction fails midway.
func (ct *container) closeBuilt(ctx context.Context) {
	for _, h := range slices.Backward(ct.hooks) {
		if h.OnStart == nil && h.OnStop != nil {
			_ = h.OnStop(ctx)
		}
	}
	ct.hooks = nil
}

func (a *App) runInvokes(ctx context.Context) error {
	a.mu.Lock()
	invokes := a.invokes
	a.mu.Unlock()
	for _, inv := range invokes {
		ft := inv.fn.Type()
		args := make([]reflect.Value, ft.NumIn())
		for i := range args {
			v, err := a.resolveSingleton(ctx, ft.In(i))
			if err != nil {
				return &Diagnostic{Code: DiagMissingDependency, What: fmt.Sprintf("Invoke function needs %s: %v", ft.In(i), err),
					Where: inv.location, Why: "the function cannot be called", Fix: "register a provider for the type"}
			}
			args[i] = v
		}
		a.setModule(inv.module)
		out := inv.fn.Call(args)
		a.setModule("")
		if len(out) == 1 && !out[0].IsNil() {
			return &Diagnostic{Code: DiagInvokeFailed, What: fmt.Sprintf("Invoke function failed: %v", out[0].Interface()),
				Where: inv.location, Why: "setup could not complete", Fix: "fix the error returned by the Invoke function"}
		}
	}
	return nil
}

func (a *App) setModule(name string) {
	a.mu.Lock()
	a.currentModule = name
	a.mu.Unlock()
}

func (a *App) resolveSingleton(ctx context.Context, t reflect.Type) (reflect.Value, error) {
	switch t {
	case contextType:
		return reflect.ValueOf(&ctx).Elem(), nil
	case loggerType:
		return reflect.ValueOf(a.logger), nil
	case envType:
		return reflect.ValueOf(a.opts.Env), nil
	}
	ct := a.container
	if !ct.built {
		return reflect.Value{}, errors.New("dependencies are built when the application starts")
	}
	if v, ok := ct.singletons[t]; ok {
		return v, nil
	}
	if p, ok := ct.providers[t]; ok && p.scope == RequestScope {
		return reflect.Value{}, fmt.Errorf("%s is request-scoped; resolve it from a request with torge.Dep", t)
	}
	return reflect.Value{}, fmt.Errorf("no provider for %s", t)
}

func (c *Context) resolve(t reflect.Type) (reflect.Value, error) {
	switch t {
	case contextType:
		ctx := c.Context()
		return reflect.ValueOf(&ctx).Elem(), nil
	case ctxPtrType:
		return reflect.ValueOf(c), nil
	case loggerType:
		return reflect.ValueOf(c.Logger()), nil
	case envType:
		return reflect.ValueOf(c.Env()), nil
	}
	if c.app == nil {
		return reflect.Value{}, errors.New("torge: context is not attached to an application")
	}
	ct := c.app.container
	if v, ok := ct.singletons[t]; ok {
		return v, nil
	}
	p, ok := ct.providers[t]
	if !ok || !ct.built {
		return reflect.Value{}, fmt.Errorf("torge: no provider for %s", t)
	}
	if c.x != nil {
		if v, ok := c.x.scoped[t]; ok {
			return v, nil
		}
	}
	args := make([]reflect.Value, len(p.params))
	for i, dep := range p.params {
		v, err := c.resolve(dep)
		if err != nil {
			return reflect.Value{}, err
		}
		args[i] = v
	}
	v, err := p.call(args)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("torge: constructor %s: %w", p.name, err)
	}
	x := c.extras()
	if x.scoped == nil {
		x.scoped = make(map[reflect.Type]reflect.Value)
	}
	x.scoped[t] = v
	if !isNilValue(v) {
		if cl, ok := reflect.TypeAssert[io.Closer](v); ok {
			c.OnDone(func() { _ = cl.Close() })
		}
	}
	return v, nil
}

// Resolve returns the singleton of type T. It is available once the
// application has started (including inside Invoke functions).
func Resolve[T any](a *App) (T, error) {
	var zero T
	v, err := a.resolveSingleton(context.Background(), reflect.TypeFor[T]())
	if err != nil {
		return zero, fmt.Errorf("torge: resolve %s: %w", reflect.TypeFor[T](), err)
	}
	if isNilValue(v) {
		return zero, nil
	}
	return v.Interface().(T), nil
}

// MustResolve is like Resolve but panics on error.
func MustResolve[T any](a *App) T {
	v, err := Resolve[T](a)
	if err != nil {
		panic(err)
	}
	return v
}

// Dep returns the dependency of type T for the current request: a singleton,
// or a request-scoped value constructed on first use and cached for the rest
// of the request. Prefer constructor injection into handler types; Dep is for
// request-scoped values and small handlers.
func Dep[T any](c *Context) (T, error) {
	var zero T
	v, err := c.resolve(reflect.TypeFor[T]())
	if err != nil {
		return zero, err
	}
	if isNilValue(v) {
		return zero, nil
	}
	return v.Interface().(T), nil
}

// MustDep is like Dep but panics on error (the panic is recovered and
// rendered as a 500 by the recovery stage).
func MustDep[T any](c *Context) T {
	v, err := Dep[T](c)
	if err != nil {
		panic(err)
	}
	return v
}
