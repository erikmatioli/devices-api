// Package config loads and validates the service configuration from
// environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
	Auth            Auth
	Idempotency     Idempotency
	Events          Events
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
	// AutoMigrate applies pending schema migrations at startup.
	AutoMigrate bool
}

// Auth configures access token verification. Exactly one of HS256Secret and
// JWKSURL is set.
type Auth struct {
	Issuer      string
	Audience    string
	HS256Secret string
	JWKSURL     string
	JWKSRefresh time.Duration
	ClockSkew   time.Duration
}

// Idempotency configures how long Idempotency-Key responses are kept.
type Idempotency struct {
	// TTL is how long a recorded response can be replayed.
	TTL time.Duration
	// LockTimeout is how long a request may stay in progress before its key
	// is considered abandoned. It must exceed the HTTP write timeout.
	LockTimeout time.Duration
	// PurgeInterval is how often expired records are deleted.
	PurgeInterval time.Duration
}

// Events configures the state change stream.
type Events struct {
	// MaxSubscribers caps concurrent streams per replica (0 means no limit).
	MaxSubscribers int
	// SubscriberBuffer is how many events may wait for a slow subscriber
	// before it is disconnected.
	SubscriberBuffer int
}

// minSecretLength is the minimum HS256 secret size (256 bits, RFC 7518).
const minSecretLength = 32

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
			AutoMigrate:    p.boolean("DATABASE_AUTO_MIGRATE", true),
		},
		Auth: Auth{
			Issuer:      p.required("AUTH_ISSUER"),
			Audience:    p.required("AUTH_AUDIENCE"),
			HS256Secret: p.str("AUTH_HS256_SECRET", ""),
			JWKSURL:     p.str("AUTH_JWKS_URL", ""),
			JWKSRefresh: p.dur("AUTH_JWKS_REFRESH", 15*time.Minute),
			ClockSkew:   p.dur("AUTH_CLOCK_SKEW", 30*time.Second),
		},
		Idempotency: Idempotency{
			TTL:           p.dur("IDEMPOTENCY_TTL", 24*time.Hour),
			LockTimeout:   p.dur("IDEMPOTENCY_LOCK_TIMEOUT", time.Minute),
			PurgeInterval: p.dur("IDEMPOTENCY_PURGE_INTERVAL", 15*time.Minute),
		},
		Events: Events{
			MaxSubscribers:   int(p.positiveInt32("EVENTS_MAX_SUBSCRIBERS", 1000)),
			SubscriberBuffer: int(p.positiveInt32("EVENTS_SUBSCRIBER_BUFFER", 64)),
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
	validateAuth(&p, cfg.Auth)
	if cfg.Idempotency.LockTimeout <= cfg.HTTP.WriteTimeout {
		p.fail("IDEMPOTENCY_LOCK_TIMEOUT", "must be greater than HTTP_WRITE_TIMEOUT")
	}

	if err := p.err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateAuth(p *parser, a Auth) {
	switch {
	case a.HS256Secret == "" && a.JWKSURL == "":
		p.fail("AUTH_HS256_SECRET", "or AUTH_JWKS_URL is required")
	case a.HS256Secret != "" && a.JWKSURL != "":
		p.fail("AUTH_HS256_SECRET", "and AUTH_JWKS_URL are mutually exclusive")
	case a.HS256Secret != "" && len(a.HS256Secret) < minSecretLength:
		p.fail("AUTH_HS256_SECRET", "must be at least 32 bytes long")
	case a.JWKSURL != "":
		u, err := url.Parse(a.JWKSURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			p.fail("AUTH_JWKS_URL", "must be an absolute http or https URL")
		}
	}
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

func (p *parser) boolean(key string, def bool) bool {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.fail(key, "must be true or false")
		return def
	}
	return b
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
