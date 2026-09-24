package cache

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryOptions configures a Memory store.
type MemoryOptions struct {
	// MaxEntries bounds the number of entries; the least recently used entry
	// is evicted when full (default 10000). Memory use is therefore bounded
	// by MaxEntries times the largest value.
	MaxEntries int
	// CleanupInterval controls how often expired entries are swept (default
	// 1m). Expired entries are also dropped lazily on access.
	CleanupInterval time.Duration
}

// Memory is an in-process LRU cache with per-entry TTLs. It is safe for
// concurrent use. Its state is local to the process, so it is not suitable
// for features that must be shared between instances.
type Memory struct {
	mu      sync.Mutex
	max     int
	ll      *list.List
	items   map[string]*list.Element
	now     func() time.Time
	stop    chan struct{}
	stopped sync.Once
}

type entry struct {
	key     string
	value   []byte
	expires time.Time
}

var (
	_ Store = (*Memory)(nil)
	_ Adder = (*Memory)(nil)
	_ Local = (*Memory)(nil)
)

// NewMemory returns a Memory store. Call Close to stop its cleanup goroutine.
func NewMemory(opts ...MemoryOptions) *Memory {
	var o MemoryOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 10000
	}
	if o.CleanupInterval <= 0 {
		o.CleanupInterval = time.Minute
	}
	m := &Memory{
		max:   o.MaxEntries,
		ll:    list.New(),
		items: make(map[string]*list.Element),
		now:   time.Now,
		stop:  make(chan struct{}),
	}
	go m.janitor(o.CleanupInterval)
	return m
}

// IsLocal implements Local.
func (m *Memory) IsLocal() bool { return true }

// Get implements Store.
func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.items[key]
	if !ok {
		return nil, ErrNotFound
	}
	e := el.Value.(*entry)
	if m.expired(e) {
		m.remove(el)
		return nil, ErrNotFound
	}
	m.ll.MoveToFront(el)
	return clone(e.value), nil
}

// Set implements Store.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set(key, value, ttl)
	return nil
}

// Add implements Adder.
func (m *Memory) Add(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.items[key]; ok && !m.expired(el.Value.(*entry)) {
		return false, nil
	}
	m.set(key, value, ttl)
	return true, nil
}

func (m *Memory) set(key string, value []byte, ttl time.Duration) {
	var expires time.Time
	if ttl > 0 {
		expires = m.now().Add(ttl)
	}
	if el, ok := m.items[key]; ok {
		e := el.Value.(*entry)
		e.value, e.expires = clone(value), expires
		m.ll.MoveToFront(el)
		return
	}
	m.items[key] = m.ll.PushFront(&entry{key: key, value: clone(value), expires: expires})
	for m.ll.Len() > m.max {
		m.remove(m.ll.Back())
	}
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		if el, ok := m.items[k]; ok {
			m.remove(el)
		}
	}
	return nil
}

// DeletePrefix implements Store.
func (m *Memory) DeletePrefix(_ context.Context, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, el := range m.items {
		if strings.HasPrefix(k, prefix) {
			m.remove(el)
		}
	}
	return nil
}

// Len returns the number of entries, including expired ones not yet swept.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ll.Len()
}

// Close stops the cleanup goroutine. The store remains usable.
func (m *Memory) Close() error {
	m.stopped.Do(func() { close(m.stop) })
	return nil
}

func (m *Memory) expired(e *entry) bool {
	return !e.expires.IsZero() && !m.now().Before(e.expires)
}

func (m *Memory) remove(el *list.Element) {
	m.ll.Remove(el)
	delete(m.items, el.Value.(*entry).key)
}

func (m *Memory) janitor(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Memory) sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, el := range m.items {
		if m.expired(el.Value.(*entry)) {
			m.remove(el)
		}
	}
}

func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append(make([]byte, 0, len(b)), b...)
}
