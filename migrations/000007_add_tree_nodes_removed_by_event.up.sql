-- Identifies which removal event tombstoned a row. Null while the row is
-- active. HEU-811.
ALTER TABLE tree_nodes ADD COLUMN removed_by_event_id UUID;
