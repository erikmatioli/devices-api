# Usage flows

These sequence diagrams show how each request moves through the system. They
complement the contract in [`api/openapi.yaml`](../api/openapi.yaml) and the
decisions in [`architecture.md`](architecture.md).

## 1. Typical client journey

```mermaid
flowchart TD
    A[Obtain a JWT with the needed scopes] --> B[Open the event stream<br/>GET /v1/devices/events]
    A --> C[Create a device<br/>POST /v1/devices + Idempotency-Key]
    C --> D[Read it<br/>GET /v1/devices/:id → ETag]
    D --> E[Change state to in-use<br/>PATCH + If-Match]
    E --> F{Rename while in use?}
    F -- yes --> G[409 Conflict]
    F -- no --> H[Set state to available, then rename]
    E -. state changed .-> B
    H --> I[Delete<br/>DELETE /v1/devices/:id]
    I --> J[204 No Content]
```

## 2. Request pipeline (all endpoints)

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant M as Middleware chain
    participant H as Handler
    participant S as Service
    participant R as Repository
    participant DB as PostgreSQL

    C->>M: HTTP request
    M->>M: assign request id, start span, start timer
    M->>M: verify JWT (iss, aud, exp, nbf) and scope
    alt invalid or missing token
        M-->>C: 401 problem+json
    else missing scope
        M-->>C: 403 problem+json
    end
    M->>H: authenticated request
    H->>H: decode body (1 MB limit, unknown fields rejected)
    H->>H: validate fields
    alt validation errors
        H-->>C: 422 problem+json with errors[]
    end
    H->>S: use case call
    S->>R: repository call
    R->>DB: SQL
    DB-->>R: rows
    R-->>S: domain object / domain error
    S-->>H: result
    H-->>C: response (JSON or problem+json)
    M->>M: record metrics, write access log, end span
```

## 3. Create with an idempotency key

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant I as Idempotency middleware
    participant H as Create handler
    participant DB as PostgreSQL

    C->>I: POST /v1/devices<br/>Idempotency-Key: k1
    I->>DB: INSERT idempotency_keys (subject, k1, body_hash, status=processing)<br/>ON CONFLICT DO NOTHING
    alt key is new
        I->>H: continue
        H->>DB: INSERT devices
        H-->>I: 201 + body
        I->>DB: UPDATE idempotency_keys SET response, status=completed
        I-->>C: 201 Created, Location
    else key exists, same hash, completed
        I-->>C: stored response + Idempotent-Replayed: true
    else key exists, different hash
        I-->>C: 422 key reused with a different payload
    else key exists, still processing
        I-->>C: 409 request in progress
    end
```

If the handler fails with a server error or panics, the key row is removed,
so a retry can run again. Client errors (4xx) are recorded and replayed like
successes. A row stuck in `processing` for longer than the lock timeout
(the replica died mid-request) can be claimed by the next request.

## 4. Update (PUT or PATCH) with a state change

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant S as Service
    participant DB as PostgreSQL
    participant L as Listener (any replica)
    participant HB as Event hub
    participant SUB as SSE subscribers

    C->>S: PATCH /v1/devices/{id}<br/>{"state":"in-use"}, If-Match: "3"
    S->>DB: BEGIN
    S->>DB: SELECT … FROM devices WHERE id=$1 FOR UPDATE
    alt not found
        S-->>C: 404
    else version differs from If-Match
        S-->>C: 412
    end
    S->>S: apply patch to domain entity<br/>(creation_time read-only, name/brand locked if stored state is in-use)
    alt domain rule violated
        S->>DB: ROLLBACK
        S-->>C: 409 or 422
    end
    S->>DB: UPDATE devices SET …, version = version + 1
    opt state changed
        S->>DB: SELECT pg_notify('device_state_changed', payload)
    end
    S->>DB: COMMIT
    DB-->>L: NOTIFY delivered only after commit
    S-->>C: 200 + ETag: "4"
    L->>HB: event
    HB-->>SUB: event: device.state_changed
```

If the transaction rolls back, PostgreSQL discards the notification, so
subscribers never see a change that was not persisted.

## 5. Delete

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant S as Service
    participant DB as PostgreSQL

    C->>S: DELETE /v1/devices/{id}
    S->>DB: DELETE FROM devices WHERE id=$1 AND state <> 'in-use'
    alt 1 row affected
        S-->>C: 204
    else 0 rows
        S->>DB: SELECT state FROM devices WHERE id=$1
        alt row exists (in-use)
            S-->>C: 409 device in use
        else no row
            S-->>C: 404
        end
    end
```

The conditional `DELETE` is atomic. The follow-up `SELECT` only chooses the
error code, so it cannot let an `in-use` device be removed.

## 6. Listing with filters and pagination

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant S as Service
    participant DB as PostgreSQL

    C->>S: GET /v1/devices?state=available&brand=acme&limit=20
    S->>DB: SELECT … WHERE state=$1 AND lower(brand)=lower($2)<br/>ORDER BY creation_time, id LIMIT 21
    DB-->>S: up to 21 rows
    S-->>C: 200 {data:[20 items], next_cursor:"…"}
    C->>S: GET /v1/devices?state=available&brand=acme&limit=20&cursor=…
    S->>DB: … AND (creation_time, id) > ($3, $4) …
    S-->>C: 200 {data:[…], next_cursor:null}
```

The query fetches one extra row to know whether another page exists, without
running a separate `COUNT`.

## 7. Event stream

```mermaid
sequenceDiagram
    autonumber
    participant SUB as Subscriber
    participant E as Events handler
    participant HB as Event hub

    SUB->>E: GET /v1/devices/events<br/>Accept: text/event-stream
    E->>HB: register (buffered channel)
    E-->>SUB: 200, retry: 3000
    loop every 15 s
        E-->>SUB: : keep-alive
    end
    HB-->>E: device.state_changed
    E-->>SUB: id / event / data
    alt subscriber too slow (buffer full)
        HB->>E: drop subscription
        E-->>SUB: stream closed
    end
    SUB--xE: disconnect
    E->>HB: unregister
```

Example event:

```
id: 0192f7a4-6c2e-7c41-9a0b-3f5d8e2a1b77
event: device.state_changed
data: {"event_id":"0192f7a4-6c2e-7c41-9a0b-3f5d8e2a1b77","device_id":"0192f7a1-1d3b-7e55-8c11-2a9f4b6c0d12","previous_state":"available","new_state":"in-use","occurred_at":"2026-10-31T14:05:09.123Z"}
```
