# Devices API

REST API written in Go to persist and manage device resources, with real-time
notifications of device state changes.

> **Status:** in progress. The device endpoints, JWT authorization,
> idempotency keys and state change notifications are available; telemetry
> follows the
> [delivery plan](docs/delivery-plan.md).

## Features

- Create, fully replace, partially update (JSON Merge Patch), fetch and delete
  devices.
- List devices with `brand` and `state` filters and cursor pagination.
- Domain rules enforced atomically in the database:
  - `creation_time` is read-only.
  - `name` and `brand` are locked while a device is `in-use`.
  - Devices that are `in-use` cannot be deleted.
- Server-Sent Events stream with the device id, previous state and new state.
  Events are emitted only after the change is committed.
- JWT authentication with per-operation scopes.
- `Idempotency-Key` support for safe retries on create.
- Optimistic concurrency with `ETag` / `If-Match`.
- RFC 9457 problem details, with `422` for validation errors.
- Structured JSON logs, OpenTelemetry traces and metrics, and a Prometheus
  endpoint.
- Container image with PostgreSQL in Docker Compose.

## Documentation

| Document | Content |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Components, layers, domain rules and decision records |
| [`docs/flows.md`](docs/flows.md) | Sequence diagrams for every use case |
| [`docs/delivery-plan.md`](docs/delivery-plan.md) | Phases, exit criteria and acceptance tracking |
| [`api/openapi.yaml`](api/openapi.yaml) | OpenAPI 3.1 contract, also served by the API at `/openapi.yaml` and rendered at `/docs` |

## API overview

| Method | Path | Scope | Description |
|---|---|---|---|
| `POST` | `/v1/devices` | `devices:write` | Create a device |
| `GET` | `/v1/devices` | `devices:read` | List devices (`brand`, `state`, `limit`, `cursor`) |
| `GET` | `/v1/devices/{id}` | `devices:read` | Fetch a device |
| `PUT` | `/v1/devices/{id}` | `devices:write` | Fully update a device |
| `PATCH` | `/v1/devices/{id}` | `devices:write` | Partially update a device |
| `DELETE` | `/v1/devices/{id}` | `devices:delete` | Delete a device |
| `GET` | `/v1/devices/events` | `devices:read` | Stream state changes (SSE) |
| `GET` | `/healthz`, `/readyz` | public | Liveness and readiness |
| `GET` | `/metrics` | public | Prometheus metrics |
| `GET` | `/openapi.yaml`, `/docs` | public | Contract and interactive documentation |

## Getting started

Requirements: Docker with Compose v2. Building from source also needs Go 1.25
or newer.

```bash
cp .env.example .env
make up
curl http://localhost:8080/readyz
```

Every `/v1` endpoint requires a JWT with the matching scope. Mint a local
token and use it:

```bash
TOKEN=$(docker compose run --rm tokengen)
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"name":"Pixel 9","brand":"Google"}' \
     http://localhost:8080/v1/devices
```

Without `make`, run `docker compose --profile tools build` and then
`docker compose up -d`. Building first keeps the build output out of
`$TOKEN`. Interactive documentation is at <http://localhost:8080/docs> (use
**Authorize** with the token).

| Task | Command |
|---|---|
| Run unit tests | `make test` |
| Run unit and integration tests (Docker) | `make test-integration` |
| Coverage report | `make cover` |
| Lint | `make lint` |
| Stop the stack | `make down` |

The [development guide](DEVELOPMENT.md) covers tool installation,
configuration variables, the project layout, maintenance tasks and
troubleshooting.
