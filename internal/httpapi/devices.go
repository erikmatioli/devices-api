package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
)

// DeviceService is the set of use cases the device handlers call.
type DeviceService interface {
	Create(ctx context.Context, in app.CreateInput) (device.Device, error)
	Get(ctx context.Context, id uuid.UUID) (device.Device, error)
	List(ctx context.Context, f app.ListFilter) (app.Page, error)
	Update(ctx context.Context, id uuid.UUID, changes device.Changes, expectedVersion *int64) (device.Device, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type deviceHandler struct {
	svc    DeviceService
	logger *slog.Logger
}

func (h *deviceHandler) create(w http.ResponseWriter, r *http.Request) {
	fields, ok := readObject(w, r, mediaJSON)
	if !ok {
		return
	}
	fr := newFieldReader(fields)
	name := fr.str("name", true)
	brand := fr.str("brand", true)
	state := fr.state("state", false)
	fr.readOnly("id", nil)
	fr.readOnly("creation_time", nil)
	fr.include(device.Changes{Name: name, Brand: brand, State: state}.Validate())
	if err := fr.done(); err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	in := app.CreateInput{Name: *name, Brand: *brand}
	if state != nil {
		in.State = *state
	}
	d, err := h.svc.Create(r.Context(), in)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Location", "/v1/devices/"+d.ID().String())
	writeDevice(w, r, http.StatusCreated, d)
}

func (h *deviceHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	if r.Header.Get("If-None-Match") == d.ETag() {
		w.Header().Set("ETag", d.ETag())
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeDevice(w, r, http.StatusOK, d)
}

func (h *deviceHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := app.ListFilter{Limit: app.DefaultPageSize}
	var errs []device.FieldError
	fail := func(field, code, msg string) {
		errs = append(errs, device.FieldError{Field: field, Code: code, Message: msg})
	}

	if v := strings.TrimSpace(q.Get("brand")); v != "" {
		if utf8.RuneCountInString(v) > device.MaxBrandLength {
			fail("brand", device.CodeTooLong, "must be at most "+strconv.Itoa(device.MaxBrandLength)+" characters")
		}
		f.Brand = v
	}
	if v := q.Get("state"); v != "" {
		st, err := device.ParseState(v)
		if err != nil {
			fail("state", device.CodeInvalidValue, "must be one of available, in-use, inactive")
		}
		f.State = st
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > app.MaxPageSize {
			fail("limit", device.CodeInvalidValue, "must be an integer between 1 and "+strconv.Itoa(app.MaxPageSize))
		}
		f.Limit = n
	}
	if v := q.Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			fail("cursor", device.CodeInvalidValue, "is not a valid cursor")
		}
		f.After = &c
	}
	if len(errs) > 0 {
		writeValidationProblem(w, r, &device.ValidationError{Fields: errs})
		return
	}

	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, toDeviceListResponse(page))
}

// replace handles PUT: name, brand and state are required. id and
// creation_time may be sent back only with their current values.
func (h *deviceHandler) replace(w http.ResponseWriter, r *http.Request) {
	h.update(w, r, true, mediaJSON)
}

// patch handles PATCH with JSON Merge Patch semantics. Since no field is
// nullable, null is rejected instead of meaning "remove".
func (h *deviceHandler) patch(w http.ResponseWriter, r *http.Request) {
	h.update(w, r, false, mediaMergePatch, mediaJSON)
}

func (h *deviceHandler) update(w http.ResponseWriter, r *http.Request, full bool, media ...string) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	fields, ok := readObject(w, r, media...)
	if !ok {
		return
	}

	fr := newFieldReader(fields)
	changes := device.Changes{
		Name:         fr.str("name", full),
		Brand:        fr.str("brand", full),
		State:        fr.state("state", full),
		CreationTime: fr.timestamp("creation_time"),
	}
	fr.readOnly("id", func(v json.RawMessage) bool {
		var s string
		return json.Unmarshal(v, &s) == nil && s == id.String()
	})
	fr.include(changes.Validate())
	if err := fr.done(); err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	d, err := h.svc.Update(r.Context(), id, changes, expected)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeDevice(w, r, http.StatusOK, d)
}

func (h *deviceHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDevice(w http.ResponseWriter, r *http.Request, status int, d device.Device) {
	w.Header().Set("ETag", d.ETag())
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(r.Context(), w, status, toDeviceResponse(d))
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeValidationProblem(w, r, &device.ValidationError{Fields: []device.FieldError{
			{Field: "id", Code: device.CodeInvalidValue, Message: "must be a UUID"},
		}})
		return uuid.Nil, false
	}
	return id, true
}

// ifMatchVersion reads an optional If-Match header holding a strong ETag
// produced by this API. "*" means any version. A value that cannot match
// any version fails the precondition straight away.
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" || v == "*" {
		return nil, true
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if n, err := strconv.ParseInt(v[1:len(v)-1], 10, 64); err == nil {
			return &n, true
		}
	}
	writeProblem(w, r, http.StatusPreconditionFailed, problemVersionMismatch, "Precondition failed",
		"If-Match must be an ETag returned by this API")
	return nil, false
}
