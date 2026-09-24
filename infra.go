package torge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"time"

	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/jobs"
)

// ---- Configuration ----

// Config registers a loaded configuration value (typically a pointer to a
// struct filled by config.Load). It is validated with the application's
// validator and, if it implements Validate() error, by that method; failures
// stop startup. The value is supplied to the container so modules and
// constructors can depend on it.
func (a *App) Config(cfg any) {
	loc := callerLocation()
	if cfg == nil {
		return
	}
	var problems []string
	if err := a.opts.Validator.Validate(cfg); err != nil && reflect.Indirect(reflect.ValueOf(cfg)).Kind() == reflect.Struct {
		problems = append(problems, err.Error())
	}
	if v, ok := cfg.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		a.mu.Lock()
		a.addDiagnostic(&Diagnostic{
			Code: DiagInvalidConfig, What: fmt.Sprintf("configuration %T is invalid: %v", cfg, problems), Where: loc,
			Why: "running with invalid configuration fails later and less predictably",
			Fix: "correct the listed values (usually environment variables) and restart",
		})
		a.mu.Unlock()
	}
	a.supply(reflect.TypeOf(cfg), reflect.ValueOf(cfg), loc, false)
}

// ---- Databases ----

// Database is the lifecycle contract Torge needs from a database handle. It
// is deliberately minimal and ORM-agnostic; sqldb.DB adapts *sql.DB, and
// contrib modules adapt pgx, GORM, Ent or Bun.
type Database interface {
	Ping(ctx context.Context) error
	Close() error
}

// Transactor is implemented by databases that support context-carried
// transactions. Inside fn, the adapter's query helpers (for example
// sqldb.DB.Conn(ctx)) use the transaction automatically.
type Transactor interface {
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// DatabaseOption configures a registered database.
type DatabaseOption func(*databaseOptions)

type databaseOptions struct {
	name        string
	pingTimeout time.Duration
	noPing      bool
}

// DatabaseName names a database for health reports and hooks (default
// "database"). Register additional databases with distinct names.
func DatabaseName(name string) DatabaseOption {
	return func(o *databaseOptions) { o.name = name }
}

// DatabasePingTimeout bounds the startup connectivity check (default 10s).
func DatabasePingTimeout(d time.Duration) DatabaseOption {
	return func(o *databaseOptions) { o.pingTimeout = d }
}

// SkipStartupPing disables the startup connectivity check.
func SkipStartupPing() DatabaseOption {
	return func(o *databaseOptions) { o.noPing = true }
}

var errDatabaseUnreachable = errors.New("database unreachable")

// Database registers a database. The application takes ownership of it:
//
//   - startup fails with a DATABASE_UNREACHABLE diagnostic if it cannot be
//     pinged, so traffic is never accepted without a database;
//   - a readiness health check is added;
//   - it is closed at shutdown, after the HTTP server and jobs have stopped;
//   - it is supplied to the container under its concrete type.
//
// The first registered database is the primary one used by App.Transaction.
func (a *App) Database(db Database, opts ...DatabaseOption) {
	loc := callerLocation()
	o := databaseOptions{name: "database", pingTimeout: 10 * time.Second}
	for _, opt := range opts {
		opt(&o)
	}
	if db == nil {
		a.mu.Lock()
		a.addDiagnostic(&Diagnostic{Code: DiagInvalidConfig, What: "nil database registered", Where: loc,
			Why: "the application would fail on first use", Fix: "open the database before registering it"})
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.databases = append(a.databases, db)
	a.mu.Unlock()
	hook := Hook{Name: o.name, OnStop: func(context.Context) error { return db.Close() }}
	if !o.noPing {
		hook.OnStart = func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, o.pingTimeout)
			defer cancel()
			if err := db.Ping(ctx); err != nil {
				return fmt.Errorf("%w: %s: %w", errDatabaseUnreachable, o.name, err)
			}
			return nil
		}
	}
	a.AddHook(hook)
	a.HealthCheck(o.name, db.Ping)
	a.supplyIfAbsent(reflect.TypeOf(db), reflect.ValueOf(db), loc)
}

// Transaction runs fn in a transaction of the primary database. The
// transaction is committed if fn returns nil and rolled back otherwise
// (including on panic). The database must implement Transactor.
func (a *App) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	a.mu.Lock()
	var db Database
	if len(a.databases) > 0 {
		db = a.databases[0]
	}
	a.mu.Unlock()
	if db == nil {
		return errors.New("torge: Transaction requires a database registered with App.Database")
	}
	tx, ok := db.(Transactor)
	if !ok {
		return fmt.Errorf("torge: database %T does not support transactions", db)
	}
	return tx.Transaction(ctx, fn)
}

func (a *App) supplyIfAbsent(t reflect.Type, v reflect.Value, loc string) {
	a.mu.Lock()
	_, exists := a.container.providers[t]
	a.mu.Unlock()
	if !exists {
		a.supply(t, v, loc, false)
	}
}

// ---- Cache ----

