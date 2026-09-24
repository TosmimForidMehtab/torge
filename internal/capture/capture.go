// Package capture records a response while it is written to the client, so
// middleware can persist and replay it (response caching, idempotency).
package capture

import (
	"bytes"
	"net/http"
)

// Response is a serializable HTTP response.
type Response struct {
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
}

// replayedHeaders are the response headers worth persisting. Hop-by-hop and
// per-request headers (Date, Set-Cookie, X-Request-Id) are deliberately
// excluded.
var replayedHeaders = []string{
	"Content-Type", "Content-Encoding", "Content-Language", "Content-Disposition",
	"Cache-Control", "ETag", "Last-Modified", "Location", "Vary",
}

// WriteTo writes the response to w. Extra headers are set first.
func (r *Response) WriteTo(w http.ResponseWriter, extra map[string]string) error {
	h := w.Header()
	for k, v := range r.Header {
		h[k] = append([]string(nil), v...)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	w.WriteHeader(r.Status)
	_, err := w.Write(r.Body)
	return err
}

// Recorder tees a response into a bounded buffer.
type Recorder struct {
	http.ResponseWriter
	limit    int
	status   int
	header   http.Header
	buf      bytes.Buffer
	overflow bool
}

// New returns a Recorder writing through w and buffering at most limit bytes.
func New(w http.ResponseWriter, limit int) *Recorder {
	return &Recorder{ResponseWriter: w, limit: limit}
}

// WriteHeader implements http.ResponseWriter.
func (r *Recorder) WriteHeader(code int) {
	if r.status == 0 && (code < 100 || code >= 200) {
		r.status = code
		r.header = make(http.Header, len(replayedHeaders))
		src := r.ResponseWriter.Header()
		for _, k := range replayedHeaders {
			if v, ok := src[k]; ok {
				r.header[k] = append([]string(nil), v...)
			}
		}
		if _, ok := src["Set-Cookie"]; ok {
			// Responses that set cookies are user-specific: never persist.
			r.overflow = true
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (r *Recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	if !r.overflow {
		if r.buf.Len()+len(b) > r.limit {
			r.overflow = true
			r.buf = bytes.Buffer{}
		} else {
			r.buf.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

// Flush implements http.Flusher.
func (r *Recorder) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// Unwrap supports http.ResponseController.
func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Result returns the captured response. ok is false if nothing was written,
// the body exceeded the limit, or the response set cookies.
func (r *Recorder) Result() (Response, bool) {
	if r.status == 0 || r.overflow {
		return Response{}, false
	}
	return Response{Status: r.status, Header: r.header, Body: bytes.Clone(r.buf.Bytes())}, true
}
