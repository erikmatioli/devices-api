package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/platform/config"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestFromLookupDefaults(t *testing.T) {
	cfg, err := config.FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL": "postgres://localhost/devices",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.HTTP.WriteTimeout != 15*time.Second {
		t.Errorf("HTTP.WriteTimeout = %v, want 15s", cfg.HTTP.WriteTimeout)
	}
	if cfg.Database.MaxConns != 10 || cfg.Database.MinConns != 2 {
		t.Errorf("pool sizes = %d/%d, want 10/2", cfg.Database.MaxConns, cfg.Database.MinConns)
	}
	if cfg.Log.Level != slog.LevelInfo || cfg.Log.Format != config.LogFormatJSON {
		t.Errorf("log = %v/%s, want INFO/json", cfg.Log.Level, cfg.Log.Format)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestFromLookupOverrides(t *testing.T) {
	cfg, err := config.FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL":       "postgres://db/devices",
		"HTTP_ADDR":          " :9090 ",
		"HTTP_READ_TIMEOUT":  "3s",
		"DATABASE_MAX_CONNS": "25",
		"DATABASE_MIN_CONNS": "5",
		"LOG_LEVEL":          "debug",
		"LOG_FORMAT":         "TEXT",
		"SHUTDOWN_TIMEOUT":   "1m",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HTTP.Addr != ":9090" {
		t.Errorf("HTTP.Addr = %q, want trimmed :9090", cfg.HTTP.Addr)
	}
	if cfg.HTTP.ReadTimeout != 3*time.Second {
		t.Errorf("HTTP.ReadTimeout = %v, want 3s", cfg.HTTP.ReadTimeout)
	}
	if cfg.Database.MaxConns != 25 || cfg.Database.MinConns != 5 {
		t.Errorf("pool sizes = %d/%d, want 25/5", cfg.Database.MaxConns, cfg.Database.MinConns)
	}
	if cfg.Log.Level != slog.LevelDebug || cfg.Log.Format != config.LogFormatText {
		t.Errorf("log = %v/%s, want DEBUG/text", cfg.Log.Level, cfg.Log.Format)
	}
	if cfg.ShutdownTimeout != time.Minute {
		t.Errorf("ShutdownTimeout = %v, want 1m", cfg.ShutdownTimeout)
	}
}

func TestFromLookupInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantErr: "DATABASE_URL is required",
		},
		{
			name:    "blank database url",
			env:     map[string]string{"DATABASE_URL": "   "},
			wantErr: "DATABASE_URL is required",
		},
		{
			name:    "malformed duration",
			env:     map[string]string{"DATABASE_URL": "x", "HTTP_IDLE_TIMEOUT": "soon"},
			wantErr: "HTTP_IDLE_TIMEOUT must be a positive duration",
		},
		{
			name:    "negative duration",
			env:     map[string]string{"DATABASE_URL": "x", "SHUTDOWN_TIMEOUT": "-1s"},
			wantErr: "SHUTDOWN_TIMEOUT must be a positive duration",
		},
		{
			name:    "zero pool size",
			env:     map[string]string{"DATABASE_URL": "x", "DATABASE_MAX_CONNS": "0"},
			wantErr: "DATABASE_MAX_CONNS must be a positive integer",
		},
		{
			name:    "min above max",
			env:     map[string]string{"DATABASE_URL": "x", "DATABASE_MAX_CONNS": "2", "DATABASE_MIN_CONNS": "3"},
			wantErr: "DATABASE_MIN_CONNS must not exceed DATABASE_MAX_CONNS",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "verbose"},
			wantErr: "LOG_LEVEL must be one of",
		},
		{
			name:    "unknown log format",
			env:     map[string]string{"DATABASE_URL": "x", "LOG_FORMAT": "xml"},
			wantErr: "LOG_FORMAT must be one of json, text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.FromLookup(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestFromLookupReportsAllErrors(t *testing.T) {
	_, err := config.FromLookup(lookupFrom(map[string]string{
		"HTTP_READ_TIMEOUT": "fast",
		"LOG_FORMAT":        "yaml",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"DATABASE_URL", "HTTP_READ_TIMEOUT", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
