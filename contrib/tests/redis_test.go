package tests

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/TosmimForidMehtab/torge/cache"
	torgeredis "github.com/TosmimForidMehtab/torge/contrib/redis"
	"github.com/TosmimForidMehtab/torge/idempotency"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/ratelimit"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

func TestStore(t *testing.T) {
	mr, c := newRedis(t)
	s := torgeredis.NewStore(c)
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	_ = s.Set(ctx, "a:1", []byte("x"), time.Minute)
	_ = s.Set(ctx, "a:2", []byte("y"), 0)
	_ = s.Set(ctx, "b:1", []byte("z"), 0)
	if v, _ := s.Get(ctx, "a:1"); string(v) != "x" {
		t.Fatal("get")
	}
	mr.FastForward(2 * time.Minute)
	if _, err := s.Get(ctx, "a:1"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatal("TTL must expire keys")
	}
	ok, _ := s.Add(ctx, "a:2", []byte("new"), 0)
	if ok {
		t.Fatal("Add must not overwrite")
	}
	if err := s.DeletePrefix(ctx, "a:"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "a:2"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatal("DeletePrefix")
	}
	if v, _ := s.Get(ctx, "b:1"); string(v) != "z" {
		t.Fatal("other keys must survive")
	}
	if cache.IsLocal(s) {
		t.Fatal("Redis is shared state")
	}

	// The store satisfies the idempotency contract.
	st := idempotency.NewStore(s)
	if existing, err := st.Reserve(ctx, "k", &idempotency.Record{Fingerprint: "f"}, time.Minute); err != nil || existing != nil {
		t.Fatalf("reserve: %v %v", existing, err)
	}
	if existing, _ := st.Reserve(ctx, "k", &idempotency.Record{Fingerprint: "f"}, time.Minute); existing == nil {
		t.Fatal("second reserve must return the pending record")
	}
}

func TestLimiter(t *testing.T) {
	_, c := newRedis(t)
	l := torgeredis.NewLimiter(c, ratelimit.PerMinute(3))
	ctx := context.Background()
	for i := range 3 {
		res, err := l.Allow(ctx, "ip:1")
		if err != nil || !res.Allowed || res.Remaining != 2-i {
			t.Fatalf("request %d: %+v %v", i, res, err)
		}
	}
	res, err := l.Allow(ctx, "ip:1")
	if err != nil || res.Allowed || res.RetryAfter <= 0 {
		t.Fatalf("expected denial: %+v %v", res, err)
	}
	if res, _ := l.Allow(ctx, "ip:2"); !res.Allowed {
		t.Fatal("keys are independent")
	}
}

func TestQueue(t *testing.T) {
	_, c := newRedis(t)
	q := torgeredis.NewQueue(c, "test")
	var attempts atomic.Int32
	done := make(chan string, 1)
	m := jobs.NewManager(jobs.Options{
		Queue: q, Workers: 2, MaxAttempts: 2,
		Backoff: func(int) time.Duration { return 10 * time.Millisecond },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	_ = m.Register("greet", jobs.Typed(func(_ context.Context, name string) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient")
		}
		done <- name
		return nil
	}))
	_ = m.Register("broken", jobs.HandlerFunc(func(context.Context, *jobs.Job) error {
		return jobs.Permanent(errors.New("bad"))
	}))
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())

	if err := m.Dispatch(context.Background(), "greet", "ada", jobs.Delay(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-done:
		if name != "ada" || attempts.Load() != 2 {
			t.Fatalf("name=%q attempts=%d", name, attempts.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job not processed")
	}

	_ = m.Dispatch(context.Background(), "broken", nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		dead, _ := q.Dead(context.Background(), 10)
		if len(dead) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job was not dead-lettered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
