package realtime_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/realtime"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func recv(t *testing.T, ch <-chan realtime.Event) realtime.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for event")
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
		return realtime.Event{}
	}
}

func TestPublishSubscribeDelivery(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	ch, unsubscribe := h.Subscribe("room:1", "")
	defer unsubscribe()

	h.Publish("room:1", realtime.Event{Name: "a", Data: "1"})
	h.Publish("room:1", realtime.Event{Name: "b", Data: "2"})
	h.Publish("other", realtime.Event{Name: "nope", Data: "x"})
	h.Publish("room:1", realtime.Event{Name: "c", Data: "3"})

	for i, want := range []struct{ id, name, data string }{
		{"1", "a", "1"},
		{"2", "b", "2"},
		{"3", "c", "3"},
	} {
		e := recv(t, ch)
		if e.ID != want.id || e.Name != want.name || e.Data != want.data {
			t.Fatalf("event %d: got %+v, want id=%s name=%s data=%s", i, e, want.id, want.name, want.data)
		}
	}
}

func TestReplayAfterLastID(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	h.Publish("news", realtime.Event{Name: "n", Data: "1"})
	h.Publish("news", realtime.Event{Name: "n", Data: "2"})
	h.Publish("news", realtime.Event{Name: "n", Data: "3"})

	ch, unsubscribe := h.Subscribe("news", "1")
	defer unsubscribe()

	if e := recv(t, ch); e.ID != "2" {
		t.Fatalf("first replayed id = %q, want 2", e.ID)
	}
	if e := recv(t, ch); e.ID != "3" {
		t.Fatalf("second replayed id = %q, want 3", e.ID)
	}

	h.Publish("news", realtime.Event{Name: "n", Data: "4"})
	if e := recv(t, ch); e.ID != "4" {
		t.Fatalf("live id = %q, want 4", e.ID)
	}
}

func TestReplayBufferCap(t *testing.T) {
	h := realtime.NewHub(realtime.WithReplayBufferSize(2))
	defer h.Close()
	for _, d := range []string{"1", "2", "3", "4"} {
		h.Publish("news", realtime.Event{Name: "n", Data: d})
	}
	ch, unsubscribe := h.Subscribe("news", "0")
	defer unsubscribe()

	// Only the last 2 events are retained.
	if e := recv(t, ch); e.ID != "3" {
		t.Fatalf("first replayed id = %q, want 3", e.ID)
	}
	if e := recv(t, ch); e.ID != "4" {
		t.Fatalf("second replayed id = %q, want 4", e.ID)
	}
	select {
	case e := <-ch:
		t.Fatalf("unexpected extra event %+v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscribeWithoutLastIDGetsOnlyLive(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	h.Publish("news", realtime.Event{Name: "n", Data: "old"})

	ch, unsubscribe := h.Subscribe("news", "")
	defer unsubscribe()

	select {
	case e := <-ch:
		t.Fatalf("unexpected replayed event %+v", e)
	case <-time.After(100 * time.Millisecond):
	}

	h.Publish("news", realtime.Event{Name: "n", Data: "new"})
	if e := recv(t, ch); e.Data != "new" {
		t.Fatalf("live data = %v, want new", e.Data)
	}
}

func TestCustomIDPreserved(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	ch, unsubscribe := h.Subscribe("t", "")
	defer unsubscribe()

	h.Publish("t", realtime.Event{Name: "a", Data: "x"})
	h.Publish("t", realtime.Event{ID: "custom-1", Name: "b", Data: "y"})
	h.Publish("t", realtime.Event{Name: "c", Data: "z"})

	if e := recv(t, ch); e.ID != "1" {
		t.Fatalf("id = %q, want 1", e.ID)
	}
	if e := recv(t, ch); e.ID != "custom-1" {
		t.Fatalf("id = %q, want custom-1", e.ID)
	}
	// The sequence advances even for custom IDs, so auto IDs stay unique.
	if e := recv(t, ch); e.ID != "3" {
		t.Fatalf("id = %q, want 3", e.ID)
	}
}

func TestUnsubscribeTerminates(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	ch, unsubscribe := h.Subscribe("t", "")
	h.Publish("t", realtime.Event{Data: "before"})
	if e := recv(t, ch); e.Data != "before" {
		t.Fatalf("data = %v, want before", e.Data)
	}

	unsubscribe()
	unsubscribe() // idempotent

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel after unsubscribe")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after unsubscribe")
	}

	// Publishing after unsubscribe must not panic or block.
	h.Publish("t", realtime.Event{Data: "after"})
}

func TestCloseUnblocksSubscribers(t *testing.T) {
	h := realtime.NewHub()
	ch1, unsub1 := h.Subscribe("a", "")
	defer unsub1()
	ch2, unsub2 := h.Subscribe("b", "")
	defer unsub2()

	h.Close()
	h.Close() // idempotent

	for i, ch := range []<-chan realtime.Event{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("subscriber %d: expected closed channel", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("subscriber %d: not unblocked by Close", i)
		}
	}

	h.Publish("a", realtime.Event{Data: "dropped"}) // no-op, must not panic

	ch, unsub := h.Subscribe("a", "")
	defer unsub()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("subscribe after Close must return a closed channel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe after Close did not return a closed channel")
	}
}

func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	ch, unsubscribe := h.Subscribe("t", "")
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 500 {
			h.Publish("t", realtime.Event{Name: "n", Data: i})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// The slow subscriber was disconnected instead of silently missing
	// events: its channel is closed after the buffered head.
	n := 0
	for range ch {
		n++
	}
	if n == 0 {
		t.Fatal("slow subscriber received nothing before disconnect")
	}
}

