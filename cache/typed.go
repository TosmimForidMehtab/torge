package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Codec encodes and decodes cached values.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// JSON is the default Codec.
type JSON struct{}

// Marshal implements Codec.
func (JSON) Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// Unmarshal implements Codec.
func (JSON) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// Typed stores values of type T in a Store.
type Typed[T any] struct {
	store Store
	codec Codec
	group flightGroup[T]
}

// NewTyped returns a Typed cache over store using JSON encoding. Combine it
// with Namespace to give each value type its own key space:
//
//	users := cache.NewTyped[User](cache.Namespace(store, "users:"))
func NewTyped[T any](store Store, codec ...Codec) *Typed[T] {
	var c Codec = JSON{}
	if len(codec) > 0 && codec[0] != nil {
		c = codec[0]
	}
	return &Typed[T]{store: store, codec: c}
}

// Get returns the value for key and whether it was found.
func (t *Typed[T]) Get(ctx context.Context, key string) (T, bool, error) {
	var v T
	data, err := t.store.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	if err := t.codec.Unmarshal(data, &v); err != nil {
		return v, false, fmt.Errorf("cache: decode %q: %w", key, err)
	}
	return v, true, nil
}

// Set stores v under key.
func (t *Typed[T]) Set(ctx context.Context, key string, v T, ttl time.Duration) error {
	data, err := t.codec.Marshal(v)
	if err != nil {
		return fmt.Errorf("cache: encode %q: %w", key, err)
	}
	return t.store.Set(ctx, key, data, ttl)
}

// Delete removes keys.
func (t *Typed[T]) Delete(ctx context.Context, keys ...string) error {
	return t.store.Delete(ctx, keys...)
}

// GetOrLoad returns the cached value for key, or calls load, caches its result
// for ttl and returns it. Concurrent calls for the same key within this
// process share a single load. Cache read and write failures are not fatal:
// the value is loaded (and returned) anyway.
func (t *Typed[T]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	if v, ok, err := t.Get(ctx, key); err == nil && ok {
		return v, nil
	}
	return t.group.do(key, func() (T, error) {
		if v, ok, err := t.Get(ctx, key); err == nil && ok {
			return v, nil
		}
		v, err := load(ctx)
		if err != nil {
			return v, err
		}
		_ = t.Set(ctx, key, v, ttl)
		return v, nil
	})
}

// flightGroup deduplicates concurrent loads of the same key.
type flightGroup[T any] struct {
	mu    sync.Mutex
	calls map[string]*flight[T]
}

type flight[T any] struct {
	wg  sync.WaitGroup
	val T
	err error
}

var errLoadPanicked = errors.New("cache: loader panicked")

func (g *flightGroup[T]) do(key string, fn func() (T, error)) (T, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*flight[T])
	}
	if f, ok := g.calls[key]; ok {
		g.mu.Unlock()
		f.wg.Wait()
		return f.val, f.err
	}
	f := &flight[T]{err: errLoadPanicked}
	f.wg.Add(1)
	g.calls[key] = f
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		f.wg.Done()
	}()
	f.val, f.err = fn()
	return f.val, f.err
}
