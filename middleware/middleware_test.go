package middleware_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/middleware"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestCORS(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Use(middleware.CORS(middleware.CORSConfig{
		AllowOrigins:     []string{"https://app.example.com", "https://*.example.org"},
		AllowCredentials: true,
	}))
	app.POST("/items", func(c *torge.Context) error { return c.NoContent(201) })
	tc := torgetest.New(t, app)

	tc.OPTIONS("/items").Header("Origin", "https://app.example.com").
		Header("Access-Control-Request-Method", "POST").Header("Access-Control-Request-Headers", "Content-Type").Do().
		ExpectStatus(204).
		ExpectHeader("Access-Control-Allow-Origin", "https://app.example.com").
		ExpectHeader("Access-Control-Allow-Credentials", "true").
		ExpectHeader("Access-Control-Allow-Headers", "Content-Type").
		ExpectHeader("Access-Control-Max-Age", "600")
	tc.POST("/items").Header("Origin", "https://a.example.org").Do().
		ExpectStatus(201).ExpectHeader("Access-Control-Allow-Origin", "https://a.example.org").
		ExpectHeader("Access-Control-Expose-Headers", "X-Request-Id")
	res := tc.POST("/items").Header("Origin", "https://evil.com").Do().ExpectStatus(201)
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("disallowed origins must not receive CORS headers")
	}
	res = tc.OPTIONS("/items").Header("Origin", "https://example.org").Header("Access-Control-Request-Method", "POST").Do()
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("the bare parent domain must not match a subdomain wildcard")
	}
}

func TestCORSRejectsUnsafeConfiguration(t *testing.T) {
	for _, cfg := range []middleware.CORSConfig{
		{AllowOrigins: []string{"*"}, AllowCredentials: true},
		{AllowOrigins: []string{"https://example.com/"}},
		{AllowOrigins: []string{"example.com"}},
		{},
	} {
		_, err := middleware.NewCORS(cfg)
		var d *torge.Diagnostic
		if !errors.As(err, &d) || d.Code != torge.DiagInvalidConfig {
			t.Errorf("%+v: expected a diagnostic, got %v", cfg, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("CORS must panic on invalid configuration")
		}
	}()
	middleware.CORS(middleware.CORSConfig{AllowOrigins: []string{"*"}, AllowCredentials: true})
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestCompress(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Use(middleware.Compress(middleware.CompressConfig{MinLength: 100}))
	big := strings.Repeat("torge ", 100)
	app.GET("/big", func(c *torge.Context) error { return c.String(200, big) })
	app.GET("/small", func(c *torge.Context) error { return c.String(200, "tiny") })
	app.GET("/png", func(c *torge.Context) error { return c.Bytes(200, "image/png", []byte(big)) })
	app.GET("/error", func(c *torge.Context) error {
		return torge.BadRequest("BAD", strings.Repeat("x", 200))
	})
	app.GET("/stream", func(c *torge.Context) error {
		return c.Stream(200, "text/plain", func(w io.Writer) error {
			_, err := io.WriteString(w, "chunk")
			return err
		})
	})
	tc := torgetest.New(t, app).WithHeader("Accept-Encoding", "gzip")

	res := tc.GET("/big").Do().ExpectStatus(200).ExpectHeader("Content-Encoding", "gzip").ExpectHeader("Vary", "Accept-Encoding")
	if gunzip(t, res.Body) != big || res.Header.Get("Content-Length") != "" {
		t.Fatal("compressed body mismatch or stale Content-Length")
	}
	tc.GET("/small").Do().ExpectHeader("Content-Encoding", "").ExpectBody("tiny")
	tc.GET("/png").Do().ExpectHeader("Content-Encoding", "")
	res = tc.GET("/error").Do().ExpectStatus(400).ExpectHeader("Content-Encoding", "gzip")
	if !strings.Contains(gunzip(t, res.Body), `"code":"BAD"`) {
		t.Fatal("errors must render through the compressor")
	}
	res = tc.GET("/stream").Do().ExpectHeader("Content-Encoding", "gzip")
	if gunzip(t, res.Body) != "chunk" {
		t.Fatal("streamed body mismatch")
	}
	torgetest.New(t, app).GET("/big").Header("Accept-Encoding", "gzip;q=0").Do().ExpectHeader("Content-Encoding", "")
}

func TestTimeout(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/slow", func(c *torge.Context) error {
		select {
		case <-c.Context().Done():
			return c.Context().Err()
		case <-time.After(time.Second):
			return c.String(200, "late")
		}
	}, middleware.Timeout(20*time.Millisecond))
	start := time.Now()
	torgetest.New(t, app).GET("/slow").Do().ExpectStatus(504).ExpectErrorCode(torge.CodeTimeout)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("timeout not applied")
	}
}