func TestSlowSubscriberReconnectsAndReplays(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	ch, _ := h.Subscribe("t", "")

	const total = 201
	for i := range total {
		h.Publish("t", realtime.Event{Name: "n", Data: i})
	}
	// The subscriber kept the first 64 live events, then was disconnected.
	var lastID string
	n := 0
	for e := range ch {
		n++
		lastID = e.ID
	}
	if n != 64 || lastID != "64" {
		t.Fatalf("disconnected after %d events at id %q, want 64 at 64", n, lastID)
	}

	// Reconnecting with Last-Event-ID replays the retained tail: the last
	// 64 of the 201 published events.
	rech, reunsub := h.Subscribe("t", lastID)
	defer reunsub()
	var first, last string
	m := 0
	for e := range rech {
		if m == 0 {
			first = e.ID
		}
		m++
		last = e.ID
		if m == 64 {
			break
		}
	}
	if m != 64 || first != "138" || last != "201" {
		t.Fatalf("replayed %d events from %q to %q, want 64 from 138 to 201", m, first, last)
	}
}

func TestTopicsFreedAfterUnsubscribe(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	for i := range 100 {
		ch, unsubscribe := h.Subscribe(fmt.Sprintf("room:%d", i), "")
		_ = ch
		unsubscribe()
	}
	if n := h.TopicCount(); n != 0 {
		t.Fatalf("topics retained = %d, want 0", n)
	}
}

func TestTopicRetainedForReplay(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	h.Publish("news", realtime.Event{Name: "n", Data: "1"})
	ch, unsubscribe := h.Subscribe("news", "")
	unsubscribe()
	// The buffer is retained so a later subscriber can still replay it.
	if n := h.TopicCount(); n != 1 {
		t.Fatalf("topics retained = %d, want 1", n)
	}
	_ = ch
	rech, reunsub := h.Subscribe("news", "0")
	defer reunsub()
	if e := recv(t, rech); e.ID != "1" {
		t.Fatalf("replayed id = %q, want 1", e.ID)
	}
}

func TestConcurrentPublishers(t *testing.T) {
	const buffer = 10000
	const publishers = 8
	const perPublisher = 200
	h := realtime.NewHub(realtime.WithReplayBufferSize(buffer))
	defer h.Close()
	ch, unsubscribe := h.Subscribe("t", "")
	defer unsubscribe()

	var wg sync.WaitGroup
	for p := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perPublisher {
				h.Publish("t", realtime.Event{Name: "n", Data: struct {
					P, I int
				}{p, i}})
			}
		}()
	}
	wg.Wait()

	total := publishers * perPublisher
	seen := make(map[string]struct{}, total)
	timeout := time.After(10 * time.Second)
	for len(seen) < total {
		select {
		case e := <-ch:
			if _, dup := seen[e.ID]; dup {
				t.Fatalf("duplicate id %q", e.ID)
			}
			seen[e.ID] = struct{}{}
		case <-timeout:
			t.Fatalf("got %d of %d events", len(seen), total)
		}
	}
	// IDs are the sequence 1..total exactly once. The buffer (10000) exceeds
	// the total published (1600), so nothing may have been dropped.
	for i := 1; i <= total; i++ {
		id := itoa(i)
		if _, ok := seen[id]; !ok {
			t.Fatalf("missing id %q", id)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func readSSEBlock(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var out string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE block: %v (got %q)", err, out)
		}
		out += line
		if line == "\n" {
			return out
		}
	}
}

func TestServeSSEStreamsReplayAndLive(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()
	h.Publish("news", realtime.Event{Name: "greeting", Data: "hello"})
	h.Publish("news", realtime.Event{Name: "greeting", Data: "hello2"})

	app := torgetest.NewApp(t)
	serveErr := make(chan error, 1)
	app.GET("/events", func(c *torge.Context) error {
		err := h.Serve(c, "news")
		serveErr <- err
		return err
	})
	srv := torgetest.Server(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "1")

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	r := bufio.NewReader(res.Body)
	// Reconnect replay: only the event published after Last-Event-ID 1.
	if got, want := readSSEBlock(t, r), "id: 2\nevent: greeting\ndata: hello2\n\n"; got != want {
		t.Fatalf("replay block = %q, want %q", got, want)
	}

	h.Publish("news", realtime.Event{Name: "greeting", Data: "live"})
	if got, want := readSSEBlock(t, r), "id: 3\nevent: greeting\ndata: live\n\n"; got != want {
		t.Fatalf("live block = %q, want %q", got, want)
	}

	// Client disconnect ends Serve with nil.
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after client disconnect")
	}
}

func TestServeSSEEncodesJSONData(t *testing.T) {
	h := realtime.NewHub()
	defer h.Close()

	app := torgetest.NewApp(t)
	serveErr := make(chan error, 1)
	app.GET("/events", func(c *torge.Context) error {
		err := h.Serve(c, "t")
		serveErr <- err
		return err
	})
	srv := torgetest.Server(t, app)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	// Give Serve a moment to subscribe, then publish a structured event.
	time.Sleep(200 * time.Millisecond)
	h.Publish("t", realtime.Event{Name: "update", Data: map[string]int{"n": 1}})

	if got, want := readSSEBlock(t, bufio.NewReader(res.Body)), "id: 1\nevent: update\ndata: {\"n\":1}\n\n"; got != want {
		t.Fatalf("block = %q, want %q", got, want)
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after client disconnect")
	}
}
