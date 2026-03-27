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

CREATE INDEX idx_tree_nodes_tree_user ON tree_nodes(tree_id, user_id)
    WHERE removed_at IS NULL;

CREATE INDEX idx_tree_nodes_parent ON tree_nodes(parent_id);

CREATE INDEX idx_tree_nodes_sponsor ON tree_nodes(sponsor_id);

CREATE INDEX idx_tree_nodes_depth ON tree_nodes(tree_id, depth);

CREATE INDEX idx_tree_nodes_user ON tree_nodes(user_id);
