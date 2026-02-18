use uuid::Uuid;

/// Errors returned by tree operations.
///
/// Every fallible operation returns `Result<T, TreeError>`.
/// Input errors (missing user, duplicate, out-of-range position)
/// are always reported through this enum, never through panics.
///
/// Internal corruption (e.g. a node missing from its parent's
/// children list) triggers a panic via `expect`. This is deliberate:
/// corruption means every subsequent operation is suspect, so
/// fail-fast is the safest response.
#[derive(Debug, PartialEq, thiserror::Error)]
pub enum TreeError {
    #[error("user {0} not found in tree")]
    UserNotFound(Uuid),

    #[error("user {0} already exists in tree")]
    UserAlreadyExists(Uuid),

    #[error("position {position} out of range for user {user_id} (has {child_count} children)")]
    PositionOutOfRange {
        user_id: Uuid,
        position: usize,
        child_count: usize,
    },

    #[error("cannot remove user {0}: has {1} children (must remove children first)")]
    HasChildren(Uuid, usize),

    #[error("tree already has a root node")]
    RootAlreadyExists,
}
