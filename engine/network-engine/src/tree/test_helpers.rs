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

/// Asserts the production check accepts this arena and slot map.
pub(crate) fn assert_live_nodes_are_slotted_once<C>(
    arena: &crate::tree::arena::Arena,
    slots: &std::collections::HashMap<crate::tree::node::NodeIndex, C>,
) where
    for<'a> &'a C: IntoIterator<Item = &'a Option<crate::tree::node::NodeIndex>>,
{
    assert_eq!(arena.check_every_live_node_is_slotted_once(slots), Ok(()));
}
