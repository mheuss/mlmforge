//! Streamline public types.
//!
//! Result types returned by StreamlineEngine operations.
//! All types derive Serialize/Deserialize for snapshot persistence.

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// Result of adding a member to a stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AddMemberResult {
    /// The stream the member was placed in.
    pub stream_id: u32,
    /// The position (depth) in the stream.
    pub position: usize,
}

/// Result of expanding streams for a user.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExpansionResult {
    /// IDs of newly created streams.
    pub new_stream_ids: Vec<u32>,
}

/// Result of freezing/unfreezing streams.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FreezeResult {
    /// Streams that were frozen.
    pub frozen: Vec<u32>,
    /// Streams that were unfrozen.
    pub unfrozen: Vec<u32>,
    /// Streams that were newly created.
    pub created: Vec<u32>,
    /// Streams that were destroyed (when freeze_on_demotion is false).
    pub destroyed: Vec<u32>,
}

/// Result of removing a member.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RemoveMemberResult {
    /// Streams the member was removed from.
    pub removed_from: Vec<u32>,
}

/// Summary of a single stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamSummary {
    pub id: u32,
    pub owner_id: Uuid,
    pub member_count: usize,
    pub frozen: bool,
    pub created_at: i64,
}

/// A user's position in a specific stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StreamPosition {
    pub stream_id: u32,
    pub position: usize,
    pub frozen: bool,
}

/// Info about a member's positions across all streams.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MemberInfo {
    pub streams: Vec<StreamPosition>,
}
