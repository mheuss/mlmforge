use std::collections::HashMap;
use uuid::Uuid;

/// Computed snapshot of a user's position in the tree.
///
/// Unlike `Node`, this is an owned output type built on demand.
/// It includes derived data (downline counts, child count) that
/// is not stored on the node itself.
#[derive(Debug, Clone)]
pub struct TreePosition {
    pub user_id: Uuid,
    pub parent_user_id: Option<Uuid>,
    pub sponsor_user_id: Option<Uuid>,
    pub position: usize,
    pub depth: u32,
    pub child_count: usize,
    /// Downline count per child position. Key is the child's
    /// index in the parent's children Vec. Value is the number
    /// of descendants that child has, not including the child.
    ///
    /// This follows downline semantics: the starting node is
    /// excluded, just like `get_downline` and `count_downline`.
    /// For the total branch size including the child, use
    /// `count_branch`.
    pub downline_counts: HashMap<usize, usize>,
    /// Unix timestamp in seconds when the user was enrolled.
    pub enrolled_at: i64,
}
