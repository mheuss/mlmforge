use uuid::Uuid;

/// Deterministic UUID for tests. The byte value makes failures readable.
pub fn test_uuid(n: u8) -> Uuid {
    Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF])
}

/// Deterministic UUID from a u16. Needed for tests with more than
/// 255 nodes (deep chain, wide fan).
///
/// The high byte is always 0xFF to avoid collisions with `Uuid::nil()`,
/// which is used as the tombstone sentinel in the arena.
pub fn test_uuid_u16(n: u16) -> Uuid {
    let bytes = n.to_le_bytes();
    Uuid::from_bytes([
        bytes[0], bytes[1], 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF,
    ])
}

/// Asserts every live non-root node is a slot child exactly once.
///
/// Takes the arena and the slot map rather than a tree, because each tree's
/// fields are private to its own module.
pub(crate) fn assert_live_nodes_are_slotted_once<C>(
    arena: &crate::tree::arena::Arena,
    slots: &std::collections::HashMap<crate::tree::node::NodeIndex, C>,
) where
    for<'a> &'a C: IntoIterator<Item = &'a Option<crate::tree::node::NodeIndex>>,
{
    use crate::tree::node::NodeIndex;
    let mut seen: std::collections::HashMap<NodeIndex, usize> = std::collections::HashMap::new();
    for children in slots.values() {
        for child in children.into_iter().flatten() {
            *seen.entry(*child).or_default() += 1;
        }
    }
    for (slot, node) in arena.nodes.iter().enumerate() {
        if node.user_id == Uuid::nil() || arena.root == Some(NodeIndex(slot)) {
            continue;
        }
        assert_eq!(
            seen.get(&NodeIndex(slot)).copied().unwrap_or(0),
            1,
            "live node {} at slot {slot} should be a slot child exactly once",
            node.user_id
        );
    }
}
