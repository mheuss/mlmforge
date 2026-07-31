-- One active claim per (tree, parent, position). Two rows disagreeing about
-- who occupies a slot cannot both be projections of a consistent event
-- stream, and reload preflight refuses the whole tree if both land (HEU-553).
-- position IS NOT NULL exempts unilevel nodes and roots. removed_at IS NULL
-- exempts soft-deleted history (ADR-023).
CREATE UNIQUE INDEX idx_tree_nodes_tree_parent_position_active
    ON tree_nodes(tree_id, parent_id, position)
    WHERE removed_at IS NULL AND position IS NOT NULL;
