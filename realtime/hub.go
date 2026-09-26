// Package realtime provides topic-based Server-Sent Events (SSE)
// broadcasting on top of Torge.
//
// A Hub keeps a per-topic replay buffer and fans published events out to
// subscribers without ever blocking the publisher. Handlers bridge a Hub
// to HTTP with Serve:
//
//	hub := realtime.NewHub()
//
//	go func() {
//		for reading := range sensor {
//			hub.Publish("room:1", realtime.Event{Name: "reading", Data: reading})
//		}
//	}()
//
//	torge.Get(app, "/rooms/:id/events", func(c *torge.Context) error {
//		return hub.Serve(c, "room:"+c.Param("id"))
//	})
//
// Raw WebSockets stay bring-your-own via hijacking; see docs/guide.md.
package realtime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/TosmimForidMehtab/torge"
)

// DefaultReplayBufferSize is the per-topic replay buffer size used by NewHub
// when no HubOption overrides it.
//
// Example:
//
//	hub := realtime.NewHub(realtime.WithReplayBufferSize(128))
const DefaultReplayBufferSize = 64

// minSubscriberBufferSize is the floor for a subscriber channel buffer so
// live events still queue even when replay is disabled. It also bounds how
// many live events a slow subscriber can lag before Publish starts dropping
// events addressed to it.
const minSubscriberBufferSize = 16

// Event is a single broadcast message. ID maps to the SSE id field, Name to
// the SSE event field, and Data to the SSE data field (encoded by Serve).
//
// A zero ID is replaced by Publish with a monotonically increasing
// per-topic numeric ID ("1", "2", ...). Set ID explicitly to use your own
// resume tokens instead.
//
// Example:
//
//	realtime.Event{Name: "message", Data: map[string]string{"text": "hi"}}
type Event struct {
	// ID sets the SSE id field; empty means Publish assigns the next
	// per-topic sequence number.
	ID string
	// Name sets the SSE event field; empty means "message".
	Name string
	// Data is the payload. Serve sends strings and []byte as-is and
	// JSON-encodes anything else.
	Data any
}

// HubOption tunes a Hub. Pass options to NewHub.
//
// Example:
//
//	hub := realtime.NewHub(realtime.WithReplayBufferSize(256))
type HubOption func(*hubConfig)

type hubConfig struct {
	replay int
}

// WithReplayBufferSize sets how many recent events per topic are kept for
// Last-Event-ID replay. Non-positive sizes disable replay; the Hub still
// streams live events.
//
// Example:
//
//	hub := realtime.NewHub(realtime.WithReplayBufferSize(0)) // live only
func WithReplayBufferSize(size int) HubOption {
	return func(c *hubConfig) {
		if size < 0 {
			size = 0
		}
		c.replay = size
	}
}

// Hub is a concurrency-safe topic-based SSE broadcast hub. The zero value is
// not usable; construct it with NewHub.
//
// Publishing never blocks: each subscriber has its own buffered channel and
// an event is dropped for a subscriber whose buffer is full, while every
// other subscriber still receives it. The Hub itself starts no goroutines,
// so Unsubscribe and Close cannot leak any.
//
// Example:
//
//	hub := realtime.NewHub()
//	ch, unsubscribe := hub.Subscribe("room:1", "")
//	defer unsubscribe()
//	hub.Publish("room:1", realtime.Event{Name: "hello", Data: "world"})
//	e := <-ch
type Hub struct {
	mu     sync.Mutex
	topics map[string]*hubTopic
	replay int
	closed bool
}

type hubTopic struct {
	next uint64
	buf  []Event
	subs map[*hubSubscriber]struct{}
}

type hubSubscriber struct {
	ch     chan Event
	closed bool
}

// NewHub returns a Hub that keeps up to DefaultReplayBufferSize recent
// events per topic for replay, or the size set by WithReplayBufferSize.
//
// Example:
//
//	hub := realtime.NewHub(realtime.WithReplayBufferSize(32))
//	hub.Publish("alerts", realtime.Event{Name: "ping", Data: "up"})
func NewHub(opts ...HubOption) *Hub {
	cfg := hubConfig{replay: DefaultReplayBufferSize}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &Hub{topics: make(map[string]*hubTopic), replay: cfg.replay}
}

