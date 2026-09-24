package torge_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type Repo struct{ name string }

type Service struct {
	repo *Repo
	log  *slog.Logger
}

type Greeter interface{ Greet() string }

type englishGreeter struct{}

func (englishGreeter) Greet() string { return "hello" }

func NewRepo() *Repo                                { return &Repo{name: "real"} }
func NewService(r *Repo, log *slog.Logger) *Service { return &Service{repo: r, log: log} }
func NewGreeter() (Greeter, error)                  { return englishGreeter{}, nil }
func NewFailing() (*Repo, error)                    { return nil, errors.New("connection refused") }
func handlerUsing(svc *Service) func(*torge.Context) error {
	return func(c *torge.Context) error { return c.String(200, svc.repo.name) }
}

func TestProvideResolveAndInvoke(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Provide(NewService)
	app.Provide(NewRepo)
	app.Provide(NewGreeter)
	app.Invoke(func(svc *Service, g Greeter) {
		app.GET("/repo", handlerUsing(svc))
		app.GET("/greet", func(c *torge.Context) error { return c.String(200, g.Greet()) })
	})
	tc := torgetest.New(t, app)
	tc.GET("/repo").Do().ExpectBody("real")
	tc.GET("/greet").Do().ExpectBody("hello")

	svc := torge.MustResolve[*Service](app)
	if svc.log == nil {
		t.Fatal("logger must be injected")
	}
	if again := torge.MustResolve[*Service](app); again != svc {
		t.Fatal("singletons must be constructed once")
	}
	if _, err := torge.Resolve[*strings.Builder](app); err == nil {
		t.Fatal("resolving an unregistered type must fail")
	}
}

func TestReplaceForTests(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Provide(NewRepo)
	app.Provide(NewService)
	app.Replace(func() *Repo { return &Repo{name: "fake"} })
	app.Invoke(func(svc *Service) { app.GET("/repo", handlerUsing(svc)) })
	torgetest.New(t, app).GET("/repo").Do().ExpectBody("fake")
}

func diagCodes(err error) string {
	var diags torge.Diagnostics
	if !errors.As(err, &diags) {
		var d *torge.Diagnostic
		if errors.As(err, &d) {
			return d.Code
		}
		return ""
	}
	codes := make([]string, len(diags))
	for i, d := range diags {
		codes[i] = d.Code
	}
	return strings.Join(codes, ",")
}

