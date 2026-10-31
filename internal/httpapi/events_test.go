package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/app/apptest"
	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/events"
	"github.com/erikmatioli/devices-api/internal/httpapi"
)

// streamServer serves the router over a real listener, which streaming
// responses need.
func streamServer(t *testing.T, hub *events.Hub, keepAlive time.Duration) *httptest.Server {
	t.Helper()
	verifier := auth.NewHS256Verifier(auth.Config{Issuer: testIssuer, Audience: testAudience, HS256Secret: testSecret})
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Dependencies{
		Logger:          discardLogger(),
		Devices:         app.NewService(apptest.NewMemoryRepository()),
		Verifier:        verifier,
		Events:          hub,
		EventsKeepAlive: keepAlive,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openStream(t *testing.T, srv *httptest.Server, query string) (*http.Response, *bufio.Reader) {
	t.Helper()
	tok, err := auth.SignHS256(testSecret, testIssuer, testAudience, "watcher", []string{auth.ScopeRead}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/devices/events"+query, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res, bufio.NewReader(res.Body)
}

// nextFrame reads one SSE frame (lines up to a blank line).
func nextFrame(t *testing.T, r *bufio.Reader) []string {
	t.Helper()
	type result struct {
		lines []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		var lines []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- result{lines, err}
				return
			}
			line = strings.TrimRight(line, "\n")
			if line == "" {
				ch <- result{lines, nil}
				return
			}
			lines = append(lines, line)
		}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read frame: %v (got %q)", res.err, res.lines)
		}
		return res.lines
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE frame")
	}
	return nil
}

func waitSubscribers(t *testing.T, hub *events.Hub, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hub.Subscribers() != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers = %d, want %d", hub.Subscribers(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stateChange(id uuid.UUID) events.StateChanged {
	return events.StateChanged{
		EventID: device.NewID(), DeviceID: id,
		PreviousState: device.StateAvailable, NewState: device.StateInUse,
		OccurredAt: time.Now().UTC(),
	}
}

func TestEventStream(t *testing.T) {
	hub := events.NewHub(8, 0, nil)
	srv := streamServer(t, hub, time.Hour)

	res, r := openStream(t, srv, "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if got := nextFrame(t, r); len(got) != 1 || got[0] != "retry: 3000" {
		t.Fatalf("first frame = %q, want the retry hint", got)
	}
	waitSubscribers(t, hub, 1)

	e := stateChange(uuid.New())
	hub.Publish(e)

	frame := nextFrame(t, r)
	if len(frame) != 3 || frame[0] != "id: "+e.EventID.String() || frame[1] != "event: device.state_changed" {
		t.Fatalf("frame = %q", frame)
	}
	var got events.StateChanged
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame[2], "data: ")), &got); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if got.DeviceID != e.DeviceID || got.PreviousState != device.StateAvailable || got.NewState != device.StateInUse {
		t.Errorf("event = %+v", got)
	}
}

func TestEventStreamFilterAndKeepAlive(t *testing.T) {
	hub := events.NewHub(8, 0, nil)
	srv := streamServer(t, hub, 50*time.Millisecond)
	wanted := uuid.New()

	_, r := openStream(t, srv, "?device_id="+wanted.String())
	nextFrame(t, r) // retry hint
	waitSubscribers(t, hub, 1)

	hub.Publish(stateChange(uuid.New()))
	hub.Publish(stateChange(wanted))

	for {
		frame := nextFrame(t, r)
		if len(frame) == 1 && frame[0] == ": keep-alive" {
			continue
		}
		if len(frame) != 3 || !strings.Contains(frame[2], wanted.String()) {
			t.Fatalf("frame = %q, want only the filtered device", frame)
		}
		break
	}
}

func TestEventStreamEndsWhenHubCloses(t *testing.T) {
	hub := events.NewHub(8, 0, nil)
	srv := streamServer(t, hub, time.Hour)
	_, r := openStream(t, srv, "")
	nextFrame(t, r)
	waitSubscribers(t, hub, 1)

	hub.Close()

	done := make(chan error, 1)
	go func() {
		_, err := r.ReadString('\n')
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected the stream to end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open after the hub closed")
	}
}

func TestEventStreamUnsubscribesOnDisconnect(t *testing.T) {
	hub := events.NewHub(8, 0, nil)
	srv := streamServer(t, hub, time.Hour)
	res, r := openStream(t, srv, "")
	nextFrame(t, r)
	waitSubscribers(t, hub, 1)

	_ = res.Body.Close()

	waitSubscribers(t, hub, 0)
}

func TestEventStreamErrors(t *testing.T) {
	hub := events.NewHub(8, 1, nil)
	srv := streamServer(t, hub, time.Hour)

	res, _ := openStream(t, srv, "?device_id=nope")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid device_id: status %d, want 422", res.StatusCode)
	}

	_, r := openStream(t, srv, "")
	nextFrame(t, r)
	waitSubscribers(t, hub, 1)
	res, _ = openStream(t, srv, "")
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Errorf("over the limit: status %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
}
