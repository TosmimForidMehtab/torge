package torgeotel_test

import (
	"testing"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/TosmimForidMehtab/torge"
	torgeotel "github.com/TosmimForidMehtab/torge/contrib/otel"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

// Smoke test using only the OpenTelemetry API's no-op providers. Behavioral
// tests with the SDK live in the contrib/tests module so the SDK never
// becomes a requirement of this module.
func TestMiddlewareWithNoopProviders(t *testing.T) {
	cfg := torgeotel.Config{TracerProvider: tracenoop.NewTracerProvider(), MeterProvider: metricnoop.NewMeterProvider()}
	app := torgetest.NewApp(t, torge.WithTracing(torgeotel.Tracing(cfg)))
	app.Use(torgeotel.Metrics(cfg))
	app.GET("/", func(c *torge.Context) error { return c.NoContent(204) })
	torgetest.New(t, app).GET("/").Do().ExpectStatus(204)
}
