// Package jobs provides a common abstraction for background jobs: handlers,
// dispatching, worker lifecycle, retries with backoff, observability hooks and
// graceful shutdown. Queue infrastructure is supplied by adapters implementing
// Queue (Redis, PostgreSQL, SQS, RabbitMQ, NATS, ...); MemoryQueue is included
// for development and tests.
//
//	m := jobs.NewManager(jobs.Options{Queue: queue})
//	m.Register("send-email", jobs.Typed(func(ctx context.Context, p Email) error {
//	    return mailer.Send(ctx, p)
//	}))
//	_ = m.Dispatch(ctx, "send-email", Email{To: "a@example.com"})
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime"
	"slices"
	"sync"
	"time"

	crand "crypto/rand"

	"github.com/TosmimForidMehtab/torge/correlation"
)

// Job is a unit of background work.
type Job struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Payload     json.RawMessage   `json:"payload,omitempty"`
	Attempt     int               `json:"attempt"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
	EnqueuedAt  time.Time         `json:"enqueued_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Metadata keys set by Dispatch.
const (
	MetaRequestID = "request_id"
	MetaTraceID   = "trace_id"
)

// Decode unmarshals the payload into v.
func (j *Job) Decode(v any) error { return json.Unmarshal(j.Payload, v) }

// Handler processes jobs. Handlers must honor ctx cancellation.
type Handler interface {
	Handle(ctx context.Context, job *Job) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, job *Job) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, job *Job) error { return f(ctx, job) }

// Typed adapts a function taking a decoded payload.
func Typed[T any](fn func(ctx context.Context, payload T) error) Handler {
	return HandlerFunc(func(ctx context.Context, job *Job) error {
		var p T
		if len(job.Payload) > 0 {
			if err := job.Decode(&p); err != nil {
				return Permanent(fmt.Errorf("decode payload: %w", err))
			}
		}
		return fn(ctx, p)
	})
}

// Middleware wraps job handlers, for example for tracing or metrics.
type Middleware func(Handler) Handler

// Queue is implemented by queue backends.
type Queue interface {
	// Enqueue stores job for delivery after delay.
	Enqueue(ctx context.Context, job *Job, delay time.Duration) error
	// Receive blocks until a job is available or ctx is done, in which case
	// it returns ctx.Err().
	Receive(ctx context.Context) (Delivery, error)
}

