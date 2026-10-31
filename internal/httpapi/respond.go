// Package httpapi exposes the service over HTTP: routing, middleware,
// request decoding and response encoding.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// Problem is an RFC 9457 problem details document.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
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
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	p := Problem{
		Type:      typ,
		Title:     title,
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		RequestID: RequestIDFrom(r.Context()),
	}
	if err := json.NewEncoder(w).Encode(p); err != nil {
		slog.Default().WarnContext(r.Context(), "failed to encode problem body", slog.Any("error", err))
	}
}