// Publish delivers e to every subscriber of topic and records it in the
// topic's replay buffer. When e.ID is empty it is set to the next per-topic
// sequence number, which always advances once per Publish even when custom
// IDs are used. Publish never blocks: a subscriber whose buffer is full
// misses that event. Publishing to a closed Hub is a no-op.
//
// Example:
//
//	hub.Publish("room:1", realtime.Event{Name: "message", Data: "hi"})
func (h *Hub) Publish(topic string, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	t := h.topicLocked(topic)
	t.next++
	if e.ID == "" {
		e.ID = strconv.FormatUint(t.next, 10)
	}
	if h.replay > 0 {
		t.buf = append(t.buf, e)
		if len(t.buf) > h.replay {
			t.buf = append([]Event(nil), t.buf[len(t.buf)-h.replay:]...)
		}
	}
	for sub := range t.subs {
		select {
		case sub.ch <- e:
		default:
			// Slow subscriber: drop for it rather than
			// blocking every other subscriber and the publisher.
		}
	}
}

// Subscribe returns a channel that first receives buffered events published
// after lastID (the client's Last-Event-ID; "" disables replay) and then
// live events. IDs are per-topic sequence numbers: a numeric lastID replays
// buffered events with a greater number, while events with custom
// non-numeric IDs, or any buffered event when lastID is not numeric, are
// replayed because they cannot be proven seen.
//
// The channel is closed when unsubscribe is called or the Hub is closed; the
// receive's ok flag distinguishes that from an event. Unsubscribe is
// idempotent and, like Close, starts no goroutines, so neither can leak.
//
// Example:
//
//	ch, unsubscribe := hub.Subscribe("room:1", lastID)
//	defer unsubscribe()
//	for e := range ch {
//		fmt.Println(e.ID, e.Name, e.Data)
//	}
func (h *Hub) Subscribe(topic, lastID string) (<-chan Event, func()) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	t := h.topicLocked(topic)
	size := h.replay
	if size < minSubscriberBufferSize {
		size = minSubscriberBufferSize
	}
	sub := &hubSubscriber{ch: make(chan Event, size)}
	t.subs[sub] = struct{}{}
	if lastID != "" {
		for _, e := range t.buf {
			// Replay fits: at most len(t.buf) <= replay <= cap(sub.ch)
			// events are queued into an empty channel.
			if afterLastID(e.ID, lastID) {
				sub.ch <- e
			}
		}
	}
	h.mu.Unlock()
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() { h.removeSub(topic, sub) })
	}
	return sub.ch, unsubscribe
}

// Close unblocks every subscriber by closing its channel and releases all
// topics. Later Publish calls are no-ops and later Subscribe calls return an
// already-closed channel. Close is idempotent.
//
// Example:
//
//	hub := realtime.NewHub()
//	defer hub.Close()
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, t := range h.topics {
		for sub := range t.subs {
			if !sub.closed {
				sub.closed = true
				close(sub.ch)
			}
		}
	}
	h.topics = make(map[string]*hubTopic)
}

// Serve bridges a Hub topic to HTTP: it reads the Last-Event-ID request
// header, subscribes, and sends each event through Torge's SSE API until
// the client disconnects or the Hub is closed, returning nil in both cases.
// It returns an error only when the SSE stream cannot start, an event
// payload cannot be encoded, or a write fails.
//
// Data encoding: nil sends an empty data field, strings and []byte are sent
// as-is, and anything else is JSON-encoded.
//
// Example:
//
//	torge.Get(app, "/events", func(c *torge.Context) error {
//		return hub.Serve(c, "room:1")
//	})
func (h *Hub) Serve(c *torge.Context, topic string) error {
	lastID := c.GetHeader("Last-Event-ID")
	sse, err := c.SSE()
	if err != nil {
		return err
	}
	ch, unsubscribe := h.Subscribe(topic, lastID)
	defer unsubscribe()
	for {
		select {
		case <-sse.Done():
			return nil
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			data, err := encodeHubData(e.Data)
			if err != nil {
				return fmt.Errorf("realtime: encode event data: %w", err)
			}
			if err := sse.Send(torge.SSEEvent{ID: e.ID, Event: e.Name, Data: data}); err != nil {
				return err
			}
		}
	}
}

func (h *Hub) topicLocked(topic string) *hubTopic {
	t, ok := h.topics[topic]
	if !ok {
		t = &hubTopic{subs: make(map[*hubSubscriber]struct{})}
		h.topics[topic] = t
	}
	return t
}

func (h *Hub) removeSub(topic string, sub *hubSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.topics[topic]; ok {
		delete(t.subs, sub)
	}
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
}

// afterLastID reports whether the buffered event id was published after
// lastID. Numeric IDs compare numerically; anything that cannot be ordered
// (a non-numeric lastID, or an event with a custom non-numeric ID alongside
// a numeric lastID) replays, since it cannot be proven already seen.
func afterLastID(id, lastID string) bool {
	last, err := strconv.ParseUint(lastID, 10, 64)
	if err != nil {
		return true
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return true
	}
	return n > last
}

// encodeHubData renders an Event payload for the SSE data field.
func encodeHubData(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case []byte:
		return string(t), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
