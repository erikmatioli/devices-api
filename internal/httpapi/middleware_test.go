package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newJSONLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (%q)", err, line)
		}
		records = append(records, rec)
	}
	return records
}

func TestRequestIDGenerated(t *testing.T) {
	var seen string
	h := requestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(seen) != 32 {
		t.Fatalf("generated id %q, want 32 hex characters", seen)
	}
	if got := rec.Header().Get(HeaderRequestID); got != seen {
		t.Errorf("response header = %q, want %q", got, seen)
	}
}

func TestRequestIDPropagated(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{name: "valid id is reused", incoming: "abc-123", keep: true},
		{name: "id with spaces is replaced", incoming: "abc 123", keep: false},
		{name: "oversized id is replaced", incoming: strings.Repeat("a", maxRequestIDLength+1), keep: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := requestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(HeaderRequestID, tt.incoming)

			h.ServeHTTP(httptest.NewRecorder(), req)

			if (seen == tt.incoming) != tt.keep {
				t.Errorf("request id = %q, keep incoming = %v", seen, tt.keep)
			}
		})
	}
}

func TestRecovererReturnsProblem(t *testing.T) {
	var buf bytes.Buffer
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), requestID, recoverer(newJSONLogger(&buf)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/devices", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Status != http.StatusInternalServerError || p.Instance != "/v1/devices" || p.RequestID == "" {
		t.Errorf("unexpected problem %+v", p)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("panic value must not leak to the client")
	}

	records := decodeRecords(t, &buf)
	if len(records) != 1 || records[0]["msg"] != "panic recovered" || records[0]["stack"] == "" {
		t.Errorf("unexpected log records %v", records)
	}
}

func TestRecovererRepanicsOnAbort(t *testing.T) {
	h := recoverer(newJSONLogger(&bytes.Buffer{}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if v, _ := recover().(error); !errors.Is(v, http.ErrAbortHandler) {
			t.Error("http.ErrAbortHandler must propagate to the server")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/devices/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short"))
	})
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /fail", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	h := chain(mux, requestID, accessLog(newJSONLogger(&buf), map[string]bool{"/healthz": true}))

	for _, path := range []string{"/v1/devices/42", "/healthz", "/fail"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	records := decodeRecords(t, &buf)
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}

	first := records[0]
	want := map[string]any{
		"msg":    "http request",
		"level":  "INFO",
		"method": "GET",
		"path":   "/v1/devices/42",
		"route":  "GET /v1/devices/{id}",
		"status": float64(http.StatusTeapot),
		"bytes":  float64(5),
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}
	if first["request_id"] == "" {
		t.Error("request_id missing from access log")
	}
	if records[1]["level"] != "DEBUG" {
		t.Errorf("probe level = %v, want DEBUG", records[1]["level"])
	}
	if records[1]["status"] != float64(http.StatusOK) {
		t.Errorf("implicit status = %v, want 200", records[1]["status"])
	}
	if records[2]["level"] != "ERROR" {
		t.Errorf("5xx level = %v, want ERROR", records[2]["level"])
	}
}
