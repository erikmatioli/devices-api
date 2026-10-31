// Package telemetry configures OpenTelemetry tracing and metrics for the
// service and exposes the instruments the application records.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Providers holds the configured providers and the Prometheus endpoint.
type Providers struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// MetricsHandler serves the Prometheus exposition format.
	MetricsHandler http.Handler
	// OTLPEnabled reports whether traces and metrics are pushed over OTLP.
	OTLPEnabled bool

	shutdown []func(context.Context) error
}

// Shutdown flushes pending telemetry and stops the exporters.
func (p *Providers) Shutdown(ctx context.Context) error {
	var errs []error
	for _, fn := range p.shutdown {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}

// Setup builds the tracer and meter providers and installs them globally.
//
// Spans are always recorded, so trace ids reach the logs even without a
// collector. They are exported over OTLP/HTTP only when
// OTEL_EXPORTER_OTLP_ENDPOINT (or the traces-specific variable) is set; the
// standard OTEL_* variables configure the exporter, sampler and resource.
// Metrics are always served for Prometheus scraping and are also pushed
// over OTLP when an endpoint is configured.
func Setup(ctx context.Context, serviceName, version string) (*Providers, error) {
	// Attributes from OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES are
	// applied last, so the environment can override the defaults.
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry resource: %w", err)
	}

	p := &Providers{OTLPEnabled: otlpConfigured()}

	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if p.OTLPEnabled {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(traceOpts...)
	p.TracerProvider = tp
	p.shutdown = append(p.shutdown, tp.Shutdown)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	promExp, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}
	meterOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExp),
		sdkmetric.WithView(durationView()),
	}
	if p.OTLPEnabled {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	}
	mp := sdkmetric.NewMeterProvider(meterOpts...)
	p.MeterProvider = mp
	p.shutdown = append(p.shutdown, mp.Shutdown)
	p.MetricsHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return p, nil
}

func otlpConfigured() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// durationBuckets follow the OpenTelemetry HTTP semantic conventions.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

func durationView() sdkmetric.View {
	return sdkmetric.NewView(
		sdkmetric.Instrument{Name: "http.server.request.duration"},
		sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: durationBuckets}},
	)
}
