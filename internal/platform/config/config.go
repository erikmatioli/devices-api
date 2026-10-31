// Package config loads and validates the service configuration from
// environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Log formats accepted by LOG_FORMAT.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// Config holds every setting the service reads at startup.
type Config struct {
	HTTP            HTTP
	Database        Database
	Log             Log
	ShutdownTimeout time.Duration
}

// HTTP configures the HTTP server.
type HTTP struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Database configures the PostgreSQL connection pool.
type Database struct {
	URL            string
	MaxConns       int32
	MinConns       int32
	ConnectTimeout time.Duration
}

// Log configures the structured logger.
type Log struct {
	Level  slog.Level
	Format string
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return FromLookup(os.LookupEnv)
}

// FromLookup builds the configuration using lookup to resolve variables.
// All invalid values are reported together so a misconfigured deployment
// can be fixed in one pass.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	p := parser{lookup: lookup}

	cfg := Config{
		HTTP: HTTP{
			Addr:              p.str("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: p.dur("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       p.dur("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:      p.dur("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:       p.dur("HTTP_IDLE_TIMEOUT", 60*time.Second),
		},
		Database: Database{
			URL:            p.required("DATABASE_URL"),
			MaxConns:       p.positiveInt32("DATABASE_MAX_CONNS", 10),
			MinConns:       p.positiveInt32("DATABASE_MIN_CONNS", 2),
			ConnectTimeout: p.dur("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
		},
		Log: Log{
			Level:  p.level("LOG_LEVEL", slog.LevelInfo),
			Format: p.oneOf("LOG_FORMAT", LogFormatJSON, LogFormatJSON, LogFormatText),
		},
		ShutdownTimeout: p.dur("SHUTDOWN_TIMEOUT", 15*time.Second),
	}

	if cfg.Database.MinConns > cfg.Database.MaxConns {
		p.fail("DATABASE_MIN_CONNS", "must not exceed DATABASE_MAX_CONNS")
	}

	if err := p.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parser reads typed values and accumulates validation errors.
type parser struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (p *parser) value(key string) (string, bool) {
	v, ok := p.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (p *parser) fail(key, msg string) {
	p.errs = append(p.errs, fmt.Errorf("%s %s", key, msg))
}

func (p *parser) err() error {
	if len(p.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %w", errors.Join(p.errs...))
}

func (p *parser) str(key, def string) string {
	if v, ok := p.value(key); ok {
		return v
	}
	return def
}

func (p *parser) required(key string) string {
	v, ok := p.value(key)
	if !ok {
		p.fail(key, "is required")
	}
	return v
}

func (p *parser) dur(key string, def time.Duration) time.Duration {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.fail(key, "must be a positive duration such as 5s or 1m")
		return def
	}
	return d
}

func (p *parser) positiveInt32(key string, def int32) int32 {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 {
		p.fail(key, "must be a positive integer")
		return def
	}
	return int32(n)
}

func (p *parser) level(key string, def slog.Level) slog.Level {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		p.fail(key, "must be one of debug, info, warn, error")
		return def
	}
	return l
}

func (p *parser) oneOf(key, def string, allowed ...string) string {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	v = strings.ToLower(v)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	p.fail(key, "must be one of "+strings.Join(allowed, ", "))
	return def
}
