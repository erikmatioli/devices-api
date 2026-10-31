//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/events"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

const quietPeriod = 300 * time.Millisecond

type collector struct{ ch chan events.StateChanged }

func (c *collector) Publish(e events.StateChanged) { c.ch <- e }

// startListener runs a listener (one per simulated replica) until the test
// ends and waits for it to be connected.
func startListener(t *testing.T) (*postgres.Listener, *collector) {
	t.Helper()
	c := &collector{ch: make(chan events.StateChanged, 16)}
	l := postgres.NewListener(testPool, c, slog.New(slog.DiscardHandler))
	l.MinBackoff = 20 * time.Millisecond
	l.MaxBackoff = 100 * time.Millisecond
	l.PingInterval = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitReady(t, l, true)
	return l, c
}

func waitReady(t *testing.T, l *postgres.Listener, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for (l.Ready(context.Background()) == nil) != want {
		if time.Now().After(deadline) {
			t.Fatalf("listener ready = %v, want %v", !want, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func expectEvent(t *testing.T, c *collector) events.StateChanged {
	t.Helper()
	select {
	case e := <-c.ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no notification received")
	}
	return events.StateChanged{}
}

func expectNoEvent(t *testing.T, c *collector) {
	t.Helper()
	select {
	case e := <-c.ch:
		t.Fatalf("unexpected notification %+v", e)
	case <-time.After(quietPeriod):
	}
}

func setState(t *testing.T, repo *postgres.DeviceRepository, d device.Device, s device.State) {
	t.Helper()
	_, err := repo.Update(context.Background(), d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{State: &s}, time.Now())
	})
	if err != nil {
		t.Fatalf("update state: %v", err)
	}
}

func TestStateChangeReachesEveryReplica(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	_, replicaA := startListener(t)
	_, replicaB := startListener(t)
	d := seed(t, repo, "Pixel", "Google", device.StateAvailable, 0)

	setState(t, repo, d, device.StateInUse)

	for name, c := range map[string]*collector{"A": replicaA, "B": replicaB} {
		e := expectEvent(t, c)
		if e.DeviceID != d.ID() || e.PreviousState != device.StateAvailable || e.NewState != device.StateInUse {
			t.Errorf("replica %s got %+v", name, e)
		}
		if e.EventID.Version() != 7 || e.OccurredAt.IsZero() {
			t.Errorf("replica %s: event id %v, occurred at %v", name, e.EventID, e.OccurredAt)
		}
	}
}

func TestOnlyStateChangesAreNotified(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	_, c := startListener(t)
	ctx := context.Background()
	d := seed(t, repo, "Pixel", "Google", device.StateInUse, 0)

	// A rejected change (rename while in use) and a no-op change.
	_, err := repo.Update(ctx, d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{Name: ptr("Renamed")}, time.Now())
	})
	if err == nil {
		t.Fatal("rename while in use should fail")
	}
	setState(t, repo, d, device.StateInUse)
	expectNoEvent(t, c)

	// A rename without a state change.
	setState(t, repo, d, device.StateAvailable)
	expectEvent(t, c)
	if _, err := repo.Update(ctx, d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{Name: ptr("Renamed")}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	expectNoEvent(t, c)
}

func TestNotificationsFollowTheTransaction(t *testing.T) {
	_, c := startListener(t)
	ctx := context.Background()
	payload := func(state device.State) string {
		b, _ := json.Marshal(events.StateChanged{EventID: device.NewID(), DeviceID: device.NewID(),
			PreviousState: device.StateAvailable, NewState: state, OccurredAt: time.Now().UTC()})
		return string(b)
	}

	rolledBack, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolledBack.Exec(ctx, `SELECT pg_notify($1, $2)`, postgres.StateChangedChannel, payload(device.StateInactive)); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	expectNoEvent(t, c)

	pending, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Exec(ctx, `SELECT pg_notify($1, $2)`, postgres.StateChangedChannel, payload(device.StateInUse)); err != nil {
		t.Fatal(err)
	}
	expectNoEvent(t, c) // not delivered before commit
	if err := pending.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if e := expectEvent(t, c); e.NewState != device.StateInUse {
		t.Errorf("got %+v, want the committed notification", e)
	}
}

func TestListenerReconnects(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	l, c := startListener(t)
	ctx := context.Background()

	if _, err := testPool.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'devices-api-listener'`); err != nil {
		t.Fatal(err)
	}
	waitReady(t, l, false)
	waitReady(t, l, true)

	d := seed(t, repo, "Pixel", "Google", device.StateAvailable, 0)
	setState(t, repo, d, device.StateInactive)
	if e := expectEvent(t, c); e.DeviceID != d.ID() {
		t.Errorf("got %+v after reconnect", e)
	}
}

func TestListenerSkipsMalformedPayloads(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	_, c := startListener(t)
	if _, err := testPool.Exec(context.Background(), `SELECT pg_notify($1, 'not json')`, postgres.StateChangedChannel); err != nil {
		t.Fatal(err)
	}

	d := seed(t, repo, "Pixel", "Google", device.StateAvailable, 0)
	setState(t, repo, d, device.StateInUse)
	if e := expectEvent(t, c); e.DeviceID != d.ID() {
		t.Errorf("got %+v, want the valid notification", e)
	}
}
