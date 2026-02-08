use std::collections::HashMap;
use uuid::Uuid;

use super::node::{Node, NodeIndex};

/// Arena-backed unilevel tree.
///
/// All nodes live in a contiguous `Vec<Node>`. A `HashMap<Uuid, NodeIndex>`
/// provides O(1) lookup by user ID. Deleted nodes are tombstoned and their
/// slots go on a free list for reuse.
///
/// For unilevel, width is unbounded. Every user can enroll unlimited
/// direct children. Position is the child's index in the parent's
/// children Vec.
#[allow(dead_code)]
pub struct UnilevelTree {
    nodes: Vec<Node>,
    index: HashMap<Uuid, NodeIndex>,
    free_list: Vec<NodeIndex>,
    root: Option<NodeIndex>,
}

impl Default for UnilevelTree {
    fn default() -> Self {
        Self::new()
    }
}

impl UnilevelTree {
    /// Creates an empty tree with no nodes.
    pub fn new() -> Self {
        Self {
            nodes: Vec::new(),
            index: HashMap::new(),
            free_list: Vec::new(),
            root: None,
        }
    }
}
