package torge_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestHelloWorld(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"message": "hello"})
	})
	torgetest.New(t, app).GET("/").Do().
		ExpectStatus(200).
		ExpectHeader("Content-Type", "application/json; charset=utf-8").
		ExpectJSON(map[string]string{"message": "hello"}).
		ExpectHeaderPresent("X-Request-Id")
}

func TestPathQueryAndHeaderAccess(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/users/:id/files/*path", func(c *torge.Context) error {
		page, err := c.QueryInt("page", 1)
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]any{
			"id": c.Param("id"), "path": c.Param("path"), "page": page,
			"q": c.Query("q"), "h": c.GetHeader("X-Test"), "route": c.RoutePattern(),
		})
	})
	tc := torgetest.New(t, app)
	tc.GET("/users/42/files/a/b.txt").Query("page", "3").Query("q", "x").Header("X-Test", "yes").Do().
		ExpectStatus(200).
		ExpectJSON(map[string]any{"id": "42", "path": "a/b.txt", "page": 3, "q": "x", "h": "yes", "route": "/users/:id/files/*path"})
	tc.GET("/users/1/files/x").Query("page", "abc").Do().ExpectStatus(400).ExpectErrorCode(torge.CodeBadRequest)
}

func TestMiddlewareOrderIsDeterministic(t *testing.T) {
	app := torgetest.NewApp(t)
	var mu sync.Mutex
	var trace []string
	mark := func(name string) torge.Middleware {
		return func(next torge.Handler) torge.Handler {
			return func(c *torge.Context) error {
				mu.Lock()
				trace = append(trace, name+">")
				mu.Unlock()
				err := next(c)
				mu.Lock()
				trace = append(trace, "<"+name)
				mu.Unlock()
				return err
			}
		}
	}
	api := app.Group("/api", mark("api-opt"))
	users := api.Group("/users")
	users.GET("/:id", func(c *torge.Context) error {
		mu.Lock()
		trace = append(trace, "handler")
		mu.Unlock()
		return c.NoContent(204)
	}, mark("route"))
	// Registered after the route: still applies, in Use order.
	users.Use(mark("users"))
	api.Use(mark("api-use"))
	app.Use(mark("global1"), mark("global2"))

	torgetest.New(t, app).GET("/api/users/1").Do().ExpectStatus(204)
	want := "global1> global2> api-opt> api-use> users> route> handler <route <users <api-use <api-opt <global2 <global1"
	if got := strings.Join(trace, " "); got != want {
		t.Fatalf("order mismatch\nwant: %s\ngot:  %s", want, got)
	}
	pipeline := strings.Join(app.Pipeline(), ",")
	if !strings.HasPrefix(pipeline, "request-id,access-log,error-boundary,recovery,security,health,") ||
		!strings.HasSuffix(pipeline, ",router") {
		t.Fatalf("unexpected pipeline %s", pipeline)
	}
}

func TestMiddlewareShortCircuitAndResponseModification(t *testing.T) {
	app := torgetest.NewApp(t)
	deny := func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			if c.GetHeader("X-Allow") != "1" {
				return torge.Forbidden("DENIED", "Not allowed")
			}
			c.Header("X-Checked", "true")
			return next(c)
		}
	}
	called := false
	app.GET("/secret", func(c *torge.Context) error {
		called = true
		return c.String(200, "ok")
	}, torge.Middleware(deny))
	tc := torgetest.New(t, app)
	tc.GET("/secret").Do().ExpectStatus(403).ExpectErrorCode("DENIED")
	if called {
		t.Fatal("handler must not run when middleware short-circuits")
	}
	tc.GET("/secret").Header("X-Allow", "1").Do().ExpectStatus(200).ExpectHeader("X-Checked", "true").ExpectBody("ok")
}

func TestNotFoundMethodNotAllowedOptionsHead(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/items", func(c *torge.Context) error { return c.String(200, "list") })
	app.POST("/items", func(c *torge.Context) error { return c.String(201, "created") })
	tc := torgetest.New(t, app)

	tc.GET("/missing").Do().ExpectStatus(404).ExpectErrorCode(torge.CodeRouteNotFound)
	tc.DELETE("/items").Do().ExpectStatus(405).ExpectHeader("Allow", "GET, HEAD, OPTIONS, POST").
		ExpectErrorCode(torge.CodeMethodNotAllowed)
	tc.OPTIONS("/items").Do().ExpectStatus(204).ExpectHeader("Allow", "GET, HEAD, OPTIONS, POST")
	res := tc.HEAD("/items").Do().ExpectStatus(200)
	if len(res.Body) != 0 {
		t.Fatalf("HEAD must not return a body, got %q", res.Body)
	}
	tc.Request("PURGE", "/items").Do().ExpectStatus(405)
}

func TestCustomMethodsAndAny(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Handle("PURGE", "/cache", func(c *torge.Context) error { return c.String(200, "purged") })
	app.Any("/any", func(c *torge.Context) error { return c.String(200, c.Method()) })
	tc := torgetest.New(t, app)
	tc.Request("PURGE", "/cache").Do().ExpectStatus(200).ExpectBody("purged")
	tc.PATCH("/any").Do().ExpectBody("PATCH")
}

func TestTrailingSlashRedirect(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/users", func(c *torge.Context) error { return c.String(200, "users") })
	app.POST("/docs/", func(c *torge.Context) error { return c.String(200, "docs") })
	tc := torgetest.New(t, app)
	tc.GET("/users/").Query("a", "1").Do().ExpectStatus(301).ExpectHeader("Location", "/users?a=1")
	tc.POST("/docs").Do().ExpectStatus(308).ExpectHeader("Location", "/docs/")
	// Protocol-relative paths are never used as redirect targets.
	tc.GET("//evil.com/").Do().ExpectStatus(404)
}

func TestErrorsArePublicSafe(t *testing.T) {
	app := torge.New(torge.WithEnv(torge.Production), torge.WithLogger(discardLogger()))
	app.GET("/db", func(c *torge.Context) error {
		return errors.New("pq: password authentication failed for user admin at 10.0.0.5")
	})
	app.GET("/wrapped", func(c *torge.Context) error {
		return torge.NotFound("USER_NOT_FOUND", "User does not exist").Wrap(errors.New("sql: no rows"))
	})
	tc := torgetest.New(t, app)
	res := tc.GET("/db").Do().ExpectStatus(500).ExpectErrorCode(torge.CodeInternal)
	if strings.Contains(string(res.Body), "password") || strings.Contains(string(res.Body), "10.0.0.5") {
		t.Fatalf("internal error leaked: %s", res.Body)
	}
	res = tc.GET("/wrapped").Do().ExpectStatus(404).
		ExpectJSONPath("error.message", "User does not exist")
	if strings.Contains(string(res.Body), "sql") || strings.Contains(string(res.Body), "debug") {
		t.Fatalf("wrapped error leaked: %s", res.Body)
	}
	if id, _ := res.JSONPath("error.request_id"); id != res.Header.Get("X-Request-Id") || id == "" {
		t.Fatalf("error must carry the request ID, got %v", id)
	}
}

func TestExposeErrorsInDevelopment(t *testing.T) {
	app := torge.New(torge.WithEnv(torge.Development), torge.WithLogger(discardLogger()))
	app.GET("/x", func(c *torge.Context) error { return errors.New("boom detail") })
	torgetest.New(t, app).GET("/x").Do().ExpectStatus(500).ExpectJSONPath("error.debug.error", "boom detail")
}

func TestExposeErrorsRefusedInProduction(t *testing.T) {
	app := torge.New(torge.WithEnv(torge.Production), torge.WithExposeErrors(true), torge.WithLogger(discardLogger()))
	err := app.Start(context.Background())
	var diags torge.Diagnostics
	if !errors.As(err, &diags) || diags[0].Code != torge.DiagUnsafeProduction {
		t.Fatalf("expected an unsafe production diagnostic, got %v", err)
	}
}

func TestErrorIsAndWrap(t *testing.T) {
	sentinel := torge.NotFound("USER_NOT_FOUND", "User does not exist")
	cause := errors.New("no rows")
	err := fmt.Errorf("service: %w", sentinel.Wrap(cause).WithMeta("id", 1))
	if !errors.Is(err, sentinel) || !errors.Is(err, cause) {
		t.Fatal("wrapped error must match the sentinel and the cause")
	}
	if sentinel.Err != nil || sentinel.Meta != nil {
		t.Fatal("With* methods must not mutate the sentinel")
	}
	if torge.StatusOf(err) != 404 || torge.StatusOf(context.DeadlineExceeded) != 504 ||
		torge.StatusOf(context.Canceled) != torge.StatusClientClosedRequest || torge.StatusOf(errors.New("x")) != 500 {
		t.Fatal("unexpected status classification")
	}
}

func TestPanicRecovery(t *testing.T) {
	var reported atomic.Bool
	app := torgetest.NewApp(t, torge.WithRecovery(&torge.RecoveryConfig{
		OnPanic: func(c *torge.Context, v any, stack []byte) { reported.Store(len(stack) > 0) },
	}))
	app.GET("/panic", func(c *torge.Context) error { panic("kaboom") })
	app.GET("/ok", func(c *torge.Context) error { return c.String(200, "ok") })
	tc := torgetest.New(t, app)
	tc.GET("/panic").Do().ExpectStatus(500).ExpectErrorCode(torge.CodeInternal)
	if !reported.Load() {
		t.Fatal("OnPanic must receive the stack")
	}
	tc.GET("/ok").Do().ExpectStatus(200)
}

func TestRequestIDTrust(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithTrustedProxies("10.0.0.0/8"))
	app.GET("/id", func(c *torge.Context) error { return c.String(200, c.RequestID()) })
	tc := torgetest.New(t, app)

	untrusted := tc.GET("/id").Header("X-Request-ID", "client-chosen").RemoteAddr("203.0.113.9:1000").Do()
	if string(untrusted.Body) == "client-chosen" {
		t.Fatal("request IDs from untrusted clients must be replaced")
	}
	tc.GET("/id").Header("X-Request-ID", "lb-123").RemoteAddr("10.1.2.3:1000").Do().
		ExpectBody("lb-123").ExpectHeader("X-Request-Id", "lb-123")
	invalid := tc.GET("/id").Header("X-Request-ID", "bad id\nwith newline").RemoteAddr("10.1.2.3:1000").Do()
	if strings.Contains(string(invalid.Body), "bad") {
		t.Fatal("malformed request IDs must be replaced")
	}
}

func TestRealIPHonorsTrustedProxiesOnly(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithTrustedProxies("10.0.0.0/8"))
	app.GET("/ip", func(c *torge.Context) error { return c.String(200, c.RealIP()+" "+c.Scheme()) })
	tc := torgetest.New(t, app)
	tc.GET("/ip").RemoteAddr("203.0.113.9:1").Header("X-Forwarded-For", "1.2.3.4").Header("X-Forwarded-Proto", "https").Do().
		ExpectBody("203.0.113.9 http")
	tc.GET("/ip").RemoteAddr("10.0.0.2:1").Header("X-Forwarded-For", "1.2.3.4, 10.0.0.7").Header("X-Forwarded-Proto", "https").Do().
		ExpectBody("1.2.3.4 https")
}

func TestBodyLimit(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithBodyLimit(10))
	echo := func(c *torge.Context) error {
		b, err := c.ReadBody()
		if err != nil {
			return err
		}
		return c.String(200, string(b))
	}
	app.POST("/small", echo)
	app.POST("/big", echo, torge.BodyLimit(100))
	tc := torgetest.New(t, app)
	tc.POST("/small").Text("0123456789").Do().ExpectStatus(200)
	tc.POST("/small").Text(strings.Repeat("x", 11)).Do().ExpectStatus(413).ExpectErrorCode(torge.CodePayloadTooLarge)
	// Unknown length (chunked): enforced while reading.
	req := tc.POST("/small").Body(io.MultiReader(strings.NewReader(strings.Repeat("y", 20))), "text/plain").Build()
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("expected 413 for oversized chunked body, got %d", rec.Code)
	}
	tc.POST("/big").Text(strings.Repeat("x", 50)).Do().ExpectStatus(200)
}

func TestSecurityHeadersAndHosts(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithSecurity(&torge.SecurityConfig{
		AllowedHosts: []string{"api.example.com", "*.example.org"},
		HSTSMaxAge:   time.Hour,
		Headers:      map[string]string{"X-Frame-Options": "", "Permissions-Policy": "camera=()"},
	}), torge.WithTrustedProxies("10.0.0.1"))
	app.GET("/", func(c *torge.Context) error { return c.String(200, "ok") })
	tc := torgetest.New(t, app)
	res := tc.GET("http://api.example.com/").Do().ExpectStatus(200).
		ExpectHeader("X-Content-Type-Options", "nosniff").
		ExpectHeader("Permissions-Policy", "camera=()").
		ExpectHeader("Strict-Transport-Security", "")
	if res.Header.Get("X-Frame-Options") != "" {
		t.Fatal("headers mapped to empty strings must be removed")
	}
	tc.GET("http://a.example.org/").RemoteAddr("10.0.0.1:1").Header("X-Forwarded-Proto", "https").Do().
		ExpectStatus(200).ExpectHeader("Strict-Transport-Security", "max-age=3600")
	tc.GET("http://evil.com/").Do().ExpectStatus(400).ExpectErrorCode(torge.CodeInvalidHost)
	tc.GET("http://example.org/").Do().ExpectStatus(400)
}

func TestStdlibAdapters(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/std", torge.WrapFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Std", "1")
		w.WriteHeader(202)
		_, _ = io.WriteString(w, "std")
	}))
	stdMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Wrapped", "yes")
			next.ServeHTTP(w, r)
		})
	}
	app.GET("/wrapped", func(c *torge.Context) error {
		return torge.NotFound("GONE", "gone")
	}, torge.WrapMiddleware(stdMW))
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "mounted "+r.URL.Path) })
	app.Mount("/legacy", mux)

	tc := torgetest.New(t, app)
	tc.GET("/std").Do().ExpectStatus(202).ExpectHeader("X-Std", "1").ExpectBody("std")
	tc.GET("/wrapped").Do().ExpectStatus(404).ExpectHeader("X-Wrapped", "yes").ExpectErrorCode("GONE")
	tc.GET("/legacy/hello").Do().ExpectStatus(200).ExpectBody("mounted /hello")
}

func TestRouteIntrospectionAndNamedURLs(t *testing.T) {
	app := torgetest.NewApp(t)
	api := app.Group("/api", torge.Tags("api"))
	api.Use(torge.Recovery())
	api.GET("/users/:id", getUser, torge.Name("user.get"))
	routes := app.Routes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	r := routes[0]
	if r.Method != "GET" || r.Path != "/api/users/:id" || r.Name != "user.get" ||
		r.Handler != "torge_test.getUser" || strings.Join(r.Middleware, ",") != "torge.Recovery" ||
		!strings.HasSuffix(strings.Split(r.Location, ":")[len(strings.Split(r.Location, ":"))-2], "app_test.go") {
		t.Fatalf("unexpected route info %+v", r)
	}
	u, err := app.URL("user.get", "id", "7")
	if err != nil || u != "/api/users/7" {
		t.Fatalf("URL: %q %v", u, err)
	}
	var sb strings.Builder
	if err := app.PrintRoutes(&sb); err != nil || !strings.Contains(sb.String(), "/api/users/:id") {
		t.Fatalf("PrintRoutes: %v\n%s", err, sb.String())
	}
}

func getUser(c *torge.Context) error { return c.String(200, c.Param("id")) }

func TestStartupDiagnosticsReportEverything(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/a", getUser)
	app.GET("/a", getUser)
	app.GET("/b/:x", getUser, torge.Name("dup"))
	app.GET("/c", getUser, torge.Name("dup"))
	app.GET("bad//path", getUser)
	app.GET("/nil", nil)
	err := app.Start(context.Background())
	var diags torge.Diagnostics
	if !errors.As(err, &diags) {
		t.Fatalf("expected Diagnostics, got %v", err)
	}
	codes := map[string]bool{}
	for _, d := range diags {
		codes[d.Code] = true
		if d.Why == "" || d.Fix == "" {
			t.Errorf("diagnostic %s must explain why and how to fix", d.Code)
		}
	}
	for _, want := range []string{torge.DiagDuplicateRoute, torge.DiagDuplicateRouteName, torge.DiagInvalidRoute, torge.DiagInvalidHandler} {
		if !codes[want] {
			t.Errorf("missing diagnostic %s in:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "app_test.go") {
		t.Errorf("diagnostics must point at the registration site:\n%v", err)
	}
	if app.State() != torge.StateFailed {
		t.Fatalf("state = %s", app.State())
	}
}

func TestLateRegistrationPanics(t *testing.T) {
	app := torgetest.NewApp(t)
	torgetest.Start(t, app)
	defer func() {
		d, ok := recover().(*torge.Diagnostic)
		if !ok || d.Code != torge.DiagLateRegistration {
			t.Fatalf("expected a late registration diagnostic, got %v", d)
		}
	}()
	app.Use(torge.Recovery())
}

func TestLifecycleOrderAndShutdown(t *testing.T) {
	app := torgetest.NewApp(t)
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	for _, name := range []string{"a", "b", "c"} {
		app.AddHook(torge.Hook{
			Name:    name,
			OnStart: func(context.Context) error { record("start " + name); return nil },
			OnStop:  func(context.Context) error { record("stop " + name); return nil },
		})
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if app.State() != torge.StateRunning {
		t.Fatalf("state = %s", app.State())
	}
	if err := app.Start(context.Background()); err == nil {
		t.Fatal("second Start must fail")
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A second Shutdown is a no-op.
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "start a,start b,start c,stop c,stop b,stop a"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	select {
	case <-app.Done():
	default:
		t.Fatal("Done must be closed after shutdown")
	}
}

func TestFailedStartStopsStartedComponents(t *testing.T) {
	app := torgetest.NewApp(t)
	var stopped []string
	app.AddHook(torge.Hook{Name: "ok", OnStart: func(context.Context) error { return nil },
		OnStop: func(context.Context) error { stopped = append(stopped, "ok"); return nil }})
	app.OnStart("broken", func(context.Context) error { return errors.New("cannot connect") })
	app.OnStop("never-started", func(context.Context) error { stopped = append(stopped, "never"); return nil })
	err := app.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot connect") {
		t.Fatalf("unexpected error %v", err)
	}
	var d *torge.Diagnostic
	if !errors.As(err, &d) || d.Code != torge.DiagStartFailed {
		t.Fatalf("expected a start diagnostic, got %v", err)
	}
	if strings.Join(stopped, ",") != "ok" {
		t.Fatalf("only started components must be stopped, got %v", stopped)
	}
}

func TestGracefulShutdownWaitsForInflightRequests(t *testing.T) {
	app := torgetest.NewApp(t)
	started := make(chan struct{})
	release := make(chan struct{})
	app.GET("/slow", func(c *torge.Context) error {
		close(started)
		<-release
		return c.String(200, "done")
	})
	var stopOrder []string
	var mu sync.Mutex
	app.OnStop("db", func(context.Context) error {
		mu.Lock()
		stopOrder = append(stopOrder, "db")
		mu.Unlock()
		return nil
	})
	ln := newLocalListener(t)
	serveErr := make(chan error, 1)
	go func() { serveErr <- app.Serve(ln) }()
	waitRunning(t, app)

	respCh := make(chan string, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			respCh <- "error: " + err.Error()
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		mu.Lock()
		stopOrder = append(stopOrder, "response")
		mu.Unlock()
		respCh <- string(b)
	}()
	<-started
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	if app.State() != torge.StateStopping {
		t.Fatalf("expected stopping, got %s", app.State())
	}
	close(release)
	if got := <-respCh; got != "done" {
		t.Fatalf("in-flight request must complete, got %q", got)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(stopOrder, ",") != "response,db" {
		t.Fatalf("dependencies must close after in-flight requests finish: %v", stopOrder)
	}
}

func TestRequestContextCancellationPropagates(t *testing.T) {
	app := torgetest.NewApp(t)
	observed := make(chan error, 1)
	app.GET("/wait", func(c *torge.Context) error {
		select {
		case <-c.Context().Done():
			observed <- c.Context().Err()
			return c.Context().Err()
		case <-time.After(5 * time.Second):
			observed <- nil
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	torgetest.New(t, app).GET("/wait").WithContext(ctx).Do().ExpectStatus(torge.StatusClientClosedRequest)
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler must observe cancellation, got %v", err)
	}
}

func TestConcurrentRequestsDoNotShareState(t *testing.T) {
	app := torgetest.NewApp(t)
	type key struct{}
	app.Use(func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			c.Set(key{}, c.Param("id")) // empty before routing
			return next(c)
		}
	})
	app.GET("/echo/:id", func(c *torge.Context) error {
		c.Set(key{}, c.Param("id"))
		time.Sleep(time.Millisecond)
		v, _ := c.Get(key{})
		return c.String(200, v.(string)+"|"+c.Query("n"))
	})
	tc := torgetest.New(t, app)
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			id := fmt.Sprint(i)
			res := tc.GET("/echo/"+id).Query("n", id).Do()
			if string(res.Body) != id+"|"+id {
				t.Errorf("request %d got %q", i, res.Body)
			}
		})
	}
	wg.Wait()
}

func TestServeHTTPBeforeStart(t *testing.T) {
	app := torge.New(torge.WithLogger(discardLogger()))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 503 {
		t.Fatalf("expected 503 before start, got %d", rec.Code)
	}
}

func TestModulesAttributeRoutes(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Register(torge.ModuleFunc(func(a *torge.App) error {
		a.GET("/from-module", getUser)
		return nil
	}), userModule{})
	app.Register(userModule{})
	err := app.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), torge.DiagDuplicateModule) {
		t.Fatalf("expected duplicate module diagnostic, got %v", err)
	}
	var mods []string
	for _, r := range app.Routes() {
		mods = append(mods, r.Path+"="+r.Module)
	}
	got := strings.Join(mods, ",")
	if !strings.Contains(got, "/users=users") || !strings.Contains(got, "/from-module=torge_test.TestModulesAttributeRoutes") {
		t.Fatalf("unexpected module attribution %s", got)
	}
}

type userModule struct{}

func (userModule) Name() string { return "users" }

func (userModule) Register(app *torge.App) error {
	app.GET("/users", getUser)
	return nil
}

func TestListenAddrUsesPORT(t *testing.T) {
	t.Setenv("PORT", "")
	if a, _ := torge.ListenAddr(""); a != ":8080" {
		t.Fatalf("default: %s", a)
	}
	t.Setenv("PORT", "10000") // Render's default
	if a, _ := torge.ListenAddr(""); a != ":10000" {
		t.Fatalf("PORT: %s", a)
	}
	if a, _ := torge.ListenAddr("127.0.0.1:9000"); a != "127.0.0.1:9000" {
		t.Fatalf("an explicit address must win: %s", a)
	}
	t.Setenv("PORT", "http")
	if _, err := torge.ListenAddr(""); err == nil {
		t.Fatal("an invalid PORT must be an error")
	}
}

// TestClientsReceiveContentLength checks the real server's view: small bodies
// get Content-Length from net/http, larger ones and HEAD responses from Torge.
func TestClientsReceiveContentLength(t *testing.T) {
	app := torgetest.NewApp(t)
	big := strings.Repeat("x", 5000)
	app.GET("/small", func(c *torge.Context) error { return c.JSON(200, map[string]int{"n": 1}) })
	app.GET("/big", func(c *torge.Context) error { return c.String(200, big) })
	srv := torgetest.Server(t, app)
	for path, want := range map[string]int64{"/small": int64(len(`{"n":1}` + "\n")), "/big": 5000} {
		for _, method := range []string{"GET", "HEAD"} {
			req, _ := http.NewRequest(method, srv.URL+path, nil)
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.ContentLength != want || len(res.TransferEncoding) != 0 {
				t.Errorf("%s %s: Content-Length %d (want %d), Transfer-Encoding %v",
					method, path, res.ContentLength, want, res.TransferEncoding)
			}
		}
	}
}
