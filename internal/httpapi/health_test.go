package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/erikmatioli/devices-api/internal/httpapi"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

type healthBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, healthBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s body: %v (%q)", path, err, rec.Body.String())
	}
	return rec, body
}

func TestLiveness(t *testing.T) {
	failing := func(context.Context) error { return errors.New("down") }
	h := httpapi.NewRouter(httpapi.Dependencies{
		Logger:    discardLogger(),
		Readiness: map[string]httpapi.CheckFunc{"database": failing},
	})

	rec, body := get(t, h, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when dependencies fail", rec.Code)
	}
	if body.Status != "ok" {
		t.Errorf("status field = %q, want ok", body.Status)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestReadiness(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name       string
		checks     map[string]httpapi.CheckFunc
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name:       "all dependencies up",
			checks:     map[string]httpapi.CheckFunc{"database": ok, "listener": ok},
			wantCode:   http.StatusOK,
			wantStatus: "ok",
			wantChecks: map[string]string{"database": "ok", "listener": "ok"},
		},
		{
			name:       "one dependency down",
			checks:     map[string]httpapi.CheckFunc{"database": down, "listener": ok},
			wantCode:   http.StatusServiceUnavailable,
			wantStatus: "unavailable",
			wantChecks: map[string]string{"database": "unavailable", "listener": "ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := httpapi.NewRouter(httpapi.Dependencies{Logger: discardLogger(), Readiness: tt.checks})

			rec, body := get(t, h, "/readyz")

			if rec.Code != tt.wantCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.wantCode)
			}
			if body.Status != tt.wantStatus {
				t.Errorf("status field = %q, want %q", body.Status, tt.wantStatus)
			}
			for name, want := range tt.wantChecks {
				if body.Checks[name] != want {
					t.Errorf("check %s = %q, want %q", name, body.Checks[name], want)
				}
			}
		})
	}
}

func TestReadinessPassesDeadline(t *testing.T) {
	var hadDeadline bool
	check := func(ctx context.Context) error {
		_, hadDeadline = ctx.Deadline()
		return nil
	}
	h := httpapi.NewRouter(httpapi.Dependencies{
		Logger:    discardLogger(),
		Readiness: map[string]httpapi.CheckFunc{"database": check},
	})

	get(t, h, "/readyz")

	if !hadDeadline {
		t.Error("readiness checks must run with a deadline")
	}
}