// Delivery is a received job awaiting acknowledgment.
type Delivery interface {
	Job() *Job
	// Ack marks the job as processed.
	Ack(ctx context.Context) error
	// Retry schedules redelivery after delay with Attempt incremented.
	Retry(ctx context.Context, delay time.Duration) error
	// Reject marks the job as permanently failed (dead-lettered when the
	// backend supports it).
	Reject(ctx context.Context, reason error) error
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks an error as non-retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// ErrUnknownJob is returned when dispatching a job name with no handler.
var ErrUnknownJob = errors.New("jobs: unknown job")

// Options configures a Manager.
type Options struct {
	// Queue is the backend. Required.
	Queue Queue
	// Workers is the number of concurrent workers (default GOMAXPROCS).
	// Set to -1 for a dispatch-only manager that never consumes.
	Workers int
	// MaxAttempts is the default attempt limit per job (default 3).
	MaxAttempts int
	// Timeout bounds a single attempt (default 5m).
	Timeout time.Duration
	// Backoff returns the delay before retry number attempt (1-based).
	// Default: exponential from 1s, capped at 5m, with jitter.
	Backoff func(attempt int) time.Duration
	// Logger receives job failures (default slog.Default()).
	Logger *slog.Logger
	// Middleware wraps every handler; the first is outermost.
	Middleware []Middleware
	// OnFailure is called when a job fails permanently.
	OnFailure func(ctx context.Context, job *Job, err error)
}

// JobOption configures a registered job.
type JobOption func(*registration)

// MaxAttempts overrides the attempt limit for a job.
func MaxAttempts(n int) JobOption { return func(r *registration) { r.maxAttempts = n } }

// Timeout overrides the per-attempt timeout for a job.
func Timeout(d time.Duration) JobOption { return func(r *registration) { r.timeout = d } }

type registration struct {
	handler     Handler
	maxAttempts int
	timeout     time.Duration
}

// DispatchOption configures a dispatch.
type DispatchOption func(*Job, *time.Duration)

// Delay schedules the job to run after d.
func Delay(d time.Duration) DispatchOption {
	return func(_ *Job, delay *time.Duration) { *delay = d }
}

// WithID sets the job ID, which backends may use for deduplication.
func WithID(id string) DispatchOption {
	return func(j *Job, _ *time.Duration) { j.ID = id }
}

// Manager registers handlers, dispatches jobs and runs workers. It implements
// Start and Stop so it can be managed by an application lifecycle.
type Manager struct {
	opts     Options
	mu       sync.RWMutex
	handlers map[string]*registration
	running  bool

	recvCancel context.CancelFunc
	jobCancel  context.CancelFunc
	wg         sync.WaitGroup
}

// NewManager returns a Manager. It panics if opts.Queue is nil.
func NewManager(opts Options) *Manager {
	if opts.Queue == nil {
		panic("jobs: Options.Queue is required")
	}
	if opts.Workers == 0 {
		opts.Workers = runtime.GOMAXPROCS(0)
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.Backoff == nil {
		opts.Backoff = ExponentialBackoff(time.Second, 5*time.Minute)
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{opts: opts, handlers: make(map[string]*registration)}
}

// Queue returns the manager's queue.
func (m *Manager) Queue() Queue { return m.opts.Queue }

// ExponentialBackoff returns base*2^(attempt-1) capped at max, with up to 20%
// random jitter to avoid synchronized retries.
func ExponentialBackoff(base, max time.Duration) func(int) time.Duration {
	return func(attempt int) time.Duration {
		d := base
		for i := 1; i < attempt && d < max; i++ {
			d *= 2
		}
		d = min(d, max)
		return d + time.Duration(rand.Int64N(int64(d)/5+1))
	}
}

// Register adds a handler for name. It fails if name is already registered or
// the manager is running.
func (m *Manager) Register(name string, h Handler, opts ...JobOption) error {
	if name == "" || h == nil {
		return errors.New("jobs: name and handler are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return fmt.Errorf("jobs: cannot register %q while running", name)
	}
	if _, dup := m.handlers[name]; dup {
		return fmt.Errorf("jobs: %q is already registered", name)
	}
	r := &registration{handler: h, maxAttempts: m.opts.MaxAttempts, timeout: m.opts.Timeout}
	for _, o := range opts {
		o(r)
	}
	m.handlers[name] = r
	return nil
}

// Names returns the registered job names.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.handlers))
	for n := range m.handlers {
		names = append(names, n)
	}
	return names
}

// Dispatch enqueues a job. payload is JSON-encoded unless it is already
// []byte or json.RawMessage. The request and trace IDs in ctx are recorded so
// job logs correlate with the originating request. When handlers are
// registered, dispatching an unknown name fails with ErrUnknownJob; a manager
// without handlers (dispatch-only) accepts any name.
func (m *Manager) Dispatch(ctx context.Context, name string, payload any, opts ...DispatchOption) error {
	m.mu.RLock()
	reg, known := m.handlers[name]
	strict := len(m.handlers) > 0
	m.mu.RUnlock()
	if strict && !known {
		return fmt.Errorf("%w: %q", ErrUnknownJob, name)
	}
	var raw json.RawMessage
	switch p := payload.(type) {
	case nil:
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("jobs: encode payload for %q: %w", name, err)
		}
		raw = b
	}
	job := &Job{ID: crand.Text(), Name: name, Payload: raw, Attempt: 1, EnqueuedAt: time.Now().UTC()}
	if known {
		job.MaxAttempts = reg.maxAttempts
	}
	if id := correlation.RequestID(ctx); id != "" {
		job.meta(MetaRequestID, id)
	}
	if id := correlation.TraceID(ctx); id != "" {
		job.meta(MetaTraceID, id)
	}
	var delay time.Duration
	for _, o := range opts {
		o(job, &delay)
	}
	if err := m.opts.Queue.Enqueue(ctx, job, delay); err != nil {
		return fmt.Errorf("jobs: enqueue %q: %w", name, err)
	}
	return nil
}

func (j *Job) meta(k, v string) {
	if j.Metadata == nil {
		j.Metadata = make(map[string]string, 2)
	}
	j.Metadata[k] = v
}

// Start launches the workers. It returns immediately.
func (m *Manager) Start(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("jobs: manager already running")
	}
	m.running = true
	if m.opts.Workers < 0 {
		return nil
	}
	recvCtx, recvCancel := context.WithCancel(context.Background())
	jobCtx, jobCancel := context.WithCancel(context.Background())
	m.recvCancel, m.jobCancel = recvCancel, jobCancel
	for range m.opts.Workers {
		m.wg.Go(func() { m.work(recvCtx, jobCtx) })
	}
	return nil
}

// Stop stops receiving new jobs and waits for in-flight jobs. If ctx expires
// first, running jobs are canceled and Stop returns ctx.Err() after they
// return.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = false
	recvCancel, jobCancel := m.recvCancel, m.jobCancel
	m.mu.Unlock()
	if recvCancel == nil {
		return nil
	}
	recvCancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		jobCancel()
		return nil
	case <-ctx.Done():
		jobCancel()
		<-done
		return ctx.Err()
	}
}

