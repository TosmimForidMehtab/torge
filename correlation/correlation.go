// Package correlation carries request and trace identifiers through a
// context.Context so that logs, outgoing HTTP calls and background jobs can be
// correlated with the request that caused them.
//
// The package is dependency-free and can be used by any code that receives a
// context.Context, including code that knows nothing about Torge.
package correlation

import (
	"context"
	"log/slog"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	traceIDKey
)

// WithRequestID returns a copy of ctx carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID stored in ctx, or "" if none.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithTraceID returns a copy of ctx carrying the trace ID. Tracing integrations
// call this so that logs can include the trace ID without depending on a
// specific tracing library.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceID returns the trace ID stored in ctx, or "" if none.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceIDKey).(string)
	return id
}

// Handler is a slog.Handler that adds request_id and trace_id attributes from
// the context passed to the logging call (for example logger.InfoContext).
type Handler struct {
	next slog.Handler
}

// NewHandler wraps next. Wrapping an already wrapped handler returns it as is.
func NewHandler(next slog.Handler) slog.Handler {
	if _, ok := next.(*Handler); ok {
		return next
	}
	return &Handler{next: next}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id := TraceID(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name)}
}

// Unwrap returns the wrapped handler.
func (h *Handler) Unwrap() slog.Handler { return h.next }
