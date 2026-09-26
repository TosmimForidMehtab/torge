package middleware_test

import (
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/middleware"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestAPIVersion(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Use(middleware.APIVersion("1", "2"))
	app.GET("/v", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"version": middleware.RequestVersion(c)})
	})
	tc := torgetest.New(t, app)

	// No header resolves to the first (oldest) version, so header-less
	// clients never silently switch to a newer one.
	tc.GET("/v").Do().
		ExpectStatus(200).
		ExpectJSONPath("version", "1").
		ExpectHeader("API-Version", "1").
		ExpectHeader("Vary", "Accept-Version")

	tc.GET("/v").Header("Accept-Version", "2").Do().
		ExpectStatus(200).
		ExpectJSONPath("version", "2").
		ExpectHeader("API-Version", "2")

	tc.GET("/v").Header("Accept-Version", "9").Do().
		ExpectStatus(400).ExpectErrorCode(torge.CodeBadRequest)
}

func TestAPIVersionConfigPanics(t *testing.T) {
	for name, fn := range map[string]func(){
		"empty": func() { middleware.APIVersion() },
		"blank": func() { middleware.APIVersion("1", "") },
		"space": func() { middleware.APIVersion("1 2") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s: expected panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestRequestVersionWithoutMiddleware(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/v", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"version": middleware.RequestVersion(c)})
	})
	tc := torgetest.New(t, app)
	tc.GET("/v").Do().ExpectStatus(200).ExpectJSONPath("version", "")
}
