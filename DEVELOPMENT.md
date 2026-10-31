# Development guide

This guide is for developers who set up the project from scratch or maintain
it. For what the API does, see the [README](README.md). For why it is built
this way, see [`docs/architecture.md`](docs/architecture.md).

## 1. Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Go | 1.25.11 or newer | Building and testing |
| Docker with Compose v2 | 24 or newer | PostgreSQL, the container image, integration tests |
| GNU Make | 4.x | Developer shortcuts (optional, every target is a plain command) |
| golangci-lint | 2.x | Linting and formatting |
| Git | any recent | Version control |

Go 1.25 is the minimum because the PostgreSQL driver (`pgx` v5.12) requires
it, and the test dependencies pin the 1.25.11 patch release. With the default
`GOTOOLCHAIN=auto`, an older 1.25.x downloads the right toolchain by itself.
The container image is built with the latest Go release.

### Installing the tools

**macOS (Homebrew)**

```bash
brew install go golangci-lint make
brew install --cask docker
```

**Linux (Debian/Ubuntu)**

Install Go from <https://go.dev/dl/> (distribution packages are often
outdated), then:

```bash
sudo apt-get install -y make
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v2.15.0
```

Install Docker Engine following <https://docs.docker.com/engine/install/>.

**Windows (winget)**

```powershell
winget install GoLang.Go
winget install GolangCI.golangci-lint
winget install ezwinports.make
winget install Docker.DockerDesktop
```

Open a new terminal afterwards so the updated `PATH` is picked up. The race
detector (`-race`) needs cgo and a C compiler, so `make test` skips it on
Windows; CI always runs with it.

Check the setup:

```bash
go version
docker compose version
golangci-lint version
```

## 2. First run

```bash
git clone <repository-url> devices-api
cd devices-api
cp .env.example .env
make up
```

`make up` builds the image and starts the API and PostgreSQL. Check that it
is running:

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

`/readyz` returns `503` while the database is unreachable.

### Running the API from source

To iterate without rebuilding the image, start only the database and run the
binary on the host:

```bash
docker compose up -d postgres
make run
```

`make run` loads the variables from `.env`. Stop everything with `make down`.
The database volume is kept; remove it with `docker compose down -v`.

## 3. Configuration

All settings come from environment variables and are validated at startup.
An invalid value stops the process with a message that lists every problem.

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | **required** | PostgreSQL connection string |
| `DATABASE_MAX_CONNS` | `10` | Maximum pool size |
| `DATABASE_MIN_CONNS` | `2` | Connections kept open when idle |
| `DATABASE_CONNECT_TIMEOUT` | `5s` | Timeout to connect and to the startup ping |
| `DATABASE_AUTO_MIGRATE` | `true` | Apply pending schema migrations at startup |
| `AUTH_ISSUER` | **required** | Expected `iss` claim |
| `AUTH_AUDIENCE` | **required** | Expected `aud` claim |
| `AUTH_HS256_SECRET` | — | Shared secret (at least 32 bytes) for local tokens |
| `AUTH_JWKS_URL` | — | Identity provider key set; use instead of the secret in production |
| `AUTH_JWKS_REFRESH` | `15m` | How often the key set is fetched again |
| `AUTH_CLOCK_SKEW` | `30s` | Tolerance on `exp`, `nbf` and `iat` |
| `IDEMPOTENCY_TTL` | `24h` | How long an `Idempotency-Key` response can be replayed |
| `IDEMPOTENCY_LOCK_TIMEOUT` | `1m` | After this, a request still in progress is treated as abandoned; must exceed `HTTP_WRITE_TIMEOUT` |
| `IDEMPOTENCY_PURGE_INTERVAL` | `15m` | How often expired records are deleted |
| `HTTP_ADDR` | `:8080` | Listen address |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | Time allowed to read request headers |
| `HTTP_READ_TIMEOUT` | `10s` | Time allowed to read the whole request |
| `HTTP_WRITE_TIMEOUT` | `15s` | Time allowed to write the response |
| `HTTP_IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `SHUTDOWN_TIMEOUT` | `15s` | Grace period to drain requests on `SIGTERM` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `LOG_FORMAT` | `json` | `json` for machines, `text` for local reading |

Exactly one of `AUTH_HS256_SECRET` and `AUTH_JWKS_URL` must be set. The
service logs a warning at startup when it runs with a shared secret.

Compose also reads `API_PORT` and `POSTGRES_PORT` (both host ports) and
`VERSION` (image build argument).

### Access tokens

Every `/v1` endpoint needs a bearer token with the right scope:

| Scope | Operations |
|---|---|
| `devices:read` | list and fetch devices, event stream |
| `devices:write` | create, replace, patch |
| `devices:delete` | delete |

Locally, mint a token signed with the shared secret from `.env`:

```bash
make token                                   # all scopes, 1 hour
make token SCOPE="devices:read" TTL=10m      # read-only
docker compose run --rm tokengen -scope "devices:read devices:write"
```

Then call the API:

```bash
TOKEN=$(make -s token)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/v1/devices
```

In `/docs`, use **Authorize** and paste the token. The `tokengen` binary is
built into a separate image target and is never shipped in the API image.

In production, point `AUTH_JWKS_URL` at the identity provider (for example
`https://<tenant>/.well-known/jwks.json`) and remove the secret. Keys are
cached and refreshed every `AUTH_JWKS_REFRESH`, and a token signed with an
unknown key id triggers a rate-limited refresh, so key rotation needs no
restart.

## 4. Everyday commands

