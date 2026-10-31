// Package idempotency records the outcome of requests sent with an
// Idempotency-Key header so retries get the original response instead of
// running the operation again.
package idempotency

import (
	"context"
	"crypto/sha256"
	"net/http"
)

// Key identifies a recorded request. Keys are scoped to the caller, so two
// clients choosing the same key never see each other's responses.
type Key struct {
	Subject string
	Value   string
}

// Response is a stored HTTP response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Outcome tells the caller what to do with a request.
type Outcome int

// Possible outcomes of Begin.
const (
	// Started means the key is new (or expired): run the request and then
	// call Complete or Release.
	Started Outcome = iota + 1
	// Replay means the request already finished; answer with Result.Response.
	Replay
	// Mismatch means the key was used before with a different request.
	Mismatch
	// InProgress means the first request with this key is still running.
	InProgress
)

// Result is returned by Begin.
type Result struct {
	Outcome  Outcome
	Response *Response
}

// Store persists idempotency records.
type Store interface {
	// Begin claims key for a request with the given fingerprint.
	Begin(ctx context.Context, key Key, fingerprint []byte) (Result, error)
	// Complete stores the response of a started request.
	Complete(ctx context.Context, key Key, resp Response) error
	// Release forgets a started request so it can be retried, typically
	// after a server error.
	Release(ctx context.Context, key Key) error
}

// Fingerprint identifies the request a key was first used with: method,
// path and the exact body bytes.
func Fingerprint(method, path string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// ReplayedHeaders lists the response headers kept with a record. Headers
// such as Date or X-Request-ID describe the new response and are not
// replayed.
var ReplayedHeaders = []string{"Content-Type", "Location", "ETag", "Cache-Control"}

// KeepHeaders returns the subset of h that is stored and replayed.
func KeepHeaders(h http.Header) http.Header {
	out := make(http.Header)
	for _, name := range ReplayedHeaders {
		if v := h.Values(name); len(v) > 0 {
			out[http.CanonicalHeaderKey(name)] = append([]string(nil), v...)
		}
	}
	return out
}
