package torge_test

import (
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func okHandler(c *torge.Context) error { return c.JSON(200, map[string]string{"ok": "true"}) }

func TestSunsetHeaders(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/old", okHandler, torge.Sunset("Mon, 01 Jun 2026 00:00:00 GMT"))
	app.GET("/older", okHandler, torge.Sunset(""))
	tc := torgetest.New(t, app)

	tc.GET("/old").Do().
		ExpectStatus(200).
		ExpectHeader("Deprecation", "true").
		ExpectHeader("Sunset", "Mon, 01 Jun 2026 00:00:00 GMT")
	res := tc.GET("/older").Do().
		ExpectStatus(200).
		ExpectHeader("Deprecation", "true")
	if got := res.Header.Get("Sunset"); got != "" {
		t.Fatalf("empty sunset date must omit the header, got %q", got)
	}
}

func TestVersionGroup(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "T", Version: "1"}})
	v1 := app.Version("v1")
	v1.GET("/users", okHandler)
	legacy := app.Version("v0", torge.Sunset("Mon, 01 Jun 2026 00:00:00 GMT"))
	legacy.GET("/users", okHandler)
	tc := torgetest.New(t, app)

	tc.GET("/api/v1/users").Do().ExpectStatus(200)
	tc.GET("/api/v0/users").Do().
		ExpectStatus(200).
		ExpectHeader("Deprecation", "true")

	var doc openapi.Document
	tc.GET("/openapi.json").Do().ExpectStatus(200).DecodeJSON(&doc)
	op := doc.Paths["/api/v1/users"]["get"]
	if !containsStr(op.Tags, "v1") {
		t.Fatalf("version tag missing: %+v", op.Tags)
	}
	if !doc.Paths["/api/v0/users"]["get"].Deprecated {
		t.Fatal("sunset route must be deprecated in OpenAPI")
	}

	found := false
	for _, r := range app.Routes() {
		if r.Path == "/api/v0/users" {
			found = true
			if !r.Deprecated {
				t.Fatalf("route info must flag deprecation: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("versioned route missing from Routes()")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
