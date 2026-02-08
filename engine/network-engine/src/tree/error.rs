use uuid::Uuid;

/// Errors returned by tree operations.
///
/// Every fallible operation returns `Result<T, TreeError>`.
/// No panics. If a node is not found or an operation is invalid,
/// the error variant carries enough context to diagnose the problem.
#[derive(Debug, thiserror::Error)]
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
