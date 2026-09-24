package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/internal/capture"
)

// CacheConfig configures response caching.
type CacheConfig struct {
	// Store holds cached responses. Required. Use a shared store (Redis)
	// when running several instances.
	Store cache.Store
	// TTL is how long responses are cached (default 1m).
	TTL time.Duration
	// Key derives the cache key (default: method, path and sorted query).
	Key func(c *torge.Context) string
	// MaxBodySize bounds cached bodies (default 1 MiB); larger responses are
	// served but not cached.
	MaxBodySize int
	// CacheAuthenticated allows caching requests that carry Authorization or
	// Cookie headers. Only enable it when Key includes the user identity.
	CacheAuthenticated bool
}

// Cache caches successful (200) GET responses. Responses with Set-Cookie, or
// Cache-Control no-store or private, are never cached, and by default neither
// are requests carrying credentials. Responses carry X-Cache: HIT or MISS.
func Cache(cfg CacheConfig) torge.Middleware {
	if cfg.Store == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "CacheConfig.Store is nil",
			Why: "responses cannot be cached without a store", Fix: "pass a cache.Store such as cache.NewMemory()"})
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Minute
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = 1 << 20
	}
	if cfg.Key == nil {
		cfg.Key = defaultCacheKey
	}
	store := cache.Namespace(cfg.Store, "torge:response:")
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			r := c.Request()
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return next(c)
			}
			if !cfg.CacheAuthenticated && (r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "") {
				return next(c)
			}
			if strings.Contains(r.Header.Get("Cache-Control"), "no-cache") {
				return next(c)
			}
			key := cfg.Key(c)
			if data, err := store.Get(c.Context(), key); err == nil {
				var resp capture.Response
				if json.Unmarshal(data, &resp) == nil {
					return resp.WriteTo(c.Response(), map[string]string{"X-Cache": "HIT"})
				}
			}
			c.Header("X-Cache", "MISS")
			rec := capture.New(c.Response(), cfg.MaxBodySize)
			restore := c.SetResponseWriter(rec)
			err := next(c)
			restore()
			if err != nil {
				return err
			}
			resp, ok := rec.Result()
			if !ok || resp.Status != http.StatusOK || r.Method == http.MethodHead {
				return nil
			}
			if cc := strings.ToLower(resp.Header.Get("Cache-Control")); strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
				return nil
			}
			if data, err := json.Marshal(resp); err == nil {
				if err := store.Set(c.Context(), key, data, cfg.TTL); err != nil {
					c.Logger().Warn("response cache write failed", "error", err)
				}
			}
			return nil
		}
	}
}

func defaultCacheKey(c *torge.Context) string {
	r := c.Request()
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(r.Host + "\x00" + r.URL.Path))
	for _, k := range keys {
		for _, v := range q[k] {
			h.Write([]byte("\x00" + k + "=" + v))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
