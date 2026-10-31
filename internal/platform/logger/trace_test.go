package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/platform/logger"
)

func TestTraceCorrelation(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(config.Log{Level: slog.LevelInfo, Format: config.LogFormatJSON}, &buf, "dev").
		With(slog.String("component", "test")).WithGroup("details")

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	log.InfoContext(ctx, "with span")
	log.InfoContext(context.Background(), "without span")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	var with, without map[string]any
	if err := json.Unmarshal(lines[0], &with); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &without); err != nil {
		t.Fatal(err)
	}

	// Attributes added after WithGroup land inside the group.
	details, _ := with["details"].(map[string]any)
	if details["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || details["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("record = %v", with)
	}
	if with["component"] != "test" {
		t.Errorf("attributes from With must be kept: %v", with)
	}
	if _, ok := without["details"]; ok {
		t.Errorf("records without a span must not carry trace ids: %v", without)
	}
}
