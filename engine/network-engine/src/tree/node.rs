use uuid::Uuid;

/// Index into the arena's node Vec.
///
/// Lightweight handle (one `usize`). Not a pointer.
/// Only meaningful within the tree that created it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct NodeIndex(pub(crate) usize);

/// A node in the tree arena.
///
/// Stores relationships as arena indices for cache-friendly traversal.
/// For unilevel trees, position equals the index in the parent's
/// `children` Vec. The first enrolled child is position 0.
///
/// Public fields (`user_id`, `depth`, `enrolled_at`) are the read-only
/// surface for consumers who receive `&Node` from traversal methods.
/// Structural fields (`parent`, `children`) are crate-internal because
/// they hold arena indices that are meaningless outside the tree.
#[derive(Debug, Clone)]
pub struct Node {
    pub user_id: Uuid,
    pub(crate) parent: Option<NodeIndex>,
    pub(crate) children: Vec<NodeIndex>,
    pub depth: u32,
    /// Unix timestamp in seconds when the user was enrolled.
    pub enrolled_at: i64,
}
