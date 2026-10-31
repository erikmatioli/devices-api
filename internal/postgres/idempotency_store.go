package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/erikmatioli/devices-api/internal/idempotency"
)

// IdempotencyStore keeps idempotency records in PostgreSQL, so retries are
// recognized by any replica and across restarts. Times come from the
// database clock, which all replicas share.
type IdempotencyStore struct {
	pool        *pgxpool.Pool
	ttl         time.Duration
	lockTimeout time.Duration
}

var _ idempotency.Store = (*IdempotencyStore)(nil)

// NewIdempotencyStore returns a store whose records expire after ttl. A
// request still in progress after lockTimeout is treated as abandoned (for
// example, the replica crashed) and the key can be claimed again.
func NewIdempotencyStore(pool *pgxpool.Pool, ttl, lockTimeout time.Duration) *IdempotencyStore {
	return &IdempotencyStore{pool: pool, ttl: ttl, lockTimeout: lockTimeout}
}

// Begin implements idempotency.Store. The claim is a single upsert, so two
// concurrent requests with the same key cannot both start.
func (s *IdempotencyStore) Begin(ctx context.Context, key idempotency.Key, fingerprint []byte) (idempotency.Result, error) {
	// A record can disappear between the claim and the read (released after a
	// server error); retrying the claim resolves that race.
	for range 3 {
		var claimed bool
		err := s.pool.QueryRow(ctx, `
			INSERT INTO idempotency_keys (subject, key, fingerprint, started_at, expires_at)
			VALUES ($1, $2, $3, now(), now() + $4 * interval '1 microsecond')
			ON CONFLICT (subject, key) DO UPDATE
			   SET fingerprint = EXCLUDED.fingerprint,
			       started_at = EXCLUDED.started_at,
			       expires_at = EXCLUDED.expires_at,
			       response_status = NULL,
			       response_headers = NULL,
			       response_body = NULL
			 WHERE idempotency_keys.expires_at < now()
			    OR (idempotency_keys.response_status IS NULL
			        AND idempotency_keys.started_at < now() - $5 * interval '1 microsecond')
			RETURNING true`,
			key.Subject, key.Value, fingerprint, s.ttl.Microseconds(), s.lockTimeout.Microseconds(),
		).Scan(&claimed)
		if err == nil {
			return idempotency.Result{Outcome: idempotency.Started}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return idempotency.Result{}, fmt.Errorf("claim idempotency key: %w", err)
		}

		res, found, err := s.existing(ctx, key, fingerprint)
		if err != nil {
			return idempotency.Result{}, err
		}
		if found {
			return res, nil
		}
	}
	return idempotency.Result{}, errors.New("claim idempotency key: record kept changing")
}

func (s *IdempotencyStore) existing(ctx context.Context, key idempotency.Key, fingerprint []byte) (idempotency.Result, bool, error) {
	var (
		stored  []byte
		status  *int
		headers []byte
		body    []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT fingerprint, response_status, response_headers, response_body
		  FROM idempotency_keys WHERE subject = $1 AND key = $2`,
		key.Subject, key.Value,
	).Scan(&stored, &status, &headers, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Result{}, false, nil
	}
	if err != nil {
		return idempotency.Result{}, false, fmt.Errorf("read idempotency key: %w", err)
	}

	switch {
	case !bytes.Equal(stored, fingerprint):
		return idempotency.Result{Outcome: idempotency.Mismatch}, true, nil
	case status == nil:
		return idempotency.Result{Outcome: idempotency.InProgress}, true, nil
	}

	resp := &idempotency.Response{Status: *status, Body: body, Header: make(http.Header)}
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &resp.Header); err != nil {
			return idempotency.Result{}, false, fmt.Errorf("decode stored headers: %w", err)
		}
	}
	return idempotency.Result{Outcome: idempotency.Replay, Response: resp}, true, nil
}

// Complete implements idempotency.Store.
func (s *IdempotencyStore) Complete(ctx context.Context, key idempotency.Key, resp idempotency.Response) error {
	headers, err := json.Marshal(resp.Header)
	if err != nil {
		return fmt.Errorf("encode response headers: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		   SET response_status = $3, response_headers = $4, response_body = $5
		 WHERE subject = $1 AND key = $2`,
		key.Subject, key.Value, resp.Status, headers, resp.Body)
	if err != nil {
		return fmt.Errorf("store idempotent response: %w", err)
	}
	return nil
}

// Release implements idempotency.Store. Completed records are kept.
func (s *IdempotencyStore) Release(ctx context.Context, key idempotency.Key) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE subject = $1 AND key = $2 AND response_status IS NULL`,
		key.Subject, key.Value)
	if err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

// PurgeExpired deletes expired records and returns how many were removed.
func (s *IdempotencyStore) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("purge idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RunPurge calls PurgeExpired every interval until ctx is done. Expired
// records are already ignored by Begin; purging only keeps the table small.
func (s *IdempotencyStore) RunPurge(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.PurgeExpired(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				logger.WarnContext(ctx, "idempotency purge failed", slog.Any("error", err))
			case n > 0:
				logger.InfoContext(ctx, "expired idempotency keys purged", slog.Int64("count", n))
			}
		}
	}
}
