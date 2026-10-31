package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/erikmatioli/devices-api/internal/app"
	"github.com/erikmatioli/devices-api/internal/device"
	"github.com/erikmatioli/devices-api/internal/events"
)

const deviceColumns = "id, name, brand, state, creation_time, version"

// DeviceRepository stores devices in PostgreSQL.
type DeviceRepository struct {
	pool *pgxpool.Pool
}

var _ app.DeviceRepository = (*DeviceRepository)(nil)

// NewDeviceRepository returns a repository backed by pool.
func NewDeviceRepository(pool *pgxpool.Pool) *DeviceRepository {
	return &DeviceRepository{pool: pool}
}

// Create implements app.DeviceRepository.
func (r *DeviceRepository) Create(ctx context.Context, d device.Device) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO devices (`+deviceColumns+`) VALUES ($1, $2, $3, $4, $5, $6)`,
		d.ID(), d.Name(), d.Brand(), string(d.State()), d.CreationTime(), d.Version())
	if err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	return nil
}

// Get implements app.DeviceRepository.
func (r *DeviceRepository) Get(ctx context.Context, id uuid.UUID) (device.Device, error) {
	d, err := scanDevice(r.pool.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE id = $1`, id))
	if err != nil {
		return device.Device{}, notFoundOr(err, "select device")
	}
	return d, nil
}

// List implements app.DeviceRepository. It uses keyset pagination on
// (creation_time, id) and fetches one extra row to know whether another
// page exists without a separate COUNT.
func (r *DeviceRepository) List(ctx context.Context, f app.ListFilter) (app.Page, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Brand != "" {
		where = append(where, "lower(brand) = lower("+arg(f.Brand)+")")
	}
	if f.State != "" {
		where = append(where, "state = "+arg(string(f.State)))
	}
	if f.After != nil {
		where = append(where, "(creation_time, id) > ("+arg(f.After.CreationTime)+", "+arg(f.After.ID)+")")
	}

	// Only placeholders are concatenated; every value travels as a parameter.
	var q strings.Builder
	q.WriteString("SELECT " + deviceColumns + " FROM devices")
	if len(where) > 0 {
		q.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	q.WriteString(" ORDER BY creation_time, id LIMIT " + arg(f.Limit+1))

	rows, err := r.pool.Query(ctx, q.String(), args...)
	if err != nil {
		return app.Page{}, fmt.Errorf("list devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (device.Device, error) {
		return scanDevice(row)
	})
	if err != nil {
		return app.Page{}, fmt.Errorf("scan devices: %w", err)
	}

	page := app.Page{Devices: devices}
	if len(devices) > f.Limit {
		page.Devices = devices[:f.Limit]
		last := page.Devices[f.Limit-1]
		page.Next = &app.Cursor{CreationTime: last.CreationTime(), ID: last.ID()}
	}
	return page, nil
}

// Update implements app.DeviceRepository. The row lock taken by
// SELECT ... FOR UPDATE serializes concurrent updates of the same device, so
// the rules in fn always see the latest committed state.
//
// A state change is announced with pg_notify inside the same transaction.
// PostgreSQL delivers notifications only when the transaction commits and
// discards them on rollback, so subscribers never hear about a change that
// was not persisted.
func (r *DeviceRepository) Update(ctx context.Context, id uuid.UUID, fn app.UpdateFunc) (device.Device, error) {
	var out device.Device
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		d, err := scanDevice(tx.QueryRow(ctx,
			`SELECT `+deviceColumns+` FROM devices WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return notFoundOr(err, "lock device")
		}

		upd, err := fn(&d)
		if err != nil {
			return err
		}
		if !upd.Modified {
			out = d
			return nil
		}

		out, err = scanDevice(tx.QueryRow(ctx,
			`UPDATE devices SET name = $2, brand = $3, state = $4, version = version + 1
			 WHERE id = $1 RETURNING `+deviceColumns,
			d.ID(), d.Name(), d.Brand(), string(d.State())))
		if err != nil {
			return fmt.Errorf("update device: %w", err)
		}

		if upd.StateChange != nil {
			payload, err := json.Marshal(events.FromStateChange(device.NewID(), *upd.StateChange))
			if err != nil {
				return fmt.Errorf("encode state change: %w", err)
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, StateChangedChannel, string(payload)); err != nil {
				return fmt.Errorf("notify state change: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return device.Device{}, err
	}
	return out, nil
}

// Delete implements app.DeviceRepository. The state condition is part of the
// DELETE itself, so a device cannot move to in-use between a check and the
// removal. The follow-up query only picks the right error.
func (r *DeviceRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM devices WHERE id = $1 AND state <> $2`, id, string(device.StateInUse))
	if err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM devices WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check device: %w", err)
	}
	if exists {
		return device.ErrDeleteInUse
	}
	return device.ErrNotFound
}

func scanDevice(row pgx.Row) (device.Device, error) {
	var (
		id           uuid.UUID
		name, brand  string
		state        string
		creationTime time.Time
		version      int64
	)
	if err := row.Scan(&id, &name, &brand, &state, &creationTime, &version); err != nil {
		return device.Device{}, err
	}
	return device.Restore(id, name, brand, device.State(state), creationTime, version), nil
}

func notFoundOr(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return device.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
