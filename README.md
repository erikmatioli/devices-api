# Devices API

REST API written in Go that persists and manages device resources, enforces
the device business rules under concurrency, and notifies subscribed clients
of state changes in real time.

## Contents

- [Scope](#scope)
- [Features](#features)
- [Quick start](#quick-start)
- [Using the API](#using-the-api)
- [Architecture in brief](#architecture-in-brief)
- [Testing](#testing)
- [Observability](#observability)
- [Configuration](#configuration)
- [Project layout](#project-layout)
- [Known limitations](#known-limitations)
- [Future improvements](#future-improvements)
- [Further documentation](#further-documentation)

## Scope

The assignment asks for a device CRUD with filtering by brand and state, three
domain rules, persistence, tests, documentation and a container image. That
core is small and can be reviewed on its own:

| Package | Holds |
|---|---|
| [`internal/device`](internal/device) | The entity and its rules, with no I/O |
| [`internal/app`](internal/app) | Use cases and the repository port |
| [`internal/httpapi/devices.go`](internal/httpapi/devices.go) | The HTTP handlers |
| [`internal/postgres/device_repository.go`](internal/postgres/device_repository.go) | Persistence |

Everything below was added on purpose. Each item is isolated from the core and
is optional at runtime or lives outside the binary.

| Addition | Why | Footprint |
|---|---|---|
| `ETag` / `If-Match` | A client writing from a stale read could overwrite a concurrent change and get around the `in-use` rule | One `version` column; `If-Match` is optional |
| `422` problem documents | Clients see every invalid field in one response | Error mapping in `httpapi` |
| Cursor pagination | An unbounded list does not hold up as the table grows | Pages of 20 by default, `cursor` for the next one |
| `Idempotency-Key` | A retried create must not produce a duplicate device | One middleware on `POST /v1/devices` and one table; the header is optional |
| JWT scopes | Reading, writing and deleting are separate permissions | HS256 with a local secret; JWKS only when `AUTH_JWKS_URL` is set |
| Event stream (SSE) | State changes must be announced only after they are persisted; `pg_notify` inside the update transaction guarantees that across replicas without a message broker | One listening connection per replica |
| OpenTelemetry and Prometheus | Traces, metrics and trace-correlated logs for running it in production | OTLP export only when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; the collector, Jaeger and Prometheus start only with `make observability` |
| k6, Postman collection, CI | Repeatable evidence that the behaviour holds | Outside the binary |

The reasoning and the alternatives considered for each decision are recorded
in [docs/architecture.md](docs/architecture.md).

## Features

| Area | What is provided |
|---|---|
| Devices | Create, fetch one, list (filter by brand and/or state, cursor pagination), full update (`PUT`), partial update (`PATCH`, JSON Merge Patch), delete |
| Domain rules | `creation_time` is read-only; `name` and `brand` are locked while a device is `in-use`; `in-use` devices cannot be deleted. All enforced atomically, with row locks or conditional statements |
| Notifications | Server-Sent Events stream with device id, previous and new state, emitted only after the change is committed (PostgreSQL `LISTEN/NOTIFY`), across every replica |
| Validation | Every invalid field is reported at once as an RFC 9457 problem with `422` |
| Security | JWT bearer tokens (HS256 locally, JWKS in production) with per-operation scopes |
| Safe retries | `Idempotency-Key` on create, scoped per caller and shared by all replicas |
| Concurrency | `ETag` / `If-Match` optimistic locking and `If-None-Match` conditional reads |
| Observability | JSON logs correlated with traces, OpenTelemetry traces and metrics, Prometheus endpoint, health and readiness probes |
| Operations | Distroless non-root image (about 23 MB), embedded migrations, graceful shutdown, Compose stacks for the API, tools and observability |
| Documentation | OpenAPI 3.1 contract served at `/openapi.yaml` and rendered at `/docs`, plus architecture, flows and a developer guide |

## Quick start

Requirements: Docker with Compose v2. Building from source also needs Go 1.26
or newer and GNU Make (optional; on Windows it also needs Git for Windows,
whose shell runs the `Makefile` recipes).

```bash
cp .env.example .env
make up                       # builds the images and starts PostgreSQL and the API
curl http://localhost:8080/readyz
```

Without `make`:

```bash
docker compose --profile tools build
docker compose up -d
```

Stop everything with `make down` (the database volume is kept).

## Using the API

Every `/v1` endpoint needs a bearer token with the right scope. Mint a local
one (signed with the development secret from `.env`):

```bash
TOKEN=$(docker compose run --rm tokengen)          # or: TOKEN=$(make -s token)
AUTH="Authorization: Bearer $TOKEN"
```

Create, read and change a device:

```bash
curl -s -H "$AUTH" -H "Content-Type: application/json" \
     -H "Idempotency-Key: 6f1c2b9e" \
     -d '{"name":"Pixel 9","brand":"Google"}' \
     http://localhost:8080/v1/devices
# 201 Created, Location: /v1/devices/<id>, ETag: "1"

curl -s -H "$AUTH" "http://localhost:8080/v1/devices?brand=google&state=available&limit=20"

curl -s -X PATCH -H "$AUTH" -H "Content-Type: application/merge-patch+json" \
     -H 'If-Match: "1"' -d '{"state":"in-use"}' \
     http://localhost:8080/v1/devices/<id>
```

Follow state changes in another terminal:

```bash
curl -N -H "$AUTH" http://localhost:8080/v1/devices/events
# event: device.state_changed
# data: {"event_id":"…","device_id":"…","previous_state":"available","new_state":"in-use","occurred_at":"…"}
```

### Endpoints

| Method | Path | Scope | Success | Notable errors |
|---|---|---|---|---|
| `POST` | `/v1/devices` | `devices:write` | `201` | `409` key in progress, `422` |
| `GET` | `/v1/devices` | `devices:read` | `200` | `422` invalid filter or cursor |
| `GET` | `/v1/devices/{id}` | `devices:read` | `200`, `304` | `404` |
| `PUT` | `/v1/devices/{id}` | `devices:write` | `200` | `404`, `409` in use, `412`, `422` |
| `PATCH` | `/v1/devices/{id}` | `devices:write` | `200` | `404`, `409` in use, `412`, `422` |
| `DELETE` | `/v1/devices/{id}` | `devices:delete` | `204` | `404`, `409` in use |
| `GET` | `/v1/devices/events` | `devices:read` | `200` stream | `503` subscriber limit |
| `GET` | `/healthz`, `/readyz` | public | `200` | `503` dependency down |
| `GET` | `/metrics`, `/openapi.yaml`, `/docs` | public | `200` | |

Common to all `/v1` routes: `401` without a valid token, `403` without the
scope, `400` for malformed JSON, `413` above 1 MB, `415` for other content
types. A Postman collection that walks through every endpoint, with assertions
and automatic tokens, is in [`postman/`](postman/README.md).

The full contract, with schemas and examples, is in
[`api/openapi.yaml`](api/openapi.yaml) and at <http://localhost:8080/docs>
(use **Authorize** with the token).

### Behaviour worth knowing

- **Rules are checked against the stored state.** A device that is `in-use`
  must be released (`state` changed) before its name or brand can change; one
  request cannot do both. Sending the current name and brand unchanged is
  accepted, so a `PUT` built from a `GET` keeps working.
- **Read-only fields.** `id` and `creation_time` are rejected on create and
  accepted on updates only with their current value.
- **Idempotency.** Same key and body after completion returns the recorded
  response with `Idempotent-Replayed: true`; same key with another body
  returns `422`; a key still being processed returns `409` with
  `Retry-After`. Server errors are not recorded, so they can be retried.
- **Events are at-most-once.** Changes that happen while a client is
  disconnected are not replayed (as allowed by the requirements). Renames and
  no-op updates produce no event.

## Architecture in brief

```
HTTP (net/http) ─▶ middleware: request id · tracing · log/metrics · recover · auth · scope · idempotency
                ─▶ handlers (decode, validate shape, map errors)
                ─▶ app.Service (use cases, If-Match, metrics)
                ─▶ device (entity and rules, no I/O)
                ─▶ postgres repository (row lock, conditional delete, pg_notify in the same transaction)

PostgreSQL ──NOTIFY after commit──▶ listener (every replica) ─▶ hub ─▶ SSE streams
```

- Hexagonal layout: the domain knows nothing about HTTP or SQL; the
  application layer defines the ports it needs; adapters implement them.
- Standard library router (Go 1.22 patterns), `pgx` for PostgreSQL, embedded
  `golang-migrate` migrations.
- Notifications use `pg_notify` inside the update transaction, so PostgreSQL
  itself guarantees they are sent only after the change is persisted, and every
  replica receives them without an extra broker.

The decisions and their trade-offs are recorded in
[`docs/architecture.md`](docs/architecture.md); every use case is drawn in
[`docs/flows.md`](docs/flows.md).

## Testing

| Command | What runs |
|---|---|
| `make test` | Unit tests (domain, service, handlers, auth, config, telemetry); no Docker |
| `make test-integration` | Plus integration tests against a real PostgreSQL started by testcontainers |
| `make cover` | Everything with a coverage summary |
| `make test-docker` | Everything with the race detector in a Linux container (useful on Windows) |
| `make lint` | golangci-lint with security, error-handling and context checks |
| `make load` | k6 smoke load test against the running stack |
| `make postman` | The Postman collection (41 requests, 145 assertions) with Newman |

Coverage is about 80% of statements overall, 100% for the domain, service,
idempotency and logger packages and 95% for the HTTP layer. The suites cover,
among others:

- 15 concurrent writers on one device without lost updates;
- concurrent claims of one idempotency key resolving to a single winner;
- notifications not delivered before commit or after rollback, reaching two
  listeners, and resuming after the listener connection is killed;
- a route-by-scope matrix and twelve kinds of rejected tokens;
- validation, malformed bodies, conditional requests and the SSE stream as
  seen by a real client.

Load smoke result on a laptop (Docker Desktop, 20 virtual users, 30 s, mixed
create/get/list/patch): about 340 requests/s with no errors; p95 latency
around 35 ms for reads and 130–150 ms for writes, which are bound by
PostgreSQL commit fsync inside Docker Desktop. Numbers on Linux hosts are
considerably better.

## Observability

- **Logs:** JSON on stdout with `request_id`, `subject`, route, status,
  duration and, when a span is active, `trace_id`/`span_id`.
- **Metrics:** `GET /metrics` in Prometheus format: request duration
  histogram by route and status, devices created/deleted, state transitions
  (`from`, `to`), open event streams, dropped slow subscribers, idempotency
  outcomes, connection pool statistics and Go runtime metrics.
- **Traces:** OpenTelemetry spans per request (named after the route) and per
  SQL statement, exported over OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set.
  W3C `traceparent` from callers is honoured.

`make observability` starts the API with an OpenTelemetry Collector, Jaeger
(<http://localhost:16686>) and Prometheus (<http://localhost:9090>).

## Configuration

All settings are environment variables, validated at startup (an invalid
value stops the process with a list of every problem). The main ones:

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | required | PostgreSQL connection string |
| `AUTH_ISSUER`, `AUTH_AUDIENCE` | required | Expected token issuer and audience |
| `AUTH_HS256_SECRET` / `AUTH_JWKS_URL` | one required | Local shared secret, or the identity provider key set |
| `HTTP_ADDR` | `:8080` | Listen address |
| `IDEMPOTENCY_TTL` | `24h` | How long responses can be replayed |
| `EVENTS_MAX_SUBSCRIBERS` | `1000` | Event streams per replica |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `json` | Logging |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | Enables OTLP export |

The complete list is in [`DEVELOPMENT.md`](DEVELOPMENT.md#3-configuration) and
[`.env.example`](.env.example).

## Project layout

```
cmd/api/              entry point: configuration, wiring, server, graceful shutdown
cmd/tokengen/         development helper that mints local access tokens
internal/device/      domain model and business rules (no I/O)
internal/app/         use cases and the ports they need
internal/httpapi/     routes, middleware, decoding, problem responses, SSE, docs
internal/postgres/    repositories, idempotency store, LISTEN loop, migrations
internal/auth/        JWT verification and the request principal
internal/idempotency/ Idempotency-Key contract and in-memory store
internal/events/      state change event and fan-out hub
internal/platform/    configuration, logger, telemetry
api/                  OpenAPI contract (embedded in the binary)
deploy/               collector and Prometheus configuration
test/load/            k6 smoke test
postman/              Postman collection and local environment
docs/                 architecture, flows, delivery plan
```

## Known limitations

- **Missed events are not recovered.** A client that is disconnected, or too
  slow and therefore dropped, misses the events sent meanwhile. This is
  allowed by the requirements; see the outbox item below.
- **Idempotency crash window.** The device insert and the idempotency record
  are separate statements. If a replica dies exactly between them, a retry
  after `IDEMPOTENCY_LOCK_TIMEOUT` creates a second device.
- **Malformed query encoding.** Go drops query parameters with invalid percent
  encoding (for example `%%%`), so such a filter is ignored rather than
  rejected with `422`.
- **Docs page uses a CDN.** `/docs` loads Swagger UI from unpkg; offline
  environments can still use `/openapi.yaml`.
- **Shared-secret tokens are for development.** Production must use
  `AUTH_JWKS_URL`; the service logs a warning when it runs with a secret.

## Future improvements

- Write the idempotency record in the same transaction as the device insert
  to close the crash window above.
- Transactional outbox with a durable broker (for example NATS JetStream or
  Kafka) and `Last-Event-ID` resume, for guaranteed event delivery.
- Rate limiting and quotas per token subject.
- Device ownership or tenancy, with authorization beyond scopes.
- Audit trail of changes, and soft delete.
- Kubernetes manifests or a Helm chart (probes, resources, disruption
  budget, autoscaling) and migrations as a separate job
  (`DATABASE_AUTO_MIGRATE=false`).
- Contract tests generated from the OpenAPI document and fuzzing of the
  request decoder.
- Self-hosted documentation assets instead of the CDN.

## Further documentation

| Document | Content |
|---|---|
| [`DEVELOPMENT.md`](DEVELOPMENT.md) | Setting up from scratch, configuration reference, daily commands, maintenance tasks, troubleshooting |
| [`docs/architecture.md`](docs/architecture.md) | Components, layers, domain rules and decision records |
| [`docs/flows.md`](docs/flows.md) | Sequence diagrams for every use case |
| [`docs/delivery-plan.md`](docs/delivery-plan.md) | Phases, exit criteria and acceptance tracking |
| [`api/openapi.yaml`](api/openapi.yaml) | OpenAPI 3.1 contract |
