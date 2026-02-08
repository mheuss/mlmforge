use std::collections::HashMap;
use uuid::Uuid;

use super::error::TreeError;
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

    /// Adds the root node. Fails if the tree already has a root.
    pub fn add_root(&mut self, user_id: Uuid, enrolled_at: i64) -> Result<NodeIndex, TreeError> {
        if self.root.is_some() {
            return Err(TreeError::RootAlreadyExists);
        }
        if self.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }

        let node = Node {
            user_id,
            parent: None,
            children: Vec::new(),
            depth: 0,
            enrolled_at,
        };

        let idx = self.alloc_slot(node);
        self.index.insert(user_id, idx);
        self.root = Some(idx);
        Ok(idx)
    }

    /// Returns a reference to the node for a user ID.
    /// Internal helper used by public traversal methods.
    #[allow(dead_code)]
    pub(crate) fn get_node(&self, user_id: Uuid) -> Result<&Node, TreeError> {
        let idx = self.resolve(user_id)?;
        Ok(&self.nodes[idx.0])
    }

    /// Resolves a user ID to a NodeIndex.
    fn resolve(&self, user_id: Uuid) -> Result<NodeIndex, TreeError> {
        self.index
            .get(&user_id)
            .copied()
            .ok_or(TreeError::UserNotFound(user_id))
    }

    /// Adds a child node under an existing parent.
    ///
    /// The child's position is determined by insertion order — it becomes
    /// the last element in the parent's children Vec. For unilevel trees,
    /// position equals the child's index in that Vec.
    ///
    /// # Errors
    ///
    /// - `UserAlreadyExists` if `user_id` is already in the tree
    /// - `UserNotFound` if `parent_id` is not in the tree
    pub fn add_node(
        &mut self,
        user_id: Uuid,
        parent_id: Uuid,
        enrolled_at: i64,
    ) -> Result<NodeIndex, TreeError> {
        if self.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }
        let parent_idx = self.resolve(parent_id)?;
        let parent_depth = self.nodes[parent_idx.0].depth;

        let node = Node {
            user_id,
            parent: Some(parent_idx),
            children: Vec::new(),
            depth: parent_depth + 1,
            enrolled_at,
        };

        let idx = self.alloc_slot(node);
        self.index.insert(user_id, idx);
        self.nodes[parent_idx.0].children.push(idx);
        Ok(idx)
    }

    /// Allocates a slot in the arena. Reuses tombstoned slots from the
    /// free list when available. Otherwise appends to the Vec.
    fn alloc_slot(&mut self, node: Node) -> NodeIndex {
        if let Some(free_idx) = self.free_list.pop() {
            self.nodes[free_idx.0] = node;
            free_idx
        } else {
            let idx = NodeIndex(self.nodes.len());
            self.nodes.push(node);
            idx
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Deterministic UUID for tests. The byte value makes failures readable.
    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
    }

    #[test]
    fn add_root_to_empty_tree() {
        let mut tree = UnilevelTree::new();
        let result = tree.add_root(test_uuid(1), 1000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_root_sets_depth_zero() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let node = tree.get_node(test_uuid(1)).unwrap();
        assert_eq!(node.depth, 0);
    }

    #[test]
    fn add_root_twice_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_root(test_uuid(2), 2000);
        assert!(matches!(result, Err(TreeError::RootAlreadyExists)));
    }

    #[test]
    fn add_node_under_root() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 2000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_node_sets_depth_from_parent() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        let node = tree.get_node(test_uuid(3)).unwrap();
        assert_eq!(node.depth, 2);
    }

    #[test]
    fn add_node_appends_to_parent_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        let parent = tree.get_node(test_uuid(1)).unwrap();
        assert_eq!(parent.children.len(), 2);
    }

    #[test]
    fn add_duplicate_user_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(1), test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::UserAlreadyExists(_))));
    }

    #[test]
    fn add_node_with_missing_parent_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(99), 2000);
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }
}
