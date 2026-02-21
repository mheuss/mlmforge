use uuid::Uuid;

/// Deterministic UUID for tests. The byte value makes failures readable.
pub fn test_uuid(n: u8) -> Uuid {
    Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
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
