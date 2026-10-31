package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/platform/config"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// lookupFrom resolves variables from env, falling back to a valid auth
// setup so each test only states what it is about. Set a key to "" to
// make it missing.
func lookupFrom(env map[string]string) func(string) (string, bool) {
	base := map[string]string{
		"AUTH_ISSUER":       "https://issuer.test",
		"AUTH_AUDIENCE":     "devices-api",
		"AUTH_HS256_SECRET": testSecret,
	}
	return func(key string) (string, bool) {
		if v, ok := env[key]; ok {
			return v, true
		}
		v, ok := base[key]
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
	if !cfg.Database.AutoMigrate {
		t.Error("AutoMigrate should default to true")
	}
	if cfg.Auth.ClockSkew != 30*time.Second || cfg.Auth.JWKSRefresh != 15*time.Minute {
		t.Errorf("auth timings = %v/%v, want 30s/15m", cfg.Auth.ClockSkew, cfg.Auth.JWKSRefresh)
	}
	if cfg.Auth.HS256Secret != testSecret || cfg.Auth.Issuer != "https://issuer.test" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
	want := config.Idempotency{TTL: 24 * time.Hour, LockTimeout: time.Minute, PurgeInterval: 15 * time.Minute}
	if cfg.Idempotency != want {
		t.Errorf("idempotency = %+v, want %+v", cfg.Idempotency, want)
	}
}

func TestFromLookupJWKS(t *testing.T) {
	cfg, err := config.FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL":      "x",
		"AUTH_HS256_SECRET": "",
		"AUTH_JWKS_URL":     "https://idp.test/.well-known/jwks.json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Auth.JWKSURL != "https://idp.test/.well-known/jwks.json" || cfg.Auth.HS256Secret != "" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
}

func TestFromLookupOverrides(t *testing.T) {
	cfg, err := config.FromLookup(lookupFrom(map[string]string{
		"DATABASE_URL":          "postgres://db/devices",
		"HTTP_ADDR":             " :9090 ",
		"HTTP_READ_TIMEOUT":     "3s",
		"DATABASE_MAX_CONNS":    "25",
		"DATABASE_MIN_CONNS":    "5",
		"LOG_LEVEL":             "debug",
		"LOG_FORMAT":            "TEXT",
		"SHUTDOWN_TIMEOUT":      "1m",
		"DATABASE_AUTO_MIGRATE": "false",
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
	if cfg.Database.AutoMigrate {
		t.Error("AutoMigrate should be disabled")
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
			name:    "malformed boolean",
			env:     map[string]string{"DATABASE_URL": "x", "DATABASE_AUTO_MIGRATE": "maybe"},
			wantErr: "DATABASE_AUTO_MIGRATE must be true or false",
		},
		{
			name:    "missing issuer",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_ISSUER": ""},
			wantErr: "AUTH_ISSUER is required",
		},
		{
			name:    "no verification method",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_HS256_SECRET": ""},
			wantErr: "AUTH_HS256_SECRET or AUTH_JWKS_URL is required",
		},
		{
			name:    "both verification methods",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_JWKS_URL": "https://idp.test/jwks"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "short secret",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_HS256_SECRET": "short"},
			wantErr: "AUTH_HS256_SECRET must be at least 32 bytes long",
		},
		{
			name:    "relative jwks url",
			env:     map[string]string{"DATABASE_URL": "x", "AUTH_HS256_SECRET": "", "AUTH_JWKS_URL": "/jwks.json"},
			wantErr: "AUTH_JWKS_URL must be an absolute http or https URL",
		},
		{
			name:    "lock timeout shorter than write timeout",
			env:     map[string]string{"DATABASE_URL": "x", "IDEMPOTENCY_LOCK_TIMEOUT": "10s", "HTTP_WRITE_TIMEOUT": "20s"},
			wantErr: "IDEMPOTENCY_LOCK_TIMEOUT must be greater than HTTP_WRITE_TIMEOUT",
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
