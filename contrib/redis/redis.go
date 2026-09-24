// Package torgeredis adapts Redis and Valkey (through go-redis) to Torge's
// shared-state contracts:
//
//   - Store implements cache.Store and cache.Adder, so it backs the
//     application cache, response caching, sessions, idempotency and webhook
//     replay protection across instances;
//   - Limiter implements ratelimit.Limiter with an atomic GCRA script using
//     the Redis server clock;
//   - Queue implements jobs.Queue with reliable lists (at-least-once
//     delivery), delayed jobs and a dead-letter list.
//
// Usage:
//
//	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
//	store := torgeredis.NewStore(client)
//	app.Cache(store) // health-checked, closed at shutdown
package torgeredis

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TosmimForidMehtab/torge/cache"
)

// Store is a cache.Store backed by Redis. It owns the client: Close closes
// it.
type Store struct {
	client redis.UniversalClient
}

var (
	_ cache.Store = (*Store)(nil)
	_ cache.Adder = (*Store)(nil)
)

// NewStore returns a Store using client.
func NewStore(client redis.UniversalClient) *Store { return &Store{client: client} }

// Client returns the underlying client.
func (s *Store) Client() redis.UniversalClient { return s.client }

// Get implements cache.Store.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, cache.ErrNotFound
	}
	return b, err
}

// Set implements cache.Store.
func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, max(ttl, 0)).Err()
}

// Add implements cache.Adder.
func (s *Store) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, key, value, max(ttl, 0)).Result()
}

// Delete implements cache.Store.
func (s *Store) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.client.Unlink(ctx, keys...).Err()
}

// DeletePrefix implements cache.Store using SCAN, so it never blocks the
// server. On a cluster every primary is scanned.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	pattern := escapeGlob(prefix) + "*"
	if cc, ok := s.client.(*redis.ClusterClient); ok {
		return cc.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
			return deleteMatching(ctx, node, pattern)
		})
	}
	return deleteMatching(ctx, s.client, pattern)
}

func deleteMatching(ctx context.Context, c redis.Cmdable, pattern string) error {
	var cursor uint64
	for {
		keys, next, err := c.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := c.Unlink(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		if cursor = next; cursor == 0 {
			return nil
		}
	}
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`).Replace(s)
}

// Ping checks connectivity; the application uses it as a health check.
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close closes the client.
func (s *Store) Close() error { return s.client.Close() }
