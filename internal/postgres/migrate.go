package postgres

import (
	"embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies every pending migration embedded in the binary. It is safe
// to run from several replicas at once: golang-migrate serializes them with
// a PostgreSQL advisory lock.
func Migrate(pool *pgxpool.Pool, logger *slog.Logger) error {
	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("open migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("prepare migrations: %w", err)
	}
	// The driver holds a dedicated connection taken from the pool. It must be
	// released, otherwise pool.Close blocks forever on shutdown.
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	logger.Info("database schema up to date",
		slog.Uint64("schema_version", uint64(version)), slog.Bool("dirty", dirty))
	return nil
}
