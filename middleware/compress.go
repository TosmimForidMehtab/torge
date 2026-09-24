package middleware

import (
	"bufio"
	"compress/gzip"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/TosmimForidMehtab/torge"
)

// CompressConfig configures response compression.
type CompressConfig struct {
	// Level is the gzip level (default gzip.DefaultCompression).
	Level int
	// MinLength is the minimum body size worth compressing (default 1024).
	// Smaller responses are sent as is.
	MinLength int
	// ContentTypes are compressible media type prefixes. The default covers
	// text, JSON, JavaScript, XML and SVG.
	ContentTypes []string
}

var defaultCompressible = []string{
	"text/", "application/json", "application/javascript", "application/xml",
	"application/problem+json", "application/x-ndjson", "image/svg+xml",
}

// Compress gzips responses for clients that accept it. Small responses,
// already-encoded responses and incompressible types are passed through.
// Streaming responses are compressed and flushed incrementally.
func Compress(cfgs ...CompressConfig) torge.Middleware {
	var cfg CompressConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.Level == 0 {
		cfg.Level = gzip.DefaultCompression
	}
	if cfg.MinLength <= 0 {
		cfg.MinLength = 1024
	}
	if cfg.ContentTypes == nil {
		cfg.ContentTypes = defaultCompressible
	}
	if _, err := gzip.NewWriterLevel(nil, cfg.Level); err != nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "invalid gzip level", Why: err.Error(), Fix: "use a level between -2 and 9"})
	}
	pool := &sync.Pool{New: func() any {
		w, _ := gzip.NewWriterLevel(nil, cfg.Level)
		return w
	}}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			r := c.Request()
			c.Response().Header().Add("Vary", "Accept-Encoding")
			if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
				return next(c)
			}
			gw := &gzipWriter{ResponseWriter: c.Response(), cfg: &cfg, pool: pool}
			restore := c.SetResponseWriter(gw)
			err := next(c)
			if err != nil && !c.Response().Written() {
				// Render the error through the compressor before closing it.
				c.HandleError(err)
			}
			restore()
			if cerr := gw.close(); err == nil {
				err = cerr
			}
			return err
		}
	}
}

func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.TrimSpace(name)
		if !strings.EqualFold(name, "gzip") && name != "*" {
			continue
		}
		q := 1.0
		for p := range strings.SplitSeq(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "q") {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					q = f
				}
			}
		}
		return q > 0
	}
	return false
}

// gzipWriter buffers the start of the body to decide whether compression is
// worthwhile, then commits headers.
type gzipWriter struct {
	http.ResponseWriter
	cfg     *CompressConfig
	pool    *sync.Pool
	status  int
	buf     []byte
	decided bool
	gz      *gzip.Writer
}

func (w *gzipWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
	}
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.decided {
		if w.gz != nil {
			return w.gz.Write(b)
		}
		return w.ResponseWriter.Write(b)
	}
	w.buf = append(w.buf, b...)
	if len(w.buf) >= w.cfg.MinLength {
		if err := w.decide(true); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// decide commits headers and flushes the buffer. enough reports whether the
// body reached MinLength (or the caller is streaming).
func (w *gzipWriter) decide(enough bool) error {
	w.decided = true
	h := w.Header()
	compress := enough && w.status >= 200 && w.status != http.StatusNoContent && w.status != http.StatusNotModified &&
		h.Get("Content-Encoding") == "" && w.compressible(h)
	if compress {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		w.gz = w.pool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(w.status)
	buf := w.buf
	w.buf = nil
	if len(buf) == 0 {
		return nil
	}
	var err error
	if w.gz != nil {
		_, err = w.gz.Write(buf)
	} else {
		_, err = w.ResponseWriter.Write(buf)
	}
	return err
}

func (w *gzipWriter) compressible(h http.Header) bool {
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(w.buf)
		h.Set("Content-Type", ct)
	}
	ct = strings.ToLower(ct)
	for _, prefix := range w.cfg.ContentTypes {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

// Flush compresses and sends buffered data immediately (for streaming).
func (w *gzipWriter) Flush() { _ = w.FlushError() }

func (w *gzipWriter) FlushError() error {
	if !w.decided {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		if err := w.decide(true); err != nil {
			return err
		}
	}
	if w.gz != nil {
		if err := w.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.decided {
		return nil, nil, errors.New("middleware: cannot hijack a compressed response")
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipWriter) close() error {
	if !w.decided {
		if w.status == 0 {
			return nil // nothing written; the error handler or caller owns the response
		}
		if err := w.decide(len(w.buf) >= w.cfg.MinLength); err != nil {
			return err
		}
	}
	if w.gz == nil {
		return nil
	}
	err := w.gz.Close()
	w.gz.Reset(nil)
	w.pool.Put(w.gz)
	w.gz = nil
	return err
}
