use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::types::TreePosition;

/// Arena-backed unilevel tree.
///
/// All nodes live in a shared `Arena`. Width is unbounded — every user
/// can enroll unlimited direct children. Position is the child's index
/// in the parent's children Vec.
pub struct UnilevelTree {
    arena: Arena,
}

impl Default for UnilevelTree {
    fn default() -> Self {
        Self::new()
    }
}

impl UnilevelTree {
    pub fn new() -> Self {
        Self {
            arena: Arena::new(),
        }
    }

    pub fn add_root(&mut self, user_id: Uuid, enrolled_at: i64) -> Result<NodeIndex, TreeError> {
        if self.arena.root.is_some() {
            return Err(TreeError::RootAlreadyExists);
        }
        if self.arena.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }

        let node = Node {
            user_id,
            parent: None,
            children: Vec::new(),
            sponsor: None,
            sponsored: Vec::new(),
            depth: 0,
            enrolled_at,
        };

        let idx = self.arena.alloc_slot(node);
        self.arena.index.insert(user_id, idx);
        self.arena.root = Some(idx);
        Ok(idx)
    }

    /// Internal helper for tests.
    #[cfg(test)]
    pub(crate) fn get_node(&self, user_id: Uuid) -> Result<&Node, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.node(idx))
    }

    /// Adds a child node under an existing parent.
    ///
    /// The child's position is determined by insertion order — it becomes
    /// the last element in the parent's children Vec.
    ///
    /// `sponsor_id` identifies who recruited this user. In unilevel trees,
    /// this is often the same as `parent_id`, but the API does not assume that.
    pub fn add_node(
        &mut self,
        user_id: Uuid,
        parent_id: Uuid,
        sponsor_id: Uuid,
        enrolled_at: i64,
    ) -> Result<NodeIndex, TreeError> {
        if self.arena.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }
        let parent_idx = self.arena.resolve(parent_id)?;
        let sponsor_idx = self.arena.resolve(sponsor_id)?;
        let parent_depth = self.arena.node(parent_idx).depth;

        let node = Node {
            user_id,
            parent: Some(parent_idx),
            children: Vec::new(),
            sponsor: Some(sponsor_idx),
            sponsored: Vec::new(),
            depth: parent_depth + 1,
            enrolled_at,
        };

        let idx = self.arena.alloc_slot(node);
        self.arena.index.insert(user_id, idx);
        self.arena.node_mut(parent_idx).children.push(idx);
        self.arena.node_mut(sponsor_idx).sponsored.push(idx);
        Ok(idx)
    }

    /// Removes a leaf node from the tree.
    ///
    /// The node must have no children. Removing a node with children
    /// would orphan its subtree, which is a data integrity error.
    /// The caller must remove children first, working from leaves up.
    ///
    /// The removed slot is added to the free list for reuse by the
    /// next `add_root` or `add_node` call.
    pub fn remove_node(&mut self, user_id: Uuid) -> Result<(), TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let child_count = self.arena.node(idx).children.len();

        if child_count > 0 {
            return Err(TreeError::HasChildren(user_id, child_count));
        }

        // Remove from parent's children list
        if let Some(parent_idx) = self.arena.node(idx).parent {
            self.arena
                .node_mut(parent_idx)
                .children
                .retain(|&child_idx| child_idx != idx);
        }

        // Remove from sponsor's sponsored list
        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena
                .node_mut(sponsor_idx)
                .sponsored
                .retain(|&s| s != idx);
        }

        if self.arena.root == Some(idx) {
            self.arena.root = None;
        }

        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);
        Ok(())
    }

    /// Computes a full position snapshot for a user.
    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_position(idx))
    }

    /// Returns all nodes in the subtree under a specific child position.
    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        self.arena.get_branch(idx, position)
    }

    /// Counts nodes in the subtree under a specific child position.
    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        self.arena.count_branch(idx, position)
    }

    /// Returns all user IDs currently in the tree.
    ///
    /// Used by pass-up context building to ensure skip sets cover
    /// all distributors in the tree, not just those with snapshots.
    pub fn user_ids(&self) -> Vec<Uuid> {
        self.arena.index.keys().copied().collect()
    }

    /// Provides read access to the arena for commission calculators
    /// and other crate-internal consumers.
    #[allow(dead_code)] // Will be used by commission calculators.
    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
    }
}

