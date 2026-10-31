package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/app/apptest"
	"github.com/erikmatioli/devices-api/internal/device"
)

var (
	fixedNow = time.Date(2026, 10, 31, 9, 30, 0, 0, time.UTC)
	fixedID  = uuid.MustParse("0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12")
)

func newService(repo *apptest.MemoryRepository) *app.Service {
	return app.NewService(repo,
		app.WithClock(func() time.Time { return fixedNow }),
		app.WithIDGenerator(func() uuid.UUID { return fixedID }),
	)
}

func ptr[T any](v T) *T { return &v }

func TestCreate(t *testing.T) {
	repo := apptest.NewMemoryRepository()
	svc := newService(repo)

	d, err := svc.Create(context.Background(), app.CreateInput{Name: "Pixel 9", Brand: "Google"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.ID() != fixedID || !d.CreationTime().Equal(fixedNow) || d.State() != device.StateAvailable {
		t.Errorf("created device = %+v", d)
	}
	if _, err := repo.Get(context.Background(), fixedID); err != nil {
		t.Errorf("device not stored: %v", err)
	}
}

func TestCreateValidationIsNotStored(t *testing.T) {
	repo := apptest.NewMemoryRepository()
	svc := newService(repo)

	_, err := svc.Create(context.Background(), app.CreateInput{Name: "", Brand: "Google"})

	var verr *device.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if _, err := repo.Get(context.Background(), fixedID); !errors.Is(err, device.ErrNotFound) {
		t.Error("invalid device must not be stored")
	}
}

func TestCreatePropagatesRepositoryError(t *testing.T) {
	repo := apptest.NewMemoryRepository()
	repo.Err = errors.New("connection reset")

	_, err := newService(repo).Create(context.Background(), app.CreateInput{Name: "Pixel", Brand: "Google"})

	if !errors.Is(err, repo.Err) {
		t.Errorf("error = %v, want the repository error", err)
	}
}

func TestUpdateWithExpectedVersion(t *testing.T) {
	ctx := context.Background()
	repo := apptest.NewMemoryRepository()
	svc := newService(repo)
	if _, err := svc.Create(ctx, app.CreateInput{Name: "Pixel 9", Brand: "Google"}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Update(ctx, fixedID, device.Changes{Name: ptr("Stale")}, ptr(int64(7)))
	if !errors.Is(err, app.ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}

	got, err := svc.Update(ctx, fixedID, device.Changes{Name: ptr("Fresh")}, ptr(int64(1)))
	if err != nil {
		t.Fatalf("Update with current version: %v", err)
	}
	if got.Name() != "Fresh" || got.Version() != 2 {
		t.Errorf("updated device = %+v, want name Fresh at version 2", got)
	}

	got, err = svc.Update(ctx, fixedID, device.Changes{State: ptr(device.StateInUse)}, nil)
	if err != nil {
		t.Fatalf("unconditional Update: %v", err)
	}
	if got.State() != device.StateInUse || got.Version() != 3 {
		t.Errorf("updated device = %+v, want in-use at version 3", got)
	}
}

func TestListClampsLimit(t *testing.T) {
	ctx := context.Background()
	repo := apptest.NewMemoryRepository()
	ids := 0
	svc := app.NewService(repo, app.WithIDGenerator(func() uuid.UUID {
		ids++
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte{byte(ids)})
	}))
	for range app.MaxPageSize + 5 {
		if _, err := svc.Create(ctx, app.CreateInput{Name: "n", Brand: "b"}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		limit int
		want  int
	}{
		{limit: 0, want: app.DefaultPageSize},
		{limit: -3, want: app.DefaultPageSize},
		{limit: 7, want: 7},
		{limit: 1000, want: app.MaxPageSize},
	}
	for _, tt := range tests {
		page, err := svc.List(ctx, app.ListFilter{Limit: tt.limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Devices) != tt.want {
			t.Errorf("limit %d returned %d devices, want %d", tt.limit, len(page.Devices), tt.want)
		}
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	repo := apptest.NewMemoryRepository()
	svc := newService(repo)
	if _, err := svc.Create(ctx, app.CreateInput{Name: "Pixel", Brand: "Google", State: device.StateInUse}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(ctx, fixedID); !errors.Is(err, device.ErrDeleteInUse) {
		t.Errorf("Delete(in-use) = %v, want ErrDeleteInUse", err)
	}
	if _, err := svc.Update(ctx, fixedID, device.Changes{State: ptr(device.StateInactive)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, fixedID); err != nil {
		t.Errorf("Delete(inactive) = %v", err)
	}
	if _, err := svc.Get(ctx, fixedID); !errors.Is(err, device.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}
