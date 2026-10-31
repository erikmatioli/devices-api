//go:build integration

package postgres_test

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/idempotency"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

func resetIdempotency(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "TRUNCATE idempotency_keys"); err != nil {
		t.Fatalf("truncate idempotency_keys: %v", err)
	}
}

func begin(t *testing.T, s *postgres.IdempotencyStore, key idempotency.Key, fp string) idempotency.Result {
	t.Helper()
	res, err := s.Begin(context.Background(), key, []byte(fp))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return res
}

func TestIdempotencyStoreLifecycle(t *testing.T) {
	resetIdempotency(t)
	ctx := context.Background()
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	key := idempotency.Key{Subject: "alice", Value: "k1"}

	if res := begin(t, store, key, "fp"); res.Outcome != idempotency.Started {
		t.Fatalf("first Begin = %v, want Started", res.Outcome)
	}
	if res := begin(t, store, key, "fp"); res.Outcome != idempotency.InProgress {
		t.Errorf("second Begin = %v, want InProgress", res.Outcome)
	}
	if res := begin(t, store, key, "other"); res.Outcome != idempotency.Mismatch {
		t.Errorf("different fingerprint = %v, want Mismatch", res.Outcome)
	}

	resp := idempotency.Response{
		Status: http.StatusCreated,
		Header: http.Header{"Location": {"/v1/devices/1"}, "Etag": {`"1"`}},
		Body:   []byte(`{"id":"1"}`),
	}
	if err := store.Complete(ctx, key, resp); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	res := begin(t, store, key, "fp")
	if res.Outcome != idempotency.Replay {
		t.Fatalf("after Complete = %v, want Replay", res.Outcome)
	}
	got := res.Response
	if got.Status != resp.Status || string(got.Body) != string(resp.Body) ||
		got.Header.Get("Location") != "/v1/devices/1" || got.Header.Get("ETag") != `"1"` {
		t.Errorf("replayed = %+v", got)
	}

	if err := store.Release(ctx, key); err != nil {
		t.Fatal(err)
	}
	if res := begin(t, store, key, "fp"); res.Outcome != idempotency.Replay {
		t.Errorf("Release must keep completed records, got %v", res.Outcome)
	}
}

func TestIdempotencyStoreReleaseAllowsRetry(t *testing.T) {
	resetIdempotency(t)
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	key := idempotency.Key{Subject: "alice", Value: "k1"}

	begin(t, store, key, "fp")
	if err := store.Release(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if res := begin(t, store, key, "changed"); res.Outcome != idempotency.Started {
		t.Errorf("after Release = %v, want Started", res.Outcome)
	}
}

func TestIdempotencyStoreTakesOverExpiredAndAbandoned(t *testing.T) {
	resetIdempotency(t)
	ctx := context.Background()
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	expired := idempotency.Key{Subject: "alice", Value: "expired"}
	abandoned := idempotency.Key{Subject: "alice", Value: "abandoned"}

	begin(t, store, expired, "fp")
	if err := store.Complete(ctx, expired, idempotency.Response{Status: 201}); err != nil {
		t.Fatal(err)
	}
	begin(t, store, abandoned, "fp")
	if _, err := testPool.Exec(ctx, `UPDATE idempotency_keys SET expires_at = now() - interval '1 second' WHERE key = 'expired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE idempotency_keys SET started_at = now() - interval '2 minutes' WHERE key = 'abandoned'`); err != nil {
		t.Fatal(err)
	}

	if res := begin(t, store, expired, "new"); res.Outcome != idempotency.Started {
		t.Errorf("expired record = %v, want Started", res.Outcome)
	}
	if res := begin(t, store, abandoned, "fp"); res.Outcome != idempotency.Started {
		t.Errorf("abandoned record = %v, want Started", res.Outcome)
	}
}

func TestIdempotencyStoreConcurrentBegin(t *testing.T) {
	resetIdempotency(t)
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	key := idempotency.Key{Subject: "alice", Value: "race"}

	const callers = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started int
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := store.Begin(context.Background(), key, []byte("fp"))
			if err != nil {
				t.Error(err)
				return
			}
			if res.Outcome == idempotency.Started {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if started != 1 {
		t.Errorf("%d callers started the same key, want exactly 1", started)
	}
}

func TestIdempotencyStorePurge(t *testing.T) {
	resetIdempotency(t)
	ctx := context.Background()
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	begin(t, store, idempotency.Key{Subject: "a", Value: "old"}, "fp")
	begin(t, store, idempotency.Key{Subject: "a", Value: "fresh"}, "fp")
	if _, err := testPool.Exec(ctx, `UPDATE idempotency_keys SET expires_at = now() - interval '1 second' WHERE key = 'old'`); err != nil {
		t.Fatal(err)
	}

	n, err := store.PurgeExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v; want 1", n, err)
	}
	var left int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&left); err != nil || left != 1 {
		t.Errorf("records left = %d, %v; want 1", left, err)
	}
}

func TestIdempotencyStoreRunPurge(t *testing.T) {
	resetIdempotency(t)
	ctx, cancel := context.WithCancel(context.Background())
	store := postgres.NewIdempotencyStore(testPool, time.Hour, time.Minute)
	begin(t, store, idempotency.Key{Subject: "a", Value: "old"}, "fp")
	if _, err := testPool.Exec(ctx, `UPDATE idempotency_keys SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		store.RunPurge(ctx, 20*time.Millisecond, slog.New(slog.DiscardHandler))
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var left int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunPurge did not remove the expired record")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunPurge did not stop when the context was canceled")
	}
}
