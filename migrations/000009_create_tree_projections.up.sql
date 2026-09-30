-- One row per tree: the stream version its tree_nodes rows reflect.
-- HEU-857.
CREATE TABLE tree_projections (
    tree_id           UUID PRIMARY KEY,
    projected_version BIGINT NOT NULL CHECK (projected_version >= 0),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