impl_arena_delegations!(UnilevelTree);
impl_tree_navigator!(UnilevelTree);

impl std::fmt::Debug for UnilevelTree {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let root_id = self.arena.root.map(|idx| self.arena.node(idx).user_id);
        write!(
            f,
            "UnilevelTree {{ nodes: {}, root: {:?} }}",
            self.arena.node_count(),
            root_id
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::test_helpers::{test_uuid, test_uuid_u16};

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
        let result = tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_node_sets_depth_from_parent() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        let node = tree.get_node(test_uuid(3)).unwrap();
        assert_eq!(node.depth, 2);
    }

    #[test]
    fn add_node_appends_to_parent_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        let parent = tree.get_node(test_uuid(1)).unwrap();
        assert_eq!(parent.children.len(), 2);
    }

    #[test]
    fn add_duplicate_user_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(1), test_uuid(1), test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::UserAlreadyExists(_))));
    }

    #[test]
    fn add_node_with_missing_parent_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(99), test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }

    #[test]
    fn remove_leaf_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        let result = tree.remove_node(test_uuid(2));
        assert!(result.is_ok());
    }

    #[test]
    fn remove_node_clears_from_parent_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        let parent = tree.get_node(test_uuid(1)).unwrap();
        assert!(parent.children.is_empty());
    }

    #[test]
    fn remove_node_with_children_fails() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        // Arena should still have only 2 slots, not 3
        assert_eq!(tree.arena.nodes.len(), 2);
    }

    #[test]
    fn get_parent_returns_parent_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), test_uuid(1), 4000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        // depth 0 = all levels
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert_eq!(downline.len(), 3);
    }

    #[test]
    fn get_downline_with_depth_limit() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
        // depth 1 = direct children only
        let downline = tree.get_downline(test_uuid(1), 1).unwrap();
        assert_eq!(downline.len(), 1);
        assert_eq!(downline[0].user_id, test_uuid(2));
    }

    #[test]
    fn get_downline_returns_bfs_order() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 5000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        // Add grandchildren under uuid(2)
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 5000)
            .unwrap();

        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.user_id, test_uuid(2));
        assert_eq!(pos.parent_user_id, Some(test_uuid(1)));
        assert_eq!(pos.position, 0); // first child of root
        assert_eq!(pos.depth, 1);
        assert_eq!(pos.child_count, 2);
    }

    #[test]
    fn get_position_includes_downline_counts() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 5000)
            .unwrap();
        tree.add_node(test_uuid(6), test_uuid(4), test_uuid(4), 6000)
            .unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        // Branch 0 (under uuid(2)): uuid(4), uuid(5), uuid(6) = 3 descendants
        // Branch 1 (under uuid(3)): 0 descendants
        assert_eq!(pos.downline_counts[&0], 3);
        assert_eq!(pos.downline_counts[&1], 0);
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 5000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        let result = tree.get_branch(test_uuid(1), 5);
        assert!(matches!(result, Err(TreeError::PositionOutOfRange { .. })));
    }

    #[test]
    fn get_branch_returns_bfs_order() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        assert_eq!(branch[0].user_id, test_uuid(2));
        assert_eq!(branch[1].user_id, test_uuid(3));
        assert_eq!(branch[2].user_id, test_uuid(4));
    }

    #[test]
    fn count_downline_matches_get_downline_len() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
        let count = tree.count_downline(test_uuid(1), 0).unwrap();
        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert_eq!(count, downline.len());
        assert_eq!(count, 3);
    }

    #[test]
    fn count_downline_respects_depth_limit() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
        assert_eq!(tree.count_downline(test_uuid(1), 1).unwrap(), 1);
        assert_eq!(tree.count_downline(test_uuid(1), 2).unwrap(), 2);
        assert_eq!(tree.count_downline(test_uuid(1), 0).unwrap(), 3);
    }

    #[test]
    fn count_branch_matches_get_branch_len() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 4000)
            .unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        assert!(tree.is_descendant_of(test_uuid(2), test_uuid(1)).unwrap());
    }

    #[test]
    fn is_descendant_of_deep_descendant() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 4000)
            .unwrap();
        assert!(tree.is_descendant_of(test_uuid(4), test_uuid(1)).unwrap());
    }

    #[test]
    fn is_descendant_of_returns_false_for_sibling() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        assert!(!tree.is_descendant_of(test_uuid(2), test_uuid(3)).unwrap());
    }

    #[test]
    fn is_descendant_of_returns_false_for_ancestor() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        // Parent is not a descendant of child
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(2)).unwrap());
    }

    #[test]
    fn is_descendant_of_self_returns_false() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(1)).unwrap());
    }

    // --- Edge cases ---

    #[test]
    fn operations_on_empty_tree_fail() {
        let tree = UnilevelTree::new();
        assert!(matches!(
            tree.get_parent(test_uuid(1)),
            Err(TreeError::UserNotFound(_))
        ));
        assert!(matches!(
            tree.get_children(test_uuid(1)),
            Err(TreeError::UserNotFound(_))
        ));
        assert!(matches!(
            tree.get_downline(test_uuid(1), 0),
            Err(TreeError::UserNotFound(_))
        ));
    }

    #[test]
    fn single_node_tree() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        assert!(tree.get_parent(test_uuid(1)).unwrap().is_none());
        assert!(tree.get_children(test_uuid(1)).unwrap().is_empty());
        assert!(tree.get_upline(test_uuid(1), 0).unwrap().is_empty());
        assert!(tree.get_downline(test_uuid(1), 0).unwrap().is_empty());
        assert_eq!(tree.count_downline(test_uuid(1), 0).unwrap(), 0);
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(1)).unwrap());
    }

    #[test]
    fn deep_chain_1000_nodes() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid_u16(0), 0).unwrap();
        for i in 1..=1000u16 {
            tree.add_node(
                test_uuid_u16(i),
                test_uuid_u16(i - 1),
                test_uuid_u16(i - 1),
                i as i64,
            )
            .unwrap();
        }
        // No stack overflow from iterative BFS
        let downline = tree.get_downline(test_uuid_u16(0), 0).unwrap();
        assert_eq!(downline.len(), 1000);
        // Deepest node's upline is 1000 long
        let upline = tree.get_upline(test_uuid_u16(1000), 0).unwrap();
        assert_eq!(upline.len(), 1000);
    }

    #[test]
    fn wide_fan_1000_children() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid_u16(0), 0).unwrap();
        for i in 1..=1000u16 {
            tree.add_node(
                test_uuid_u16(i),
                test_uuid_u16(0),
                test_uuid_u16(0),
                i as i64,
            )
            .unwrap();
        }
        let children = tree.get_children(test_uuid_u16(0)).unwrap();
        assert_eq!(children.len(), 1000);
        assert_eq!(tree.count_downline(test_uuid_u16(0), 0).unwrap(), 1000);
    }

    // --- Sponsor tests ---

    #[test]
    fn sponsor_is_set_on_add_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        let sponsor = tree.get_sponsor(test_uuid(2)).unwrap();
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));
    }

    #[test]
    fn sponsor_different_from_parent() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(1), 3000)
            .unwrap();
        let parent = tree.get_parent(test_uuid(3)).unwrap().unwrap();
        let sponsor = tree.get_sponsor(test_uuid(3)).unwrap().unwrap();
        assert_eq!(parent.user_id, test_uuid(2));
        assert_eq!(sponsor.user_id, test_uuid(1));
    }

    #[test]
    fn get_sponsored_returns_recruits() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000)
            .unwrap();
        let sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert_eq!(sponsored.len(), 2);
    }

    #[test]
    fn get_sponsor_upline_walks_sponsor_chain() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000)
            .unwrap();
        let upline = tree.get_sponsor_upline(test_uuid(3), 0).unwrap();
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(2));
        assert_eq!(upline[1].user_id, test_uuid(1));
    }

    #[test]
    fn remove_node_clears_sponsor_link() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000)
            .unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        let sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert!(sponsored.is_empty());
    }

    #[test]
    fn root_has_no_sponsor() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let sponsor = tree.get_sponsor(test_uuid(1)).unwrap();
        assert!(sponsor.is_none());
    }
}
