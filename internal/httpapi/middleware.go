package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// HeaderRequestID carries the request correlation id in both directions.
const HeaderRequestID = "X-Request-ID"

const maxRequestIDLength = 128

type middleware func(http.Handler) http.Handler

// chain wraps h so that the first middleware is the outermost one.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type requestIDKey struct{}

// RequestIDFrom returns the request id stored in ctx, or an empty string.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID reuses a well-formed X-Request-ID from the caller, or generates
// a new one, and echoes it in the response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error.
	return hex.EncodeToString(b[:])
}

// requestMeta collects facts learned deeper in the handler chain, such as the
// authenticated subject, so the access log can report them.
type requestMeta struct {
	subject string
}

type requestMetaKey struct{}

func requestMetaFrom(ctx context.Context) *requestMeta {
	m, _ := ctx.Value(requestMetaKey{}).(*requestMeta)
	return m
}

// statusRecorder captures the status code and body size of a response.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach Flush and deadlines of the
// underlying writer, which streaming responses rely on.
func (rec *statusRecorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}

// accessLog writes one record per request. Probe traffic is logged at debug
// level to keep the log volume useful.
func accessLog(logger *slog.Logger, quiet map[string]bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			meta := &requestMeta{}

			// The mux records the matched pattern on the request it receives,
			// so keep a reference to that request for the log record.
			inner := r.WithContext(context.WithValue(r.Context(), requestMetaKey{}, meta))
			next.ServeHTTP(rec, inner)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case rec.status >= http.StatusInternalServerError:
				level = slog.LevelError
			case quiet[r.URL.Path]:
				level = slog.LevelDebug
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", inner.Pattern),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				slog.String("subject", meta.subject),
				slog.String("remote_addr", r.RemoteAddr),
				slog.String("user_agent", r.UserAgent()),
			)
		})
	}
}

// recoverer turns a panic into a 500 response and logs the stack trace.
func recoverer(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverPanic(logger, w, r)
			next.ServeHTTP(w, r)
		})
	}
}

func recoverPanic(logger *slog.Logger, w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
		slog.String("request_id", RequestIDFrom(r.Context())),
		slog.Any("panic", v),
		slog.String("stack", string(debug.Stack())),
	)
	writeProblem(w, r, http.StatusInternalServerError,
		"/problems/internal-error", "Internal server error", "")
}
