package torgeredis

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TosmimForidMehtab/torge/ratelimit"
)

// gcraScript implements GCRA atomically using the server clock, so limits
// are consistent across application instances regardless of clock skew.
// Times are in microseconds. Returns {allowed, a, b} where for allowed
// requests a = slack before denial and b = reset time, and for denied
// requests a = retry after and b = reset time.
var gcraScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local tat = tonumber(redis.call('GET', KEYS[1]) or now)
if tat < now then tat = now end
local new_tat = tat + interval
local allow_at = new_tat - burst
if now < allow_at then
  return {0, allow_at - now, tat - now}
end
redis.call('SET', KEYS[1], new_tat, 'PX', math.ceil((new_tat - now) / 1000))
return {1, now - allow_at, new_tat - now}
`)

// Limiter is a distributed ratelimit.Limiter.
type Limiter struct {
	client redis.Scripter
	rate   ratelimit.Rate
	prefix string
}

var _ ratelimit.Limiter = (*Limiter)(nil)

// NewLimiter returns a Limiter enforcing rate. Keys are prefixed with
// "torge:ratelimit:".
func NewLimiter(client redis.Scripter, rate ratelimit.Rate) *Limiter {
	if rate.Limit <= 0 || rate.Period <= 0 {
		panic("torgeredis: rate limit and period must be positive")
	}
	return &Limiter{client: client, rate: rate, prefix: "torge:ratelimit:"}
}

// Allow implements ratelimit.Limiter.
func (l *Limiter) Allow(ctx context.Context, key string) (ratelimit.Result, error) {
	interval := l.rate.Interval().Microseconds()
	capacity := l.rate.Capacity()
	res, err := gcraScript.Run(ctx, l.client, []string{l.prefix + key}, interval, interval*int64(capacity)).Int64Slice()
	if err != nil {
		return ratelimit.Result{}, fmt.Errorf("torgeredis: rate limit: %w", err)
	}
	if len(res) != 3 {
		return ratelimit.Result{}, fmt.Errorf("torgeredis: unexpected rate limit reply %v", res)
	}
	us := func(v int64) time.Duration { return time.Duration(v) * time.Microsecond }
	if res[0] == 0 {
		return ratelimit.Result{Limit: capacity, RetryAfter: us(res[1]), ResetAfter: us(res[2])}, nil
	}
	remaining := int(math.Floor(float64(res[1]) / float64(interval)))
	return ratelimit.Result{Allowed: true, Limit: capacity, Remaining: min(remaining, capacity-1), ResetAfter: us(res[2])}, nil
}
