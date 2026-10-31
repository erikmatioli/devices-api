package httpapi

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
)

type deviceResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Brand        string `json:"brand"`
	State        string `json:"state"`
	CreationTime string `json:"creation_time"`
}

type deviceListResponse struct {
	Data       []deviceResponse `json:"data"`
	NextCursor *string          `json:"next_cursor"`
}

func toDeviceResponse(d device.Device) deviceResponse {
	return deviceResponse{
		ID:           d.ID().String(),
		Name:         d.Name(),
		Brand:        d.Brand(),
		State:        string(d.State()),
		CreationTime: d.CreationTime().Format(time.RFC3339Nano),
	}
}

func toDeviceListResponse(p app.Page) deviceListResponse {
	resp := deviceListResponse{Data: make([]deviceResponse, len(p.Devices))}
	for i, d := range p.Devices {
		resp.Data[i] = toDeviceResponse(d)
	}
	if p.Next != nil {
		c := encodeCursor(*p.Next)
		resp.NextCursor = &c
	}
	return resp
}

// Cursors are 24 bytes: the creation time in Unix microseconds followed by
// the device id, encoded as unpadded base64url. Clients treat them as opaque.
const cursorBytes = 8 + 16

var errInvalidCursor = errors.New("invalid cursor")

func encodeCursor(c app.Cursor) string {
	var b [cursorBytes]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.CreationTime.UnixMicro())) //nolint:gosec // round-trips through int64
	copy(b[8:], c.ID[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func decodeCursor(s string) (app.Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != cursorBytes {
		return app.Cursor{}, errInvalidCursor
	}
	id, err := uuid.FromBytes(b[8:])
	if err != nil {
		return app.Cursor{}, errInvalidCursor
	}
	micros := int64(binary.BigEndian.Uint64(b[:8])) //nolint:gosec // inverse of encodeCursor
	return app.Cursor{CreationTime: time.UnixMicro(micros).UTC(), ID: id}, nil
}