func TestDependencyDiagnostics(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		app := torgetest.NewApp(t)
		app.Provide(NewService)
		err := app.Start(context.Background())
		if diagCodes(err) != torge.DiagMissingDependency || !strings.Contains(err.Error(), "*torge_test.Repo") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		app := torgetest.NewApp(t)
		app.Provide(NewRepo)
		app.Provide(NewRepo)
		if err := app.Start(context.Background()); diagCodes(err) != torge.DiagDuplicateDependency {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		type A struct{}
		type B struct{}
		app := torgetest.NewApp(t)
		app.Provide(func(*B) *A { return &A{} })
		app.Provide(func(*A) *B { return &B{} })
		err := app.Start(context.Background())
		if diagCodes(err) != torge.DiagDependencyCycle || !strings.Contains(err.Error(), "->") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("scope", func(t *testing.T) {
		type PerRequest struct{}
		type Singleton struct{}
		app := torgetest.NewApp(t)
		app.Provide(func(*torge.Context) *PerRequest { return &PerRequest{} }, torge.RequestScoped())
		app.Provide(func(*PerRequest) *Singleton { return &Singleton{} })
		app.Provide(func(*torge.Context) *Repo { return nil })
		if err := app.Start(context.Background()); diagCodes(err) != torge.DiagInvalidScope+","+torge.DiagInvalidScope {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("invalid constructor", func(t *testing.T) {
		app := torgetest.NewApp(t)
		app.Provide(42)
		app.Provide(func() (int, int) { return 0, 0 })
		if err := app.Start(context.Background()); diagCodes(err) != torge.DiagInvalidProvider+","+torge.DiagInvalidProvider {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("constructor error", func(t *testing.T) {
		app := torgetest.NewApp(t)
		app.Provide(NewFailing)
		err := app.Start(context.Background())
		if diagCodes(err) != torge.DiagStartFailed || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("got %v", err)
		}
	})
}

type closer struct {
	name   string
	closed *[]string
	mu     *sync.Mutex
}

func (c *closer) Close() error {
	c.mu.Lock()
	*c.closed = append(*c.closed, c.name)
	c.mu.Unlock()
	return nil
}

type lifecycleComponent struct {
	events *[]string
	dep    *closer
}

func (l *lifecycleComponent) Start(context.Context) error {
	*l.events = append(*l.events, "start")
	return nil
}

func (l *lifecycleComponent) Stop(context.Context) error {
	*l.events = append(*l.events, "stop")
	return nil
}

func TestContainerManagesConstructedLifecycles(t *testing.T) {
	var mu sync.Mutex
	var events []string
	app := torgetest.NewApp(t)
	app.Provide(func() *closer { return &closer{name: "closer", closed: &events, mu: &mu} })
	app.Provide(func(c *closer) *lifecycleComponent { return &lifecycleComponent{events: &events, dep: c} })
	type suppliedCloser struct{ closer }
	app.Supply(&suppliedCloser{closer{name: "supplied", closed: &events, mu: &mu}})
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The component starts after its dependency was built, stops first, and
	// the dependency is closed last. Supplied values are never closed.
	if got := strings.Join(events, ","); got != "start,stop,closer" {
		t.Fatalf("got %s", got)
	}
}

type requestValue struct {
	id     string
	closed *atomic.Int32
}

func (r *requestValue) Close() error { r.closed.Add(1); return nil }

func TestRequestScopedDependencies(t *testing.T) {
	var built, closed atomic.Int32
	app := torgetest.NewApp(t)
	app.Provide(func(c *torge.Context, ctx context.Context) *requestValue {
		built.Add(1)
		if ctx != c.Context() {
			t.Error("request-scoped constructors must receive the request context")
		}
		return &requestValue{id: c.RequestID(), closed: &closed}
	}, torge.RequestScoped())
	app.Provide(NewRepo)
	app.GET("/", func(c *torge.Context) error {
		a := torge.MustDep[*requestValue](c)
		b := torge.MustDep[*requestValue](c)
		repo := torge.MustDep[*Repo](c)
		if a != b || a.id != c.RequestID() || repo.name != "real" {
			t.Error("request-scoped values must be cached per request")
		}
		return c.NoContent(204)
	})
	tc := torgetest.New(t, app)
	tc.GET("/").Do().ExpectStatus(204)
	tc.GET("/").Do().ExpectStatus(204)
	if built.Load() != 2 || closed.Load() != 2 {
		t.Fatalf("built=%d closed=%d, want one per request, closed after each", built.Load(), closed.Load())
	}
	if _, err := torge.Resolve[*requestValue](app); err == nil || !strings.Contains(err.Error(), "request-scoped") {
		t.Fatalf("resolving request-scoped values from the app must fail, got %v", err)
	}
}

func TestSupplyAsInterfaceAndConfig(t *testing.T) {
	type Settings struct {
		Port int `validate:"min=1,max=65535"`
	}
	app := torgetest.NewApp(t)
	torge.SupplyAs[Greeter](app, englishGreeter{})
	app.Config(&Settings{Port: 8080})
	app.Invoke(func(g Greeter, s *Settings) error {
		if g.Greet() != "hello" || s.Port != 8080 {
			return errors.New("unexpected values")
		}
		return nil
	})
	torgetest.Start(t, app)

	bad := torgetest.NewApp(t)
	bad.Config(&Settings{Port: 0})
	if err := bad.Start(context.Background()); diagCodes(err) != torge.DiagInvalidConfig {
		t.Fatalf("got %v", err)
	}
}

type fakeStripe struct {
	started atomic.Bool
	healthy error
}

func (f *fakeStripe) Start(context.Context) error { f.started.Store(true); return nil }
func (f *fakeStripe) Stop(context.Context) error  { f.started.Store(false); return nil }
func (f *fakeStripe) CheckHealth(context.Context) error {
	return f.healthy
}

func TestServices(t *testing.T) {
	app := torgetest.NewApp(t)
	stripe := &fakeStripe{healthy: errors.New("api down")}
	app.Service("stripe", stripe)
	app.Service("stripe", stripe)
	if err := app.Start(context.Background()); diagCodes(err) != torge.DiagDuplicateService {
		t.Fatalf("got %v", err)
	}

	app = torgetest.NewApp(t)
	app.Service("stripe", stripe)
	tc := torgetest.New(t, app)
	if !stripe.started.Load() {
		t.Fatal("services implementing Lifecycle must be started")
	}
	got, err := torge.ServiceAs[*fakeStripe](app, "stripe")
	if err != nil || got != stripe {
		t.Fatalf("ServiceAs: %v", err)
	}
	if _, err := torge.ServiceAs[*Repo](app, "stripe"); err == nil {
		t.Fatal("wrong type must fail")
	}
	tc.GET("/ready").Do().ExpectStatus(503)
	tc.GET("/health").Do().ExpectStatus(503).ExpectJSONPath("checks.service:stripe.status", "down").
		ExpectJSONPath("checks.service:stripe.error", "api down")
}
