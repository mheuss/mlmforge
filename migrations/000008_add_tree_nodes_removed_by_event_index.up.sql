-- Indexes tree_nodes on (tree_id, removed_by_event_id).
-- Partial: rows with a null removed_by_event_id are not indexed.
CREATE INDEX idx_tree_nodes_tree_removed_by_event
    ON tree_nodes(tree_id, removed_by_event_id)
    WHERE removed_by_event_id IS NOT NULL;
