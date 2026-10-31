package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/platform/logger"
)

func TestNewJSON(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(config.Log{Level: slog.LevelInfo, Format: config.LogFormatJSON}, &buf, "1.2.3")

	log.Debug("hidden")
	log.Info("service started", slog.String("addr", ":8080"))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (debug must be filtered): %q", len(lines), buf.String())
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	want := map[string]any{
		"msg":     "service started",
		"service": logger.ServiceName,
		"version": "1.2.3",
		"addr":    ":8080",
	}
	for k, v := range want {
		if record[k] != v {
			t.Errorf("%s = %v, want %v", k, record[k], v)
		}
	}
}

func TestNewText(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(config.Log{Level: slog.LevelDebug, Format: config.LogFormatText}, &buf, "dev")

	log.Debug("cache warmed")

	out := buf.String()
	for _, want := range []string{"level=DEBUG", `msg="cache warmed"`, "service=devices-api", "version=dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}