| Command | What it does |
|---|---|
| `make help` | List all targets |
| `make build` | Compile `./bin/devices-api` |
| `make run` | Run from source with `.env` |
| `make test` | Unit tests (no Docker needed) |
| `make test-integration` | Unit and integration tests against a PostgreSQL container |
| `make cover` | All tests with coverage summary (`coverage.out`) |
| `make lint` | golangci-lint |
| `make fmt` | Format code (gofmt and goimports) |
| `make tidy` | `go mod tidy` and `go mod verify` |
| `make token` | Print a development access token (`SUB`, `SCOPE`, `TTL`) |
| `make up` / `make down` | Start or stop the Compose stack |
| `make logs` | Follow API logs |
| `make docker-build` | Build the image tagged with the git version |

Before pushing, run `make fmt lint test-integration`. CI runs the same
checks plus an image build.

### Tests

- Unit tests have no build tag and need nothing but Go.
- Integration tests carry the `integration` build tag. They start a
  disposable `postgres:16-alpine` container through testcontainers, apply the
  migrations once and truncate tables between tests. To reuse an existing
  database instead (faster, or where Docker-in-Docker is not available), set
  `TEST_DATABASE_URL`:

  ```bash
  TEST_DATABASE_URL=postgres://devices:devices@localhost:5432/devices?sslmode=disable make test-integration
  ```

  The tests truncate the tables, so never point this at a database whose data
  matters.

## 5. Project layout

```
cmd/api/              entry point: wiring, HTTP server, graceful shutdown
cmd/tokengen/         development helper that mints local access tokens
internal/auth/        JWT verification (HS256 or JWKS) and the request principal
internal/device/      domain model and business rules (no I/O)
internal/app/         use cases and the ports they need (repository interface)
internal/app/apptest/ in-memory repository used by service and handler tests
internal/platform/    configuration and logger
internal/httpapi/     routes, middleware, request decoding, problem responses, docs
internal/postgres/    pool, repository and embedded SQL migrations
api/openapi.yaml      API contract, embedded and served at /openapi.yaml and /docs
docs/                 architecture, flows and delivery plan
```

Dependencies point inwards. `internal/httpapi` and `internal/postgres` never
import each other; `cmd/api` wires them together.

## 6. Maintenance

### Adding a configuration variable

1. Add the field to the matching struct in `internal/platform/config`.
2. Read it in `FromLookup` with a default and validation.
3. Cover it in `config_test.go`.
4. Document it in the table above and in `.env.example`.

### Adding or changing an endpoint

1. Describe it in `api/openapi.yaml` first, including every error response.
2. Put business rules in `internal/device` and orchestration in
   `internal/app`; handlers only decode, validate the shape of the input and
   map errors.
3. Decode bodies with `readObject` and `fieldReader` so unknown fields, wrong
   types and nulls produce the same `422` format as the other endpoints.
4. Map new domain errors to a status in `writeError` (`respond.go`).
5. Add handler tests in `internal/httpapi` (they run against the in-memory
   repository) and repository tests when SQL changes.
6. Lint the contract: `npx @redocly/cli lint api/openapi.yaml`.

### Changing the database schema

Migrations live in `internal/postgres/migrations` and are embedded in the
binary. With `DATABASE_AUTO_MIGRATE=true` (the default) they run at startup;
an advisory lock keeps concurrent replicas from applying them twice.

1. Add a pair of files with the next sequence number:
   `000002_<description>.up.sql` and `000002_<description>.down.sql`.
2. Never edit a migration that has already been released; add a new one.
3. Prefer changes that old and new binaries can both run against (add
   columns as nullable or with defaults, backfill, then tighten), so rolling
   deployments keep working.
4. Run `make test-integration`; the suite applies every migration from an
   empty database.

To manage migrations by hand (for example to roll back), use the
[migrate CLI](https://github.com/golang-migrate/migrate/tree/master/cmd/migrate)
with the same files:

```bash
migrate -path internal/postgres/migrations -database "$DATABASE_URL" down 1
```

### Updating dependencies

```bash
go get -u ./...
make tidy test lint
```

If a dependency raises the minimum Go version, `go mod tidy` updates the `go`
line in `go.mod`. Update the CI matrix and this guide to match.

### Releasing an image

```bash
git tag v0.1.0
make docker-build
```

The binary reports its version in every log record (`version` field).

## 7. Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `bind: address already in use` on 5432 or 8080 | Another service uses the port. Set `POSTGRES_PORT` or `API_PORT` in `.env` and update `DATABASE_URL`. |
| `fatal: invalid configuration: DATABASE_URL is required` | `.env` is missing or not loaded. Run `cp .env.example .env`, or export the variable. |
| `ping database: … connection refused` | PostgreSQL is not running or still starting. Run `docker compose up -d postgres` and wait for the health check. |
| Integration tests fail with `Cannot connect to the Docker daemon` | Start Docker, or set `TEST_DATABASE_URL` to an existing PostgreSQL. |
| `Dirty database version N` at startup | A migration failed halfway. Fix the cause, then run `migrate ... force N-1` and restart. |
| `-race requires cgo` | Install a C compiler, or run without `-race` (`make test` does this on Windows). |
| `409` "Request in progress" on a retry | The first request with that `Idempotency-Key` has not finished. Wait for `Retry-After` and retry. |
| `422` "Idempotency key reused" | The key was already used with a different body. Use a new key per distinct request. |
| `401` with `error="invalid_token"` | The token expired, or it was minted with a different secret, issuer or audience than the API uses. Mint a new one with the same `.env`. |
| `403` with `error="insufficient_scope"` | The token lacks the scope named in the header. Mint one with `SCOPE=...`. |
| `go` or `make` not found after installing | Open a new terminal so `PATH` is reloaded. |
