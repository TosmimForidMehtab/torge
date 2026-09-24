package correlation_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge/correlation"
)

func TestHandlerAddsCorrelationAttributes(t *testing.T) {
	var buf bytes.Buffer
	h := correlation.NewHandler(slog.NewJSONHandler(&buf, nil))
	if correlation.NewHandler(h) != h {
		t.Fatal("wrapping twice must be a no-op")
	}
	log := slog.New(h).With("svc", "api").WithGroup("g")
	ctx := correlation.WithTraceID(correlation.WithRequestID(context.Background(), "req-1"), "trace-1")
	log.InfoContext(ctx, "hello", "k", "v")
	out := buf.String()
	for _, want := range []string{`"svc":"api"`, `"request_id":"req-1"`, `"trace_id":"trace-1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	buf.Reset()
	log.Info("no context")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatal("records without a request context must not get IDs")
	}
	if correlation.RequestID(context.Background()) != "" || correlation.TraceID(context.Background()) != "" {
		t.Fatal("missing IDs must be empty")
	}
}
