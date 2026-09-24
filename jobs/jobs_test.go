package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/correlation"
	"github.com/TosmimForidMehtab/torge/jobs"
)

type Email struct {
	To string `json:"to"`
}

func newManager(q jobs.Queue) *jobs.Manager {
	return jobs.NewManager(jobs.Options{
		Queue:   q,
		Workers: 2,
		Backoff: func(int) time.Duration { return time.Millisecond },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestDispatchAndProcess(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	m := newManager(q)
	got := make(chan string, 1)
	gotRequestID := make(chan string, 1)
	if err := m.Register("send-email", jobs.Typed(func(ctx context.Context, e Email) error {
		got <- e.To
		gotRequestID <- correlation.RequestID(ctx)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())

	ctx := correlation.WithRequestID(context.Background(), "req-1")
	if err := m.Dispatch(ctx, "send-email", Email{To: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if to := <-got; to != "a@example.com" {
		t.Fatalf("got %q", to)
	}
	if id := <-gotRequestID; id != "req-1" {
		t.Fatalf("request ID must propagate to jobs, got %q", id)
	}
	if err := m.Dispatch(ctx, "unknown", nil); !errors.Is(err, jobs.ErrUnknownJob) {
		t.Fatalf("got %v", err)
	}
}

func TestRetriesAndPermanentFailure(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	var attempts atomic.Int32
	var failed atomic.Pointer[jobs.Job]
	m := jobs.NewManager(jobs.Options{
		Queue: q, Workers: 1, MaxAttempts: 3,
		Backoff:   func(int) time.Duration { return time.Millisecond },
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnFailure: func(_ context.Context, j *jobs.Job, _ error) { failed.Store(j) },
	})
	_ = m.Register("flaky", jobs.HandlerFunc(func(context.Context, *jobs.Job) error {
		attempts.Add(1)
		return errors.New("temporary")
	}))
	_ = m.Register("broken", jobs.HandlerFunc(func(context.Context, *jobs.Job) error {
		panic("bad data")
	}), jobs.MaxAttempts(1))
	_ = m.Start(context.Background())
	defer m.Stop(context.Background())

	_ = m.Dispatch(context.Background(), "flaky", nil)
	waitFor(t, func() bool { return failed.Load() != nil })
	if attempts.Load() != 3 || failed.Load().Attempt != 3 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
	failed.Store(nil)
	_ = m.Dispatch(context.Background(), "broken", nil)
	waitFor(t, func() bool { return failed.Load() != nil })
	if len(q.Failed()) != 2 {
		t.Fatalf("dead letters = %d", len(q.Failed()))
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	m := newManager(q)
	var attempts atomic.Int32
	_ = m.Register("bad-payload", jobs.Typed(func(context.Context, Email) error {
		attempts.Add(1)
		return nil
	}))
	_ = m.Start(context.Background())
	defer m.Stop(context.Background())
	_ = m.Dispatch(context.Background(), "bad-payload", []byte(`{"to": 5}`))
	waitFor(t, func() bool { return len(q.Failed()) == 1 })
	if attempts.Load() != 0 {
		t.Fatal("undecodable payloads must fail permanently without running the handler")
	}
}

func TestGracefulStop(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	m := newManager(q)
	started := make(chan struct{})
	var finished atomic.Bool
	_ = m.Register("slow", jobs.HandlerFunc(func(ctx context.Context, _ *jobs.Job) error {
		close(started)
		time.Sleep(50 * time.Millisecond)
		finished.Store(true)
		return nil
	}))
	_ = m.Start(context.Background())
	_ = m.Dispatch(context.Background(), "slow", nil)
	<-started
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("Stop must wait for in-flight jobs")
	}
}

func TestStopDeadlineCancelsJobs(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	m := newManager(q)
	started := make(chan struct{})
	var canceled atomic.Bool
	_ = m.Register("stuck", jobs.HandlerFunc(func(ctx context.Context, _ *jobs.Job) error {
		close(started)
		<-ctx.Done()
		canceled.Store(true)
		return ctx.Err()
	}))
	_ = m.Start(context.Background())
	_ = m.Dispatch(context.Background(), "stuck", nil)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !canceled.Load() {
		t.Fatal("running jobs must be canceled when the stop deadline passes")
	}
}

func TestDelayedDispatch(t *testing.T) {
	q := jobs.NewMemoryQueue(0)
	defer q.Close()
	m := newManager(q)
	ran := make(chan time.Time, 1)
	_ = m.Register("later", jobs.HandlerFunc(func(context.Context, *jobs.Job) error { ran <- time.Now(); return nil }))
	_ = m.Start(context.Background())
	defer m.Stop(context.Background())
	start := time.Now()
	_ = m.Dispatch(context.Background(), "later", nil, jobs.Delay(40*time.Millisecond))
	if at := <-ran; at.Sub(start) < 40*time.Millisecond {
		t.Fatal("delayed job ran early")
	}
}
