package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
)

func TestCursorRoundTrip(t *testing.T) {
	want := app.Cursor{
		CreationTime: time.Date(2026, 10, 31, 14, 5, 9, 123456000, time.UTC),
		ID:           uuid.MustParse("0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12"),
	}

	got, err := decodeCursor(encodeCursor(want))
	if err != nil {
		t.Fatalf("decodeCursor: %v", err)
	}
	if !got.CreationTime.Equal(want.CreationTime) || got.ID != want.ID {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestCursorBeforeEpoch(t *testing.T) {
	want := app.Cursor{CreationTime: time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.New()}
	got, err := decodeCursor(encodeCursor(want))
	if err != nil || !got.CreationTime.Equal(want.CreationTime) {
		t.Errorf("round trip = %+v, %v", got, err)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "!!!", "c2hvcnQ", encodeCursor(app.Cursor{}) + "AA"} {
		if _, err := decodeCursor(s); err == nil {
			t.Errorf("decodeCursor(%q) succeeded", s)
		}
	}
}
