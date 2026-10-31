# Delivery plan

The work is split into phases that each leave the project compiling, tested and
runnable. Every phase is delivered as a series of small commits that follow
[Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`,
`test:`, `docs:`, `build:`, `ci:`, `refactor:`, `chore:`). Each commit body
explains what changed and why.

## Working agreement

- The contract comes first: `api/openapi.yaml` is updated before or together
  with the handler it describes.
- A phase is done when `make lint test` passes, the containers start with
  `docker compose up`, and the documentation reflects the change.
- Tests are written together with the code they cover. Domain and handler logic
  are covered by unit tests, and persistence by integration tests against a
  real PostgreSQL.
- Coverage target: at least 80% overall, and close to 100% for the domain
  package.

## Phases

| # | Phase | Scope | Exit criteria |
|---|---|---|---|
| 0 | Design | Architecture and ADRs, usage flows, delivery plan, OpenAPI draft, README skeleton | Design reviewed and approved |
| 1 | Foundation | `go.mod`, package layout, env config, `slog` logger, Makefile, golangci-lint, multi-stage Dockerfile, compose with PostgreSQL, `/healthz` and `/readyz`, graceful shutdown, CI workflow | `docker compose up` serves health endpoints; CI is green |
| 2 | Domain | `Device` entity, `State` type, creation, update and patch rules, domain errors | Table-driven unit tests cover every rule |
| 3 | Persistence | Migrations (table, indexes), pgx repository, transactions with row locks, keyset pagination | Integration tests with testcontainers pass |
| 4 | HTTP API | Handlers for all endpoints, input validation (`422`), problem+json, ETag / If-Match, served OpenAPI and docs page | `httptest` suite covers success and error paths; contract matches responses |
| 5 | Security | JWT middleware (JWKS and HS256), scope checks per route, `cmd/tokengen` | `401` and `403` paths tested; README shows how to get a token |
| 6 | Idempotency | `Idempotency-Key` middleware, key store, expiry job | Replay, payload mismatch and concurrent-request tests pass |
| 7 | Events | `pg_notify` in the update transaction, LISTEN loop with reconnect, hub, SSE endpoint | End-to-end test proves an event after commit and no event after rollback |
| 8 | Observability | OTel traces and metrics, Prometheus `/metrics`, log correlation, compose profile with collector, Jaeger and Prometheus | Traces visible in Jaeger; metrics scraped |
| 9 | Hardening and handover | Coverage report, k6 smoke load test, final README, `DEVELOPMENT.md`, future improvements | All acceptance criteria checked |

## Acceptance criteria tracking

| Criterion from the brief | Phase |
|---|---|
| Create, full and partial update, fetch one, fetch all, by brand, by state, delete | 3, 4 |
| Notify subscribers with device id, previous and new state | 7 |
| Creation time cannot be updated | 2, 4 |
| Name and brand locked while in use | 2, 3 |
| In-use devices cannot be deleted | 3 |
| Notifications only after the change is persisted | 7 |
| Compiles and runs | 1 onwards |
| Reasonable test coverage | every phase, reported in 9 |
| API documented | 0, 4 |
| Persistent database | 3 |
| Containerized | 1 |
| Git repository | 0 |
| README with all instructions | 0, finalized in 9 |
| Go 1.23+ | 1 |
