//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

var baseTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// seed creates a device whose creation time is baseTime plus offset.
func seed(t *testing.T, repo *postgres.DeviceRepository, name, brand string, state device.State, offset time.Duration) device.Device {
	t.Helper()
	d, err := device.New(device.NewParams{
		ID: device.NewID(), Name: name, Brand: brand, State: state, Now: baseTime.Add(offset),
	})
	if err != nil {
		t.Fatalf("new device: %v", err)
	}
	if err := repo.Create(context.Background(), d); err != nil {
		t.Fatalf("create device: %v", err)
	}
	return d
}

func names(devices []device.Device) []string {
	out := make([]string, len(devices))
	for i, d := range devices {
		out[i] = d.Name()
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCreateAndGet(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()

	want, err := device.New(device.NewParams{
		ID: device.NewID(), Name: "Pixel 9", Brand: "Google",
		Now: time.Date(2026, 10, 31, 12, 0, 0, 123456789, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, want.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestGetNotFound(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)

	_, err := repo.Get(context.Background(), device.NewID())

	if !errors.Is(err, device.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()

	seed(t, repo, "d1", "Google", device.StateAvailable, 1*time.Minute)
	seed(t, repo, "d2", "Apple", device.StateInUse, 2*time.Minute)
	seed(t, repo, "d3", "google", device.StateInUse, 3*time.Minute)
	seed(t, repo, "d4", "Samsung", device.StateInactive, 4*time.Minute)
	seed(t, repo, "d5", "GOOGLE", device.StateAvailable, 5*time.Minute)

	tests := []struct {
		name   string
		filter app.ListFilter
		want   []string
	}{
		{name: "all, oldest first", filter: app.ListFilter{Limit: 10}, want: []string{"d1", "d2", "d3", "d4", "d5"}},
		{name: "brand ignores case", filter: app.ListFilter{Brand: "gOoGlE", Limit: 10}, want: []string{"d1", "d3", "d5"}},
		{name: "state", filter: app.ListFilter{State: device.StateInUse, Limit: 10}, want: []string{"d2", "d3"}},
		{name: "brand and state", filter: app.ListFilter{Brand: "google", State: device.StateAvailable, Limit: 10}, want: []string{"d1", "d5"}},
		{name: "no match", filter: app.ListFilter{Brand: "Nokia", Limit: 10}, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := repo.List(ctx, tt.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := names(page.Devices); !equalStrings(got, tt.want) {
				t.Errorf("devices = %v, want %v", got, tt.want)
			}
			if page.Next != nil {
				t.Errorf("unexpected next cursor %+v", page.Next)
			}
		})
	}
}

func TestListPagination(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()

	// Two devices share a creation time to prove the id tie-breaker keeps
	// pages disjoint.
	var want []string
	for i := range 7 {
		offset := time.Duration(i) * time.Minute
		if i == 4 {
			offset = 3 * time.Minute
		}
		seed(t, repo, "d"+strconv.Itoa(i), "Brand", device.StateAvailable, offset)
	}
	all, err := repo.List(ctx, app.ListFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want = names(all.Devices)

	var (
		got    []string
		cursor *app.Cursor
		pages  int
	)
	for {
		page, err := repo.List(ctx, app.ListFilter{Limit: 3, After: cursor})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		pages++
		got = append(got, names(page.Devices)...)
		if page.Next == nil {
			break
		}
		cursor = page.Next
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
	}

	if pages != 3 {
		t.Errorf("pages = %d, want 3 for 7 items of 3 per page", pages)
	}
	if !equalStrings(got, want) {
		t.Errorf("paginated = %v, want %v", got, want)
	}
}

func TestListExactPageHasNoCursor(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	for i := range 3 {
		seed(t, repo, "d"+strconv.Itoa(i), "Brand", device.StateAvailable, time.Duration(i)*time.Minute)
	}

	page, err := repo.List(context.Background(), app.ListFilter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Devices) != 3 || page.Next != nil {
		t.Errorf("got %d devices and cursor %+v, want 3 and none", len(page.Devices), page.Next)
	}
}

func TestUpdate(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()
	d := seed(t, repo, "Pixel 9", "Google", device.StateAvailable, 0)

	got, err := repo.Update(ctx, d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{Name: ptr("Pixel 9 Pro"), State: ptr(device.StateInUse)}, time.Now())
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name() != "Pixel 9 Pro" || got.State() != device.StateInUse || got.Version() != 2 {
		t.Errorf("updated device = %+v, want new name, in-use, version 2", got)
	}

	stored, err := repo.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored != got {
		t.Errorf("stored %+v differs from returned %+v", stored, got)
	}
}

func TestUpdateNoOpKeepsVersion(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	d := seed(t, repo, "Pixel 9", "Google", device.StateAvailable, 0)

	got, err := repo.Update(context.Background(), d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{Name: ptr("Pixel 9")}, time.Now())
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Version() != 1 {
		t.Errorf("version = %d, want 1 for a no-op update", got.Version())
	}
}

func TestUpdateErrorRollsBack(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()
	d := seed(t, repo, "Pixel 9", "Google", device.StateInUse, 0)

	_, err := repo.Update(ctx, d.ID(), func(cur *device.Device) (device.Update, error) {
		return cur.Apply(device.Changes{Name: ptr("Renamed")}, time.Now())
	})
	if !errors.Is(err, device.ErrNameBrandLocked) {
		t.Fatalf("error = %v, want ErrNameBrandLocked", err)
	}

	stored, err := repo.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored != d {
		t.Errorf("device changed after a rejected update: %+v", stored)
	}
}

func TestUpdateNotFound(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)

	called := false
	_, err := repo.Update(context.Background(), device.NewID(), func(*device.Device) (device.Update, error) {
		called = true
		return device.Update{}, nil
	})

	if !errors.Is(err, device.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if called {
		t.Error("update function must not run for a missing device")
	}
}

// TestUpdateSerializesConcurrentWriters shows that the row lock prevents
// lost updates: every writer sees the result of the previous one.
func TestUpdateSerializesConcurrentWriters(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()
	d := seed(t, repo, "Pixel 9", "Google", device.StateAvailable, 0)

	const writers = 15
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Update(ctx, d.ID(), func(cur *device.Device) (device.Update, error) {
				next := device.StateInUse
				if cur.State() == device.StateInUse {
					next = device.StateAvailable
				}
				return cur.Apply(device.Changes{State: &next}, time.Now())
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent update: %v", err)
		}
	}

	stored, err := repo.Get(ctx, d.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version() != 1+writers {
		t.Errorf("version = %d, want %d: some updates were lost", stored.Version(), 1+writers)
	}
	// An odd number of toggles from available ends in in-use.
	if stored.State() != device.StateInUse {
		t.Errorf("state = %s, want in-use after %d toggles", stored.State(), writers)
	}
}

func TestDelete(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	ctx := context.Background()

	available := seed(t, repo, "a", "Brand", device.StateAvailable, 0)
	inactive := seed(t, repo, "b", "Brand", device.StateInactive, time.Minute)
	inUse := seed(t, repo, "c", "Brand", device.StateInUse, 2*time.Minute)

	for _, d := range []device.Device{available, inactive} {
		if err := repo.Delete(ctx, d.ID()); err != nil {
			t.Errorf("Delete(%s): %v", d.State(), err)
		}
		if _, err := repo.Get(ctx, d.ID()); !errors.Is(err, device.ErrNotFound) {
			t.Errorf("device %s still present after delete", d.State())
		}
	}

	if err := repo.Delete(ctx, inUse.ID()); !errors.Is(err, device.ErrDeleteInUse) {
		t.Errorf("Delete(in-use) = %v, want ErrDeleteInUse", err)
	}
	if _, err := repo.Get(ctx, inUse.ID()); err != nil {
		t.Errorf("in-use device must survive a delete attempt: %v", err)
	}

	if err := repo.Delete(ctx, device.NewID()); !errors.Is(err, device.ErrNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrNotFound", err)
	}
}

func TestSchemaRejectsCreationTimeChange(t *testing.T) {
	resetDevices(t)
	repo := postgres.NewDeviceRepository(testPool)
	d := seed(t, repo, "Pixel 9", "Google", device.StateAvailable, 0)

	_, err := testPool.Exec(context.Background(),
		`UPDATE devices SET creation_time = creation_time + interval '1 day' WHERE id = $1`, d.ID())

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("error = %v, want check_violation (23514)", err)
	}
}

func TestSchemaRejectsInvalidRows(t *testing.T) {
	resetDevices(t)
	tests := map[string]string{
		"unknown state": `INSERT INTO devices (id, name, brand, state, creation_time) VALUES (gen_random_uuid(), 'n', 'b', 'lost', now())`,
		"empty name":    `INSERT INTO devices (id, name, brand, state, creation_time) VALUES (gen_random_uuid(), '', 'b', 'available', now())`,
	}
	for name, stmt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := testPool.Exec(context.Background(), stmt)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Errorf("error = %v, want check_violation (23514)", err)
			}
		})
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	if err := postgres.Migrate(testPool, slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("second Migrate: %v", err)
	}
}
