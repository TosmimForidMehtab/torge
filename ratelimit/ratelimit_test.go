package ratelimit_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/ratelimit"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestGCRA(t *testing.T) {
	rate := ratelimit.PerSecond(10).WithBurst(3)
	now := time.Unix(1000, 0)
	var tat time.Time
	var res ratelimit.Result
	for i := range 3 {
		res, tat = ratelimit.GCRA(rate, now, tat)
		if !res.Allowed || res.Remaining != 2-i {
			t.Fatalf("burst request %d: %+v", i, res)
		}
	}
	res, tat = ratelimit.GCRA(rate, now, tat)
	if res.Allowed || res.RetryAfter != 100*time.Millisecond {
		t.Fatalf("expected denial with 100ms retry, got %+v", res)
	}
	res, _ = ratelimit.GCRA(rate, now.Add(100*time.Millisecond), tat)
	if !res.Allowed {
		t.Fatalf("expected a token after one interval, got %+v", res)
	}
}

func TestMemoryLimiterConcurrency(t *testing.T) {
	l := ratelimit.NewMemory(ratelimit.PerHour(50))
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if res, _ := l.Allow(context.Background(), "k"); res.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 50 {
		t.Fatalf("expected exactly 50 allowed, got %d", allowed.Load())
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("redis down")
}

func TestMiddleware(t *testing.T) {
	app := torgetest.NewApp(t)
	limiter := ratelimit.NewMemory(ratelimit.PerMinute(2))
	app.GET("/limited", func(c *torge.Context) error { return c.NoContent(204) },
		ratelimit.Middleware(ratelimit.Config{Limiter: limiter, Key: ratelimit.ByRoute(ratelimit.ByIP)}))
	app.GET("/open", func(c *torge.Context) error { return c.NoContent(204) },
		ratelimit.Middleware(ratelimit.Config{Limiter: failingLimiter{}}))
	app.GET("/closed", func(c *torge.Context) error { return c.NoContent(204) },
		ratelimit.Middleware(ratelimit.Config{Limiter: failingLimiter{}, FailClosed: true}))
	tc := torgetest.New(t, app)

	tc.GET("/limited").Do().ExpectStatus(204).ExpectHeader("RateLimit-Limit", "2").ExpectHeader("RateLimit-Remaining", "1")
	tc.GET("/limited").Do().ExpectStatus(204).ExpectHeader("RateLimit-Remaining", "0")
	tc.GET("/limited").Do().ExpectStatus(429).ExpectErrorCode(torge.CodeTooManyRequests).ExpectHeader("Retry-After", "30")
	// Other clients have their own budget.
	tc.GET("/limited").RemoteAddr("198.51.100.7:1").Do().ExpectStatus(204)

	tc.GET("/open").Do().ExpectStatus(204)
	tc.GET("/closed").Do().ExpectStatus(503)
}
