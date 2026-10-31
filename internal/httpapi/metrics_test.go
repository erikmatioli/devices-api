package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/app/apptest"
	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/httpapi"
	"github.com/erikmatioli/devices-api/internal/idempotency"
)

type recordedRequest struct {
	method, route string
	status        int
}

type fakeMetrics struct {
	mu       sync.Mutex
	requests []recordedRequest
	outcomes []string
}

func (m *fakeMetrics) RequestCompleted(_ context.Context, method, route string, status int, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, recordedRequest{method, route, status})
}

func (m *fakeMetrics) IdempotencyOutcome(_ context.Context, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcomes = append(m.outcomes, outcome)
}

func metricsRouter(t *testing.T, m *fakeMetrics) http.Handler {
	t.Helper()
	return httpapi.NewRouter(httpapi.Dependencies{
		Logger:      discardLogger(),
		Devices:     app.NewService(apptest.NewMemoryRepository()),
		Verifier:    auth.NewHS256Verifier(auth.Config{Issuer: testIssuer, Audience: testAudience, HS256Secret: testSecret}),
		Idempotency: idempotency.NewMemoryStore(time.Hour, time.Minute, nil),
		Metrics:     m,
		MetricsHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("# metrics\n"))
		}),
	})
}

func authorized(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	tok, err := auth.SignHS256(testSecret, testIssuer, testAudience, "u", []string{auth.ScopeRead, auth.ScopeWrite}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

func TestRequestMetricsUseRoutePattern(t *testing.T) {
	m := &fakeMetrics{}
	h := metricsRouter(t, m)

	h.ServeHTTP(httptest.NewRecorder(), authorized(t, httptest.NewRequest(http.MethodGet, "/v1/devices/0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12", nil)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	want := []recordedRequest{
		{http.MethodGet, "GET /v1/devices/{id}", http.StatusNotFound},
		{http.MethodGet, "", http.StatusNotFound},
	}
	if len(m.requests) != len(want) {
		t.Fatalf("requests = %+v", m.requests)
	}
	for i := range want {
		if m.requests[i] != want[i] {
			t.Errorf("request %d = %+v, want %+v", i, m.requests[i], want[i])
		}
	}
}

func TestIdempotencyOutcomeMetrics(t *testing.T) {
	m := &fakeMetrics{}
	h := metricsRouter(t, m)
	post := func(body string) {
		req := authorized(t, httptest.NewRequest(http.MethodPost, "/v1/devices", stringsReader(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k1")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	post(`{"name":"a","brand":"b"}`)
	post(`{"name":"a","brand":"b"}`)
	post(`{"name":"c","brand":"b"}`)

	want := []string{"started", "replayed", "mismatch"}
	if len(m.outcomes) != len(want) {
		t.Fatalf("outcomes = %v", m.outcomes)
	}
	for i := range want {
		if m.outcomes[i] != want[i] {
			t.Errorf("outcomes = %v, want %v", m.outcomes, want)
		}
	}
}

func TestMetricsEndpointIsPublic(t *testing.T) {
	rec := httptest.NewRecorder()
	metricsRouter(t, &fakeMetrics{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "# metrics\n" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
}

func TestSpanIsNamedAfterRoute(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "http.server")

	req := authorized(t, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/devices", nil))
	metricsRouter(t, &fakeMetrics{}).ServeHTTP(httptest.NewRecorder(), req)

	ro, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("span is not readable")
	}
	if ro.Name() != "GET /v1/devices" {
		t.Errorf("span name = %q, want the route pattern", ro.Name())
	}
	found := false
	for _, a := range ro.Attributes() {
		if a.Key == "http.route" && a.Value.AsString() == "GET /v1/devices" {
			found = true
		}
	}
	if !found {
		t.Errorf("attributes = %v, want http.route", ro.Attributes())
	}
	span.End()
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
