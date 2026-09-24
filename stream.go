package torge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// beginStream prepares a long-lived response: the server write timeout is
// lifted for this request, and the request context is canceled when the
// application starts shutting down, because streams never become idle on
// their own and would otherwise hold graceful shutdown until its timeout.
func (c *Context) beginStream() {
	_ = http.NewResponseController(c.res).SetWriteDeadline(time.Time{})
	if c.app == nil {
		return
	}
	ctx, cancel := context.WithCancel(c.Context())
	stop := context.AfterFunc(c.app.stopping, cancel)
	c.SetContext(ctx)
	c.OnDone(func() {
		stop()
		cancel()
	})
}

// Stream writes a streaming response. fn receives a writer that flushes to
// the client on every Write. The server write timeout is lifted for this
// request, and c.Context() is canceled when the client disconnects or the
// application begins shutting down; fn must return when it is. Writes fail
// once the context is canceled.
func (c *Context) Stream(status int, contentType string, fn func(w io.Writer) error) error {
	if c.res.Written() {
		return errAlreadyWritten
	}
	c.beginStream()
	if contentType != "" {
		c.res.Header().Set("Content-Type", contentType)
	}
	c.res.Header().Del("Content-Length")
	c.res.WriteHeader(status)
	return fn(flushWriter{c: c})
}

type flushWriter struct{ c *Context }

func (w flushWriter) Write(p []byte) (int, error) {
	if err := w.c.Context().Err(); err != nil {
		return 0, err
	}
	n, err := w.c.res.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.c.res.FlushError()
}

// Flush sends buffered response data to the client.
func (c *Context) Flush() error { return c.res.FlushError() }

// SSEEvent is a Server-Sent Event.
type SSEEvent struct {
	// ID sets the event ID, which clients send back as Last-Event-ID.
	ID string
	// Event is the event type; empty means "message".
	Event string
	// Data is the payload. Multi-line data is split into data lines.
	Data string
	// Retry tells the client how long to wait before reconnecting.
	Retry time.Duration
}

// SSE is a Server-Sent Events stream. Obtain one with Context.SSE.
type SSE struct {
	c   *Context
	buf bytes.Buffer
}

// ErrInvalidSSEField is returned when an event's ID or type contains a line
// break.
var ErrInvalidSSEField = errors.New("torge: SSE id and event must not contain line breaks")

// SSE starts a Server-Sent Events response. The write timeout is lifted for
// this request. Send returns an error once the client disconnects; select on
// Done to stop producing events.
//
//	sse, err := c.SSE()
//	if err != nil { return err }
//	for {
//	    select {
//	    case <-sse.Done():
//	        return nil
//	    case msg := <-updates:
//	        if err := sse.SendJSON("update", msg); err != nil { return err }
//	    }
//	}
func (c *Context) SSE() (*SSE, error) {
	if c.res.Written() {
		return nil, errAlreadyWritten
	}
	c.beginStream()
	h := c.res.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Del("Content-Length")
	c.res.WriteHeader(http.StatusOK)
	if err := c.res.FlushError(); err != nil {
		return nil, err
	}
	return &SSE{c: c}, nil
}

// Done is closed when the client disconnects or the request is canceled.
func (s *SSE) Done() <-chan struct{} { return s.c.Context().Done() }

// Context returns the request context.
func (s *SSE) Context() context.Context { return s.c.Context() }

// Send writes one event and flushes it.
func (s *SSE) Send(e SSEEvent) error {
	if err := s.c.Context().Err(); err != nil {
		return err
	}
	if strings.ContainsAny(e.ID, "\r\n") || strings.ContainsAny(e.Event, "\r\n") {
		return ErrInvalidSSEField
	}
	b := &s.buf
	b.Reset()
	if e.ID != "" {
		b.WriteString("id: ")
		b.WriteString(e.ID)
		b.WriteByte('\n')
	}
	if e.Event != "" {
		b.WriteString("event: ")
		b.WriteString(e.Event)
		b.WriteByte('\n')
	}
	if e.Retry > 0 {
		b.WriteString("retry: ")
		b.WriteString(strconv.FormatInt(e.Retry.Milliseconds(), 10))
		b.WriteByte('\n')
	}
	data := strings.ReplaceAll(e.Data, "\r\n", "\n")
	for line := range strings.SplitSeq(data, "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := s.c.res.Write(b.Bytes()); err != nil {
		return err
	}
	return s.c.res.FlushError()
}

// SendJSON sends an event whose data is v encoded as JSON.
func (s *SSE) SendJSON(event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Send(SSEEvent{Event: event, Data: string(data)})
}

// Comment sends a comment line, commonly used as a keep-alive.
func (s *SSE) Comment(text string) error {
	if err := s.c.Context().Err(); err != nil {
		return err
	}
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if _, err := s.c.res.WriteString(": " + text + "\n\n"); err != nil {
		return err
	}
	return s.c.res.FlushError()
}
