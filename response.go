package torge

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
)

// ResponseWriter wraps http.ResponseWriter to record the status code and the
// number of bytes written, and to run hooks just before headers are sent.
//
// It supports http.Flusher, http.Hijacker, io.ReaderFrom and
// http.ResponseController (through Unwrap), so streaming, WebSockets and
// sendfile keep working.
type ResponseWriter struct {
	http.ResponseWriter
	status  int
	size    int64
	written bool
	before  []func()
}

func newResponseWriter(w http.ResponseWriter) *ResponseWriter {
	return &ResponseWriter{ResponseWriter: w}
}

// Status returns the status code sent, or 0 if headers were not written.
func (w *ResponseWriter) Status() int { return w.status }

// Size returns the number of body bytes written.
func (w *ResponseWriter) Size() int64 { return w.size }

// Written reports whether headers have been sent.
func (w *ResponseWriter) Written() bool { return w.written }

// Before registers fn to run immediately before headers are written. It is the
// last point at which headers and cookies can be changed. It reports false if
// headers were already written, in which case fn is not registered.
func (w *ResponseWriter) Before(fn func()) bool {
	if w.written {
		return false
	}
	w.before = append(w.before, fn)
	return true
}

// WriteHeader implements http.ResponseWriter. Informational (1xx) statuses
// other than 101 are passed through without committing the response.
func (w *ResponseWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.runBefore()
	w.status = code
	w.written = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *ResponseWriter) runBefore() {
	hooks := w.before
	w.before = nil
	// Run in reverse registration order so that outer middleware sees the
	// response after inner middleware adjusted it, mirroring unwinding.
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i]()
	}
}

// Write implements http.ResponseWriter.
func (w *ResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.size += int64(n)
	return n, err
}

// WriteString implements io.StringWriter.
func (w *ResponseWriter) WriteString(s string) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	n, err := io.WriteString(w.ResponseWriter, s)
	w.size += int64(n)
	return n, err
}

// ReadFrom implements io.ReaderFrom, enabling sendfile when the underlying
// writer supports it.
func (w *ResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(writerOnly{w.ResponseWriter}, r)
	}
	w.size += n
	return n, err
}

// writerOnly hides ReadFrom to prevent infinite recursion in io.Copy.
type writerOnly struct{ io.Writer }

// Flush implements http.Flusher.
func (w *ResponseWriter) Flush() { _ = w.FlushError() }

// FlushError flushes buffered data to the client, committing headers first.
func (w *ResponseWriter) FlushError() error {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack implements http.Hijacker.
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		if w.status == 0 {
			w.status = http.StatusSwitchingProtocols
		}
		w.written = true
	}
	return conn, rw, err
}

// Unwrap returns the underlying writer for http.ResponseController.
func (w *ResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// errAlreadyWritten is returned by helpers that need to write headers after the
// response was committed.
var errAlreadyWritten = errors.New("torge: response already written")
