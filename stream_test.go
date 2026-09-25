package torge_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestSSE(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/events", func(c *torge.Context) error {
		sse, err := c.SSE()
		if err != nil {
			return err
		}
		if err := sse.Send(torge.SSEEvent{ID: "1", Event: "greeting", Data: "hello\nworld", Retry: time.Second}); err != nil {
			return err
		}
		if err := sse.Send(torge.SSEEvent{Event: "bad\nevent"}); err != torge.ErrInvalidSSEField {
			return fmt.Errorf("expected ErrInvalidSSEField, got %v", err)
		}
		if err := sse.Comment("keep-alive"); err != nil {
			return err
		}
		return sse.SendJSON("data", map[string]int{"n": 1})
	})
	res := torgetest.New(t, app).GET("/events").Do().ExpectStatus(200).ExpectHeader("Content-Type", "text/event-stream")
	want := "id: 1\nevent: greeting\nretry: 1000\ndata: hello\ndata: world\n\n: keep-alive\n\nevent: data\ndata: {\"n\":1}\n\n"
	if string(res.Body) != want {
		t.Fatalf("got %q", res.Body)
	}
}

func TestStreamingOverNetworkRespectsCancellation(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithServer(torge.ServerConfig{WriteTimeout: 50 * time.Millisecond}))
	stopped := make(chan error, 1)
	app.GET("/stream", func(c *torge.Context) error {
		err := c.Stream(200, "text/plain", func(w io.Writer) error {
			for i := 0; ; i++ {
				if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
					return err
				}
				select {
				case <-c.Context().Done():
					return c.Context().Err()
				case <-time.After(20 * time.Millisecond):
				}
			}
		})
		stopped <- err
		return err
	})
	srv := torgetest.Server(t, app) // serves with the app's 50ms write timeout

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/stream", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(res.Body)
	// Read past the server write timeout: streaming lifts it.
	for i := range 8 {
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "line ") {
			t.Fatalf("line %d: %q %v", i, line, err)
		}
	}
	cancel()
	res.Body.Close()
	select {
	case err := <-stopped:
		if err == nil {
			t.Fatal("stream must end with the cancellation error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not observe client disconnect")
	}
}

func TestShutdownEndsStreamsPromptly(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithServer(torge.ServerConfig{ShutdownTimeout: 10 * time.Second}))
	streaming := make(chan struct{})
	app.GET("/events", func(c *torge.Context) error {
		sse, err := c.SSE()
		if err != nil {
			return err
		}
		close(streaming)
		<-sse.Done() // a stream never becomes idle on its own
		return nil
	})
	ln := newLocalListener(t)
	serveErr := make(chan error, 1)
	go func() { serveErr <- app.Serve(ln) }()
	waitRunning(t, app)

	res, err := http.Get("http://" + ln.Addr().String() + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	<-streaming

	start := time.Now()
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown waited %s for an open stream", elapsed)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
}
