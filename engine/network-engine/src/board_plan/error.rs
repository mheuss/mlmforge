use uuid::Uuid;

/// Errors returned by board plan operations.
///
/// Every fallible operation returns `Result<T, BoardPlanError>`.
/// Input errors (missing member, duplicate, invalid dimensions)
/// are always reported through this enum, never through panics.
#[derive(Debug, Clone, PartialEq, thiserror::Error)]
pub enum BoardPlanError {
    #[error("member already exists: {0}")]
    MemberAlreadyExists(Uuid),

    #[error("sponsor not found: {0}")]
    SponsorNotFound(Uuid),

    #[error("board not found: {0}")]
    BoardNotFound(Uuid),

    #[error("member not found: {0}")]
    MemberNotFound(Uuid),

    #[error("no boards have open positions")]
    NoBoardsAvailable,

    #[error("invalid dimensions {width}x{height}: {reason}")]
    InvalidDimensions {
        width: u8,
        height: u8,
        reason: String,
    },

    #[error("member not in displaced pool: {0}")]
    MemberNotDisplaced(Uuid),
}