func TestCSRF(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Use(middleware.CSRF(middleware.CSRFConfig{TrustedOrigins: []string{"https://admin.example.com"}}))
	app.POST("/transfer", func(c *torge.Context) error { return c.NoContent(204) })
	tc := torgetest.New(t, app)
	tc.POST("http://bank.example.com/transfer").Header("Sec-Fetch-Site", "cross-site").Do().
		ExpectStatus(403).ExpectErrorCode("CSRF_REJECTED")
	tc.POST("http://bank.example.com/transfer").Header("Sec-Fetch-Site", "same-origin").Do().ExpectStatus(204)
	tc.POST("http://bank.example.com/transfer").Header("Origin", "https://admin.example.com").Do().ExpectStatus(204)
	tc.POST("http://bank.example.com/transfer").Do().ExpectStatus(204) // non-browser client
}

func TestStatic(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":      {Data: []byte("<h1>home</h1>")},
		"css/site.css":    {Data: []byte("body{}")},
		"docs/index.html": {Data: []byte("docs")},
		"empty/.keep":     {Data: nil},
	}
	app := torgetest.NewApp(t)
	app.Use(middleware.Static(middleware.StaticConfig{Root: fsys, Prefix: "/assets", MaxAge: time.Hour}))
	app.GET("/assets/api", func(c *torge.Context) error { return c.String(200, "route") })
	tc := torgetest.New(t, app)
	tc.GET("/assets/css/site.css").Do().ExpectStatus(200).ExpectBody("body{}").
		ExpectHeader("Cache-Control", "public, max-age=3600").ExpectHeader("Content-Type", "text/css; charset=utf-8")
	tc.GET("/assets/").Do().ExpectStatus(200).ExpectBody("<h1>home</h1>")
	tc.GET("/assets/docs").Do().ExpectStatus(200).ExpectBody("docs")
	tc.GET("/assets/empty/").Do().ExpectStatus(404)
	tc.GET("/assets/../../etc/passwd").Do().ExpectStatus(404)
	tc.GET("/assets/api").Do().ExpectBody("route")
	tc.GET("/other.css").Do().ExpectStatus(404)

	spa := torgetest.NewApp(t)
	spa.Use(middleware.Static(middleware.StaticConfig{Root: fsys, SPA: true}))
	stc := torgetest.New(t, spa)
	stc.GET("/dashboard/settings").Do().ExpectStatus(200).ExpectBody("<h1>home</h1>")
	stc.GET("/missing.js").Do().ExpectStatus(404)
}

func TestResponseCache(t *testing.T) {
	store := cache.NewMemory()
	defer store.Close()
	var calls atomic.Int32
	app := torgetest.NewApp(t)
	app.GET("/items", func(c *torge.Context) error {
		n := calls.Add(1)
		return c.JSON(200, map[string]any{"n": n, "q": c.Query("q")})
	}, middleware.Cache(middleware.CacheConfig{Store: store, TTL: time.Minute}))
	app.GET("/private", func(c *torge.Context) error {
		calls.Add(1)
		c.Header("Cache-Control", "private")
		return c.String(200, "mine")
	}, middleware.Cache(middleware.CacheConfig{Store: store}))
	tc := torgetest.New(t, app)

	tc.GET("/items").Query("q", "a").Do().ExpectHeader("X-Cache", "MISS").ExpectJSONPath("n", 1)
	tc.GET("/items").Query("q", "a").Do().ExpectHeader("X-Cache", "HIT").ExpectJSONPath("n", 1).
		ExpectHeader("Content-Type", "application/json; charset=utf-8")
	tc.GET("/items").Query("q", "b").Do().ExpectHeader("X-Cache", "MISS").ExpectJSONPath("n", 2)
	tc.GET("/items").Query("q", "a").Header("Authorization", "Bearer x").Do().ExpectJSONPath("n", 3)
	tc.GET("/private").Do()
	tc.GET("/private").Do().ExpectHeader("X-Cache", "MISS")
	if calls.Load() != 5 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if err := store.DeletePrefix(context.Background(), "torge:response:"); err != nil {
		t.Fatal(err)
	}
	tc.GET("/items").Query("q", "a").Do().ExpectHeader("X-Cache", "MISS")
}
