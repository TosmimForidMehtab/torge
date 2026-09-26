package middleware

import (
	"fmt"
	"strings"

	"github.com/TosmimForidMehtab/torge"
)

// apiVersionKey carries the negotiated API version in the request.
type apiVersionKey struct{}

// APIVersion negotiates an API version from the Accept-Version header,
// for example "1" or "2". The first allowed version is the default when
// the client sends no header: header-less clients stay pinned to the
// oldest version instead of silently switching to a new, possibly
// breaking, one the day it is added, so list versions oldest-first.
// Unknown versions fail with a 400 listing the supported versions. The
// resolved version is sent back in an API-Version response header, a Vary:
// Accept-Version header is set so caches and CDNs key on the version, and
// the version is available to handlers with RequestVersion:
//
//	v1 := app.Version("v1")
//	v2 := app.Version("v2")
//	app.Use(middleware.APIVersion("1", "2"))
//
//	func show(c *torge.Context) error {
//		if middleware.RequestVersion(c) == "1" {
//			return c.JSON(200, legacyView())
//		}
//		return c.JSON(200, currentView())
//	}
//
// For media-type negotiation (Accept: application/vnd.api.v2+json) use
// Context.Format in the handler instead.
func APIVersion(allowed ...string) torge.Middleware {
	if len(allowed) == 0 {
		panic(&torge.Diagnostic{
			Code: torge.DiagInvalidConfig, What: "middleware.APIVersion needs at least one version",
			Why: "with no allowed versions every request would fail",
			Fix: `pass the versions you serve, e.g. middleware.APIVersion("1", "2")`,
		})
	}
	for _, v := range allowed {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, " ,") {
			panic(&torge.Diagnostic{
				Code: torge.DiagInvalidConfig, What: fmt.Sprintf("invalid API version %q", v),
				Why: "versions travel in a header and must be single tokens",
				Fix: `use plain tokens like "1" or "2"`,
			})
		}
	}
	deflt := allowed[0]
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			v := strings.TrimSpace(c.GetHeader("Accept-Version"))
			if v == "" {
				v = deflt
			} else {
				ok := false
				for _, a := range allowed {
					if v == a {
						ok = true
						break
					}
				}
				if !ok {
					return torge.BadRequest(torge.CodeBadRequest,
						fmt.Sprintf("Unsupported API version %q; supported: %s", v, strings.Join(allowed, ", ")))
				}
			}
			c.Set(apiVersionKey{}, v)
			c.Header("API-Version", v)
			addVary(c, "Accept-Version")
			return next(c)
		}
	}
}

// addVary appends value to the Vary response header without duplicating it.
func addVary(c *torge.Context, value string) {
	h := c.Response().Header()
	for v := range strings.SplitSeq(h.Get("Vary"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return
		}
	}
	if h.Get("Vary") == "" {
		h.Set("Vary", value)
	} else {
		h.Set("Vary", h.Get("Vary")+", "+value)
	}
}

// RequestVersion returns the version negotiated by APIVersion, or "" when
// the middleware is not in use.
func RequestVersion(c *torge.Context) string {
	v, ok := c.Get(apiVersionKey{})
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
