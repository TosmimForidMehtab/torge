package torge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestSendVariants(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/text", func(c *torge.Context) error { return c.Send(200, "hello") })
	app.GET("/bytes", func(c *torge.Context) error { return c.Send(200, []byte{1, 2, 3}) })
	app.GET("/json", func(c *torge.Context) error { return c.Send(201, User{ID: "1", Name: "Ada"}) })
	app.GET("/nil", func(c *torge.Context) error { return c.Send(204, nil) })
	tc := torgetest.New(t, app)

	tc.GET("/text").Do().ExpectStatus(200).
		ExpectHeader("Content-Type", "text/plain; charset=utf-8").
		ExpectBody("hello")
	tc.GET("/bytes").Do().ExpectStatus(200).
		ExpectHeader("Content-Type", "application/octet-stream")
	tc.GET("/json").Do().ExpectStatus(201).ExpectJSONPath("name", "Ada")
	tc.GET("/nil").Do().ExpectStatus(204)
}

func TestLocationTypeLinks(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/go", func(c *torge.Context) error {
		c.Location("/users/1")
		return c.Send(201, "created")
	})
	app.GET("/typed", func(c *torge.Context) error {
		c.Type("html")
		return c.Status(200)
	})
	app.GET("/linked", func(c *torge.Context) error {
		c.Links(map[string]string{"next": "/users?page=3", "last": "/users?page=9"})
		return c.Status(200)
	})
	tc := torgetest.New(t, app)

	tc.GET("/go").Do().ExpectStatus(201).ExpectHeader("Location", "/users/1")
	tc.GET("/typed").Do().ExpectStatus(200).ExpectHeader("Content-Type", "text/html; charset=utf-8")
	tc.GET("/linked").Do().ExpectStatus(200).
		ExpectHeader("Link", `</users?page=9>; rel="last", </users?page=3>; rel="next"`)
}

func TestClearCookie(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/logout", func(c *torge.Context) error {
		c.ClearCookie("session")
		return c.Status(200)
	})
	tc := torgetest.New(t, app)

	res := tc.GET("/logout").Do().ExpectStatus(200)
	setCookie := res.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "session=;") || !strings.Contains(setCookie, "1970") {
		t.Fatalf("bad clear-cookie header %q", setCookie)
	}
}

func TestHostnameSecureXHR(t *testing.T) {
	app := torgetest.NewApp(t)

	https := httptest.NewRequest("GET", "https://Example.COM:443/x", nil)
	c := app.NewContext(httptest.NewRecorder(), https)
	if c.Hostname() != "example.com" {
		t.Fatalf("bad hostname %q", c.Hostname())
	}
	if !c.Secure() {
		t.Fatal("https request must be secure")
	}

	plain := httptest.NewRequest("GET", "http://example.com/x", nil)
	plain.Header.Set("X-Requested-With", "XMLHttpRequest")
	p := app.NewContext(httptest.NewRecorder(), plain)
	if p.Secure() {
		t.Fatal("http request must not be secure")
	}
	if !p.XHR() {
		t.Fatal("expected XHR request")
	}
	if c.XHR() {
		t.Fatal("https request without header must not be XHR")
	}
}

func TestIs(t *testing.T) {
	app := torgetest.NewApp(t)
	app.POST("/is", func(c *torge.Context) error {
		return c.JSON(200, map[string]bool{"json": c.Is("json"), "html": c.Is("html")})
	})
	tc := torgetest.New(t, app)

	tc.POST("/is").Body(strings.NewReader(`{}`), "application/hal+json").Do().
		ExpectStatus(200).ExpectJSONPath("json", true)
	tc.POST("/is").Body(strings.NewReader(`{}`), "text/html").Do().
		ExpectStatus(200).ExpectJSONPath("html", true).ExpectJSONPath("json", false)
}

func TestAccepts(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/accepts", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"match": c.Accepts("application/json", "text/html")})
	})
	tc := torgetest.New(t, app)

	tc.GET("/accepts").Header("Accept", "text/html, application/json;q=0.9").Do().
		ExpectStatus(200).ExpectJSONPath("match", "text/html")
	tc.GET("/accepts").Header("Accept", "application/json").Do().
		ExpectStatus(200).ExpectJSONPath("match", "application/json")
	tc.GET("/accepts").Do().ExpectStatus(200).ExpectJSONPath("match", "application/json")
	tc.GET("/accepts").Header("Accept", "text/csv").Do().
		ExpectStatus(200).ExpectJSONPath("match", "")
}

func TestFormat(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/user", func(c *torge.Context) error {
		user := User{ID: "1", Name: "Ada", Email: "ada@example.com"}
		return c.Format(map[string]torge.Handler{
			"html": func(c *torge.Context) error { return c.HTML(200, "<h1>Ada</h1>") },
			"json": func(c *torge.Context) error { return c.JSON(200, user) },
		})
	})
	tc := torgetest.New(t, app)

	tc.GET("/user").Header("Accept", "application/json").Do().
		ExpectStatus(200).ExpectJSONPath("name", "Ada")
	tc.GET("/user").Header("Accept", "text/html").Do().
		ExpectStatus(200).ExpectBody("<h1>Ada</h1>")
	tc.GET("/user").Header("Accept", "text/csv").Do().
		ExpectStatus(406).ExpectErrorCode(torge.CodeNotAcceptable)
}

func TestNotAcceptableConstructor(t *testing.T) {
	err := torge.NotAcceptable(torge.CodeNotAcceptable, "nope")
	if err.Status != http.StatusNotAcceptable || err.Code != torge.CodeNotAcceptable {
		t.Fatalf("bad 406 error %+v", err)
	}
}
