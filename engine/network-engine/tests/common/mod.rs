use uuid::Uuid;

/// Generates a deterministic UUID from an index.
///
/// Uses big-endian byte representation so each index produces a unique,
/// readable UUID. Shared across all integration/property test files.
pub fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_be_bytes();
    Uuid::from_bytes(bytes)
}
