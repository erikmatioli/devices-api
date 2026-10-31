package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// CheckFunc reports whether a dependency is usable.
type CheckFunc func(ctx context.Context) error

const (
	healthOK          = "ok"
	healthUnavailable = "unavailable"
)

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

type healthHandler struct {
	logger  *slog.Logger
	checks  map[string]CheckFunc
	names   []string
	timeout time.Duration
}

func newHealthHandler(logger *slog.Logger, checks map[string]CheckFunc, timeout time.Duration) *healthHandler {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)
	return &healthHandler{logger: logger, checks: checks, names: names, timeout: timeout}
}

// live answers the liveness probe. It only proves the process can serve
// requests and never touches dependencies, so a database outage does not
// make the orchestrator restart healthy containers.
func (h *healthHandler) live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(r.Context(), w, http.StatusOK, healthResponse{Status: healthOK})
}

// ready answers the readiness probe by running every dependency check.
func (h *healthHandler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	resp := healthResponse{Status: healthOK, Checks: make(map[string]string, len(h.names))}
	status := http.StatusOK
	for _, name := range h.names {
		if err := h.checks[name](ctx); err != nil {
			h.logger.WarnContext(ctx, "readiness check failed",
				slog.String("check", name), slog.Any("error", err))
			resp.Checks[name] = healthUnavailable
			resp.Status = healthUnavailable
			status = http.StatusServiceUnavailable
			continue
		}
		resp.Checks[name] = healthOK
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(ctx, w, status, resp)
}
