// Package ratelimit provides rate limiting middleware with pluggable limiters.
//
// Limiters implement the generic cell rate algorithm (GCRA), which behaves
// like a token bucket: a sustained rate plus a burst capacity, with a single
// timestamp of state per key. Memory is an in-process limiter; distributed
// deployments must use a shared limiter (contrib/redis) so all instances
// enforce one limit.
//
//	limiter := ratelimit.NewMemory(ratelimit.PerMinute(60).WithBurst(10))
//	api.Use(ratelimit.Middleware(ratelimit.Config{Limiter: limiter, Key: ratelimit.ByUser}))
package ratelimit

import (
	"context"
	"hash/maphash"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

// Rate is a sustained rate with a burst capacity.
type Rate struct {
	// Limit requests are allowed per Period on average.
	Limit int
	// Period is the window for Limit.
	Period time.Duration
	// Burst is the number of requests that may arrive at once (default
	// Limit).
	Burst int
}

// PerSecond returns a rate of n requests per second.
func PerSecond(n int) Rate { return Rate{Limit: n, Period: time.Second} }

// PerMinute returns a rate of n requests per minute.
func PerMinute(n int) Rate { return Rate{Limit: n, Period: time.Minute} }

// PerHour returns a rate of n requests per hour.
func PerHour(n int) Rate { return Rate{Limit: n, Period: time.Hour} }

// WithBurst returns r with a burst capacity.
func (r Rate) WithBurst(n int) Rate { r.Burst = n; return r }

// Interval returns the time between requests at the sustained rate.
func (r Rate) Interval() time.Duration { return r.Period / time.Duration(r.Limit) }

// Capacity returns the burst capacity.
func (r Rate) Capacity() int {
	if r.Burst > 0 {
		return r.Burst
	}
	return r.Limit
}

// Result is the outcome of a limiter decision.
type Result struct {
	Allowed bool
	// Limit is the burst capacity.
	Limit int
	// Remaining is how many more requests would be allowed right now.
	Remaining int
	// ResetAfter is when the bucket is full again.
	ResetAfter time.Duration
	// RetryAfter is how long to wait before retrying a denied request.
	RetryAfter time.Duration
}

// Limiter decides whether a request identified by key may proceed.
type Limiter interface {
	Allow(ctx context.Context, key string) (Result, error)
}

// GCRA computes a decision from the stored theoretical arrival time (tat)
// and returns the new tat to store. It is exported for limiter
// implementations backed by other stores.
func GCRA(rate Rate, now, tat time.Time) (Result, time.Time) {
	interval := rate.Interval()
	capacity := rate.Capacity()
	burst := interval * time.Duration(capacity)
	if tat.Before(now) {
		tat = now
	}
	newTat := tat.Add(interval)
	allowAt := newTat.Add(-burst)
	if now.Before(allowAt) {
		return Result{
			Allowed:    false,
			Limit:      capacity,
			RetryAfter: allowAt.Sub(now),
			ResetAfter: tat.Sub(now),
		}, tat
	}
	remaining := int(math.Floor(float64(now.Sub(allowAt)) / float64(interval)))
	return Result{
		Allowed:    true,
		Limit:      capacity,
		Remaining:  min(remaining, capacity-1),
		ResetAfter: newTat.Sub(now),
	}, newTat
}

// Memory is an in-process GCRA limiter. State is sharded to reduce lock
// contention and idle keys are swept periodically.
type Memory struct {
	rate   Rate
	seed   maphash.Seed
	shards [32]shard
	now    func() time.Time
}

type shard struct {
	mu    sync.Mutex
	tats  map[string]time.Time
	calls int
}

var _ Limiter = (*Memory)(nil)

// NewMemory returns an in-memory limiter for rate. It panics if the rate is
// not positive.
func NewMemory(rate Rate) *Memory {
	if rate.Limit <= 0 || rate.Period <= 0 {
		panic("ratelimit: rate limit and period must be positive")
	}
	m := &Memory{rate: rate, seed: maphash.MakeSeed(), now: time.Now}
	for i := range m.shards {
		m.shards[i].tats = make(map[string]time.Time)
	}
	return m
}

// IsLocal reports that the limiter's state is process-local.
func (m *Memory) IsLocal() bool { return true }

// Allow implements Limiter.
func (m *Memory) Allow(_ context.Context, key string) (Result, error) {
	s := &m.shards[maphash.String(m.seed, key)%uint64(len(m.shards))]
	now := m.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	res, tat := GCRA(m.rate, now, s.tats[key])
	s.tats[key] = tat
	s.calls++
	if s.calls%1024 == 0 {
		// Keys whose tat is in the past are indistinguishable from new keys.
		for k, t := range s.tats {
			if t.Before(now) {
				delete(s.tats, k)
			}
		}
	}
	return res, nil
}

// KeyFunc derives the rate limit key for a request.
type KeyFunc func(c *torge.Context) string

// ByIP limits per client IP (see torge.Context.RealIP for proxy handling).
func ByIP(c *torge.Context) string { return "ip:" + c.RealIP() }

// ByUser limits per authenticated user, falling back to the client IP.
func ByUser(c *torge.Context) string {
	if u := c.User(); u != nil {
		return "user:" + u.ID()
	}
	return ByIP(c)
}

// ByRoute scopes another key function to the matched route, so each route
// has its own budget.
func ByRoute(inner KeyFunc) KeyFunc {
	return func(c *torge.Context) string {
		return c.Method() + " " + c.RoutePattern() + "|" + inner(c)
	}
}

// Config configures the middleware.
type Config struct {
	// Limiter decides. Required.
	Limiter Limiter
	// Key derives the key (default ByIP).
	Key KeyFunc
	// Skip exempts requests.
	Skip func(c *torge.Context) bool
	// FailClosed rejects requests with 503 when the limiter errors. By
	// default requests are allowed (and the error logged), so a limiter
	// outage does not take the API down.
	FailClosed bool
}

// Middleware enforces the limit. Responses carry RateLimit-Limit,
// RateLimit-Remaining and RateLimit-Reset headers; denied requests get 429
// with Retry-After.
func Middleware(cfg Config) torge.Middleware {
	if cfg.Limiter == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "ratelimit.Config.Limiter is nil",
			Why: "requests cannot be limited", Fix: "use ratelimit.NewMemory or a shared limiter"})
	}
	if cfg.Key == nil {
		cfg.Key = ByIP
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}
			res, err := cfg.Limiter.Allow(c.Context(), cfg.Key(c))
			if err != nil {
				if cfg.FailClosed {
					return torge.ServiceUnavailable("RATE_LIMITER_UNAVAILABLE", "Service temporarily unavailable").Wrap(err)
				}
				c.Logger().Error("rate limiter failed; allowing request", "error", err)
				return next(c)
			}
			h := c.Response().Header()
			h.Set("RateLimit-Limit", strconv.Itoa(res.Limit))
			h.Set("RateLimit-Remaining", strconv.Itoa(res.Remaining))
			h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(res.ResetAfter)))
			if !res.Allowed {
				h.Set("Retry-After", strconv.Itoa(max(ceilSeconds(res.RetryAfter), 1)))
				return torge.TooManyRequests(torge.CodeTooManyRequests, "Too many requests")
			}
			return next(c)
		}
	}
}

func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
