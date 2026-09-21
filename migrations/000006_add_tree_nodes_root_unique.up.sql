-- One active root per tree. Two depth-0 rows cannot both be projections of a
-- consistent event stream, and reload preflight refuses the whole tree if both
-- land (HEU-810). removed_at IS NULL exempts soft-deleted history (ADR-023),
-- so a removed root can be replaced.
CREATE UNIQUE INDEX idx_tree_nodes_tree_root_active
    ON tree_nodes(tree_id)
    WHERE removed_at IS NULL AND depth = 0;
