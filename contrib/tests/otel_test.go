package tests

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/TosmimForidMehtab/torge"
	torgeotel "github.com/TosmimForidMehtab/torge/contrib/otel"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func setupOTel(t *testing.T) (torgeotel.Config, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()); _ = mp.Shutdown(context.Background()) })
	return torgeotel.Config{
		TracerProvider: tp, MeterProvider: mp,
		Propagator: propagation.TraceContext{},
	}, rec, reader
}

func TestTracingAndMetrics(t *testing.T) {
	cfg, spans, reader := setupOTel(t)
	var logs bytes.Buffer
	app := torgetest.NewApp(t,
		torge.WithTracing(torgeotel.Tracing(cfg)),
		torge.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	app.Use(torgeotel.Metrics(cfg))
	app.GET("/users/:id", func(c *torge.Context) error { return c.String(200, "ok") })
	app.GET("/fail", func(c *torge.Context) error { return errors.New("boom") })
	tc := torgetest.New(t, app)

	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc.GET("/users/42").Header("traceparent", parent).Do().ExpectStatus(200)
	tc.GET("/fail").Do().ExpectStatus(500)

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(ended))
	}
	s := ended[0]
	if s.Name() != "GET /users/:id" || s.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("span %q trace %s", s.Name(), s.SpanContext().TraceID())
	}
	attrs := map[attribute.Key]attribute.Value{}
	for _, a := range s.Attributes() {
		attrs[a.Key] = a.Value
	}
	if attrs["http.route"].AsString() != "/users/:id" || attrs["http.response.status_code"].AsInt64() != 200 ||
		attrs["torge.request_id"].AsString() == "" {
		t.Fatalf("attributes: %v", attrs)
	}
	if ended[1].Status().Code != codes.Error {
		t.Fatal("5xx responses must mark the span as an error")
	}
	if !strings.Contains(logs.String(), `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Fatalf("access logs must carry the trace ID:\n%s", logs.String())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			found[m.Name] = true
			if m.Name == "http.server.request.duration" {
				h := m.Data.(metricdata.Histogram[float64])
				var total uint64
				for _, dp := range h.DataPoints {
					total += dp.Count
				}
				if total != 2 {
					t.Fatalf("expected 2 recorded requests, got %d", total)
				}
			}
		}
	}
	for _, name := range []string{"http.server.request.duration", "http.server.active_requests", "http.server.errors"} {
		if !found[name] {
			t.Errorf("missing metric %s", name)
		}
	}
}

func TestTransportPropagates(t *testing.T) {
	cfg, spans, _ := setupOTel(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
	}))
	defer srv.Close()
	client := &http.Client{Transport: torgeotel.TransportWith(cfg)(http.DefaultTransport)}
	res, err := client.Get(srv.URL + "/x?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got == "" {
		t.Fatal("trace context must be injected")
	}
	span := spans.Ended()[0]
	for _, a := range span.Attributes() {
		if a.Key == "url.full" && strings.Contains(a.Value.AsString(), "secret") {
			t.Fatal("query strings must not be recorded")
		}
	}
}

func TestJobSpans(t *testing.T) {
	cfg, spans, _ := setupOTel(t)
	h := torgeotel.Jobs(cfg)(jobs.HandlerFunc(func(context.Context, *jobs.Job) error { return errors.New("x") }))
	_ = h.Handle(context.Background(), &jobs.Job{ID: "1", Name: "send", Attempt: 2})
	s := spans.Ended()[0]
	if s.Name() != "job send" || s.Status().Code != codes.Error {
		t.Fatalf("span %q %v", s.Name(), s.Status())
	}
}
