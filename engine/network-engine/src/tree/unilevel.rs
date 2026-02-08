use std::collections::{HashMap, VecDeque};
use uuid::Uuid;

use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::types::TreePosition;

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

    /// Returns the parent of a node.
    ///
    /// Returns `Ok(None)` for the root node (no parent).
    /// Returns `Err(UserNotFound)` if the user is not in the tree.
    pub fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        let idx = self.resolve(user_id)?;
        match self.nodes[idx.0].parent {
            Some(parent_idx) => Ok(Some(&self.nodes[parent_idx.0])),
            None => Ok(None),
        }
    }

    /// Returns the direct children of a node in position order.
    ///
    /// Position 0 is the first enrolled child, position 1 is the second,
    /// and so on. Returns an empty Vec for leaf nodes.
    pub fn get_children(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        let idx = self.resolve(user_id)?;
        let children = self.nodes[idx.0]
            .children
            .iter()
            .map(|&child_idx| &self.nodes[child_idx.0])
            .collect();
        Ok(children)
    }

    /// Removes a leaf node from the tree.
    ///
    /// The node must have no children. Removing a node with children
    /// would orphan its subtree, which is a data integrity error.
    /// The caller must remove children first, working from leaves up.
    ///
    /// The removed slot is added to the free list for reuse by the
    /// next `add_root` or `add_node` call.
    ///
    /// # Errors
    ///
    /// - `UserNotFound` if `user_id` is not in the tree
    /// - `HasChildren` if the node has children
    pub fn remove_node(&mut self, user_id: Uuid) -> Result<(), TreeError> {
        let idx = self.resolve(user_id)?;
        let child_count = self.nodes[idx.0].children.len();

        if child_count > 0 {
            return Err(TreeError::HasChildren(user_id, child_count));
        }

        // Remove from parent's children list
        if let Some(parent_idx) = self.nodes[idx.0].parent {
            self.nodes[parent_idx.0]
                .children
                .retain(|&child_idx| child_idx != idx);
        }

        // Clear root if removing the root node
        if self.root == Some(idx) {
            self.root = None;
        }

        // Remove from index and add slot to free list
        self.index.remove(&user_id);
        self.free_list.push(idx);
        Ok(())
    }

    /// Walks upward from a node toward the root.
    ///
    /// Returns ancestors in order from immediate parent to root.
    /// The starting node is not included in the result.
    ///
    /// # Depth parameter
    ///
    /// - `0` means walk all the way to root (no limit).
    /// - Any other value limits the walk to that many levels up.
    pub fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.resolve(user_id)?;
        let mut result = Vec::new();
        let mut current = self.nodes[idx.0].parent;
        let mut steps = 0u32;

        while let Some(parent_idx) = current {
            if depth > 0 && steps >= depth {
                break;
            }
            result.push(&self.nodes[parent_idx.0]);
            current = self.nodes[parent_idx.0].parent;
            steps += 1;
        }

        Ok(result)
    }

    /// Walks downward from a node, returning descendants in BFS order.
    ///
    /// BFS (breadth-first) gives level-ordered results: all level-1
    /// children first, then level-2 grandchildren, and so on. This
    /// matches how distributors think about their organization.
    ///
    /// The starting node is not included in the result.
    ///
    /// # Depth parameter
    ///
    /// - `0` means walk all levels (no limit).
    /// - Any other value limits the walk to that many levels below
    ///   the starting node. `1` returns direct children only.
    pub fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let start_idx = self.resolve(user_id)?;
        let start_depth = self.nodes[start_idx.0].depth;
        let mut result = Vec::new();
        let mut queue = VecDeque::new();

        for &child_idx in &self.nodes[start_idx.0].children {
            queue.push_back(child_idx);
        }

        while let Some(idx) = queue.pop_front() {
            let node = &self.nodes[idx.0];
            let relative_depth = node.depth - start_depth;

            if depth > 0 && relative_depth > depth {
                continue;
            }

            result.push(node);

            if depth == 0 || relative_depth < depth {
                for &child_idx in &node.children {
                    queue.push_back(child_idx);
                }
            }
        }

        Ok(result)
    }

    /// Computes a full position snapshot for a user.
    ///
    /// Unlike `get_node`, this builds an owned `TreePosition` with
    /// derived data: branch counts (descendants per child position),
    /// child count, and the user's position in their parent's children.
    ///
    /// Branch counts are computed by walking each child's full subtree.
    /// For a node with many children each having deep subtrees, this
    /// can be expensive. It is a point query, not a bulk operation.
    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.resolve(user_id)?;
        let node = &self.nodes[idx.0];

        let parent_user_id = node
            .parent
            .map(|parent_idx| self.nodes[parent_idx.0].user_id);

        // Determine this node's position in its parent's children list.
        // Root has position 0 by convention.
        let position = if let Some(parent_idx) = node.parent {
            self.nodes[parent_idx.0]
                .children
                .iter()
                .position(|&child_idx| child_idx == idx)
                .unwrap_or(0)
        } else {
            0
        };

        // Count descendants under each child position
        let mut branch_counts = HashMap::new();
        for (child_pos, &child_idx) in node.children.iter().enumerate() {
            let count = self.count_subtree(child_idx);
            branch_counts.insert(child_pos, count);
        }

        Ok(TreePosition {
            user_id: node.user_id,
            parent_user_id,
            position,
            depth: node.depth,
            child_count: node.children.len(),
            branch_counts,
            enrolled_at: node.enrolled_at,
        })
    }

    /// Returns all nodes in the subtree under a specific child position.
    ///
    /// For unilevel, position is the child's index in the parent's
    /// children Vec. `get_branch(user, 2)` returns the subtree under
    /// the user's 3rd personally enrolled distributor.
    ///
    /// Results include the child at the given position and all of
    /// its descendants, in BFS order.
    ///
    /// # Errors
    ///
    /// - `UserNotFound` if `user_id` is not in the tree
    /// - `PositionOutOfRange` if `position` exceeds the child count
    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.resolve(user_id)?;
        let node = &self.nodes[idx.0];

        if position >= node.children.len() {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: node.children.len(),
            });
        }

        let branch_root = node.children[position];
        let mut result = Vec::new();
        let mut queue = VecDeque::new();
        queue.push_back(branch_root);

        while let Some(current) = queue.pop_front() {
            result.push(&self.nodes[current.0]);
            for &child_idx in &self.nodes[current.0].children {
                queue.push_back(child_idx);
            }
        }

        Ok(result)
    }

    /// Counts descendants without allocating a result Vec.
    ///
    /// Same traversal as `get_downline` but only increments a counter.
    /// Use this when you need the count but not the actual nodes.
    ///
    /// # Depth parameter
    ///
    /// - `0` means count all descendants (no limit).
    /// - Any other value limits the count to that many levels below.
    pub fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
        let start_idx = self.resolve(user_id)?;
        let start_depth = self.nodes[start_idx.0].depth;
        let mut count = 0;
        let mut queue = VecDeque::new();

        for &child_idx in &self.nodes[start_idx.0].children {
            queue.push_back(child_idx);
        }

        while let Some(idx) = queue.pop_front() {
            let node = &self.nodes[idx.0];
            let relative_depth = node.depth - start_depth;

            if depth > 0 && relative_depth > depth {
                continue;
            }

            count += 1;

            if depth == 0 || relative_depth < depth {
                for &child_idx in &node.children {
                    queue.push_back(child_idx);
                }
            }
        }

        Ok(count)
    }

    /// Counts nodes in the subtree under a specific child position.
    ///
    /// Same traversal as `get_branch` but only increments a counter.
    /// Includes the child at the given position and all its descendants.
    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.resolve(user_id)?;
        let node = &self.nodes[idx.0];

        if position >= node.children.len() {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: node.children.len(),
            });
        }

        let branch_root = node.children[position];
        let mut count = 0;
        let mut queue = VecDeque::new();
        queue.push_back(branch_root);

        while let Some(current) = queue.pop_front() {
            count += 1;
            for &child_idx in &self.nodes[current.0].children {
                queue.push_back(child_idx);
            }
        }

        Ok(count)
    }

    /// Checks whether `user_id` is a descendant of `ancestor_id`.
    ///
    /// Walks upline from `user_id` toward root. If `ancestor_id` is
    /// encountered, returns true. If root is reached without finding
    /// the ancestor, returns false.
    ///
    /// Walking upline is O(d) where d is the depth difference. This is
    /// better than walking the ancestor's downline, which could be the
    /// entire subtree.
    ///
    /// A node is not considered a descendant of itself.
    pub fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError> {
        let ancestor_idx = self.resolve(ancestor_id)?;

        if user_id == ancestor_id {
            return Ok(false);
        }

        let mut current_idx = self.resolve(user_id)?;
        loop {
            match self.nodes[current_idx.0].parent {
                Some(parent_idx) => {
                    if parent_idx == ancestor_idx {
                        return Ok(true);
                    }
                    current_idx = parent_idx;
                }
                None => return Ok(false),
            }
        }
    }

    /// Counts all descendants of a node (not including the node itself).
    /// Used internally by get_position for branch counts.
    fn count_subtree(&self, start_idx: NodeIndex) -> usize {
        let mut count = 0;
        let mut queue = VecDeque::new();

        // Seed with start node's children, not start node itself.
        // Branch count = descendants UNDER the child, not including the child.
        for &child_idx in &self.nodes[start_idx.0].children {
            queue.push_back(child_idx);
        }

        while let Some(idx) = queue.pop_front() {
            count += 1;
            for &child_idx in &self.nodes[idx.0].children {
                queue.push_back(child_idx);
            }
        }

        count
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

    #[test]
    fn remove_leaf_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let result = tree.remove_node(test_uuid(2));
        assert!(result.is_ok());
    }

    #[test]
    fn remove_node_clears_from_parent_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        let parent = tree.get_node(test_uuid(1)).unwrap();
        assert!(parent.children.is_empty());
    }

    #[test]
    fn remove_node_with_children_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let result = tree.remove_node(test_uuid(1));
        assert!(matches!(result, Err(TreeError::HasChildren(_, 1))));
    }

    #[test]
    fn remove_nonexistent_user_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.remove_node(test_uuid(99));
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }

    #[test]
    fn removed_slot_is_reused_by_next_add() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        // Arena should still have only 2 slots, not 3
        assert_eq!(tree.nodes.len(), 2);
    }

    #[test]
    fn get_parent_returns_parent_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let parent = tree.get_parent(test_uuid(2)).unwrap();
        assert_eq!(parent.unwrap().user_id, test_uuid(1));
    }

    #[test]
    fn get_parent_of_root_returns_none() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let parent = tree.get_parent(test_uuid(1)).unwrap();
        assert!(parent.is_none());
    }

    #[test]
    fn get_parent_of_nonexistent_user_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.get_parent(test_uuid(99));
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }

    #[test]
    fn get_children_returns_direct_children_in_position_order() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), 4000).unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 3);
        assert_eq!(children[0].user_id, test_uuid(2));
        assert_eq!(children[1].user_id, test_uuid(3));
        assert_eq!(children[2].user_id, test_uuid(4));
    }

    #[test]
    fn get_children_of_leaf_returns_empty() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        assert!(children.is_empty());
    }

    #[test]
    fn get_upline_returns_ancestors_to_root() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        // depth 0 = all the way to root
        let upline = tree.get_upline(test_uuid(4), 0).unwrap();
        assert_eq!(upline.len(), 3);
        assert_eq!(upline[0].user_id, test_uuid(3)); // immediate parent
        assert_eq!(upline[1].user_id, test_uuid(2));
        assert_eq!(upline[2].user_id, test_uuid(1)); // root
    }

    #[test]
    fn get_upline_with_depth_limit() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        let upline = tree.get_upline(test_uuid(4), 2).unwrap();
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(3));
        assert_eq!(upline[1].user_id, test_uuid(2));
    }

    #[test]
    fn get_upline_of_root_returns_empty() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let upline = tree.get_upline(test_uuid(1), 0).unwrap();
        assert!(upline.is_empty());
    }

    #[test]
    fn get_downline_returns_all_descendants() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        // depth 0 = all levels
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert_eq!(downline.len(), 3);
    }

    #[test]
    fn get_downline_with_depth_limit() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        // depth 1 = direct children only
        let downline = tree.get_downline(test_uuid(1), 1).unwrap();
        assert_eq!(downline.len(), 1);
        assert_eq!(downline[0].user_id, test_uuid(2));
    }

    #[test]
    fn get_downline_returns_bfs_order() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 5000).unwrap();
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        // BFS: level 1 first (2, 3), then level 2 (4, 5)
        assert_eq!(downline[0].user_id, test_uuid(2));
        assert_eq!(downline[1].user_id, test_uuid(3));
        assert_eq!(downline[2].user_id, test_uuid(4));
        assert_eq!(downline[3].user_id, test_uuid(5));
    }

    #[test]
    fn get_downline_of_leaf_returns_empty() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert!(downline.is_empty());
    }

    #[test]
    fn get_position_returns_correct_metadata() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        // Add grandchildren under uuid(2)
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 5000).unwrap();

        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.user_id, test_uuid(2));
        assert_eq!(pos.parent_user_id, Some(test_uuid(1)));
        assert_eq!(pos.position, 0); // first child of root
        assert_eq!(pos.depth, 1);
        assert_eq!(pos.child_count, 2);
    }

    #[test]
    fn get_position_includes_branch_counts() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 5000).unwrap();
        tree.add_node(test_uuid(6), test_uuid(4), 6000).unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        // Branch 0 (under uuid(2)): uuid(4), uuid(5), uuid(6) = 3 descendants
        // Branch 1 (under uuid(3)): 0 descendants
        assert_eq!(pos.branch_counts[&0], 3);
        assert_eq!(pos.branch_counts[&1], 0);
    }

    #[test]
    fn get_position_root_has_no_parent() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let pos = tree.get_position(test_uuid(1)).unwrap();
        assert!(pos.parent_user_id.is_none());
        assert_eq!(pos.position, 0);
    }

    #[test]
    fn get_branch_returns_subtree_under_position() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 5000).unwrap();
        // Branch at position 0 under root = everything under uuid(2)
        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        let ids: Vec<Uuid> = branch.iter().map(|n| n.user_id).collect();
        assert!(ids.contains(&test_uuid(2)));
        assert!(ids.contains(&test_uuid(4)));
        assert!(ids.contains(&test_uuid(5)));
        assert!(!ids.contains(&test_uuid(3)));
    }

    #[test]
    fn get_branch_position_out_of_range_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let result = tree.get_branch(test_uuid(1), 5);
        assert!(matches!(result, Err(TreeError::PositionOutOfRange { .. })));
    }

    #[test]
    fn get_branch_returns_bfs_order() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        assert_eq!(branch[0].user_id, test_uuid(2));
        assert_eq!(branch[1].user_id, test_uuid(3));
        assert_eq!(branch[2].user_id, test_uuid(4));
    }

    #[test]
    fn count_downline_matches_get_downline_len() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        let count = tree.count_downline(test_uuid(1), 0).unwrap();
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert_eq!(count, downline.len());
        assert_eq!(count, 3);
    }

    #[test]
    fn count_downline_respects_depth_limit() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        assert_eq!(tree.count_downline(test_uuid(1), 1).unwrap(), 1);
        assert_eq!(tree.count_downline(test_uuid(1), 2).unwrap(), 2);
        assert_eq!(tree.count_downline(test_uuid(1), 0).unwrap(), 3);
    }

    #[test]
    fn count_branch_matches_get_branch_len() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 4000).unwrap();
        let count = tree.count_branch(test_uuid(1), 0).unwrap();
        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        assert_eq!(count, branch.len());
        assert_eq!(count, 2); // uuid(2) + uuid(4)
    }

    #[test]
    fn count_branch_position_out_of_range_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.count_branch(test_uuid(1), 0);
        assert!(matches!(result, Err(TreeError::PositionOutOfRange { .. })));
    }

    #[test]
    fn is_descendant_of_direct_child() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        assert!(tree.is_descendant_of(test_uuid(2), test_uuid(1)).unwrap());
    }

    #[test]
    fn is_descendant_of_deep_descendant() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 4000).unwrap();
        assert!(tree.is_descendant_of(test_uuid(4), test_uuid(1)).unwrap());
    }

    #[test]
    fn is_descendant_of_returns_false_for_sibling() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        assert!(!tree.is_descendant_of(test_uuid(2), test_uuid(3)).unwrap());
    }

    #[test]
    fn is_descendant_of_returns_false_for_ancestor() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        // Parent is not a descendant of child
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(2)).unwrap());
    }

    #[test]
    fn is_descendant_of_self_returns_false() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(1)).unwrap());
    }
}
