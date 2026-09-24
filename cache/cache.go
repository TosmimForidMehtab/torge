// Package cache defines a small, pluggable key-value cache contract with an
// in-memory implementation. Distributed stores (Redis, Valkey) live in
// contrib modules and implement the same Store interface.
//
// Values are opaque bytes; use Typed for JSON-encoded values with
// load-through caching.
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get when a key is absent or expired.
var ErrNotFound = errors.New("cache: key not found")

// ErrUnsupported is returned when a store does not support an operation.
var ErrUnsupported = errors.New("cache: operation not supported by store")

// Store is a key-value cache. Implementations must be safe for concurrent use.
type Store interface {
	// Get returns the value for key or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores value under key. A ttl <= 0 means no expiry.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete removes keys. Missing keys are not an error.
	Delete(ctx context.Context, keys ...string) error
	// DeletePrefix removes every key starting with prefix (namespace
	// invalidation).
	DeletePrefix(ctx context.Context, prefix string) error
}

// Adder is implemented by stores that can atomically set a key only if it is
// absent. Idempotency, replay protection and locking rely on it.
type Adder interface {
	// Add stores value if key is absent and reports whether it did.
	Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

// Local is implemented by stores whose data lives in the current process.
// Frameworks use it to warn when a feature that needs shared state across
// instances (rate limits, idempotency, sessions) is backed by local memory in
// production.
type Local interface {
	IsLocal() bool
}

// IsLocal reports whether s keeps its data in process memory.
func IsLocal(s any) bool {
	l, ok := s.(Local)
	return ok && l.IsLocal()
}

// Add calls s.Add if the store supports it, or returns ErrUnsupported.
func Add(ctx context.Context, s Store, key string, value []byte, ttl time.Duration) (bool, error) {
	a, ok := s.(Adder)
	if !ok {
		return false, ErrUnsupported
	}
	return a.Add(ctx, key, value, ttl)
}

// Namespace returns a Store that prefixes every key with prefix, so several
// features can share one backend without collisions and a whole namespace
// can be invalidated with DeletePrefix(ctx, "").
func Namespace(s Store, prefix string) Store {
	if n, ok := s.(*namespaced); ok {
		return &namespaced{store: n.store, prefix: n.prefix + prefix}
	}
	return &namespaced{store: s, prefix: prefix}
}

type namespaced struct {
	store  Store
	prefix string
}

func (n *namespaced) Get(ctx context.Context, key string) ([]byte, error) {
	return n.store.Get(ctx, n.prefix+key)
}

func (n *namespaced) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return n.store.Set(ctx, n.prefix+key, value, ttl)
}

func (n *namespaced) Delete(ctx context.Context, keys ...string) error {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = n.prefix + k
	}
	return n.store.Delete(ctx, full...)
}

func (n *namespaced) DeletePrefix(ctx context.Context, prefix string) error {
	return n.store.DeletePrefix(ctx, n.prefix+prefix)
}

func (n *namespaced) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return Add(ctx, n.store, n.prefix+key, value, ttl)
}

func (n *namespaced) IsLocal() bool { return IsLocal(n.store) }

// Unwrap returns the underlying store.
func (n *namespaced) Unwrap() Store { return n.store }
