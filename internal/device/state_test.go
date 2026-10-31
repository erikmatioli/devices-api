package device_test

import (
	"testing"

	"github.com/erikmatioli/devices-api/internal/device"
)

func TestParseState(t *testing.T) {
	tests := []struct {
		in      string
		want    device.State
		wantErr bool
	}{
		{in: "available", want: device.StateAvailable},
		{in: "in-use", want: device.StateInUse},
		{in: "inactive", want: device.StateInactive},
		{in: "IN-USE", wantErr: true},
		{in: "in_use", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := device.ParseState(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseState(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseState(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStatesAreValid(t *testing.T) {
	if len(device.States) != 3 {
		t.Fatalf("expected 3 states, got %d", len(device.States))
	}
	for _, s := range device.States {
		if !s.Valid() || s.String() != string(s) {
			t.Errorf("state %q should be valid and print as itself", s)
		}
	}
}
