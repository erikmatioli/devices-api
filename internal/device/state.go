package device

import "fmt"

// State is the lifecycle state of a device.
type State string

// Supported device states.
const (
	StateAvailable State = "available"
	StateInUse     State = "in-use"
	StateInactive  State = "inactive"
)

// States lists every valid state in a stable order.
var States = []State{StateAvailable, StateInUse, StateInactive}

// ParseState converts s into a State, rejecting unknown values.
func ParseState(s string) (State, error) {
	st := State(s)
	if !st.Valid() {
		return "", fmt.Errorf("unknown device state %q", s)
	}
	return st, nil
}

// Valid reports whether s is one of the supported states.
func (s State) Valid() bool {
	switch s {
	case StateAvailable, StateInUse, StateInactive:
		return true
	default:
		return false
	}
}

func (s State) String() string {
	return string(s)
}
