package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/idempotency"
)

const createBody = `{"name":"Pixel 9","brand":"Google"}`

func TestIdempotentCreateReplays(t *testing.T) {
	api := newAPI(t)

	first := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "order-42")
	second := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "order-42")

	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d/%d, want 201/201", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("replayed body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	for _, h := range []string{"Location", "ETag", "Content-Type"} {
		if first.Header().Get(h) != second.Header().Get(h) {
			t.Errorf("header %s = %q on replay, want %q", h, second.Header().Get(h), first.Header().Get(h))
		}
	}
	if first.Header().Get("Idempotent-Replayed") != "" || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("only the replay must carry Idempotent-Replayed: true")
	}
	if second.Header().Get("X-Request-ID") == first.Header().Get("X-Request-ID") {
		t.Error("the replay is a new response and needs its own request id")
	}

	var list listBody
	api.do(http.MethodGet, "/v1/devices", "").json(t, &list)
	if len(list.Data) != 1 {
		t.Errorf("devices created = %d, want 1", len(list.Data))
	}
}

func TestIdempotencyKeyReusedWithDifferentBody(t *testing.T) {
	api := newAPI(t)
	api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "k1")

	res := api.do(http.MethodPost, "/v1/devices", `{"name":"Other","brand":"Google"}`, "Idempotency-Key", "k1")

	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	if p := res.problem(t); p.Type != "/problems/idempotency-key-reused" {
		t.Errorf("problem = %+v", p)
	}
}

func TestIdempotencyKeyInProgress(t *testing.T) {
	api := newAPI(t)
	key := idempotency.Key{Subject: "test-user", Value: "k1"}
	fp := idempotency.Fingerprint(http.MethodPost, "/v1/devices", []byte(createBody))
	if _, err := api.idem.Begin(context.Background(), key, fp); err != nil {
		t.Fatal(err)
	}

	res := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "k1")

	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.Code)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	if p := res.problem(t); p.Type != "/problems/idempotency-key-in-progress" {
		t.Errorf("problem = %+v", p)
	}
}

func TestIdempotencyClientErrorsAreReplayed(t *testing.T) {
	api := newAPI(t)
	invalid := `{"name":"","brand":"Google"}`

	first := api.do(http.MethodPost, "/v1/devices", invalid, "Idempotency-Key", "k1")
	second := api.do(http.MethodPost, "/v1/devices", invalid, "Idempotency-Key", "k1")

	if first.Code != http.StatusUnprocessableEntity || second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("statuses = %d/%d", first.Code, second.Code)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("a recorded 4xx must be replayed")
	}
}

func TestIdempotencyServerErrorsAreNotRecorded(t *testing.T) {
	api := newAPI(t)
	api.repo.Err = errors.New("database unavailable")

	if res := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "k1"); res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}

	api.repo.Err = nil
	res := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "k1")
	if res.Code != http.StatusCreated || res.Header().Get("Idempotent-Replayed") != "" {
		t.Errorf("retry after a 500: status %d, replayed %q; want a fresh 201",
			res.Code, res.Header().Get("Idempotent-Replayed"))
	}
}

func TestIdempotencyKeysAreScopedPerSubject(t *testing.T) {
	api := newAPI(t)
	token := func(sub string) string {
		tok, err := auth.SignHS256(testSecret, testIssuer, testAudience, sub, []string{auth.ScopeWrite}, time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + tok
	}

	a := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "shared", "Authorization", token("alice"))
	b := api.do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", "shared", "Authorization", token("bob"))

	if a.Code != http.StatusCreated || b.Code != http.StatusCreated {
		t.Fatalf("statuses = %d/%d", a.Code, b.Code)
	}
	if b.Header().Get("Idempotent-Replayed") != "" || a.Header().Get("Location") == b.Header().Get("Location") {
		t.Error("different subjects must not share idempotency records")
	}
}

func TestInvalidIdempotencyKey(t *testing.T) {
	for _, key := range []string{"has space", strings.Repeat("k", 256), "naïve"} {
		res := newAPI(t).do(http.MethodPost, "/v1/devices", createBody, "Idempotency-Key", key)
		if res.Code != http.StatusUnprocessableEntity || res.problem(t).codes() != "Idempotency-Key:invalid_value" {
			t.Errorf("key %q: status %d, body %s", key, res.Code, res.Body.String())
		}
	}
}

func TestIdempotencyBodyLimit(t *testing.T) {
	big := `{"name":"` + strings.Repeat("x", 1<<20) + `"}`
	res := newAPI(t).do(http.MethodPost, "/v1/devices", big, "Idempotency-Key", "k1")
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", res.Code)
	}
}

func TestRequestsWithoutKeyAreNotDeduplicated(t *testing.T) {
	api := newAPI(t)
	api.do(http.MethodPost, "/v1/devices", createBody)
	api.do(http.MethodPost, "/v1/devices", createBody)

	var list listBody
	api.do(http.MethodGet, "/v1/devices", "").json(t, &list)
	if len(list.Data) != 2 {
		t.Errorf("devices = %d, want 2", len(list.Data))
	}
}
