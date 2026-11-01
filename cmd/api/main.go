// Command api runs the Devices API HTTP server.
//
// Running it with the "healthcheck" argument probes a local instance
// instead, which lets distroless containers declare a health check without
// shipping curl.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/auth"
	"github.com/erikmatioli/devices-api/internal/events"
	"github.com/erikmatioli/devices-api/internal/httpapi"
	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/platform/logger"
	"github.com/erikmatioli/devices-api/internal/platform/telemetry"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

// version is set at build time through -ldflags.
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(cfg.Log, os.Stdout, version)
	slog.SetDefault(log)
	// Export and metric callback failures from OpenTelemetry are warnings in
	// the service log rather than plain lines from the standard logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("telemetry error", slog.Any("error", err))
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel, err := telemetry.Setup(ctx, logger.ServiceName, version)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			log.Warn("telemetry shutdown", slog.Any("error", err))
		}
	}()
	metrics, err := telemetry.NewMetrics(tel.MeterProvider)
	if err != nil {
		return fmt.Errorf("create metrics: %w", err)
	}
	if tel.OTLPEnabled {
		log.Info("exporting traces and metrics over OTLP")
	}

	pool, err := postgres.NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.Database.AutoMigrate {
		if err := postgres.Migrate(pool, log); err != nil {
			return err
		}
	}

	verifier, err := auth.New(ctx, auth.Config{
		Issuer:      cfg.Auth.Issuer,
		Audience:    cfg.Auth.Audience,
		HS256Secret: []byte(cfg.Auth.HS256Secret),
		JWKSURL:     cfg.Auth.JWKSURL,
		JWKSRefresh: cfg.Auth.JWKSRefresh,
		ClockSkew:   cfg.Auth.ClockSkew,
	}, log)
	if err != nil {
		return err
	}
	if cfg.Auth.HS256Secret != "" {
		log.Warn("access tokens are verified with a shared HS256 secret; use AUTH_JWKS_URL in production")
	}

	repo := postgres.NewDeviceRepository(pool)
	devices := app.NewService(repo, app.WithMetrics(metrics))
	if err := metrics.ObserveInventory(repo.CountByState); err != nil {
		return fmt.Errorf("register inventory metric: %w", err)
	}

	idempotencyStore := postgres.NewIdempotencyStore(pool, cfg.Idempotency.TTL, cfg.Idempotency.LockTimeout)
	go idempotencyStore.RunPurge(ctx, cfg.Idempotency.PurgeInterval, log)

	hub := events.NewHub(cfg.Events.SubscriberBuffer, cfg.Events.MaxSubscribers, metrics)
	listener := postgres.NewListener(pool, hub, log)
	go listener.Run(ctx)

	handler := httpapi.NewRouter(httpapi.Dependencies{
		Logger:         log,
		Devices:        devices,
		Verifier:       verifier,
		Idempotency:    idempotencyStore,
		Events:         hub,
		Metrics:        metrics,
		MetricsHandler: tel.MetricsHandler,
		Readiness: map[string]httpapi.CheckFunc{
			"database":        pool.Ping,
			"events_listener": listener.Ready,
		},
	})

	// Tracing is the outermost layer so every middleware runs inside the
	// request span. HTTP metrics are recorded by the router with the matched
	// route, so the handler's own metrics are disabled.
	traced := otelhttp.NewHandler(handler, "http.server",
		otelhttp.WithMeterProvider(noop.NewMeterProvider()),
		otelhttp.WithFilter(func(r *http.Request) bool {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				return false
			}
			return true
		}),
	)

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           traced,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// Event streams never end on their own; closing the hub ends them so
	// Shutdown can drain the remaining requests.
	srv.RegisterOnShutdown(hub.Close)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", slog.String("addr", cfg.HTTP.Addr))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutdown signal received, draining connections",
		slog.String("timeout", cfg.ShutdownTimeout.String()))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped")
	return nil
}

// healthcheck calls the liveness endpoint of the instance listening on
// HTTP_ADDR.
func healthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse HTTP_ADDR: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort(host, port) + "/healthz"
	// The target is always the local instance derived from HTTP_ADDR.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
