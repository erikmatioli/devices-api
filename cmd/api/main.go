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

	"github.com/erikmatioli/devices-api/internal/httpapi"
	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/platform/logger"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	handler := httpapi.NewRouter(httpapi.Dependencies{
		Logger: log,
		Readiness: map[string]httpapi.CheckFunc{
			"database": pool.Ping,
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

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
