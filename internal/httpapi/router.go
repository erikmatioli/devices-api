package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

// Dependencies groups what the router needs from the composition root.
type Dependencies struct {
	Logger *slog.Logger
	// Readiness lists the dependency checks run by GET /readyz, by name.
	Readiness map[string]CheckFunc
}

// NewRouter builds the HTTP handler with every route and the shared
// middleware chain.
func NewRouter(deps Dependencies) http.Handler {
	health := newHealthHandler(deps.Logger, deps.Readiness, readinessTimeout)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.live)
	mux.HandleFunc("GET /readyz", health.ready)

	probes := map[string]bool{"/healthz": true, "/readyz": true}
	return chain(mux,
		requestID,
		accessLog(deps.Logger, probes),
		recoverer(deps.Logger),
	)
}
