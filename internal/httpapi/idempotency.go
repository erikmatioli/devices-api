package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/idempotency"
)

// Idempotency headers.
const (
	HeaderIdempotencyKey      = "Idempotency-Key"
	HeaderIdempotentReplayed  = "Idempotent-Replayed"
	maxIdempotencyKeyLength   = 255
	problemKeyReused          = "/problems/idempotency-key-reused"
	problemKeyInProgress      = "/problems/idempotency-key-in-progress"
	idempotencyRetryAfterSecs = "1"
)

// idempotent makes a handler safe to retry when the client sends an
// Idempotency-Key header:
//
//   - first request: run the handler and record its response;
//   - same key and same request, finished: replay the recorded response;
//   - same key with a different request: 422;
//   - same key while the first request is still running: 409.
//
// Server errors (5xx) are not recorded, so the client can retry them. The
// key is scoped to the token subject, so it must run after authenticate.
func idempotent(store idempotency.Store, logger *slog.Logger, metrics Metrics) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header.Values(HeaderIdempotencyKey)
			if len(values) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if len(values) > 1 || !validIdempotencyKey(values[0]) {
				writeValidationProblem(w, r, &device.ValidationError{Fields: []device.FieldError{{
					Field:   HeaderIdempotencyKey,
					Code:    device.CodeInvalidValue,
					Message: "must be a single value of 1 to 255 printable ASCII characters",
				}}})
				return
			}

			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					writeProblem(w, r, http.StatusRequestEntityTooLarge, problemTooLarge, "Payload too large",
						"the request body must not exceed 1 MB")
					return
				}
				writeProblem(w, r, http.StatusBadRequest, problemMalformed, "Malformed request", "the request body could not be read")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			principal, _ := auth.PrincipalFrom(r.Context())
			key := idempotency.Key{Subject: principal.Subject, Value: values[0]}
			res, err := store.Begin(r.Context(), key, idempotency.Fingerprint(r.Method, r.URL.Path, body))
			if err != nil {
				writeError(w, r, logger, err)
				return
			}

			metrics.IdempotencyOutcome(r.Context(), outcomeName(res.Outcome))
			switch res.Outcome {
			case idempotency.Replay:
				replay(w, res.Response)
				return
			case idempotency.Mismatch:
				writeProblem(w, r, http.StatusUnprocessableEntity, problemKeyReused, "Idempotency key reused",
					"this Idempotency-Key was already used with a different request")
				return
			case idempotency.InProgress:
				w.Header().Set("Retry-After", idempotencyRetryAfterSecs)
				writeProblem(w, r, http.StatusConflict, problemKeyInProgress, "Request in progress",
					"a request with this Idempotency-Key is still being processed; retry shortly")
				return
			}

			// The outcome is recorded even if the client disconnects, so its
			// retry gets the answer instead of running the request again.
			storeCtx := context.WithoutCancel(r.Context())
			buf := &responseBuffer{header: make(http.Header)}
			finished := false
			defer func() {
				if !finished {
					// The handler panicked: free the key so a retry can run.
					if err := store.Release(storeCtx, key); err != nil {
						logger.ErrorContext(storeCtx, "failed to release idempotency key", slog.Any("error", err))
					}
				}
			}()

			next.ServeHTTP(buf, r)
			finished = true

			if buf.status >= http.StatusInternalServerError {
				if err := store.Release(storeCtx, key); err != nil {
					logger.ErrorContext(storeCtx, "failed to release idempotency key", slog.Any("error", err))
				}
			} else if err := store.Complete(storeCtx, key, idempotency.Response{
				Status: buf.status,
				Header: idempotency.KeepHeaders(buf.header),
				Body:   buf.body.Bytes(),
			}); err != nil {
				logger.ErrorContext(storeCtx, "failed to record idempotent response",
					slog.String("request_id", RequestIDFrom(r.Context())), slog.Any("error", err))
			}
			buf.flushTo(w)
		})
	}
}

func outcomeName(o idempotency.Outcome) string {
	switch o {
	case idempotency.Started:
		return "started"
	case idempotency.Replay:
		return "replayed"
	case idempotency.Mismatch:
		return "mismatch"
	case idempotency.InProgress:
		return "in_progress"
	default:
		return "unknown"
	}
}

func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > maxIdempotencyKeyLength {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

func replay(w http.ResponseWriter, resp *idempotency.Response) {
	for name, values := range resp.Header {
		w.Header()[name] = values
	}
	w.Header().Set(HeaderIdempotentReplayed, "true")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// responseBuffer holds a response in memory until it has been recorded.
type responseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *responseBuffer) Header() http.Header { return b.header }

func (b *responseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *responseBuffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *responseBuffer) flushTo(w http.ResponseWriter) {
	for name, values := range b.header {
		w.Header()[name] = values
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
