package httpclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/correlation"
	"github.com/TosmimForidMehtab/torge/httpclient"
)

func newClient(t *testing.T, url string, policy httpclient.RetryPolicy) *httpclient.Client {
	t.Helper()
	policy.InitialBackoff, policy.MaxBackoff = time.Millisecond, 20*time.Millisecond
	c, err := httpclient.New(httpclient.Config{BaseURL: url, Retry: policy, UserAgent: "torge-test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRetriesIdempotentRequests(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, httpclient.RetryPolicy{})

	var out struct{ OK bool }
	if err := c.PutJSON(context.Background(), "/things/1", map[string]int{"n": 1}, &out); err != nil || !out.OK {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
	for _, b := range bodies {
		if b != `{"n":1}` {
			t.Fatalf("body must be replayed on every attempt, got %q", bodies)
		}
	}
}

func TestDoesNotRetryNonIdempotentRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream"}`)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, httpclient.RetryPolicy{})

	err := c.PostJSON(context.Background(), "/payments?api_key=secret", map[string]int{"amount": 1}, nil)
	var se *httpclient.StatusError
	if !errors.As(err, &se) || se.StatusCode != 502 || string(se.Body) != `{"error":"upstream"}` {
		t.Fatalf("unexpected error %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("POST must not be retried, got %d attempts", calls.Load())
	}
	if se.URL != srv.URL+"/payments" {
		t.Fatalf("errors must not contain query strings, got %q", se.URL)
	}

	// With an Idempotency-Key, POST is retried.
	calls.Store(0)
	req, _ := c.NewRequest(context.Background(), http.MethodPost, "/payments", map[string]int{"amount": 1})
	req.Header.Set("Idempotency-Key", "k1")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if calls.Load() != 3 {
		t.Fatalf("POST with Idempotency-Key must be retried, got %d attempts", calls.Load())
	}
}

func TestRespectsLongRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, httpclient.RetryPolicy{})
	err := c.GetJSON(context.Background(), "/", nil)
	if !httpclient.IsStatus(err, 429) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestPropagatesRequestIDAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `"`+r.Header.Get("X-Request-ID")+"|"+r.Header.Get("User-Agent")+`"`)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, httpclient.RetryPolicy{})
	ctx := correlation.WithRequestID(context.Background(), "req-42")
	var got string
	if err := c.GetJSON(ctx, "/", &got); err != nil {
		t.Fatal(err)
	}
	if got != "req-42|torge-test" {
		t.Fatalf("got %q", got)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := httpclient.New(httpclient.Config{BaseURL: srv.URL, Retry: httpclient.RetryPolicy{
		MaxAttempts: 10, InitialBackoff: time.Second, MaxBackoff: time.Second,
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.GetJSON(ctx, "/", nil)
	if err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("expected a prompt context error, got %v after %s", err, time.Since(start))
	}
}

func TestInvalidBaseURL(t *testing.T) {
	if _, err := httpclient.New(httpclient.Config{BaseURL: "not a url"}); err == nil {
		t.Fatal("expected error")
	}
}
