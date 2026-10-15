-- One row per tree: the latest engine snapshot and the stream version it was taken at.
CREATE TABLE tree_snapshots (
    tree_id   UUID PRIMARY KEY,
    version   BIGINT NOT NULL CHECK (version >= 1),
    tree_type TEXT NOT NULL CHECK (tree_type IN ('unilevel', 'binary', 'matrix')),
    data      BYTEA NOT NULL,
    taken_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
