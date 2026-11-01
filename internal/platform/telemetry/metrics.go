package telemetry

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/erikmatioli/devices-api/internal/device"
)

// meterName is the instrumentation scope of the service's own metrics.
const meterName = "github.com/erikmatioli/devices-api"

// Metrics records the service metrics. It implements the metric ports of
// the application (app.Metrics), the HTTP layer (httpapi.Metrics) and the
// event hub (events.Observer), so those packages stay free of OpenTelemetry.
type Metrics struct {
	meter              metric.Meter
	requestDuration    metric.Float64Histogram
	devicesCreated     metric.Int64Counter
	devicesDeleted     metric.Int64Counter
	stateTransitions   metric.Int64Counter
	eventSubscribers   metric.Int64UpDownCounter
	eventsDropped      metric.Int64Counter
	idempotentOutcomes metric.Int64Counter
}

// NewMetrics creates the instruments on mp.
func NewMetrics(mp metric.MeterProvider) (*Metrics, error) {
	m := mp.Meter(meterName)
	var (
		out = Metrics{meter: m}
		err error
	)
	if out.requestDuration, err = m.Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"), metric.WithDescription("Duration of HTTP server requests.")); err != nil {
		return nil, err
	}
	if out.devicesCreated, err = m.Int64Counter("devices.created",
		metric.WithUnit("{device}"), metric.WithDescription("Devices created.")); err != nil {
		return nil, err
	}
	if out.devicesDeleted, err = m.Int64Counter("devices.deleted",
		metric.WithUnit("{device}"), metric.WithDescription("Devices deleted.")); err != nil {
		return nil, err
	}
	if out.stateTransitions, err = m.Int64Counter("devices.state.transitions",
		metric.WithUnit("{transition}"), metric.WithDescription("Committed device state changes.")); err != nil {
		return nil, err
	}
	if out.eventSubscribers, err = m.Int64UpDownCounter("devices.events.subscribers",
		metric.WithUnit("{subscriber}"), metric.WithDescription("Open event streams.")); err != nil {
		return nil, err
	}
	if out.eventsDropped, err = m.Int64Counter("devices.events.dropped_subscribers",
		metric.WithUnit("{subscriber}"), metric.WithDescription("Event streams closed because the client was too slow.")); err != nil {
		return nil, err
	}
	if out.idempotentOutcomes, err = m.Int64Counter("idempotency.requests",
		metric.WithUnit("{request}"), metric.WithDescription("Requests sent with an Idempotency-Key, by outcome.")); err != nil {
		return nil, err
	}
	return &out, nil
}

// RequestCompleted records one HTTP request. route is the matched pattern,
// which keeps label cardinality bounded.
func (m *Metrics) RequestCompleted(ctx context.Context, method, route string, status int, d time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.requestDuration.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
		attribute.String("http.response.status_code", strconv.Itoa(status)),
	))
}

// IdempotencyOutcome records how a keyed request was handled.
func (m *Metrics) IdempotencyOutcome(ctx context.Context, outcome string) {
	m.idempotentOutcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// DeviceCreated implements app.Metrics.
func (m *Metrics) DeviceCreated(ctx context.Context) {
	m.devicesCreated.Add(ctx, 1)
}

// DeviceDeleted implements app.Metrics.
func (m *Metrics) DeviceDeleted(ctx context.Context) {
	m.devicesDeleted.Add(ctx, 1)
}

// DeviceStateChanged implements app.Metrics.
func (m *Metrics) DeviceStateChanged(ctx context.Context, from, to device.State) {
	m.stateTransitions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from", string(from)),
		attribute.String("to", string(to)),
	))
}

// inventoryTimeout bounds the count query run on every collection.
const inventoryTimeout = 2 * time.Second

// CountByState returns the number of stored devices in each state.
type CountByState func(context.Context) (map[device.State]int64, error)

// ObserveInventory registers the devices.inventory gauge, which reports how
// many devices are in each state when metrics are collected. Every state is
// reported, with zero when no device is in it, so dashboards and alerts see
// a complete series set.
func (m *Metrics) ObserveInventory(count CountByState) error {
	gauge, err := m.meter.Int64ObservableGauge("devices.inventory",
		metric.WithUnit("{device}"), metric.WithDescription("Stored devices, by state."))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, inventoryTimeout)
		defer cancel()
		counts, err := count(ctx)
		if err != nil {
			return fmt.Errorf("observe device inventory: %w", err)
		}
		for _, s := range device.States {
			o.ObserveInt64(gauge, counts[s], metric.WithAttributes(attribute.String("state", string(s))))
		}
		return nil
	}, gauge)
	return err
}

// SubscriberAdded implements events.Observer.
func (m *Metrics) SubscriberAdded() {
	m.eventSubscribers.Add(context.Background(), 1)
}

// SubscriberRemoved implements events.Observer.
func (m *Metrics) SubscriberRemoved() {
	m.eventSubscribers.Add(context.Background(), -1)
}

// SubscriberDropped implements events.Observer.
func (m *Metrics) SubscriberDropped() {
	m.eventsDropped.Add(context.Background(), 1)
}
