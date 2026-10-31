// Package device holds the Device domain model and its business rules.
// It has no knowledge of HTTP, SQL or any other transport.
package device

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Field limits, in characters.
const (
	MaxNameLength  = 100
	MaxBrandLength = 50
)

// Timestamps are kept at microsecond precision, the resolution of
// PostgreSQL timestamptz, so a value returned on create is identical to the
// one read back later.
const timePrecision = time.Microsecond

// Device is a managed device. Its fields are only changed through methods
// that enforce the domain rules.
type Device struct {
	id           uuid.UUID
	name         string
	brand        string
	state        State
	creationTime time.Time
	version      int64
}

// NewID returns a time-ordered (version 7) identifier, which keeps index
// inserts close together and makes ids sortable by creation.
func NewID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}

// NewParams holds the input to create a device.
type NewParams struct {
	ID    uuid.UUID
	Name  string
	Brand string
	// State is optional and defaults to StateAvailable.
	State State
	Now   time.Time
}

// New validates p and returns a device at version 1.
func New(p NewParams) (Device, error) {
	var errs validationErrors
	name := normalizeText(&errs, "name", p.Name, MaxNameLength)
	brand := normalizeText(&errs, "brand", p.Brand, MaxBrandLength)

	state := p.State
	if state == "" {
		state = StateAvailable
	} else if !state.Valid() {
		errs.add("state", CodeInvalidValue, stateMessage)
	}

	if err := errs.err(); err != nil {
		return Device{}, err
	}
	return Device{
		id:           p.ID,
		name:         name,
		brand:        brand,
		state:        state,
		creationTime: normalizeTime(p.Now),
		version:      1,
	}, nil
}

// Restore rebuilds a device from trusted storage without validation.
func Restore(id uuid.UUID, name, brand string, state State, creationTime time.Time, version int64) Device {
	return Device{
		id:           id,
		name:         name,
		brand:        brand,
		state:        state,
		creationTime: normalizeTime(creationTime),
		version:      version,
	}
}

// ID returns the device identifier.
func (d Device) ID() uuid.UUID { return d.id }

// Name returns the device name.
func (d Device) Name() string { return d.name }

// Brand returns the device brand.
func (d Device) Brand() string { return d.brand }

// State returns the current state.
func (d Device) State() State { return d.state }

// CreationTime returns when the device was created, in UTC.
func (d Device) CreationTime() time.Time { return d.creationTime }

// Version returns the optimistic-locking version, incremented on every
// persisted change.
func (d Device) Version() int64 { return d.version }

// ETag returns the entity tag that represents the current version.
func (d Device) ETag() string { return `"` + strconv.FormatInt(d.version, 10) + `"` }

// Changes describes an update. Nil fields are left untouched, which serves
// both partial updates (only some fields set) and full updates (all fields
// set).
type Changes struct {
	Name  *string
	Brand *string
	State *State
	// CreationTime is accepted only when equal to the stored value, so
	// clients can send back a full representation they read earlier.
	CreationTime *time.Time
}

// Validate checks the values present in c without applying them, so callers
// that also collect transport errors can report every problem at once. Rules
// that depend on the stored device (read-only creation time, in-use lock)
// are checked by Apply.
func (c Changes) Validate() error {
	var errs validationErrors
	if c.Name != nil {
		normalizeText(&errs, "name", *c.Name, MaxNameLength)
	}
	if c.Brand != nil {
		normalizeText(&errs, "brand", *c.Brand, MaxBrandLength)
	}
	if c.State != nil && !c.State.Valid() {
		errs.add("state", CodeInvalidValue, stateMessage)
	}
	return errs.err()
}

// StateChanged records a state transition.
type StateChanged struct {
	DeviceID   uuid.UUID
	From       State
	To         State
	OccurredAt time.Time
}

// Update is the outcome of applying Changes.
type Update struct {
	// Modified reports whether any field changed. When false the device
	// does not need to be persisted again.
	Modified bool
	// StateChange is set only when the state changed.
	StateChange *StateChanged
}

// Apply validates c against the domain rules and, if they hold, applies it.
// On error the device is left unchanged.
//
// Rules, in order:
//   - field values must be valid (422 at the API);
//   - creation time is read-only (422);
//   - name and brand cannot change while the stored state is in-use (409).
//     The check uses the state before this update, so a single request
//     cannot release the device and rename it at the same time. Sending
//     the current values is a no-op and is accepted.
func (d *Device) Apply(c Changes, now time.Time) (Update, error) {
	var errs validationErrors

	name, brand, state := d.name, d.brand, d.state
	if c.Name != nil {
		name = normalizeText(&errs, "name", *c.Name, MaxNameLength)
	}
	if c.Brand != nil {
		brand = normalizeText(&errs, "brand", *c.Brand, MaxBrandLength)
	}
	if c.State != nil {
		if c.State.Valid() {
			state = *c.State
		} else {
			errs.add("state", CodeInvalidValue, stateMessage)
		}
	}
	if c.CreationTime != nil && !normalizeTime(*c.CreationTime).Equal(d.creationTime) {
		errs.add("creation_time", CodeReadOnly, "cannot be changed")
	}
	if err := errs.err(); err != nil {
		return Update{}, err
	}

	identityChanged := name != d.name || brand != d.brand
	if identityChanged && d.state == StateInUse {
		return Update{}, ErrNameBrandLocked
	}

	var upd Update
	if state != d.state {
		upd.StateChange = &StateChanged{
			DeviceID:   d.id,
			From:       d.state,
			To:         state,
			OccurredAt: normalizeTime(now),
		}
	}
	upd.Modified = identityChanged || upd.StateChange != nil

	d.name, d.brand, d.state = name, brand, state
	return upd, nil
}

// EnsureDeletable reports whether the device may be deleted.
func (d Device) EnsureDeletable() error {
	if d.state == StateInUse {
		return ErrDeleteInUse
	}
	return nil
}

const stateMessage = "must be one of available, in-use, inactive"

// normalizeText trims s and validates its length and characters.
func normalizeText(errs *validationErrors, field, s string, maxLen int) string {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case !utf8.ValidString(s):
		errs.add(field, CodeInvalidValue, "must be valid UTF-8")
	case n == 0:
		errs.add(field, CodeRequired, "must not be empty")
	case n > maxLen:
		errs.add(field, CodeTooLong, "must be at most "+strconv.Itoa(maxLen)+" characters")
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		errs.add(field, CodeInvalidValue, "must not contain control characters")
	}
	return s
}

func normalizeTime(t time.Time) time.Time {
	return t.UTC().Truncate(timePrecision)
}
