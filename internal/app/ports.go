// Package app contains the use cases of the service and the ports
// (interfaces) they need from adapters such as the database.
package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
)

// Cursor marks the position after which the next page starts. Pages are
// ordered by creation time and then id, which is unique and stable.
type Cursor struct {
	CreationTime time.Time
	ID           uuid.UUID
}

// ListFilter selects and paginates devices. Zero values mean "no filter".
type ListFilter struct {
	// Brand matches exactly, ignoring case.
	Brand string
	State device.State
	Limit int
	After *Cursor
}

// Page is one page of a device listing.
type Page struct {
	Devices []device.Device
	// Next is nil on the last page.
	Next *Cursor
}

// UpdateFunc mutates a device that the repository has locked for update.
// Returning an error aborts the update without persisting anything.
type UpdateFunc func(d *device.Device) (device.Update, error)

// DeviceRepository persists devices.
type DeviceRepository interface {
	// Create stores a new device.
	Create(ctx context.Context, d device.Device) error
	// Get returns the device or device.ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (device.Device, error)
	// List returns devices matching f, ordered by creation time.
	List(ctx context.Context, f ListFilter) (Page, error)
	// Update loads the device under a row lock, calls fn and persists the
	// result in the same transaction when fn reports a modification. It
	// returns the device as stored after the call.
	Update(ctx context.Context, id uuid.UUID, fn UpdateFunc) (device.Device, error)
	// Delete removes a device that is not in use. It returns
	// device.ErrNotFound or device.ErrDeleteInUse when it cannot.
	Delete(ctx context.Context, id uuid.UUID) error
}
