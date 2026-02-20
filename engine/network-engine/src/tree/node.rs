use uuid::Uuid;

/// Index into the arena's node Vec.
///
/// Lightweight handle (one `usize`). Not a pointer.
/// Only meaningful within the tree that created it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct NodeIndex(pub(crate) usize);

/// A node in the tree arena.
///
/// Stores two sets of relationships as arena indices:
/// - Placement topology: `parent` / `children` — who is above/below in the tree.
/// - Sponsor topology: `sponsor` / `sponsored` — who recruited whom.
///
/// Both edge types use arena indices for cache-friendly traversal.
/// The tree stores sponsor edges as data for traversal but does not
/// use them to make placement decisions (decision 020).
///
/// Public fields (`user_id`, `depth`, `enrolled_at`) are the read-only
/// surface for consumers who receive `&Node` from traversal methods.
/// Structural fields are crate-internal because they hold arena indices
/// that are meaningless outside the tree.
#[derive(Debug, Clone)]
pub struct Node {
    pub user_id: Uuid,
    pub(crate) parent: Option<NodeIndex>,
    pub(crate) children: Vec<NodeIndex>,
    #[allow(dead_code)] // Used by Arena; wired in task 4 (UnilevelTree retrofit)
    pub(crate) sponsor: Option<NodeIndex>,
    #[allow(dead_code)] // Used by Arena; wired in task 4 (UnilevelTree retrofit)
    pub(crate) sponsored: Vec<NodeIndex>,
    pub depth: u32,
    /// Unix timestamp in seconds when the user was enrolled.
    pub enrolled_at: i64,
}
