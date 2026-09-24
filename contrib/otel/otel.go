// Package torgeotel integrates Torge with OpenTelemetry: server spans and
// metrics for HTTP requests, client spans with context propagation for
// outgoing requests, spans for background jobs, and provider flushing at
// shutdown. It uses the OpenTelemetry API only; configure exporters and the
// SDK as usual.
//
//	app := torge.New(torge.WithTracing(torgeotel.Tracing()))
//	app.Use(torgeotel.Metrics())
//	torgeotel.FlushOnShutdown(app, tracerProvider, meterProvider)
//
//	client, _ := httpclient.New(httpclient.Config{
//	    Middleware: []func(http.RoundTripper) http.RoundTripper{torgeotel.Transport},
//	})
package torgeotel

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/correlation"
	"github.com/TosmimForidMehtab/torge/jobs"
)

const instrumentation = "github.com/TosmimForidMehtab/torge/contrib/otel"

// Config selects providers. Nil fields use the global providers.
type Config struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Propagator     propagation.TextMapPropagator
}

func (c Config) tracer() trace.Tracer {
	tp := c.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer(instrumentation)
}

func (c Config) meter() metric.Meter {
	mp := c.MeterProvider
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	return mp.Meter(instrumentation)
}

func (c Config) propagator() propagation.TextMapPropagator {
	if c.Propagator != nil {
		return c.Propagator
	}
	return otel.GetTextMapPropagator()
}

func first(cfgs []Config) Config {
	if len(cfgs) > 0 {
		return cfgs[0]
	}
	return Config{}
}

// Tracing creates a server span per request, continuing incoming trace
// context. The trace ID is attached to the request context so access logs
// and application logs carry trace_id. Install it with torge.WithTracing so
// it wraps every later pipeline stage.
func Tracing(cfgs ...Config) torge.Middleware {
	cfg := first(cfgs)
	tracer, prop := cfg.tracer(), cfg.propagator()
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			r := c.Request()
			ctx := prop.Extract(c.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
					attribute.String("url.scheme", c.Scheme()),
					attribute.String("server.address", r.Host),
					attribute.String("client.address", c.RealIP()),
					attribute.String("user_agent.original", r.UserAgent()),
				))
			defer span.End()
			if sc := span.SpanContext(); sc.HasTraceID() {
				ctx = correlation.WithTraceID(ctx, sc.TraceID().String())
			}
			if id := c.RequestID(); id != "" {
				span.SetAttributes(attribute.String("torge.request_id", id))
			}
			c.SetContext(ctx)

			err := next(c)

			status := c.StatusCode()
			if status == 0 {
				status = torge.StatusOf(err)
			}
			if route := c.RoutePattern(); route != "" {
				span.SetName(r.Method + " " + route)
				span.SetAttributes(attribute.String("http.route", route))
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if u := c.User(); u != nil {
				span.SetAttributes(attribute.String("enduser.id", u.ID()))
			}
			if status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(status))
				if err != nil {
					span.RecordError(err)
				}
			}
			return err
		}
	}
}

// Metrics records HTTP server metrics:
//
//   - http.server.request.duration (histogram, seconds) by method, route and
//     status code, which also yields request counts and status code
//     distribution;
//   - http.server.active_requests (up-down counter);
//   - http.server.errors (counter of 5xx responses).
//
// Use the same meter provider for custom application metrics.
func Metrics(cfgs ...Config) torge.Middleware {
	m := first(cfgs).meter()
	duration, err1 := m.Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of HTTP server requests."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	active, err2 := m.Int64UpDownCounter("http.server.active_requests",
		metric.WithUnit("{request}"), metric.WithDescription("Number of in-flight HTTP server requests."))
	errorsTotal, err3 := m.Int64Counter("http.server.errors",
		metric.WithUnit("{request}"), metric.WithDescription("HTTP server responses with a 5xx status."))
	if err := firstErr(err1, err2, err3); err != nil {
		otel.Handle(err)
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			ctx := c.Context()
			method := attribute.String("http.request.method", c.Method())
			active.Add(ctx, 1, metric.WithAttributes(method))
			start := time.Now()
			err := next(c)
			active.Add(ctx, -1, metric.WithAttributes(method))
			status := c.StatusCode()
			if status == 0 {
				status = torge.StatusOf(err)
			}
			route := c.RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			attrs := metric.WithAttributes(method,
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status))
			duration.Record(ctx, time.Since(start).Seconds(), attrs)
			if status >= 500 {
				errorsTotal.Add(ctx, 1, attrs)
			}
			return err
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// Transport wraps an http.RoundTripper with client spans and trace context
// propagation. It matches httpclient.Config.Middleware.
func Transport(next http.RoundTripper) http.RoundTripper {
	return TransportWith(Config{})(next)
}

// TransportWith is Transport with explicit providers.
func TransportWith(cfg Config) func(http.RoundTripper) http.RoundTripper {
	tracer, prop := cfg.tracer(), cfg.propagator()
	return func(next http.RoundTripper) http.RoundTripper {
		if next == nil {
			next = http.DefaultTransport
		}
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			ctx, span := tracer.Start(req.Context(), req.Method,
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					attribute.String("http.request.method", req.Method),
					attribute.String("server.address", req.URL.Hostname()),
					attribute.String("url.full", redactURL(req)),
				))
			defer span.End()
			req = req.Clone(ctx)
			prop.Inject(ctx, propagation.HeaderCarrier(req.Header))
			res, err := next.RoundTrip(req)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return res, err
			}
			span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
			if res.StatusCode >= 400 {
				span.SetStatus(codes.Error, strconv.Itoa(res.StatusCode))
			}
			return res, nil
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func redactURL(r *http.Request) string {
	u := *r.URL
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

// Jobs returns job middleware creating a consumer span per attempt.
func Jobs(cfgs ...Config) jobs.Middleware {
	tracer := first(cfgs).tracer()
	return func(next jobs.Handler) jobs.Handler {
		return jobs.HandlerFunc(func(ctx context.Context, job *jobs.Job) error {
			ctx, span := tracer.Start(ctx, "job "+job.Name,
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.operation.type", "process"),
					attribute.String("torge.job.name", job.Name),
					attribute.String("torge.job.id", job.ID),
					attribute.Int("torge.job.attempt", job.Attempt),
					attribute.String("torge.request_id", job.Metadata[jobs.MetaRequestID]),
				))
			defer span.End()
			if sc := span.SpanContext(); sc.HasTraceID() {
				ctx = correlation.WithTraceID(ctx, sc.TraceID().String())
			}
			err := next.Handle(ctx, job)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			return err
		})
	}
}

// Shutdowner is implemented by SDK tracer and meter providers.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// FlushOnShutdown registers a stop hook that flushes and shuts down the given
// providers. Register it before other components so telemetry is flushed
// last, after everything else has stopped.
func FlushOnShutdown(app *torge.App, providers ...Shutdowner) {
	app.OnStop("opentelemetry", func(ctx context.Context) error {
		var errs []error
		for _, p := range providers {
			if err := p.Shutdown(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return firstErr(errs...)
	})
}
