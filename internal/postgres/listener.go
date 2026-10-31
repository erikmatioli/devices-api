package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/erikmatioli/devices-api/internal/events"
)

// StateChangedChannel is the PostgreSQL notification channel for device
// state changes.
const StateChangedChannel = "device_state_changed"

// listenerApplicationName identifies the listener connection in
// pg_stat_activity.
const listenerApplicationName = "devices-api-listener"

// Publisher receives decoded notifications.
type Publisher interface {
	Publish(events.StateChanged)
}

// Listener keeps a dedicated connection that LISTENs for state changes and
// forwards them to a Publisher. Every replica runs one, so a change made
// through any replica reaches the subscribers of all of them.
//
// Notifications sent while the connection is down are lost; the API does
// not promise recovery of missed events.
type Listener struct {
	connConfig *pgx.ConnConfig
	publisher  Publisher
	logger     *slog.Logger
	ready      atomic.Bool

	// Tunables; tests shorten them.
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
	PingInterval time.Duration
}

// NewListener returns a listener that connects with the pool's settings but
// on its own connection, so it never takes capacity from request handling.
func NewListener(pool *pgxpool.Pool, publisher Publisher, logger *slog.Logger) *Listener {
	cfg := pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = listenerApplicationName
	return &Listener{
		connConfig:   cfg,
		publisher:    publisher,
		logger:       logger,
		MinBackoff:   500 * time.Millisecond,
		MaxBackoff:   10 * time.Second,
		PingInterval: 30 * time.Second,
	}
}

// Ready reports whether the listener is connected. It backs the readiness
// probe: a replica that cannot hear notifications should not serve streams.
func (l *Listener) Ready(context.Context) error {
	if !l.ready.Load() {
		return errors.New("notification listener is not connected")
	}
	return nil
}

// Run listens until ctx is done, reconnecting with exponential backoff
// whenever the connection drops.
func (l *Listener) Run(ctx context.Context) {
	backoff := l.MinBackoff
	for {
		connected, err := l.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = l.MinBackoff
		}
		l.logger.WarnContext(ctx, "notification listener disconnected; reconnecting",
			slog.Any("error", err), slog.String("retry_in", backoff.String()))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, l.MaxBackoff)
	}
}

// listen runs one connection until it fails. connected reports whether the
// LISTEN succeeded, which resets the backoff.
func (l *Listener) listen(ctx context.Context) (connected bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, l.connConfig)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer func() {
		l.ready.Store(false)
		// ctx may already be canceled; closing still needs a little time.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+StateChangedChannel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	l.ready.Store(true)
	l.logger.InfoContext(ctx, "notification listener connected", slog.String("channel", StateChangedChannel))

	for {
		// Waiting with a deadline lets an idle connection be checked with a
		// ping, so a silently dropped connection is noticed. A timeout leaves
		// the pgx connection usable.
		waitCtx, cancel := context.WithTimeout(ctx, l.PingInterval)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()

		switch {
		case err == nil:
			l.handle(ctx, n.Payload)
		case ctx.Err() != nil:
			return true, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			if err := conn.Ping(ctx); err != nil {
				return true, fmt.Errorf("ping: %w", err)
			}
		default:
			return true, fmt.Errorf("wait for notification: %w", err)
		}
	}
}

func (l *Listener) handle(ctx context.Context, payload string) {
	var e events.StateChanged
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		l.logger.ErrorContext(ctx, "discarding malformed notification", slog.Any("error", err))
		return
	}
	l.publisher.Publish(e)
}
