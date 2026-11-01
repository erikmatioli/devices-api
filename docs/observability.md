# Observability

How to tell whether the Devices API is healthy, which numbers matter, and what
to do when an alert fires.

## Contents

- [Local stack](#local-stack)
- [Service level objectives](#service-level-objectives)
- [Dashboards](#dashboards)
- [Key indicators](#key-indicators)
- [Metrics reference](#metrics-reference)
- [Alerts and runbooks](#alerts-and-runbooks)
- [Caveats](#caveats)
- [Changing dashboards and rules](#changing-dashboards-and-rules)

## Local stack

`make observability` starts the API with every signal wired up:

| Tool | URL | Shows |
|---|---|---|
| Grafana | <http://localhost:3000> | The two dashboards below, open read-only without signing in |
| Prometheus | <http://localhost:9090> | Raw metrics, recording rules and alert state (`/alerts`) |
| Jaeger | <http://localhost:16686> | Traces of service `devices-api`, one span per request and per SQL statement |

Logs are JSON on stdout (`make logs`). Every line about a request carries
`request_id` and `trace_id`, so a log line leads to its trace and back.

To see the panels move, generate traffic with `make load` (k6) and
`make postman` (which also produces every kind of 4xx).

## Service level objectives

| Objective | Indicator | Target |
|---|---|---|
| Availability | Share of `/v1` requests answered without a 5xx status | 99.9% |
| Latency | Share of `/v1` requests completed within 500 ms | 99% |

- **Scope.** Only the `/v1` API counts. Probes, `/metrics` and the
  documentation are excluded. So is the event stream, because a stream lasts as
  long as the client keeps it open and its duration says nothing about speed.
- **Error budget.** 99.9% leaves 0.1% of requests that may fail: about 43
  minutes of full outage in 30 days, or many more minutes of partial
  failure.
- **4xx do not count.** A rejected request (invalid body, stale ETag,
  device in use) means the API worked as designed. Those are tracked
  separately because a sudden rise usually points to a broken client.

The objectives live in [`deploy/prometheus/rules.yml`](../deploy/prometheus/rules.yml)
as recording rules (`job:devices_api_error_ratio:rate5m` and so on) used by
both the alerts and anyone querying Prometheus.

## Dashboards

Both dashboards are provisioned from
[`deploy/grafana/dashboards`](../deploy/grafana/dashboards), filter by
instance, and link to each other, to Jaeger and to the Prometheus alerts.

### Devices API · Service overview

What callers experience.

| Row | Answers |
|---|---|
| Service level objectives | Are we within the objectives over the selected range, and how much error budget is left? Current request rate, p95 latency and 5xx ratio |
| Traffic | Which routes are called, and what share ends in 2xx, 4xx or 5xx? |
| Latency | p50/p95/p99, which route is slow, and how the distribution moves (heatmap) |
| Errors | 5xx by route; 4xx by cause: missing token, missing scope, not found, in-use rule, stale ETag, invalid fields |

### Devices API · Domain and dependencies

What the product and its dependencies are doing.

| Row | Answers |
|---|---|
| Device inventory | How many devices are available, in use and inactive, and how much of the usable fleet is in use |
| Lifecycle | Creations, deletions and state transitions per minute |
| Safe retries and events | How often retries are deduplicated by `Idempotency-Key`, how many event streams are open, whether slow subscribers are being dropped |
| Database | Query and connection-acquire latency, pool usage against its size |
| Go runtime | CPU, resident memory and heap, goroutines, garbage-collection pauses |

## Key indicators

| Indicator | Healthy | Worth a look when |
|---|---|---|
| Availability | ≥ 99.9% | Below the objective, or the budget drops quickly |
| p95 latency | Well under 500 ms (tens of ms locally) | Rising without more traffic: check database latency and pool usage |
| 409 responses | Rare | Growing: clients try to change or delete devices that are in use |
| 412 responses | Rare | Growing: clients write from stale reads, so concurrent edits are frequent |
| 422 responses | Low | A spike right after a client release points to a contract mismatch |
| Idempotency `mismatch` | Zero | Clients reuse keys for different requests, a client bug |
| Pool acquire latency | Close to zero | Requests wait for connections: raise `DATABASE_MAX_CONNS` or find slow queries |
| Share in use | Depends on the fleet | Close to 100%: little capacity left for new assignments |
| Dropped subscribers | Zero | Event consumers cannot keep up |
| Goroutines | Flat under steady traffic | Climbing steadily: something is not being released |

## Metrics reference

The service's own metrics (instrumentation scope
`github.com/erikmatioli/devices-api`):

| Metric (Prometheus name) | Type | Labels | Meaning |
|---|---|---|---|
| `http_server_request_duration_seconds` | histogram | `http_request_method`, `http_route`, `http_response_status_code` | Every HTTP request, by matched route pattern (bounded cardinality) |
| `devices_created_total` | counter | | Committed creations |
| `devices_deleted_total` | counter | | Committed deletions |
| `devices_state_transitions_total` | counter | `from`, `to` | Committed state changes |
| `devices_inventory` | gauge | `state` | Stored devices per state, counted when metrics are collected |
| `devices_events_subscribers` | gauge | | Open event streams |
| `devices_events_dropped_subscribers_total` | counter | | Streams closed because the client read too slowly |
| `idempotency_requests_total` | counter | `outcome` | Requests with an `Idempotency-Key`: `started`, `replayed`, `mismatch`, `in_progress` |

Also exported: `db_client_operation_duration_seconds` and `pgxpool_*` (from
otelpgx), `go_*` and `process_*` (Go runtime), and `target_info` with the
service name and version.

## Alerts and runbooks

Alerts are evaluated by Prometheus (see `/alerts`). The local stack has no
Alertmanager; in production the `severity` label decides the route:
`critical` pages, `warning` opens a ticket, `info` goes to a channel.

| Alert | Severity | Fires when |
|---|---|---|
| [DevicesApiDown](#devicesapidown) | critical | An instance cannot be scraped for 1 minute |
| [DevicesApiErrorBudgetBurnFast](#devicesapierrorbudgetburn) | critical | 5xx ratio above 14.4× the budget over 1 hour and 5 minutes |
| [DevicesApiErrorBudgetBurnSlow](#devicesapierrorbudgetburn) | warning | 5xx ratio above 6× the budget over 6 hours and 30 minutes |
| [DevicesApiLatencyBudgetBurn](#devicesapilatencybudgetburn) | warning | Requests slower than 500 ms above 14.4× the latency budget over 1 hour and 5 minutes |
| [DevicesApiDatabasePoolSaturated](#devicesapidatabasepoolsaturated) | warning | Over 90% of the pool in use for 5 minutes |
| [DevicesApiDatabaseSlow](#devicesapidatabaseslow) | warning | p95 of SQL statements above 100 ms for 10 minutes |
| [DevicesApiInventoryUnavailable](#devicesapiinventoryunavailable) | warning | A running instance stops reporting `devices_inventory` for 5 minutes |
| [DevicesApiEventSubscribersDropped](#devicesapieventsubscribersdropped) | info | Any subscriber dropped in the last 10 minutes |

The budget alerts need both windows above the threshold. The long window
proves the problem is significant; the short one proves it is still
happening, so the alert clears soon after a recovery. A 15-second database
outage under load, for example, raises the 5-minute ratio but not the hourly
one, and pages no one.

### DevicesApiDown

1. `docker compose ps` (or the orchestrator) shows whether the container is
   running and healthy.
2. If it restarts in a loop, `make logs` shows why. Configuration errors are
   printed on startup and list every invalid variable.
3. If it runs but cannot be scraped, check the network between Prometheus and
   the instance.

### DevicesApiErrorBudgetBurn

1. On **Service overview → Errors**, find the routes returning 5xx.
2. Filter the logs by `"level":"ERROR"`; each line has the `request_id` and
   `trace_id` of a failed request.
3. Open the trace in Jaeger: the failing span (HTTP handler or SQL
   statement) carries the error.
4. Usual causes: database unreachable (`/readyz` reports it), pool exhausted,
   a failing migration after a deploy. Roll back if the rise started with a
   release.

### DevicesApiLatencyBudgetBurn

1. **Latency p95 by route** shows whether one route or all of them slowed
   down.
2. All routes slower: look at **Database → acquire** latency (pool too small)
   and **query** latency (database under pressure).
3. One route slower: open a slow trace of that route in Jaeger and compare its
   SQL spans with a fast one.

### DevicesApiDatabasePoolSaturated

1. Check whether traffic grew (**Requests/s**) or queries got slower
   (**Database operation latency**).
2. More traffic: raise `DATABASE_MAX_CONNS`, keeping the total across
   replicas below the database's `max_connections`, or add replicas.
3. Slower queries: treat it as DevicesApiDatabaseSlow.

### DevicesApiDatabaseSlow

1. Check the database host's CPU, I/O and locks (`pg_stat_activity`).
2. The SQL spans in Jaeger show which statement is slow.
3. Long waits on `SELECT … FOR UPDATE` mean many concurrent writes to the
   same device. That is expected to be rare; a single client hammering one
   device is the usual cause.

### DevicesApiInventoryUnavailable

The gauge is computed with a `count(*) … GROUP BY state` on every collection.
When that query fails, the series disappears instead of reporting stale
numbers.

1. Search the logs for `"msg":"telemetry error"`; the `error` field has the
   cause, often `context deadline exceeded` when the database is slow or
   unreachable.
2. If the database is fine but the table is very large, see
   [Caveats](#caveats).

### DevicesApiEventSubscribersDropped

A subscriber that does not read its stream fast enough is disconnected, so
it cannot slow down the others. It is expected to reconnect and reconcile
with `GET /v1/devices`.

1. **Event streams** shows how many were dropped and when.
2. Isolated drops: a client on a poor network, no action needed.
3. Sustained drops: a consumer is too slow for the event rate; raise
   `EVENTS_SUBSCRIBER_BUFFER` or fix the consumer.

## Caveats

- **The first occurrence of a series is invisible to `increase()` and
  `rate()`.** Prometheus needs two samples to compute a difference, so a
  status code that happens once or twice since the instance started shows
  as zero over a range. That is why the "Client errors by cause" panel shows
  totals since the instances started. Under real traffic the effect on
  ratios is negligible.
- **Inventory cost.** The count runs on every scrape (every 5 seconds locally;
  15 to 60 seconds is typical in production) and on every OTLP export when
  enabled. It is cheap for thousands or millions of rows. For much larger
  tables, cache the result for a short time, or read an estimate from
  PostgreSQL statistics.
- **Replicas.** Each replica reports the same inventory, so the dashboards
  take the maximum, not the sum. Counters and histograms are summed.

## Changing dashboards and rules

- **Dashboards** are plain JSON loaded at startup and reloaded every few
  seconds. Grafana does not save edits made in its UI to these files. Sign in
  as `admin` / `admin` (local only), edit, export the dashboard as JSON and
  replace the file under `deploy/grafana/dashboards`.
- **Rules** are tested with `promtool`: `make check-rules` validates
  `rules.yml` and runs the scenarios in `rules.test.yml`. CI runs the same
  target. Restart Prometheus (`docker compose restart prometheus`) to load a
  change locally.
