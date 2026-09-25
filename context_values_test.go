package torge_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/correlation"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type testUser string

func (u testUser) ID() string { return string(u) }

// Request IDs and principals are attached to the request context lazily; every
// way of observing the context must still see them.
func TestContextCarriesRequestIDAndUser(t *testing.T) {
	app := torgetest.NewApp(t)
	withUser := func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			c.SetUser(testUser("ada"))
			return next(c)
		}
	}
	app.GET("/ctx", func(c *torge.Context) error {
		id := c.RequestID()
		if id == "" {
			t.Error("request ID missing")
		}
		for name, ctx := range map[string]context.Context{
			"Context()":           c.Context(),
			"Request().Context()": c.Request().Context(),
		} {
			if got := correlation.RequestID(ctx); got != id {
				t.Errorf("%s: request ID %q, want %q", name, got, id)
			}
			if u := torge.UserFrom(ctx); u == nil || u.ID() != "ada" {
				t.Errorf("%s: user %v, want ada", name, u)
			}
		}
		// Values survive a derived context set by middleware.
		ctx, cancel := context.WithTimeout(c.Context(), time.Minute)
		defer cancel()
		c.SetContext(ctx)
		if correlation.RequestID(c.Context()) != id || torge.UserFrom(c.Context()) == nil {
			t.Error("values lost after SetContext")
		}
		return c.NoContent(http.StatusNoContent)
	}, torge.Middleware(withUser))

	app.GET("/std", torge.WrapFunc(func(w http.ResponseWriter, r *http.Request) {
		if correlation.RequestID(r.Context()) == "" || torge.UserFrom(r.Context()) == nil {
			t.Error("standard handlers must see the request ID and user")
		}
		w.WriteHeader(http.StatusNoContent)
	}), torge.Middleware(withUser))

	app.GET("/std-mw", func(c *torge.Context) error { return c.NoContent(http.StatusNoContent) },
		torge.WrapMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if correlation.RequestID(r.Context()) == "" {
					t.Error("standard middleware must see the request ID")
				}
				next.ServeHTTP(w, r)
			})
		}))

	// The user can be replaced after the context was already observed.
	app.GET("/relogin", func(c *torge.Context) error {
		_ = c.Context()
		c.SetUser(testUser("grace"))
		if u := torge.UserFrom(c.Context()); u == nil || u.ID() != "grace" {
			t.Errorf("user after second SetUser = %v, want grace", u)
		}
		return c.NoContent(http.StatusNoContent)
	})

	tc := torgetest.New(t, app)
	for _, p := range []string{"/ctx", "/std", "/std-mw", "/relogin"} {
		tc.GET(p).Do().ExpectStatus(http.StatusNoContent)
	}
}

// The response header shares storage with the request ID; changing the header
// through the http.Header API must not change the ID.
func TestRequestIDHeaderIsIndependent(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/", func(c *torge.Context) error {
		id := c.RequestID()
		h := c.Response().Header()
		h.Add("X-Request-Id", "extra")
		h.Set("X-Request-Id", "replaced")
		h.Del("X-Request-Id")
		if c.RequestID() != id || correlation.RequestID(c.Context()) != id {
			t.Errorf("request ID changed to %q", c.RequestID())
		}
		return c.NoContent(http.StatusNoContent)
	})
	res := torgetest.New(t, app).GET("/").Do().ExpectStatus(http.StatusNoContent)
	if got := res.Header.Get("X-Request-Id"); got != "" {
		t.Errorf("deleted header still sent: %q", got)
	}
}
