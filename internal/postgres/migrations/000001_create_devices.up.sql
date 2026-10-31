-- Devices and the indexes behind the list filters and keyset pagination.

CREATE TABLE devices (
    id            uuid        PRIMARY KEY,
    name          text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    brand         text        NOT NULL CHECK (char_length(brand) BETWEEN 1 AND 50),
    state         text        NOT NULL CHECK (state IN ('available', 'in-use', 'inactive')),
    creation_time timestamptz NOT NULL,
    version       bigint      NOT NULL DEFAULT 1 CHECK (version > 0)
);

-- Listing without filters and the pagination order.
CREATE INDEX devices_creation_time_id_idx ON devices (creation_time, id);

-- Brand filter is case insensitive.
CREATE INDEX devices_brand_creation_time_id_idx ON devices (lower(brand), creation_time, id);

CREATE INDEX devices_state_creation_time_id_idx ON devices (state, creation_time, id);

-- Defence in depth: the domain already rejects creation time changes, and
-- the database refuses them from any other client too.
CREATE FUNCTION devices_keep_creation_time() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.creation_time IS DISTINCT FROM OLD.creation_time THEN
        RAISE EXCEPTION 'creation_time cannot be updated'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER devices_keep_creation_time
    BEFORE UPDATE OF creation_time ON devices
    FOR EACH ROW EXECUTE FUNCTION devices_keep_creation_time();
