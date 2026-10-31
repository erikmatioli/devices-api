# Development guide

This guide is for developers who set up the project from scratch or maintain
it. For what the API does, see the [README](README.md). For why it is built
this way, see [`docs/architecture.md`](docs/architecture.md).

## 1. Prerequisites

| Tool | Version | Used for |
|---|---|---|
| Go | 1.25 or newer | Building and testing |
| Docker with Compose v2 | 24 or newer | PostgreSQL, the container image, integration tests |
| GNU Make | 4.x | Developer shortcuts (optional, every target is a plain command) |
| golangci-lint | 2.x | Linting and formatting |
| Git | any recent | Version control |

Go 1.25 is the minimum because the PostgreSQL driver (`pgx` v5.12) requires
it. The container image is built with the latest Go release.

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
| `HTTP_ADDR` | `:8080` | Listen address |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | Time allowed to read request headers |
| `HTTP_READ_TIMEOUT` | `10s` | Time allowed to read the whole request |
| `HTTP_WRITE_TIMEOUT` | `15s` | Time allowed to write the response |
| `HTTP_IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `SHUTDOWN_TIMEOUT` | `15s` | Grace period to drain requests on `SIGTERM` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `LOG_FORMAT` | `json` | `json` for machines, `text` for local reading |

Compose also reads `API_PORT` and `POSTGRES_PORT` (both host ports) and
`VERSION` (image build argument).

## 4. Everyday commands

| Command | What it does |
|---|---|
| `make help` | List all targets |
| `make build` | Compile `./bin/devices-api` |
| `make run` | Run from source with `.env` |
| `make test` | Unit tests |
| `make cover` | Tests with coverage summary (`coverage.out`) |
| `make lint` | golangci-lint |
| `make fmt` | Format code (gofmt and goimports) |
| `make tidy` | `go mod tidy` and `go mod verify` |
| `make up` / `make down` | Start or stop the Compose stack |
| `make logs` | Follow API logs |
| `make docker-build` | Build the image tagged with the git version |

Before pushing, run `make fmt lint test`. CI runs the same checks plus an
image build.

## 5. Project layout

```
cmd/api/              entry point: wiring, HTTP server, graceful shutdown
internal/platform/    configuration and logger
internal/httpapi/     routes, middleware, health probes, responses
internal/postgres/    database pool and, later, repositories and migrations
api/openapi.yaml      API contract
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
| `-race requires cgo` | Install a C compiler, or run without `-race` (`make test` does this on Windows). |
| `go` or `make` not found after installing | Open a new terminal so `PATH` is reloaded. |
