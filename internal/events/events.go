// Package events defines the device state change notification and fans it
// out to the clients subscribed in this process.
package events

import (
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
)

// TypeStateChanged is the SSE event name of a state change.
const TypeStateChanged = "device.state_changed"

// StateChanged is the notification sent to subscribers. Its JSON form is
// part of the public contract.
type StateChanged struct {
	EventID       uuid.UUID    `json:"event_id"`
	DeviceID      uuid.UUID    `json:"device_id"`
	PreviousState device.State `json:"previous_state"`
	NewState      device.State `json:"new_state"`
	OccurredAt    time.Time    `json:"occurred_at"`
}

// FromStateChange builds the notification for a domain state transition.
func FromStateChange(eventID uuid.UUID, sc device.StateChanged) StateChanged {
	return StateChanged{
		EventID:       eventID,
		DeviceID:      sc.DeviceID,
		PreviousState: sc.From,
		NewState:      sc.To,
		OccurredAt:    sc.OccurredAt,
	}
}
