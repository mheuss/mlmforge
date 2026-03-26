//! Streamline-specific errors.

use uuid::Uuid;

/// Errors returned by streamline operations.
///
/// Every fallible operation returns `Result<T, StreamlineError>`.
/// Input errors (missing member, frozen stream, duplicate) are
/// always reported through this enum, never through panics.
#[derive(Debug, Clone, PartialEq, thiserror::Error)]
pub enum StreamlineError {
    #[error("member already exists: {0}")]
    MemberAlreadyExists(Uuid),

    #[error("member not found: {0}")]
    MemberNotFound(Uuid),

    #[error("sponsor not found: {0}")]
    SponsorNotFound(Uuid),

    #[error("stream not found: {0}")]
    StreamNotFound(u32),

    #[error("stream {0} is frozen and cannot accept new members")]
    StreamFrozen(u32),

    #[error("sponsor {0} does not own stream {1}")]
    SponsorDoesNotOwnStream(Uuid, u32),

    #[error("no streams available for placement")]
    NoStreamsAvailable,

    #[error("user {0} has no owned streams")]
    NoOwnedStreams(Uuid),

    #[error("stream choice override not allowed (enrollment_stream_choice is disabled)")]
    StreamChoiceNotAllowed,

    #[error("tree operation failed: {0}")]
    TreeError(String),
}
