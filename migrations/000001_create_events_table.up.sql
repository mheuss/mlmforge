CREATE TABLE IF NOT EXISTS events (
    global_position BIGSERIAL    PRIMARY KEY,
    id              UUID         NOT NULL UNIQUE,
    stream          TEXT         NOT NULL,
    type            TEXT         NOT NULL,
    version         BIGINT       NOT NULL,
    payload         JSONB        NOT NULL,
    metadata        JSONB,
    timestamp       TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT events_stream_version_key UNIQUE (stream, version)
);

CREATE INDEX IF NOT EXISTS idx_events_category
    ON events (split_part(stream, '-', 1), global_position);
