package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/platform/telemetry"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestSetupWithoutCollector(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	ctx := context.Background()

	p, err := telemetry.Setup(ctx, "devices-api", "1.2.3")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = p.Shutdown(ctx) }()

	if p.OTLPEnabled {
		t.Error("OTLP must stay disabled without an endpoint")
	}
	_, span := p.TracerProvider.Tracer("test").Start(ctx, "op")
	if !span.SpanContext().IsValid() {
		t.Error("spans must be recorded even without an exporter, for log correlation")
	}
	span.End()

	out := scrape(t, p.MetricsHandler)
	for _, want := range []string{"go_goroutines", `service_name="devices-api"`, `service_version="1.2.3"`} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output lacks %s", want)
		}
	}
}

func TestSetupWithCollector(t *testing.T) {
	// The exporters only connect when they flush, so an unreachable endpoint
	// is enough to exercise the configuration path.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	ctx := context.Background()

	p, err := telemetry.Setup(ctx, "devices-api", "dev")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !p.OTLPEnabled {
		t.Error("OTLP should be enabled when an endpoint is set")
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_ = p.Shutdown(shutdownCtx) // the flush fails against the fake endpoint
}

func TestMetricsAreExposed(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	ctx := context.Background()
	p, err := telemetry.Setup(ctx, "devices-api", "dev")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Shutdown(ctx) }()

	m, err := telemetry.NewMetrics(p.MeterProvider)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	m.RequestCompleted(ctx, http.MethodGet, "GET /v1/devices/{id}", 200, 30*time.Millisecond)
	m.RequestCompleted(ctx, http.MethodGet, "", 404, time.Millisecond)
	m.DeviceCreated(ctx)
	m.DeviceDeleted(ctx)
	m.DeviceStateChanged(ctx, device.StateAvailable, device.StateInUse)
	m.IdempotencyOutcome(ctx, "replayed")
	m.SubscriberAdded()
	m.SubscriberAdded()
	m.SubscriberRemoved()
	m.SubscriberDropped()

	out := scrape(t, p.MetricsHandler)
	for _, want := range []string{
		`http_server_request_duration_seconds_bucket{http_request_method="GET",http_response_status_code="200",http_route="GET /v1/devices/{id}"`,
		`http_route="unmatched"`,
		`devices_created_total`,
		`devices_deleted_total`,
		`devices_state_transitions_total{from="available"`,
		`outcome="replayed"} 1`,
		`devices_events_subscribers{`,
		`devices_events_dropped_subscribers_total`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output lacks %s", want)
		}
	}
	if !strings.Contains(out, `le="0.025"`) {
		t.Error("request duration should use the semantic convention buckets")
	}
}
