package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
)

// ErrVersionMismatch is returned when an update was conditioned on a version
// that is no longer current.
var ErrVersionMismatch = errors.New("device version does not match")

// Page size limits for listings.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Service implements the device use cases.
type Service struct {
	repo  DeviceRepository
	now   func() time.Time
	newID func() uuid.UUID
}

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the time source, mainly for tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithIDGenerator replaces the id generator, mainly for tests.
func WithIDGenerator(newID func() uuid.UUID) Option {
	return func(s *Service) { s.newID = newID }
}

// NewService returns a Service that stores devices in repo.
func NewService(repo DeviceRepository, opts ...Option) *Service {
	s := &Service{repo: repo, now: time.Now, newID: device.NewID}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CreateInput is the data needed to create a device.
type CreateInput struct {
	Name  string
	Brand string
	// State is optional and defaults to available.
	State device.State
}

// Create validates and stores a new device.
func (s *Service) Create(ctx context.Context, in CreateInput) (device.Device, error) {
	d, err := device.New(device.NewParams{
		ID:    s.newID(),
		Name:  in.Name,
		Brand: in.Brand,
		State: in.State,
		Now:   s.now(),
	})
	if err != nil {
		return device.Device{}, err
	}
	if err := s.repo.Create(ctx, d); err != nil {
		return device.Device{}, err
	}
	return d, nil
}

// Get returns a single device.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (device.Device, error) {
	return s.repo.Get(ctx, id)
}

// List returns a page of devices. The limit is clamped to
// [1, MaxPageSize], with DefaultPageSize when unset.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	switch {
	case f.Limit <= 0:
		f.Limit = DefaultPageSize
	case f.Limit > MaxPageSize:
		f.Limit = MaxPageSize
	}
	return s.repo.List(ctx, f)
}

// Update applies changes to a device. When expectedVersion is set, the
// update only proceeds if the stored version still matches it, which gives
// clients optimistic concurrency control through If-Match.
func (s *Service) Update(ctx context.Context, id uuid.UUID, changes device.Changes, expectedVersion *int64) (device.Device, error) {
	return s.repo.Update(ctx, id, func(d *device.Device) (device.Update, error) {
		if expectedVersion != nil && d.Version() != *expectedVersion {
			return device.Update{}, ErrVersionMismatch
		}
		return d.Apply(changes, s.now())
	})
}

// Delete removes a device that is not in use.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}
