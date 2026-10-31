-- Responses recorded for requests sent with an Idempotency-Key header.
-- A row without response_status is a request still being processed.

CREATE TABLE idempotency_keys (
    subject          text        NOT NULL,
    key              text        NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    fingerprint      bytea       NOT NULL,
    response_status  integer     CHECK (response_status BETWEEN 100 AND 599),
    response_headers jsonb,
    response_body    bytea,
    started_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL,
    PRIMARY KEY (subject, key)
);

-- Supports the periodic purge of expired records.
CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);