// Cache registers the application cache. It is supplied to the container as
// cache.Store. If the store implements Ping(ctx) error a readiness check is
// added, and if it implements io.Closer it is closed at shutdown.
//
// In production, a process-local store (cache.Memory) is reported as a
// warning: each instance would see a different cache.
func (a *App) Cache(store cache.Store) {
	loc := callerLocation()
	if store == nil {
		return
	}
	if p, ok := store.(interface{ Ping(context.Context) error }); ok {
		a.HealthCheck("cache", p.Ping)
	}
	if c, ok := store.(io.Closer); ok {
		a.OnStop("cache", func(context.Context) error { return c.Close() })
	}
	if cache.IsLocal(store) {
		a.warnLocal("cache", loc)
	}
	a.supply(reflect.TypeFor[cache.Store](), reflect.ValueOf(&store).Elem(), loc, false)
}

func (a *App) warnLocal(feature, loc string) {
	if a.opts.Env != Production {
		return
	}
	a.logger.Warn((&Diagnostic{
		Code: DiagLocalState, What: feature + " uses process-local state in production", Where: loc, Warning: true,
		Why: "with more than one instance, each instance sees different data",
		Fix: "use a shared backend (for example contrib/redis) or accept per-instance behavior explicitly",
	}).Error())
}

// ---- External services ----

// Service registers a named external service client (a payment SDK, object
// storage, another API). If it implements Lifecycle it is started and stopped
// with the application; if it implements HealthChecker a readiness check
// named "service:<name>" is added. Retrieve it with ServiceAs.
func (a *App) Service(name string, svc any) {
	loc := callerLocation()
	a.mu.Lock()
	a.mustBeRegistering(loc, "service "+name)
	if _, dup := a.services[name]; dup || svc == nil {
		what := fmt.Sprintf("service %q registered twice", name)
		if svc == nil {
			what = fmt.Sprintf("service %q is nil", name)
		}
		a.addDiagnostic(&Diagnostic{Code: DiagDuplicateService, What: what, Where: loc,
			Why: "lookups by name would be ambiguous or fail", Fix: "register each service once with a unique name"})
		a.mu.Unlock()
		return
	}
	a.services[name] = svc
	a.mu.Unlock()
	if l, ok := svc.(Lifecycle); ok {
		a.Manage("service:"+name, l)
	}
	if h, ok := svc.(HealthChecker); ok {
		a.HealthCheck("service:"+name, h.CheckHealth)
	}
}

// ServiceAs returns the service registered under name as type T.
func ServiceAs[T any](a *App, name string) (T, error) {
	var zero T
	a.mu.Lock()
	svc, ok := a.services[name]
	a.mu.Unlock()
	if !ok {
		return zero, fmt.Errorf("torge: no service named %q", name)
	}
	v, ok := svc.(T)
	if !ok {
		return zero, fmt.Errorf("torge: service %q is %T, not %s", name, svc, reflect.TypeFor[T]())
	}
	return v, nil
}

// Services returns the registered service names, sorted.
func (a *App) Services() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.services))
	for n := range a.services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ---- Jobs ----

// Jobs sets the background job manager. Its workers start after all other
// components and stop right after the HTTP server, before dependencies are
// closed. It is supplied to the container as *jobs.Manager.
func (a *App) Jobs(m *jobs.Manager) {
	loc := callerLocation()
	a.mu.Lock()
	a.mustBeRegistering(loc, "job manager")
	a.jobs, a.jobsImplicit = m, false
	a.mu.Unlock()
	a.supplyIfAbsent(reflect.TypeFor[*jobs.Manager](), reflect.ValueOf(m), loc)
}

// Job registers a job handler. Without a manager set by Jobs, an in-memory
// manager is created (and reported as process-local state in production).
func (a *App) Job(name string, h jobs.Handler, opts ...jobs.JobOption) {
	loc := callerLocation()
	a.mu.Lock()
	if a.jobs == nil {
		a.jobs = jobs.NewManager(jobs.Options{Queue: jobs.NewMemoryQueue(0), Logger: a.logger})
		a.jobsImplicit = true
		a.mu.Unlock()
		a.supplyIfAbsent(reflect.TypeFor[*jobs.Manager](), reflect.ValueOf(a.jobs), loc)
		a.mu.Lock()
	}
	m := a.jobs
	a.mu.Unlock()
	if err := m.Register(name, h, opts...); err != nil {
		a.mu.Lock()
		a.addDiagnostic(&Diagnostic{Code: DiagInvalidHandler, What: err.Error(), Where: loc,
			Why: "the job could not be registered", Fix: "use a unique job name and a non-nil handler"})
		a.mu.Unlock()
	}
}

// finalizeInfrastructure emits warnings that depend on the final setup.
func (a *App) finalizeInfrastructure() {
	a.mu.Lock()
	implicit := a.jobsImplicit
	a.mu.Unlock()
	if implicit {
		a.warnLocal("background jobs (in-memory queue)", "")
	}
}
