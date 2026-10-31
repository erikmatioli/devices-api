package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/erikmatioli/devices-api/internal/platform/config"
	"github.com/erikmatioli/devices-api/internal/postgres"
)

func TestNewPoolRejectsInvalidURL(t *testing.T) {
	_, err := postgres.NewPool(context.Background(), config.Database{
		URL: "postgres://user:secret@host:notaport/db", MaxConns: 1, MinConns: 1, ConnectTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "parse database url") {
		t.Fatalf("error = %v, want a parse error", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Error("the password must not appear in the error")
	}
}

func TestNewPoolFailsWhenUnreachable(t *testing.T) {
	_, err := postgres.NewPool(context.Background(), config.Database{
		URL:            "postgres://user:pass@127.0.0.1:1/db?sslmode=disable",
		MaxConns:       1,
		MinConns:       1,
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "ping database") {
		t.Fatalf("error = %v, want a ping error", err)
	}
}
