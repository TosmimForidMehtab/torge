package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(MemoryOptions{MaxEntries: 3})
	defer m.Close()
	now := time.Unix(0, 0)
	m.now = func() time.Time { return now }

	value := []byte("v")
	_ = m.Set(ctx, "a", value, time.Second)
	value[0] = 'x'
	got, err := m.Get(ctx, "a")
	if err != nil || string(got) != "v" {
		t.Fatalf("stored values must be copied: %q %v", got, err)
	}
	now = now.Add(time.Second)
	if _, err := m.Get(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired entries must not be returned")
	}

	for _, k := range []string{"k1", "k2", "k3"} {
		_ = m.Set(ctx, k, []byte(k), 0)
	}
	_, _ = m.Get(ctx, "k1") // k1 becomes most recently used
	_ = m.Set(ctx, "k4", nil, 0)
	if _, err := m.Get(ctx, "k2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the least recently used entry must be evicted")
	}
	if m.Len() != 3 {
		t.Fatalf("len = %d", m.Len())
	}

	ok, _ := m.Add(ctx, "k1", []byte("new"), 0)
	if ok {
		t.Fatal("Add must not overwrite")
	}
	ok, _ = m.Add(ctx, "k9", []byte("new"), 0)
	if !ok {
		t.Fatal("Add must set absent keys")
	}
}

func TestNamespace(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	defer m.Close()
	users := Namespace(m, "users:")
	orders := Namespace(m, "orders:")
	_ = users.Set(ctx, "1", []byte("ada"), 0)
	_ = orders.Set(ctx, "1", []byte("order"), 0)
	if err := users.DeletePrefix(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Get(ctx, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("namespace invalidation failed")
	}
	if v, _ := orders.Get(ctx, "1"); string(v) != "order" {
		t.Fatal("other namespaces must be untouched")
	}
	if !IsLocal(users) {
		t.Fatal("namespaces must report the locality of their store")
	}
	if ok, err := Add(ctx, users, "x", nil, 0); !ok || err != nil {
		t.Fatalf("Add through namespace: %v %v", ok, err)
	}
}

type user struct{ Name string }

func TestTypedGetOrLoadDeduplicates(t *testing.T) {
	m := NewMemory()
	defer m.Close()
	users := NewTyped[user](Namespace(m, "u:"))
	var loads atomic.Int32
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			u, err := users.GetOrLoad(context.Background(), "1", time.Minute, func(context.Context) (user, error) {
				loads.Add(1)
				<-gate
				return user{Name: "ada"}, nil
			})
			if err != nil || u.Name != "ada" {
				t.Errorf("got %+v %v", u, err)
			}
		})
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("expected one load, got %d", loads.Load())
	}
	if u, ok, _ := users.Get(context.Background(), "1"); !ok || u.Name != "ada" {
		t.Fatal("loaded value must be cached")
	}
}

func TestTypedLoadError(t *testing.T) {
	m := NewMemory()
	defer m.Close()
	c := NewTyped[int](m)
	boom := errors.New("boom")
	if _, err := c.GetOrLoad(context.Background(), "k", 0, func(context.Context) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatal("load errors must propagate")
	}
	if _, ok, _ := c.Get(context.Background(), "k"); ok {
		t.Fatal("failed loads must not be cached")
	}
}
