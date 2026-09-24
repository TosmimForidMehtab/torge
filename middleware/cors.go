// Package middleware provides Torge's optional official middleware. Each
// middleware is independent and replaceable: CORS, Compress, Timeout, CSRF,
// Static and Cache. Request IDs, recovery, access logs and security headers
// are part of the core pipeline (see torge.Options); authentication, sessions,
// rate limiting, idempotency and webhooks have their own packages.
package middleware

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

// CORSConfig configures Cross-Origin Resource Sharing.
type CORSConfig struct {
	// AllowOrigins lists allowed origins: exact origins such as
	// "https://app.example.com", subdomain wildcards such as
	// "https://*.example.com", or "*" for any origin.
	AllowOrigins []string
	// AllowOriginFunc decides dynamically; it is consulted after
	// AllowOrigins.
	AllowOriginFunc func(origin string) bool
	// AllowMethods defaults to GET, HEAD, POST, PUT, PATCH, DELETE.
	AllowMethods []string
	// AllowHeaders lists allowed request headers. When empty, the headers
	// requested by the preflight are allowed.
	AllowHeaders []string
	// ExposeHeaders lists response headers readable by scripts (default
	// X-Request-Id).
	ExposeHeaders []string
	// AllowCredentials allows cookies and HTTP authentication. It cannot be
	// combined with the "*" origin.
	AllowCredentials bool
	// MaxAge is how long preflight results may be cached (default 10m).
	MaxAge time.Duration
}

// Validate reports configuration mistakes that would make CORS insecure or
// silently broken.
func (cfg CORSConfig) Validate() error {
	if len(cfg.AllowOrigins) == 0 && cfg.AllowOriginFunc == nil {
		return fmt.Errorf("no allowed origins configured")
	}
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			if cfg.AllowCredentials {
				return fmt.Errorf(`origin "*" cannot be combined with AllowCredentials; list the trusted origins explicitly`)
			}
			continue
		}
		u, err := url.Parse(strings.Replace(o, "*.", "wildcard.", 1))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || strings.HasSuffix(o, "/") {
			return fmt.Errorf("invalid origin %q: use scheme://host[:port] without a path or trailing slash", o)
		}
	}
	return nil
}

// CORS returns CORS middleware. It panics with a *torge.Diagnostic if cfg is
// invalid, so misconfiguration fails at startup; use NewCORS to handle the
// error yourself. Register it with App.Use so preflight requests are answered
// before routing.
func CORS(cfg CORSConfig) torge.Middleware {
	m, err := NewCORS(cfg)
	if err != nil {
		panic(err)
	}
	return m
}

// NewCORS is like CORS but returns configuration errors.
func NewCORS(cfg CORSConfig) (torge.Middleware, error) {
	if err := cfg.Validate(); err != nil {
		return nil, &torge.Diagnostic{
			Code: torge.DiagInvalidConfig, What: "invalid CORS configuration: " + err.Error(),
			Why: "browsers would reject cross-origin requests, or credentials would be exposed to any site",
			Fix: "list trusted origins such as https://app.example.com in CORSConfig.AllowOrigins",
		}
	}
	if len(cfg.AllowMethods) == 0 {
		cfg.AllowMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}
	}
	if cfg.ExposeHeaders == nil {
		cfg.ExposeHeaders = []string{"X-Request-Id"}
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 10 * time.Minute
	}
	allowAny := slices.Contains(cfg.AllowOrigins, "*")
	exact := make(map[string]bool)
	var wildcards []string // "https://" + ".example.com"
	for _, o := range cfg.AllowOrigins {
		o = strings.ToLower(o)
		if scheme, rest, ok := strings.Cut(o, "://*."); ok {
			wildcards = append(wildcards, scheme+"://", "."+rest)
		} else {
			exact[o] = true
		}
	}
	allowed := func(origin string) bool {
		if allowAny {
			return true
		}
		lo := strings.ToLower(origin)
		if exact[lo] {
			return true
		}
		for i := 0; i < len(wildcards); i += 2 {
			if strings.HasPrefix(lo, wildcards[i]) && strings.HasSuffix(lo, wildcards[i+1]) &&
				len(lo) > len(wildcards[i])+len(wildcards[i+1]) {
				return true
			}
		}
		return cfg.AllowOriginFunc != nil && cfg.AllowOriginFunc(origin)
	}
	methods := strings.Join(cfg.AllowMethods, ", ")
	headers := strings.Join(cfg.AllowHeaders, ", ")
	expose := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := strconv.Itoa(int(cfg.MaxAge.Seconds()))

	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			h := c.Response().Header()
			origin := c.GetHeader("Origin")
			if !allowAny || cfg.AllowCredentials {
				h.Add("Vary", "Origin")
			}
			preflight := c.Method() == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != ""
			if origin == "" || !allowed(origin) {
				if preflight {
					// Answer without CORS headers: the browser blocks the request.
					return c.NoContent(http.StatusNoContent)
				}
				return next(c)
			}
			if allowAny && !cfg.AllowCredentials {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}
			if cfg.AllowCredentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if !preflight {
				if expose != "" {
					h.Set("Access-Control-Expose-Headers", expose)
				}
				return next(c)
			}
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", methods)
			if headers != "" {
				h.Set("Access-Control-Allow-Headers", headers)
			} else if req := c.GetHeader("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			}
			h.Set("Access-Control-Max-Age", maxAge)
			return c.NoContent(http.StatusNoContent)
		}
	}, nil
}