func (m *Manager) work(recvCtx, jobCtx context.Context) {
	for {
		d, err := m.opts.Queue.Receive(recvCtx)
		if err != nil {
			if recvCtx.Err() != nil {
				return
			}
			m.opts.Logger.Error("jobs: receive failed", "error", err)
			select {
			case <-recvCtx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		m.process(jobCtx, d)
	}
}

func (m *Manager) process(base context.Context, d Delivery) {
	job := d.Job()
	m.mu.RLock()
	reg := m.handlers[job.Name]
	m.mu.RUnlock()
	ctx := base
	if id := job.Metadata[MetaRequestID]; id != "" {
		ctx = correlation.WithRequestID(ctx, id)
	}
	if id := job.Metadata[MetaTraceID]; id != "" {
		ctx = correlation.WithTraceID(ctx, id)
	}
	log := m.opts.Logger.With("job", job.Name, "job_id", job.ID, "attempt", job.Attempt)
	// Acknowledgment calls must succeed even when stopping.
	ackCtx := context.WithoutCancel(ctx)
	if reg == nil {
		err := fmt.Errorf("%w: %q", ErrUnknownJob, job.Name)
		log.ErrorContext(ctx, "job rejected", "error", err)
		_ = d.Reject(ackCtx, err)
		return
	}
	h := reg.handler
	for _, v := range slices.Backward(m.opts.Middleware) {
		h = v(h)
	}
	runCtx, cancel := context.WithTimeout(ctx, reg.timeout)
	start := time.Now()
	err := safeHandle(runCtx, h, job)
	cancel()
	if err == nil {
		if ackErr := d.Ack(ackCtx); ackErr != nil {
			log.ErrorContext(ctx, "job ack failed", "error", ackErr)
		}
		log.DebugContext(ctx, "job completed", "duration", time.Since(start))
		return
	}
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = reg.maxAttempts
	}
	if !IsPermanent(err) && job.Attempt < maxAttempts {
		delay := m.opts.Backoff(job.Attempt)
		log.WarnContext(ctx, "job failed; retrying", "error", err, "retry_in", delay)
		if rerr := d.Retry(ackCtx, delay); rerr != nil {
			log.ErrorContext(ctx, "job retry scheduling failed", "error", rerr)
		}
		return
	}
	log.ErrorContext(ctx, "job failed permanently", "error", err)
	if rerr := d.Reject(ackCtx, err); rerr != nil {
		log.ErrorContext(ctx, "job reject failed", "error", rerr)
	}
	if m.opts.OnFailure != nil {
		m.opts.OnFailure(ackCtx, job, err)
	}
}

func safeHandle(ctx context.Context, h Handler, job *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			err = fmt.Errorf("job panicked: %v\n%s", r, buf)
		}
	}()
	return h.Handle(ctx, job)
}
