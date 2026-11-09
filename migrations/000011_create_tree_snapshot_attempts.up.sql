-- One row per tree: an attempt's version, whether it stored a snapshot, and what it observed.
CREATE TABLE tree_snapshot_attempts (
    tree_id      UUID PRIMARY KEY,
    version      BIGINT NOT NULL CHECK (version >= 1),
    stored       BOOLEAN NOT NULL,
    observed     TEXT NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((stored AND observed = '') OR (NOT stored AND observed <> ''))
);
