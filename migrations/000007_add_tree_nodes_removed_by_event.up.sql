-- Identifies which removal event tombstoned a row. Null while the row is
-- active, because only the soft delete writes it. A reconcile compares a
-- removal event's id against this column rather than against tree_nodes.id,
-- which holds a placing event's id and is a different kind of identifier
-- (HEU-811, decided on HEU-820).
ALTER TABLE tree_nodes ADD COLUMN removed_by_event_id UUID;
