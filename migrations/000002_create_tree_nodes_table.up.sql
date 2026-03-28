CREATE TABLE tree_nodes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tree_id      UUID NOT NULL,
    user_id      UUID NOT NULL,
    parent_id    UUID,
    sponsor_id   UUID,
    position     INT,
    depth        INT NOT NULL,
    enrolled_at  TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_tree_nodes_tree_user ON tree_nodes(tree_id, user_id)
    WHERE removed_at IS NULL;

CREATE INDEX idx_tree_nodes_tree_parent_active ON tree_nodes(tree_id, parent_id)
    WHERE removed_at IS NULL;

CREATE INDEX idx_tree_nodes_tree_sponsor_active ON tree_nodes(tree_id, sponsor_id)
    WHERE removed_at IS NULL;

CREATE INDEX idx_tree_nodes_depth ON tree_nodes(tree_id, depth);

CREATE INDEX idx_tree_nodes_user ON tree_nodes(user_id);
