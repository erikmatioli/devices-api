// Package httpapi exposes the service over HTTP: routing, middleware,
// request decoding and response encoding.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
)

// Problem type URIs, relative to the API root.
const (
	problemValidation      = "/problems/validation-error"
	problemMalformed       = "/problems/malformed-request"
	problemTooLarge        = "/problems/payload-too-large"
	problemMediaType       = "/problems/unsupported-media-type"
	problemNotFound        = "/problems/not-found"
	problemMethod          = "/problems/method-not-allowed"
	problemDeviceNotFound  = "/problems/device-not-found"
	problemDeviceInUse     = "/problems/device-in-use"
	problemVersionMismatch = "/problems/version-mismatch"
	problemInternal        = "/problems/internal-error"
)

// Problem is an RFC 9457 problem details document.
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
}

// FieldError is one entry of a validation problem.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent; the client sees a truncated body.
		slog.Default().WarnContext(ctx, "failed to encode response body", slog.Any("error", err))
	}
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, typ, title, detail string) {
	sendProblem(w, r, Problem{Type: typ, Title: title, Status: status, Detail: detail})
}

func sendProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.Instance = r.URL.Path
	p.RequestID = RequestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		slog.Default().WarnContext(r.Context(), "failed to encode problem body", slog.Any("error", err))
	}
}

func writeValidationProblem(w http.ResponseWriter, r *http.Request, verr *device.ValidationError) {
	fields := make([]FieldError, len(verr.Fields))
	for i, f := range verr.Fields {
		fields[i] = FieldError{Field: f.Field, Code: f.Code, Message: f.Message}
	}
	sendProblem(w, r, Problem{
		Type:   problemValidation,
		Title:  "Validation failed",
		Status: http.StatusUnprocessableEntity,
		Detail: "one or more fields are invalid",
		Errors: fields,
	})
}

// writeError maps errors from the use cases to HTTP responses. Anything not
// recognized is logged and answered with a generic 500, so internal details
// never reach the client.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var verr *device.ValidationError
	switch {
	case errors.As(err, &verr):
		writeValidationProblem(w, r, verr)
	case errors.Is(err, device.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, problemDeviceNotFound, "Device not found", "")
	case errors.Is(err, device.ErrInUse):
		writeProblem(w, r, http.StatusConflict, problemDeviceInUse, "Device in use", err.Error())
	case errors.Is(err, app.ErrVersionMismatch):
		writeProblem(w, r, http.StatusPreconditionFailed, problemVersionMismatch, "Precondition failed",
			"the device was modified by another request; fetch it again and retry")
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		// The client went away; there is nobody to answer.
		logger.DebugContext(r.Context(), "request canceled by client", slog.String("request_id", RequestIDFrom(r.Context())))
	default:
		logger.ErrorContext(r.Context(), "request failed",
			slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("error", err))
		writeProblem(w, r, http.StatusInternalServerError, problemInternal, "Internal server error", "")
	}
}
