package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/erikmatioli/devices-api/internal/device"
)

// maxBodyBytes caps request bodies; device payloads are a few hundred bytes.
const maxBodyBytes = 1 << 20

// Accepted request media types.
const (
	mediaJSON       = "application/json"
	mediaMergePatch = "application/merge-patch+json"
)

// readObject decodes the request body as a single JSON object and returns
// its members undecoded. It writes the error response itself and returns
// false when the body cannot be used.
func readObject(w http.ResponseWriter, r *http.Request, accepted ...string) (map[string]json.RawMessage, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !slices.Contains(accepted, mediaType) {
		writeProblem(w, r, http.StatusUnsupportedMediaType, problemMediaType, "Unsupported media type",
			"Content-Type must be one of: "+strings.Join(accepted, ", "))
		return nil, false
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		var tooLarge *http.MaxBytesError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooLarge):
			writeProblem(w, r, http.StatusRequestEntityTooLarge, problemTooLarge, "Payload too large",
				"the request body must not exceed 1 MB")
		case errors.Is(err, io.EOF):
			writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request", "the request body is empty")
		case errors.As(err, &typeErr):
			writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request", "the request body must be a JSON object")
		default:
			writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request", "the request body is not valid JSON")
		}
		return nil, false
	}
	if fields == nil {
		writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request", "the request body must be a JSON object")
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request",
			"the request body must contain a single JSON object")
		return nil, false
	}
	return fields, true
}

// fieldReader extracts typed members from a decoded object and collects
// every problem, so a single response lists all invalid fields.
type fieldReader struct {
	fields map[string]json.RawMessage
	known  map[string]bool
	errs   []device.FieldError
}

func newFieldReader(fields map[string]json.RawMessage) *fieldReader {
	return &fieldReader{fields: fields, known: make(map[string]bool)}
}

func (f *fieldReader) fail(field, code, msg string) {
	f.errs = append(f.errs, device.FieldError{Field: field, Code: code, Message: msg})
}

// raw returns the member, reporting missing required members and nulls.
func (f *fieldReader) raw(name string, required bool) (json.RawMessage, bool) {
	f.known[name] = true
	v, ok := f.fields[name]
	if !ok {
		if required {
			f.fail(name, device.CodeRequired, "is required")
		}
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		f.fail(name, device.CodeInvalidValue, "must not be null")
		return nil, false
	}
	return v, true
}

func (f *fieldReader) str(name string, required bool) *string {
	v, ok := f.raw(name, required)
	if !ok {
		return nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		f.fail(name, codeInvalidType, "must be a string")
		return nil
	}
	return &s
}

func (f *fieldReader) state(name string, required bool) *device.State {
	s := f.str(name, required)
	if s == nil {
		return nil
	}
	st, err := device.ParseState(*s)
	if err != nil {
		f.fail(name, device.CodeInvalidValue, "must be one of available, in-use, inactive")
		return nil
	}
	return &st
}

func (f *fieldReader) timestamp(name string) *time.Time {
	s := f.str(name, false)
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		f.fail(name, device.CodeInvalidValue, "must be an RFC 3339 timestamp")
		return nil
	}
	return &t
}

// readOnly rejects a member that clients may not set, unless it carries the
// current value (allowed is nil when no value is acceptable).
func (f *fieldReader) readOnly(name string, allowed func(json.RawMessage) bool) {
	f.known[name] = true
	v, ok := f.fields[name]
	if ok && (allowed == nil || !allowed(v)) {
		f.fail(name, device.CodeReadOnly, "cannot be set")
	}
}

// include adds the field errors of a domain validation error.
func (f *fieldReader) include(err error) {
	var verr *device.ValidationError
	if errors.As(err, &verr) {
		f.errs = append(f.errs, verr.Fields...)
	}
}

// done reports unknown members and returns the collected errors.
func (f *fieldReader) done() error {
	var unknown []string
	for name := range f.fields {
		if !f.known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		f.fail(name, codeUnknownField, "is not a known field")
	}
	if len(f.errs) == 0 {
		return nil
	}
	return &device.ValidationError{Fields: f.errs}
}

// Field error codes produced only by the transport layer.
const (
	codeInvalidType  = "invalid_type"
	codeUnknownField = "unknown_field"
)
