CREATE TABLE IF NOT EXISTS volumes (
    id         TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    size_mb    INTEGER     NOT NULL,
    state      TEXT        NOT NULL DEFAULT 'pending',
    backend    TEXT        NOT NULL,
    node_id    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_volumes_state ON volumes(state);
CREATE INDEX IF NOT EXISTS idx_volumes_name  ON volumes(name);

CREATE TABLE IF NOT EXISTS volume_events (
    id         BIGSERIAL   PRIMARY KEY,
    volume_id  TEXT        NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
    event      TEXT        NOT NULL,
    from_state TEXT        NOT NULL,
    to_state   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_volume_events_volume_id ON volume_events(volume_id);
