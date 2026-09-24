package benchmarks

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"github.com/TosmimForidMehtab/torge"
)

// Each framework serves the same route table. Handlers do the same work:
// read one path parameter and write a small JSON object.

var routes = []string{
	"/user", "/user/repos", "/users/:user", "/users/:user/repos",
	"/repos/:owner/:repo", "/repos/:owner/:repo/issues", "/repos/:owner/:repo/issues/:number",
	"/orgs/:org/members/:user", "/search/repositories", "/emojis",
}

const target = "/repos/torge/torge/issues/42"

type payload struct {
	Number string `json:"number"`
	OK     bool   `json:"ok"`
}

type sink struct{ h http.Header }

func (s *sink) Header() http.Header         { return s.h }
func (s *sink) Write(b []byte) (int, error) { return len(b), nil }
func (s *sink) WriteHeader(int)             {}

func run(b *testing.B, h http.Handler) {
	b.Helper()
	req := httptest.NewRequest("GET", target, nil)
	w := &sink{h: make(http.Header)}
	// Sanity check: every framework must produce the same body.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got payload
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Number != "42" {
		b.Fatalf("unexpected response %d %q", rec.Code, rec.Body)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		clear(w.h)
		h.ServeHTTP(w, req)
	}
}

func torgeApp(b *testing.B, opts ...torge.Option) http.Handler {
	opts = append([]torge.Option{torge.WithEnv(torge.Production),
		torge.WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil)))}, opts...)
	app := torge.New(opts...)
	h := func(c *torge.Context) error {
		return c.JSON(200, payload{Number: c.Param("number"), OK: true})
	}
	for _, r := range routes {
		app.GET(r, h)
	}
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	return app
}

// BenchmarkTorgeBare disables the built-in pipeline for a like-for-like
// comparison with routers that do nothing else by default.
func BenchmarkTorgeBare(b *testing.B) { run(b, torgeApp(b, torge.WithoutDefaults())) }

// BenchmarkTorgeDefaults includes request IDs, access logging (to a discard
// sink), recovery and security headers.
func BenchmarkTorgeDefaults(b *testing.B) { run(b, torgeApp(b)) }

func BenchmarkStdlib(b *testing.B) {
	mux := http.NewServeMux()
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload{Number: r.PathValue("number"), OK: true})
	}
	for _, r := range routes {
		segs := strings.Split(r, "/")
		for i, s := range segs {
			if strings.HasPrefix(s, ":") {
				segs[i] = "{" + s[1:] + "}"
			}
		}
		mux.HandleFunc("GET "+strings.Join(segs, "/"), h)
	}
	run(b, mux)
}

func BenchmarkGin(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	h := func(c *gin.Context) { c.JSON(200, payload{Number: c.Param("number"), OK: true}) }
	for _, r := range routes {
		e.GET(r, h)
	}
	run(b, e)
}

func BenchmarkEcho(b *testing.B) {
	e := echo.New()
	h := func(c echo.Context) error { return c.JSON(200, payload{Number: c.Param("number"), OK: true}) }
	for _, r := range routes {
		e.GET(r, h)
	}
	run(b, e)
}

// "With middleware" variants compare like with like: each framework logs one
// line per request (to a discard sink) and recovers panics, as Torge's
// defaults do. Torge's defaults additionally assign request IDs and set
// security headers; Echo's variant includes its RequestID middleware.

func BenchmarkGinWithLoggerRecovery(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(gin.LoggerWithWriter(io.Discard), gin.Recovery())
	h := func(c *gin.Context) { c.JSON(200, payload{Number: c.Param("number"), OK: true}) }
	for _, r := range routes {
		e.GET(r, h)
	}
	run(b, e)
}

func BenchmarkEchoWithLoggerRecoveryRequestID(b *testing.B) {
	e := echo.New()
	logger := echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		LogMethod: true, LogURI: true, LogStatus: true, LogLatency: true, LogRemoteIP: true, LogRequestID: true,
		LogValuesFunc: func(_ echo.Context, v echomw.RequestLoggerValues) error {
			_, err := fmt.Fprintf(io.Discard, "%s %s %s %d %s %s\n", v.RequestID, v.Method, v.URI, v.Status, v.Latency, v.RemoteIP)
			return err
		},
	})
	e.Use(echomw.RequestID(), logger, echomw.Recover())
	h := func(c echo.Context) error { return c.JSON(200, payload{Number: c.Param("number"), OK: true}) }
	for _, r := range routes {
		e.GET(r, h)
	}
	run(b, e)
}

// BenchmarkTorgeDefaultsTextLog uses slog's text handler, matching the text
// log lines written by Gin's and Echo's loggers.
func BenchmarkTorgeDefaultsTextLog(b *testing.B) {
	run(b, torgeApp(b, torge.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))))
}
