package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/events"
)

// EventHub is the subscription side of the event fan-out.
type EventHub interface {
	Subscribe() (*events.Subscription, error)
	Unsubscribe(*events.Subscription)
}

const (
	defaultKeepAlive    = 15 * time.Second
	sseRetryMillis      = 3000
	problemStreamsLimit = "/problems/too-many-subscribers"
)

type eventsHandler struct {
	hub       EventHub
	logger    *slog.Logger
	keepAlive time.Duration
}

// stream serves GET /v1/devices/events as Server-Sent Events. An optional
// device_id query parameter limits the stream to one device.
func (h *eventsHandler) stream(w http.ResponseWriter, r *http.Request) {
	var only uuid.UUID
	if v := r.URL.Query().Get("device_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeValidationProblem(w, r, &device.ValidationError{Fields: []device.FieldError{
				{Field: "device_id", Code: device.CodeInvalidValue, Message: "must be a UUID"},
			}})
			return
		}
		only = id
	}

	sub, err := h.hub.Subscribe()
	if err != nil {
		if errors.Is(err, events.ErrTooManySubscribers) {
			w.Header().Set("Retry-After", "5")
		}
		writeProblem(w, r, http.StatusServiceUnavailable, problemStreamsLimit, "Service unavailable",
			"the event stream cannot accept more subscribers right now")
		return
	}
	defer h.hub.Unsubscribe(sub)

	// The stream outlives the server write timeout on purpose.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.logger.WarnContext(r.Context(), "cannot clear write deadline for event stream", slog.Any("error", err))
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)
	w.WriteHeader(http.StatusOK)

	if _, err := fmt.Fprintf(w, "retry: %d\n\n", sseRetryMillis); err != nil {
		return
	}
	if rc.Flush() != nil {
		return
	}

	ticker := time.NewTicker(h.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case e, ok := <-sub.C:
			if !ok {
				if sub.Dropped() {
					h.logger.InfoContext(r.Context(), "event subscriber dropped for being too slow",
						slog.String("request_id", RequestIDFrom(r.Context())))
				}
				return
			}
			if only != uuid.Nil && e.DeviceID != only {
				continue
			}
			if err := writeEvent(w, e); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, e events.StateChanged) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.EventID, events.TypeStateChanged, data)
	return err
}
