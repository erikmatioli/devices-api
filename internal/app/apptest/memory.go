// Package apptest provides test doubles for the application ports.
package apptest

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
)

// MemoryRepository is an in-memory app.DeviceRepository that follows the
// same contract as the PostgreSQL implementation. It is meant for tests of
// the layers above persistence.
type MemoryRepository struct {
	mu      sync.Mutex
	devices map[uuid.UUID]device.Device
	// Err, when set, is returned by every method to simulate an outage.
	Err error
}

var _ app.DeviceRepository = (*MemoryRepository)(nil)

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{devices: make(map[uuid.UUID]device.Device)}
}

// Create implements app.DeviceRepository.
func (m *MemoryRepository) Create(_ context.Context, d device.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.devices[d.ID()] = d
	return nil
}

// Get implements app.DeviceRepository.
func (m *MemoryRepository) Get(_ context.Context, id uuid.UUID) (device.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return device.Device{}, m.Err
	}
	d, ok := m.devices[id]
	if !ok {
		return device.Device{}, device.ErrNotFound
	}
	return d, nil
}

// List implements app.DeviceRepository.
func (m *MemoryRepository) List(_ context.Context, f app.ListFilter) (app.Page, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return app.Page{}, m.Err
	}

	var matched []device.Device
	for _, d := range m.devices {
		if f.Brand != "" && !strings.EqualFold(d.Brand(), f.Brand) {
			continue
		}
		if f.State != "" && d.State() != f.State {
			continue
		}
		if f.After != nil && !after(d, *f.After) {
			continue
		}
		matched = append(matched, d)
	}
	slices.SortFunc(matched, func(a, b device.Device) int {
		if c := a.CreationTime().Compare(b.CreationTime()); c != 0 {
			return c
		}
		return compareIDs(a.ID(), b.ID())
	})

	page := app.Page{Devices: matched}
	if len(matched) > f.Limit {
		page.Devices = matched[:f.Limit]
		last := page.Devices[f.Limit-1]
		page.Next = &app.Cursor{CreationTime: last.CreationTime(), ID: last.ID()}
	}
	return page, nil
}

// Update implements app.DeviceRepository.
func (m *MemoryRepository) Update(_ context.Context, id uuid.UUID, fn app.UpdateFunc) (device.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return device.Device{}, m.Err
	}
	d, ok := m.devices[id]
	if !ok {
		return device.Device{}, device.ErrNotFound
	}

	upd, err := fn(&d)
	if err != nil {
		return device.Device{}, err
	}
	if !upd.Modified {
		return m.devices[id], nil
	}
	stored := device.Restore(d.ID(), d.Name(), d.Brand(), d.State(), d.CreationTime(), d.Version()+1)
	m.devices[id] = stored
	return stored, nil
}

// Delete implements app.DeviceRepository.
func (m *MemoryRepository) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	d, ok := m.devices[id]
	if !ok {
		return device.ErrNotFound
	}
	if err := d.EnsureDeletable(); err != nil {
		return err
	}
	delete(m.devices, id)
	return nil
}

func after(d device.Device, c app.Cursor) bool {
	if cmp := d.CreationTime().Compare(c.CreationTime); cmp != 0 {
		return cmp > 0
	}
	return compareIDs(d.ID(), c.ID) > 0
}

// compareIDs orders ids by their bytes, as PostgreSQL orders uuid values.
func compareIDs(a, b uuid.UUID) int {
	return bytes.Compare(a[:], b[:])
}
