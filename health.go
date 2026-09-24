package torge

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HealthConfig configures the health endpoints. Empty paths use the defaults;
// set a path to "-" to disable that endpoint.
//
//   - LivePath (default /live) answers whether the process is alive. It runs
//     only checks registered with Liveness(), so a slow dependency never
//     causes a restart.
//   - ReadyPath (default /ready) answers whether the application should
//     receive traffic: it is 503 while starting or stopping, and when a
//     required check fails.
//   - HealthPath (default /health) returns a detailed report of every check.
//
// Health endpoints run before global middleware, so authentication or rate
// limiting added with App.Use never blocks orchestrator probes, and they are
// excluded from access logs by default.
type HealthConfig struct {
	LivePath   string
	ReadyPath  string
	HealthPath string
	// Timeout bounds each check (default 5s). Checks run concurrently, so a
	// probe never takes longer than the slowest timeout.
	Timeout time.Duration
}

// HealthCheck reports whether a dependency is healthy. It must honor ctx.
type HealthCheck func(ctx context.Context) error

// HealthChecker is implemented by components that can check their own health.
// Services registered with App.Service that implement it get a readiness
// check automatically.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// HealthOption configures a health check.
type HealthOption func(*healthCheck)

// CheckTimeout overrides the timeout of one check.
func CheckTimeout(d time.Duration) HealthOption {
	return func(h *healthCheck) { h.timeout = d }
}

// Liveness includes the check in the liveness endpoint. Use it only for
// failures that a restart fixes (for example a deadlocked worker).
func Liveness() HealthOption {
	return func(h *healthCheck) { h.liveness = true }
}

// Optional reports the check without letting its failure make the
// application unready.
func Optional() HealthOption {
	return func(h *healthCheck) { h.optional = true }
}

type healthCheck struct {
	name     string
	check    HealthCheck
	timeout  time.Duration
	liveness bool
	optional bool
}

// Health statuses.
const (
	HealthUp       = "up"
	HealthDown     = "down"
	HealthStarting = "starting"
	HealthStopping = "stopping"
)

// HealthReport is the body of the health endpoints.
type HealthReport struct {
	Status string                 `json:"status"`
	Checks map[string]CheckResult `json:"checks,omitempty"`
}

// CheckResult is the outcome of a single check.
type CheckResult struct {
	Status     string  `json:"status"`
	DurationMS float64 `json:"duration_ms"`
	Optional   bool    `json:"optional,omitempty"`
	// Error is included only when ExposeErrors is enabled.
	Error string `json:"error,omitempty"`
	err   error
}

type healthRegistry struct {
	app    *App
	mu     sync.RWMutex
	checks []*healthCheck
}

func newHealthRegistry(a *App) *healthRegistry { return &healthRegistry{app: a} }

func (h *healthRegistry) config() HealthConfig {
	cfg := HealthConfig{}
	if h.app.opts.Health != nil {
		cfg = *h.app.opts.Health
	}
	setDefault(&cfg.LivePath, "/live")
	setDefault(&cfg.ReadyPath, "/ready")
	setDefault(&cfg.HealthPath, "/health")
	setDefault(&cfg.Timeout, 5*time.Second)
	return cfg
}

func (h *healthRegistry) paths() []string {
	cfg := h.config()
	var out []string
	for _, p := range []string{cfg.LivePath, cfg.ReadyPath, cfg.HealthPath} {
		if p != "-" {
			out = append(out, p)
		}
	}
	return out
}

// HealthCheck registers a named check. Checks are readiness checks unless
// registered with Liveness().
//
//	app.HealthCheck("database", db.Ping)
func (a *App) HealthCheck(name string, check HealthCheck, opts ...HealthOption) {
	loc := callerLocation()
	hc := &healthCheck{name: name, check: check}
	for _, o := range opts {
		o(hc)
	}
	h := a.health
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, existing := range h.checks {
		if existing.name == name {
			a.mu.Lock()
			a.addDiagnostic(&Diagnostic{
				Code: DiagDuplicateHealthCheck, What: fmt.Sprintf("health check %q registered twice", name), Where: loc,
				Why: "reports are keyed by name, so one check would hide the other", Fix: "give each check a unique name",
			})
			a.mu.Unlock()
			return
		}
	}
	h.checks = append(h.checks, hc)
}

// Kinds of health evaluation.
const (
	livenessProbe = iota
	readinessProbe
	fullReport
)

// CheckHealth runs the readiness checks and returns a report, for programmatic
// use.
func (a *App) CheckHealth(ctx context.Context) HealthReport {
	report, _ := a.health.evaluate(ctx, fullReport)
	return report
}

func (h *healthRegistry) evaluate(ctx context.Context, kind int) (HealthReport, int) {
	switch state := h.app.State(); {
	case kind != livenessProbe && state == StateStarting, kind != livenessProbe && state == StateBuilding:
		return HealthReport{Status: HealthStarting}, http.StatusServiceUnavailable
	case kind != livenessProbe && state >= StateStopping:
		return HealthReport{Status: HealthStopping}, http.StatusServiceUnavailable
	}
	h.mu.RLock()
	var checks []*healthCheck
	for _, c := range h.checks {
		if kind != livenessProbe || c.liveness {
			checks = append(checks, c)
		}
	}
	h.mu.RUnlock()

	report := HealthReport{Status: HealthUp}
	if len(checks) == 0 {
		return report, http.StatusOK
	}
	results := make([]CheckResult, len(checks))
	defTimeout := h.config().Timeout
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() { results[i] = runCheck(ctx, c, defTimeout) })
	}
	wg.Wait()

	status := http.StatusOK
	report.Checks = make(map[string]CheckResult, len(checks))
	for i, c := range checks {
		r := results[i]
		if r.Status == HealthDown && !c.optional {
			report.Status, status = HealthDown, http.StatusServiceUnavailable
			h.app.logger.WarnContext(ctx, "health check failed", "check", c.name, "error", r.err)
		}
		if h.app.opts.ExposeErrors && r.err != nil {
			r.Error = r.err.Error()
		}
		report.Checks[c.name] = r
	}
	return report, status
}

func runCheck(ctx context.Context, c *healthCheck, def time.Duration) CheckResult {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = def
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("health check panicked: %v", r)
			}
		}()
		done <- c.check(ctx)
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = fmt.Errorf("health check timed out after %s", timeout)
	}
	res := CheckResult{
		Status:     HealthUp,
		DurationMS: float64(time.Since(start).Microseconds()) / 1000,
		Optional:   c.optional,
		err:        err,
	}
	if err != nil {
		res.Status = HealthDown
	}
	return res
}

// middleware serves the health endpoints ahead of global middleware.
func (h *healthRegistry) middleware(next Handler) Handler {
	cfg := h.config()
	return func(c *Context) error {
		r := c.req
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return next(c)
		}
		kind := -1
		switch r.URL.Path {
		case cfg.LivePath:
			kind = livenessProbe
		case cfg.ReadyPath:
			kind = readinessProbe
		case cfg.HealthPath:
			kind = fullReport
		}
		if kind < 0 {
			return next(c)
		}
		report, status := h.evaluate(c.Context(), kind)
		if kind != fullReport {
			// Probes only need the status; keep the body small.
			report.Checks = nil
		}
		c.Header("Cache-Control", "no-store")
		return c.JSON(status, report)
	}
}
