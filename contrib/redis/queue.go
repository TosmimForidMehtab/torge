package torgeredis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TosmimForidMehtab/torge/jobs"
)

// Queue is a jobs.Queue backed by Redis lists.
//
// Delivery is at-least-once: a received job moves atomically from the ready
// list to a processing list and is removed on Ack, Retry or Reject. Jobs left
// in the processing list by a crashed worker can be returned to the ready
// list with RequeueProcessing once no worker is using the queue. Handlers
// should therefore be idempotent.
type Queue struct {
	client                           redis.UniversalClient
	ready, processing, delayed, dead string
	poll                             time.Duration
}

var _ jobs.Queue = (*Queue)(nil)

// NewQueue returns a Queue whose keys start with "torge:jobs:<name>:".
func NewQueue(client redis.UniversalClient, name string) *Queue {
	p := "torge:jobs:{" + name + "}:" // hash tag keeps keys in one cluster slot
	return &Queue{
		client: client, ready: p + "ready", processing: p + "processing",
		delayed: p + "delayed", dead: p + "dead", poll: time.Second,
	}
}

// Enqueue implements jobs.Queue.
func (q *Queue) Enqueue(ctx context.Context, job *jobs.Job, delay time.Duration) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if delay > 0 {
		at := time.Now().Add(delay).UnixMilli()
		return q.client.ZAdd(ctx, q.delayed, redis.Z{Score: float64(at), Member: payload}).Err()
	}
	return q.client.LPush(ctx, q.ready, payload).Err()
}

var promoteScript = redis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, 100)
for _, item in ipairs(due) do
  redis.call('ZREM', KEYS[1], item)
  redis.call('LPUSH', KEYS[2], item)
end
return #due
`)

// Receive implements jobs.Queue.
func (q *Queue) Receive(ctx context.Context) (jobs.Delivery, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := strconv.FormatInt(time.Now().UnixMilli(), 10)
		if err := promoteScript.Run(ctx, q.client, []string{q.delayed, q.ready}, now).Err(); err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("torgeredis: promote delayed jobs: %w", err)
		}
		payload, err := q.client.BLMove(ctx, q.ready, q.processing, "RIGHT", "LEFT", q.poll).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		var job jobs.Job
		if err := json.Unmarshal([]byte(payload), &job); err != nil {
			_ = q.client.LRem(ctx, q.processing, 1, payload).Err()
			_ = q.client.LPush(ctx, q.dead, payload).Err()
			continue
		}
		return &delivery{q: q, job: &job, raw: payload}, nil
	}
}

// RequeueProcessing moves every job in the processing list back to the ready
// list. Call it only when no worker is consuming the queue, for example at
// deployment time after all instances stopped.
func (q *Queue) RequeueProcessing(ctx context.Context) (int, error) {
	n := 0
	for {
		_, err := q.client.LMove(ctx, q.processing, q.ready, "RIGHT", "LEFT").Result()
		if errors.Is(err, redis.Nil) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
	}
}

// Dead returns up to limit dead-lettered jobs.
func (q *Queue) Dead(ctx context.Context, limit int64) ([]*jobs.Job, error) {
	raws, err := q.client.LRange(ctx, q.dead, 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*jobs.Job, 0, len(raws))
	for _, r := range raws {
		var j jobs.Job
		if json.Unmarshal([]byte(r), &j) == nil {
			out = append(out, &j)
		}
	}
	return out, nil
}

type delivery struct {
	q   *Queue
	job *jobs.Job
	raw string
}

func (d *delivery) Job() *jobs.Job { return d.job }

func (d *delivery) Ack(ctx context.Context) error {
	return d.q.client.LRem(ctx, d.q.processing, 1, d.raw).Err()
}

func (d *delivery) Retry(ctx context.Context, delay time.Duration) error {
	next := *d.job
	next.Attempt++
	payload, err := json.Marshal(&next)
	if err != nil {
		return err
	}
	_, err = d.q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, d.q.processing, 1, d.raw)
		p.ZAdd(ctx, d.q.delayed, redis.Z{Score: float64(time.Now().Add(delay).UnixMilli()), Member: payload})
		return nil
	})
	return err
}

func (d *delivery) Reject(ctx context.Context, _ error) error {
	_, err := d.q.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, d.q.processing, 1, d.raw)
		p.LPush(ctx, d.q.dead, d.raw)
		return nil
	})
	return err
}
