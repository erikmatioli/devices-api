package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/idempotency"
)

const readinessTimeout = 2 * time.Second

// Dependencies groups what the router needs from the composition root.
type Dependencies struct {
	Logger   *slog.Logger
	Devices  DeviceService
	Verifier TokenVerifier
	// Idempotency records responses of requests sent with an
	// Idempotency-Key. When nil, the header is ignored.
	Idempotency idempotency.Store
	// Readiness lists the dependency checks run by GET /readyz, by name.
	Readiness map[string]CheckFunc
}

// NewRouter builds the HTTP handler with every route and the shared
// middleware chain.
func NewRouter(deps Dependencies) http.Handler {
	health := newHealthHandler(deps.Logger, deps.Readiness, readinessTimeout)
	devices := &deviceHandler{svc: deps.Devices, logger: deps.Logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.live)
	mux.HandleFunc("GET /readyz", health.ready)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)
	mux.HandleFunc("GET /docs", serveDocs)

	authn := authenticate(deps.Verifier, deps.Logger)
	protect := func(scope string, h http.HandlerFunc) http.Handler {
		return authn(requireScope(scope, h))
	}
	create := http.Handler(http.HandlerFunc(devices.create))
	if deps.Idempotency != nil {
		create = idempotent(deps.Idempotency, deps.Logger)(create)
	}
	mux.Handle("POST /v1/devices", authn(requireScope(auth.ScopeWrite, create)))
	mux.Handle("GET /v1/devices", protect(auth.ScopeRead, devices.list))
	mux.Handle("GET /v1/devices/{id}", protect(auth.ScopeRead, devices.get))
	mux.Handle("PUT /v1/devices/{id}", protect(auth.ScopeWrite, devices.replace))
	mux.Handle("PATCH /v1/devices/{id}", protect(auth.ScopeWrite, devices.patch))
	mux.Handle("DELETE /v1/devices/{id}", protect(auth.ScopeDelete, devices.delete))

	probes := map[string]bool{"/healthz": true, "/readyz": true}
	return chain(problemFallback(mux),
		requestID,
		accessLog(deps.Logger, probes),
		recoverer(deps.Logger),
		secureHeaders,
	)
}

// problemFallback answers unknown paths and unsupported methods with problem
// documents instead of the plain-text bodies of http.ServeMux, keeping the
// Allow header the mux computes for 405 responses.
func problemFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}

		capture := &headerCapture{header: make(http.Header)}
		mux.ServeHTTP(capture, r)
		switch capture.status {
		case http.StatusNotFound:
			writeProblem(w, r, http.StatusNotFound, problemNotFound, "Not found", "no route matches "+r.URL.Path)
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", capture.header.Get("Allow"))
			writeProblem(w, r, http.StatusMethodNotAllowed, problemMethod, "Method not allowed",
				r.Method+" is not supported; use "+strings.ReplaceAll(capture.header.Get("Allow"), ",", ", "))
		default:
			// Redirects and other mux responses pass through unchanged.
			for k, v := range capture.header {
				w.Header()[k] = v
			}
			w.WriteHeader(capture.status)
		}
	})
}

// headerCapture records the status and headers the mux writes, discarding
// the body.
type headerCapture struct {
	header http.Header
	status int
}

func (c *headerCapture) Header() http.Header { return c.header }

func (c *headerCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return len(b), nil
}

func (c *headerCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// secureHeaders sets headers that are safe for every response.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
