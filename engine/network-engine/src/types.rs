use std::collections::HashMap;
use uuid::Uuid;

/// Computed snapshot of a user's position in the tree.
///
/// Unlike `Node`, this is an owned output type built on demand.
/// It includes derived data (branch counts, child count) that
/// is not stored on the node itself.
#[derive(Debug, Clone)]
pub struct TreePosition {
    pub user_id: Uuid,
    pub parent_user_id: Option<Uuid>,
    pub position: usize,
    pub depth: u32,
    pub child_count: usize,
    /// Descendant count per child position. Key is the child's
    /// index in the parent's children Vec.
    pub branch_counts: HashMap<usize, usize>,
    pub enrolled_at: i64,
}
