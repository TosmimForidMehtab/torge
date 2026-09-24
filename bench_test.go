package torge_test

// Benchmarks for the request hot path. Run with:
//
//	go test -run '^$' -bench . -benchmem
//
// Compare BenchmarkStdlibMux* (net/http.ServeMux) with BenchmarkTorge* to see
// the framework's overhead. The default pipeline (request IDs, access log,
// recovery, security headers) is benchmarked separately from a bare pipeline
// so each stage's cost is visible. Cross-framework comparisons (Gin, Echo,
// Fiber) live in the separate benchmarks module to keep this module
// dependency-free.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
)

type discardWriter struct{ h http.Header }

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *discardWriter) WriteHeader(int)             {}

func newWriter() *discardWriter { return &discardWriter{h: make(http.Header, 8)} }

func benchApp(b *testing.B, opts ...torge.Option) *torge.App {
	b.Helper()
	base := []torge.Option{torge.WithEnv(torge.Production), torge.WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil)))}
	return torge.New(append(base, opts...)...)
}

func serveBench(b *testing.B, h http.Handler, req *http.Request) {
	b.Helper()
	w := newWriter()
	b.ReportAllocs()
	for b.Loop() {
		clear(w.h)
		h.ServeHTTP(w, req)
	}
}

var githubRoutes = []string{
	"/user", "/user/repos", "/user/orgs", "/users/:user", "/users/:user/repos", "/users/:user/orgs",
	"/repos/:owner/:repo", "/repos/:owner/:repo/issues", "/repos/:owner/:repo/issues/:number",
	"/repos/:owner/:repo/pulls/:number/files", "/orgs/:org/members/:user", "/gists/:id/star",
	"/search/repositories", "/emojis", "/static/*filepath",
}

func routedApp(b *testing.B, opts ...torge.Option) *torge.App {
	app := benchApp(b, opts...)
	h := func(c *torge.Context) error { return nil }
	for _, r := range githubRoutes {
		app.GET(r, h)
	}
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	return app
}

func BenchmarkTorgeRoutingStatic(b *testing.B) {
	serveBench(b, routedApp(b, torge.WithoutDefaults()), httptest.NewRequest("GET", "/user/repos", nil))
}

func BenchmarkTorgeRoutingParams(b *testing.B) {
	serveBench(b, routedApp(b, torge.WithoutDefaults()), httptest.NewRequest("GET", "/repos/torge/torge/issues/42", nil))
}

func BenchmarkTorgeRoutingWildcard(b *testing.B) {
	serveBench(b, routedApp(b, torge.WithoutDefaults()), httptest.NewRequest("GET", "/static/css/app/site.css", nil))
}

func BenchmarkStdlibMuxParams(b *testing.B) {
	mux := http.NewServeMux()
	h := func(http.ResponseWriter, *http.Request) {}
	for _, r := range githubRoutes {
		p := r
		for _, seg := range strings.Split(r, "/") {
			if strings.HasPrefix(seg, ":") {
				p = strings.Replace(p, seg, "{"+seg[1:]+"}", 1)
			}
		}
		p = strings.Replace(p, "*filepath", "{filepath...}", 1)
		mux.HandleFunc("GET "+p, h)
	}
	serveBench(b, mux, httptest.NewRequest("GET", "/repos/torge/torge/issues/42", nil))
}

func BenchmarkTorgeDefaultPipeline(b *testing.B) {
	serveBench(b, routedApp(b), httptest.NewRequest("GET", "/repos/torge/torge/issues/42", nil))
}

func BenchmarkTorgeMiddlewareChain5(b *testing.B) {
	app := benchApp(b, torge.WithoutDefaults())
	pass := func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error { return next(c) }
	}
	app.Use(pass, pass, pass, pass, pass)
	app.GET("/", func(c *torge.Context) error { return nil })
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	serveBench(b, app, httptest.NewRequest("GET", "/", nil))
}

type benchUser struct {
	ID    int      `json:"id"`
	Name  string   `json:"name"`
	Email string   `json:"email"`
	Tags  []string `json:"tags"`
}

func BenchmarkTorgeJSONResponse(b *testing.B) {
	app := benchApp(b, torge.WithoutDefaults())
	u := benchUser{ID: 1, Name: "Ada Lovelace", Email: "ada@example.com", Tags: []string{"math", "engines"}}
	app.GET("/user", func(c *torge.Context) error { return c.JSON(200, u) })
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	serveBench(b, app, httptest.NewRequest("GET", "/user", nil))
}

func BenchmarkTorgeTypedHandler(b *testing.B) {
	type in struct {
		ID   int `path:"id" validate:"min=1"`
		Body benchUser
	}
	app := benchApp(b, torge.WithoutDefaults())
	torge.Put(app, "/users/:id", func(c *torge.Context, in *in) (*benchUser, error) { return &in.Body, nil })
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	body := `{"id":1,"name":"Ada","email":"ada@example.com","tags":["a"]}`
	w := newWriter()
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest("PUT", "/users/7", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		clear(w.h)
		app.ServeHTTP(w, req)
	}
}

func BenchmarkTorgeErrorResponse(b *testing.B) {
	app := benchApp(b, torge.WithoutDefaults())
	notFound := torge.NotFound("USER_NOT_FOUND", "User does not exist")
	app.GET("/user", func(c *torge.Context) error { return notFound })
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	serveBench(b, app, httptest.NewRequest("GET", "/user", nil))
}

type benchRepo struct{}

func BenchmarkTorgeDependencyResolution(b *testing.B) {
	app := benchApp(b, torge.WithoutDefaults())
	app.Provide(func() *benchRepo { return &benchRepo{} })
	app.GET("/", func(c *torge.Context) error {
		_, err := torge.Dep[*benchRepo](c)
		return err
	})
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	serveBench(b, app, httptest.NewRequest("GET", "/", nil))
}
