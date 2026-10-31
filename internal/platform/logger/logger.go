// Package logger builds the structured logger shared by the whole service.
package logger

import (
	"io"
	"log/slog"

	"github.com/erikmatioli/devices-api/internal/platform/config"
)

// ServiceName identifies this service in logs and telemetry.
const ServiceName = "devices-api"

// New returns a logger that writes to w in the configured format. Every
// record carries the service name and version so logs from several
// deployments can be told apart, and records logged with a context that
// holds a span carry its trace_id and span_id.
func New(cfg config.Log, w io.Writer, version string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}

	var h slog.Handler
	if cfg.Format == config.LogFormatText {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(traceHandler{h}).With(
		slog.String("service", ServiceName),
		slog.String("version", version),
	)
}
