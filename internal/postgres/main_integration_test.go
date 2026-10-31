//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

// testPool is shared by every integration test in this package.
var testPool *pgxpool.Pool

// TestMain starts a disposable PostgreSQL container, or uses
// TEST_DATABASE_URL when set (for example a CI service container), and
// applies the migrations once.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("devices"),
			tcpostgres.WithUsername("devices"),
			tcpostgres.WithPassword("devices"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "start postgres container:", err)
			return 1
		}
		defer func() { _ = testcontainers.TerminateContainer(ctr) }()

		url, err = ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintln(os.Stderr, "container connection string:", err)
			return 1
		}
	}

	pool, err := postgres.NewPool(ctx, config.Database{
		URL:            url,
		MaxConns:       20,
		MinConns:       1,
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer pool.Close()

	if err := postgres.Migrate(pool, slog.New(slog.DiscardHandler)); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}

	testPool = pool
	return m.Run()
}

// resetDevices empties the table so each test starts from a known state.
func resetDevices(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "TRUNCATE devices"); err != nil {
		t.Fatalf("truncate devices: %v", err)
	}
}
