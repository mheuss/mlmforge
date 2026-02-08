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
#[derive(Debug, Clone)]
pub struct Node {
    pub user_id: Uuid,
    pub(crate) parent: Option<NodeIndex>,
    pub(crate) children: Vec<NodeIndex>,
    pub depth: u32,
    pub enrolled_at: i64,
}
