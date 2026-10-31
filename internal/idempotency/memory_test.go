package idempotency_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/idempotency"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestMemoryStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	store := idempotency.NewMemoryStore(24*time.Hour, time.Minute, clock.Now)
	key := idempotency.Key{Subject: "alice", Value: "k1"}
	fp := idempotency.Fingerprint("POST", "/v1/devices", []byte(`{"name":"a"}`))

	expectOutcome := func(t *testing.T, fingerprint []byte, want idempotency.Outcome) idempotency.Result {
		t.Helper()
		res, err := store.Begin(ctx, key, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != want {
			t.Fatalf("outcome = %v, want %v", res.Outcome, want)
		}
		return res
	}

	expectOutcome(t, fp, idempotency.Started)
	expectOutcome(t, fp, idempotency.InProgress)
	expectOutcome(t, []byte("other"), idempotency.Mismatch)

	header := http.Header{"Location": {"/v1/devices/1"}}
	if err := store.Complete(ctx, key, idempotency.Response{Status: 201, Header: header, Body: []byte("body")}); err != nil {
		t.Fatal(err)
	}
	header.Set("Location", "mutated")

	res := expectOutcome(t, fp, idempotency.Replay)
	if res.Response.Status != 201 || !bytes.Equal(res.Response.Body, []byte("body")) ||
		res.Response.Header.Get("Location") != "/v1/devices/1" {
		t.Errorf("replayed response = %+v", res.Response)
	}

	clock.now = clock.now.Add(25 * time.Hour)
	expectOutcome(t, []byte("new request after expiry"), idempotency.Started)
}

func TestMemoryStoreAbandonedAndReleased(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Now()}
	store := idempotency.NewMemoryStore(time.Hour, time.Minute, clock.Now)
	key := idempotency.Key{Subject: "alice", Value: "k1"}

	if res, _ := store.Begin(ctx, key, []byte("a")); res.Outcome != idempotency.Started {
		t.Fatal("first Begin should start")
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if res, _ := store.Begin(ctx, key, []byte("a")); res.Outcome != idempotency.Started {
		t.Errorf("abandoned request should be taken over, got %v", res.Outcome)
	}

	if err := store.Release(ctx, key); err != nil {
		t.Fatal(err)
	}
	if res, _ := store.Begin(ctx, key, []byte("different")); res.Outcome != idempotency.Started {
		t.Errorf("released key should start again, got %v", res.Outcome)
	}
}

func TestKeysAreScopedBySubject(t *testing.T) {
	ctx := context.Background()
	store := idempotency.NewMemoryStore(time.Hour, time.Minute, nil)

	for _, subject := range []string{"alice", "bob"} {
		res, err := store.Begin(ctx, idempotency.Key{Subject: subject, Value: "same"}, []byte(subject))
		if err != nil || res.Outcome != idempotency.Started {
			t.Errorf("%s: outcome %v, err %v", subject, res.Outcome, err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a := idempotency.Fingerprint("POST", "/v1/devices", []byte("x"))
	if !bytes.Equal(a, idempotency.Fingerprint("POST", "/v1/devices", []byte("x"))) {
		t.Error("fingerprint must be deterministic")
	}
	for _, other := range [][]byte{
		idempotency.Fingerprint("PUT", "/v1/devices", []byte("x")),
		idempotency.Fingerprint("POST", "/v1/devicesx", nil),
		idempotency.Fingerprint("POST", "/v1/devices", []byte("y")),
	} {
		if bytes.Equal(a, other) {
			t.Error("different requests must not share a fingerprint")
		}
	}
}

func TestKeepHeaders(t *testing.T) {
	h := http.Header{
		"Content-Type": {"application/json"},
		"Location":     {"/v1/devices/1"},
		"Etag":         {`"1"`},
		"X-Request-Id": {"abc"},
		"Date":         {"now"},
	}
	kept := idempotency.KeepHeaders(h)
	if len(kept) != 3 || kept.Get("ETag") != `"1"` || kept.Get("X-Request-ID") != "" {
		t.Errorf("kept = %v", kept)
	}
}
