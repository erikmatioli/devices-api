package device_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
)

var (
	created = time.Date(2026, 10, 31, 14, 0, 0, 123456789, time.UTC)
	later   = created.Add(time.Hour)
)

func ptr[T any](v T) *T { return &v }

func mustNew(t *testing.T, state device.State) device.Device {
	t.Helper()
	d, err := device.New(device.NewParams{
		ID:    uuid.MustParse("0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12"),
		Name:  "Pixel 9",
		Brand: "Google",
		State: state,
		Now:   created,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// fieldCodes flattens a validation error into "field:code" pairs.
func fieldCodes(t *testing.T, err error) []string {
	t.Helper()
	var verr *device.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	out := make([]string, len(verr.Fields))
	for i, f := range verr.Fields {
		out[i] = f.Field + ":" + f.Code
	}
	return out
}

func TestNew(t *testing.T) {
	d := mustNew(t, "")

	if d.State() != device.StateAvailable {
		t.Errorf("default state = %q, want available", d.State())
	}
	if d.Version() != 1 || d.ETag() != `"1"` {
		t.Errorf("version = %d, etag = %s, want 1 and \"1\"", d.Version(), d.ETag())
	}
	if want := created.Truncate(time.Microsecond); !d.CreationTime().Equal(want) {
		t.Errorf("creation time = %v, want %v truncated to microseconds", d.CreationTime(), want)
	}
	if d.CreationTime().Location() != time.UTC {
		t.Error("creation time must be in UTC")
	}
}

func TestNewNormalizesInput(t *testing.T) {
	local := time.FixedZone("UTC-3", -3*60*60)
	d, err := device.New(device.NewParams{
		ID:    device.NewID(),
		Name:  "  Galaxy S25  ",
		Brand: "\tSamsung\n",
		State: device.StateInUse,
		Now:   created.In(local),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.Name() != "Galaxy S25" || d.Brand() != "Samsung" {
		t.Errorf("name/brand = %q/%q, want trimmed values", d.Name(), d.Brand())
	}
	if d.State() != device.StateInUse {
		t.Errorf("state = %q, want in-use", d.State())
	}
	if d.CreationTime().Location() != time.UTC {
		t.Error("creation time must be converted to UTC")
	}
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name  string
		input device.NewParams
		want  []string
	}{
		{
			name:  "empty fields",
			input: device.NewParams{Name: "", Brand: "   "},
			want:  []string{"name:required", "brand:required"},
		},
		{
			name: "too long",
			input: device.NewParams{
				Name:  strings.Repeat("n", device.MaxNameLength+1),
				Brand: strings.Repeat("b", device.MaxBrandLength+1),
			},
			want: []string{"name:too_long", "brand:too_long"},
		},
		{
			name:  "control characters",
			input: device.NewParams{Name: "Pixel\x009", Brand: "Goo\ngle"},
			want:  []string{"name:invalid_value", "brand:invalid_value"},
		},
		{
			name:  "invalid utf-8",
			input: device.NewParams{Name: "Pixel \xff", Brand: "Google"},
			want:  []string{"name:invalid_value"},
		},
		{
			name:  "unknown state",
			input: device.NewParams{Name: "Pixel", Brand: "Google", State: "broken"},
			want:  []string{"state:invalid_value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.ID = device.NewID()
			tt.input.Now = created

			_, err := device.New(tt.input)

			got := fieldCodes(t, err)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("field errors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewLengthCountsCharacters(t *testing.T) {
	// 100 multi-byte characters are within the limit even though they
	// take more than 100 bytes.
	name := strings.Repeat("é", device.MaxNameLength)
	if _, err := device.New(device.NewParams{ID: device.NewID(), Name: name, Brand: "Brand", Now: created}); err != nil {
		t.Fatalf("name of %d characters rejected: %v", device.MaxNameLength, err)
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name         string
		start        device.State
		changes      device.Changes
		wantErr      error
		wantFields   []string
		wantModified bool
		wantFrom     device.State
		wantTo       device.State
		wantName     string
		wantBrand    string
	}{
		{
			name:         "rename an available device",
			start:        device.StateAvailable,
			changes:      device.Changes{Name: ptr("Pixel 9 Pro"), Brand: ptr("Alphabet")},
			wantModified: true,
			wantName:     "Pixel 9 Pro",
			wantBrand:    "Alphabet",
		},
		{
			name:         "rename an inactive device",
			start:        device.StateInactive,
			changes:      device.Changes{Name: ptr("Pixel 9 Pro")},
			wantModified: true,
			wantName:     "Pixel 9 Pro",
			wantBrand:    "Google",
		},
		{
			name:         "state change only",
			start:        device.StateAvailable,
			changes:      device.Changes{State: ptr(device.StateInUse)},
			wantModified: true,
			wantFrom:     device.StateAvailable,
			wantTo:       device.StateInUse,
			wantName:     "Pixel 9",
			wantBrand:    "Google",
		},
		{
			name:         "release an in-use device",
			start:        device.StateInUse,
			changes:      device.Changes{State: ptr(device.StateAvailable)},
			wantModified: true,
			wantFrom:     device.StateInUse,
			wantTo:       device.StateAvailable,
			wantName:     "Pixel 9",
			wantBrand:    "Google",
		},
		{
			name:         "rename and take in use in one request",
			start:        device.StateAvailable,
			changes:      device.Changes{Name: ptr("Pixel 10"), State: ptr(device.StateInUse)},
			wantModified: true,
			wantFrom:     device.StateAvailable,
			wantTo:       device.StateInUse,
			wantName:     "Pixel 10",
			wantBrand:    "Google",
		},
		{
			name:    "rename an in-use device",
			start:   device.StateInUse,
			changes: device.Changes{Name: ptr("Pixel 10")},
			wantErr: device.ErrNameBrandLocked,
		},
		{
			name:    "change brand of an in-use device",
			start:   device.StateInUse,
			changes: device.Changes{Brand: ptr("Other")},
			wantErr: device.ErrNameBrandLocked,
		},
		{
			name:    "release and rename in one request",
			start:   device.StateInUse,
			changes: device.Changes{Name: ptr("Pixel 10"), State: ptr(device.StateAvailable)},
			wantErr: device.ErrNameBrandLocked,
		},
		{
			name:         "full update of an in-use device with current name and brand",
			start:        device.StateInUse,
			changes:      device.Changes{Name: ptr(" Pixel 9 "), Brand: ptr("Google"), State: ptr(device.StateInUse)},
			wantModified: false,
			wantName:     "Pixel 9",
			wantBrand:    "Google",
		},
		{
			name:         "same creation time is accepted",
			start:        device.StateAvailable,
			changes:      device.Changes{CreationTime: ptr(created.In(time.FixedZone("x", 3600)))},
			wantModified: false,
			wantName:     "Pixel 9",
			wantBrand:    "Google",
		},
		{
			name:       "creation time cannot change",
			start:      device.StateAvailable,
			changes:    device.Changes{CreationTime: ptr(later)},
			wantFields: []string{"creation_time:read_only"},
		},
		{
			name:       "invalid values are reported together",
			start:      device.StateInUse,
			changes:    device.Changes{Name: ptr(""), State: ptr(device.State("lost"))},
			wantFields: []string{"name:required", "state:invalid_value"},
		},
		{
			name:         "empty changes",
			start:        device.StateAvailable,
			changes:      device.Changes{},
			wantModified: false,
			wantName:     "Pixel 9",
			wantBrand:    "Google",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := mustNew(t, tt.start)
			before := d

			upd, err := d.Apply(tt.changes, later)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantFields != nil:
				got := fieldCodes(t, err)
				if strings.Join(got, ",") != strings.Join(tt.wantFields, ",") {
					t.Fatalf("field errors = %v, want %v", got, tt.wantFields)
				}
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}

			if err != nil {
				if d != before {
					t.Errorf("device changed on error: %+v -> %+v", before, d)
				}
				return
			}

			if upd.Modified != tt.wantModified {
				t.Errorf("Modified = %v, want %v", upd.Modified, tt.wantModified)
			}
			if d.Name() != tt.wantName || d.Brand() != tt.wantBrand {
				t.Errorf("name/brand = %q/%q, want %q/%q", d.Name(), d.Brand(), tt.wantName, tt.wantBrand)
			}
			if !d.CreationTime().Equal(before.CreationTime()) {
				t.Error("creation time must never change")
			}

			if tt.wantTo == "" {
				if upd.StateChange != nil {
					t.Errorf("unexpected state change %+v", upd.StateChange)
				}
				return
			}
			sc := upd.StateChange
			if sc == nil {
				t.Fatal("expected a state change")
			}
			if sc.From != tt.wantFrom || sc.To != tt.wantTo || sc.DeviceID != d.ID() {
				t.Errorf("state change = %+v, want %s -> %s for %s", sc, tt.wantFrom, tt.wantTo, d.ID())
			}
			if !sc.OccurredAt.Equal(later.Truncate(time.Microsecond)) {
				t.Errorf("occurred at = %v, want %v", sc.OccurredAt, later)
			}
		})
	}
}

func TestChangesValidate(t *testing.T) {
	if err := (device.Changes{}).Validate(); err != nil {
		t.Errorf("empty changes: %v", err)
	}
	if err := (device.Changes{Name: ptr("ok"), Brand: ptr("ok"), State: ptr(device.StateInactive)}).Validate(); err != nil {
		t.Errorf("valid changes: %v", err)
	}
	err := device.Changes{Name: ptr(" "), Brand: ptr(strings.Repeat("b", 51)), State: ptr(device.State("x"))}.Validate()
	if got := strings.Join(fieldCodes(t, err), ","); got != "name:required,brand:too_long,state:invalid_value" {
		t.Errorf("field errors = %s", got)
	}
}

func TestInUseErrorsShareSentinel(t *testing.T) {
	for _, err := range []error{device.ErrNameBrandLocked, device.ErrDeleteInUse} {
		if !errors.Is(err, device.ErrInUse) {
			t.Errorf("%v does not wrap ErrInUse", err)
		}
	}
}

func TestEnsureDeletable(t *testing.T) {
	tests := []struct {
		state   device.State
		wantErr error
	}{
		{device.StateAvailable, nil},
		{device.StateInactive, nil},
		{device.StateInUse, device.ErrDeleteInUse},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			d := mustNew(t, tt.state)
			if err := d.EnsureDeletable(); !errors.Is(err, tt.wantErr) {
				t.Errorf("EnsureDeletable() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRestore(t *testing.T) {
	id := device.NewID()
	d := device.Restore(id, "Pixel", "Google", device.StateInactive, created, 7)

	if d.ID() != id || d.Name() != "Pixel" || d.Brand() != "Google" || d.State() != device.StateInactive {
		t.Errorf("restored fields mismatch: %+v", d)
	}
	if d.Version() != 7 || d.ETag() != `"7"` {
		t.Errorf("version = %d, etag = %s", d.Version(), d.ETag())
	}
	if !d.CreationTime().Equal(created.Truncate(time.Microsecond)) {
		t.Errorf("creation time = %v", d.CreationTime())
	}
}

func TestNewIDIsVersion7(t *testing.T) {
	a, b := device.NewID(), device.NewID()
	if a.Version() != 7 {
		t.Errorf("version = %d, want 7", a.Version())
	}
	if a == b {
		t.Error("ids must be unique")
	}
}

func TestValidationErrorMessage(t *testing.T) {
	_, err := device.New(device.NewParams{ID: device.NewID(), Now: created})
	want := "invalid device: name must not be empty; brand must not be empty"
	if err == nil || err.Error() != want {
		t.Errorf("Error() = %v, want %q", err, want)
	}
}
