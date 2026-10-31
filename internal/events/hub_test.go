package events_test

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/events"
)

type countingObserver struct {
	added, removed, dropped atomic.Int64
}

func (o *countingObserver) SubscriberAdded()   { o.added.Add(1) }
func (o *countingObserver) SubscriberRemoved() { o.removed.Add(1) }
func (o *countingObserver) SubscriberDropped() { o.dropped.Add(1) }

func event(n int) events.StateChanged {
	return events.StateChanged{EventID: uuid.New(), PreviousState: device.StateAvailable, NewState: device.StateInUse,
		OccurredAt: time.Unix(int64(n), 0).UTC()}
}

func receive(t *testing.T, s *events.Subscription) events.StateChanged {
	t.Helper()
	select {
	case e, ok := <-s.C:
		if !ok {
			t.Fatal("subscription closed unexpectedly")
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
	return events.StateChanged{}
}

func TestPublishReachesEverySubscriber(t *testing.T) {
	hub := events.NewHub(4, 0, nil)
	a, _ := hub.Subscribe()
	b, _ := hub.Subscribe()

	e := event(1)
	hub.Publish(e)

	if got := receive(t, a); got.EventID != e.EventID {
		t.Errorf("a got %v", got.EventID)
	}
	if got := receive(t, b); got.EventID != e.EventID {
		t.Errorf("b got %v", got.EventID)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	obs := &countingObserver{}
	hub := events.NewHub(2, 0, obs)
	slow, _ := hub.Subscribe()
	fast, _ := hub.Subscribe()

	for i := range 3 {
		hub.Publish(event(i))
		receive(t, fast)
	}

	// The slow subscriber gets what fit in its buffer, then the channel closes.
	receive(t, slow)
	receive(t, slow)
	if _, ok := <-slow.C; ok {
		t.Fatal("slow subscription should be closed")
	}
	if !slow.Dropped() || fast.Dropped() {
		t.Errorf("dropped flags = %v/%v, want true/false", slow.Dropped(), fast.Dropped())
	}
	if hub.Subscribers() != 1 || obs.dropped.Load() != 1 {
		t.Errorf("subscribers = %d, dropped = %d", hub.Subscribers(), obs.dropped.Load())
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	obs := &countingObserver{}
	hub := events.NewHub(1, 0, obs)
	s, _ := hub.Subscribe()

	hub.Unsubscribe(s)
	hub.Unsubscribe(s)
	hub.Publish(event(1))

	if _, ok := <-s.C; ok {
		t.Error("channel should be closed")
	}
	if obs.added.Load() != 1 || obs.removed.Load() != 1 || hub.Subscribers() != 0 {
		t.Errorf("added %d removed %d subscribers %d", obs.added.Load(), obs.removed.Load(), hub.Subscribers())
	}
}

func TestSubscriberLimit(t *testing.T) {
	hub := events.NewHub(1, 2, nil)
	first, _ := hub.Subscribe()
	if _, err := hub.Subscribe(); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscribe(); !errors.Is(err, events.ErrTooManySubscribers) {
		t.Errorf("third Subscribe = %v, want ErrTooManySubscribers", err)
	}
	hub.Unsubscribe(first)
	if _, err := hub.Subscribe(); err != nil {
		t.Errorf("Subscribe after a slot freed: %v", err)
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	hub := events.NewHub(1, 0, nil)
	s, _ := hub.Subscribe()

	hub.Close()

	if _, ok := <-s.C; ok {
		t.Error("subscription should end on Close")
	}
	if s.Dropped() {
		t.Error("closing the hub is not a drop")
	}
	if _, err := hub.Subscribe(); err == nil {
		t.Error("Subscribe after Close should fail")
	}
	hub.Publish(event(1)) // must not panic
}

func TestEventJSON(t *testing.T) {
	sc := device.StateChanged{
		DeviceID:   uuid.MustParse("0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12"),
		From:       device.StateAvailable,
		To:         device.StateInUse,
		OccurredAt: time.Date(2026, 10, 31, 14, 5, 9, 123000000, time.UTC),
	}
	e := events.FromStateChange(uuid.MustParse("0192f7a4-6c2e-7c41-9a0b-3f5d8e2a1b77"), sc)

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event_id":"0192f7a4-6c2e-7c41-9a0b-3f5d8e2a1b77","device_id":"0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12",` +
		`"previous_state":"available","new_state":"in-use","occurred_at":"2026-10-31T14:05:09.123Z"}`
	if string(b) != want {
		t.Errorf("json = %s\nwant   %s", b, want)
	}
}
