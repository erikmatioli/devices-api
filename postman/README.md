# Postman collection

`devices-api.postman_collection.json` exercises every endpoint as an ordered
flow with assertions on status codes, headers, bodies and problem documents.
`local.postman_environment.json` points it at a local stack.

## Running it in Postman

1. Start the stack: `make up`.
2. In Postman, **Import** both files from this folder.
3. Select the **Devices API · local** environment.
4. Open the collection and choose **Run**. Keep the default order; requests
   share variables (device ids, ETags, cursor) from one to the next.

No token has to be pasted: the collection's pre-request script mints HS256
tokens from `authSecret`, `authIssuer` and `authAudience`, which match the
defaults in `.env.example`. If you changed `AUTH_*` in `.env`, change the
environment to the same values.

| Folder | What it checks |
|---|---|
| 00 · Operations | Liveness, readiness (database and listener), contract, metrics |
| 01 · Security | `401` without or with an invalid token, `403` without the scope |
| 02 · Device lifecycle | Create, idempotent replay and key reuse, get and `304`, list with cursor, by brand, by state, `PUT` with `If-Match`, stale `412`, `PATCH`, in-use rules (`409`), read-only creation time (`422`), delete and `404` |
| 03 · Validation and errors | Every `422` field code, `400`, `415`, `404` and `405` problem documents |
| 04 · Event stream (manual) | Opens the SSE stream; skipped in runs |

## Watching events

Send **04 · Event stream (manual) → Stream state changes (SSE)** on its own
and leave it open. In another tab, run folder **02** (or patch a device's
state): each committed state change appears as a `device.state_changed`
event. Collection runs skip this request because a stream never ends; set
the environment variable `runEventStream` to `true` to include it.

## Running it from the command line

```bash
make postman
```

This runs the same collection with Newman (Node.js required) against
`http://localhost:8080`; the exit code is non-zero if any assertion fails.
