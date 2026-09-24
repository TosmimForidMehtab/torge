package jobs

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MemoryQueue is an in-process queue for development and tests. Jobs are
// lost when the process exits and are not shared between instances; use a
// durable backend in production.
type MemoryQueue struct {
	ch     chan *Job
	done   chan struct{}
	mu     sync.Mutex
	timers map[*time.Timer]struct{}
	closed bool
	failed []*Job
}

var (
	_ Queue    = (*MemoryQueue)(nil)
	_ Delivery = (*memoryDelivery)(nil)
)

// ErrQueueClosed is returned when enqueuing into a closed MemoryQueue.
var ErrQueueClosed = errors.New("jobs: queue closed")

// NewMemoryQueue returns a queue buffering up to capacity ready jobs
// (default 1024). Enqueue blocks while the buffer is full.
func NewMemoryQueue(capacity int) *MemoryQueue {
	if capacity <= 0 {
		capacity = 1024
	}
	return &MemoryQueue{ch: make(chan *Job, capacity), done: make(chan struct{}), timers: make(map[*time.Timer]struct{})}
}

// IsLocal reports that the queue's state is process-local.
func (q *MemoryQueue) IsLocal() bool { return true }

// Enqueue implements Queue.
func (q *MemoryQueue) Enqueue(ctx context.Context, job *Job, delay time.Duration) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	if delay > 0 {
		var t *time.Timer
		t = time.AfterFunc(delay, func() {
			q.mu.Lock()
			delete(q.timers, t)
			closed := q.closed
			q.mu.Unlock()
			if !closed {
				select {
				case q.ch <- job:
				case <-q.done:
				}
			}
		})
		q.timers[t] = struct{}{}
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()
	select {
	case q.ch <- job:
		return nil
	case <-q.done:
		return ErrQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Receive implements Queue.
func (q *MemoryQueue) Receive(ctx context.Context) (Delivery, error) {
	select {
	case job := <-q.ch:
		return &memoryDelivery{q: q, job: job}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Len returns the number of jobs ready for delivery.
func (q *MemoryQueue) Len() int { return len(q.ch) }

// Failed returns permanently failed jobs (the dead-letter list).
func (q *MemoryQueue) Failed() []*Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]*Job(nil), q.failed...)
}

// Close stops pending delayed deliveries and rejects further enqueues.
func (q *MemoryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	close(q.done)
	for t := range q.timers {
		t.Stop()
	}
	q.timers = nil
	return nil
}

type memoryDelivery struct {
	q   *MemoryQueue
	job *Job
}

func (d *memoryDelivery) Job() *Job                 { return d.job }
func (d *memoryDelivery) Ack(context.Context) error { return nil }

func (d *memoryDelivery) Retry(ctx context.Context, delay time.Duration) error {
	next := *d.job
	next.Attempt++
	return d.q.Enqueue(ctx, &next, delay)
}

func (d *memoryDelivery) Reject(context.Context, error) error {
	d.q.mu.Lock()
	d.q.failed = append(d.q.failed, d.job)
	d.q.mu.Unlock()
	return nil
}
