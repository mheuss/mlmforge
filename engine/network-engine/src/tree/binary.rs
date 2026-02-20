use std::collections::HashMap;
use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::types::TreePosition;

/// Arena-backed binary tree.
///
/// Each node has at most two children: position 0 (left) and
/// position 1 (right). Placement requires an explicit position.
/// The tree validates positions but never picks alternatives
/// (decision 020).
pub struct BinaryTree {
    arena: Arena,
    /// Binary child slots per parent node.
    /// Left = slots[parent][0], Right = slots[parent][1].
    /// The Node's children Vec is rebuilt from occupied slots.
    slots: HashMap<NodeIndex, [Option<NodeIndex>; 2]>,
}

impl Default for BinaryTree {
    fn default() -> Self {
        Self::new()
    }
}

impl BinaryTree {
    pub fn new() -> Self {
        Self {
            arena: Arena::new(),
            slots: HashMap::new(),
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
        self.slots.insert(idx, [None, None]);
        Ok(idx)
    }

    #[cfg(test)]
    pub(crate) fn get_node(&self, user_id: Uuid) -> Result<&Node, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.node(idx))
    }

    /// Adds a child node at an explicit position under a parent.
    ///
    /// Position 0 = left, position 1 = right. Both the position and
    /// sponsor are required. The tree validates that the position is
    /// 0 or 1 and not already occupied.
    pub fn add_node(
        &mut self,
        user_id: Uuid,
        parent_id: Uuid,
        position: usize,
        sponsor_id: Uuid,
        enrolled_at: i64,
    ) -> Result<NodeIndex, TreeError> {
        if position > 1 {
            return Err(TreeError::PositionOutOfRange {
                user_id: parent_id,
                position,
                child_count: 2,
            });
        }
        if self.arena.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }
        let parent_idx = self.arena.resolve(parent_id)?;
        let sponsor_idx = self.arena.resolve(sponsor_id)?;

        let parent_slots = self.slots.get(&parent_idx).copied().unwrap_or([None, None]);
        if parent_slots[position].is_some() {
            return Err(TreeError::PositionOccupied {
                user_id: parent_id,
                position,
            });
        }

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
        self.slots.insert(idx, [None, None]);

        // Update parent's binary slots and rebuild children Vec.
        let slots = self
            .slots
            .get_mut(&parent_idx)
            .expect("slots entry missing — arena and slots map out of sync");
        slots[position] = Some(idx);
        self.rebuild_children(parent_idx);

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

        if let Some(parent_idx) = self.arena.node(idx).parent {
            let slots = self
                .slots
                .get_mut(&parent_idx)
                .expect("slots entry missing — arena and slots map out of sync");
            for slot in slots.iter_mut() {
                if *slot == Some(idx) {
                    *slot = None;
                }
            }
            self.rebuild_children(parent_idx);
        }

        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena
                .node_mut(sponsor_idx)
                .sponsored
                .retain(|&s| s != idx);
        }

        if self.arena.root == Some(idx) {
            self.arena.root = None;
        }

        self.slots.remove(&idx);
        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);
        Ok(())
    }

    /// Rebuilds a node's children Vec from its binary slots.
    /// Children appear in position order: left (0) first, right (1) second.
    /// Only occupied slots are included.
    fn rebuild_children(&mut self, parent_idx: NodeIndex) {
        let slots = self
            .slots
            .get(&parent_idx)
            .copied()
            .expect("slots entry missing — arena and slots map out of sync");
        let mut children = Vec::with_capacity(2);
        if let Some(left) = slots[0] {
            children.push(left);
        }
        if let Some(right) = slots[1] {
            children.push(right);
        }
        self.arena.node_mut(parent_idx).children = children;
    }

    // --- Delegated traversals ---

    pub fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        match self.arena.node(idx).parent {
            Some(parent_idx) => Ok(Some(self.arena.node(parent_idx))),
            None => Ok(None),
        }
    }

    pub fn get_children(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let children = self
            .arena
            .node(idx)
            .children
            .iter()
            .map(|&child_idx| self.arena.node(child_idx))
            .collect();
        Ok(children)
    }

    /// Walks upward from a node toward the root.
    ///
    /// Returns ancestors in order from immediate parent to root.
    /// The starting node is not included in the result.
    ///
    /// Depth 0 means walk all the way to root. Any other value limits
    /// the walk to that many levels up.
    pub fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_upline(idx, depth))
    }

    /// Walks downward from a node, returning descendants in BFS order.
    ///
    /// The starting node is not included in the result.
    ///
    /// Depth 0 means walk all levels. Any other value limits the walk
    /// to that many levels below the starting node.
    pub fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.bfs_downline(idx, depth))
    }

    /// Computes a full position snapshot for a user.
    ///
    /// For binary trees, position is determined by the parent's slots
    /// map rather than children Vec index. Downline counts are keyed
    /// by slot position (0=left, 1=right).
    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let mut pos = self.arena.get_position(idx);

        // For binary, position is determined by slots, not children Vec index.
        if let Some(parent_idx) = self.arena.node(idx).parent {
            let parent_slots = self
                .slots
                .get(&parent_idx)
                .expect("slots entry missing — arena and slots map out of sync");
            if parent_slots[0] == Some(idx) {
                pos.position = 0;
            } else if parent_slots[1] == Some(idx) {
                pos.position = 1;
            }
        }

        // Override downline_counts to use slot positions, not children Vec indices.
        let node_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing — arena and slots map out of sync");
        pos.downline_counts.clear();
        for (slot_pos, slot) in node_slots.iter().enumerate() {
            if let Some(child_idx) = slot {
                pos.downline_counts
                    .insert(slot_pos, self.arena.count_subtree(*child_idx));
            }
        }

        Ok(pos)
    }

    /// Returns the subtree under a binary position (0=left, 1=right).
    ///
    /// Results include the child at the given position and all of
    /// its descendants, in BFS order.
    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing — arena and slots map out of sync");

        if position > 1 {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: 2,
            });
        }

        match node_slots[position] {
            Some(child_idx) => {
                let mut result = Vec::new();
                let mut queue = std::collections::VecDeque::new();
                queue.push_back(child_idx);
                while let Some(current) = queue.pop_front() {
                    result.push(self.arena.node(current));
                    for &c in &self.arena.node(current).children {
                        queue.push_back(c);
                    }
                }
                Ok(result)
            }
            None => Ok(vec![]),
        }
    }

    /// Counts descendants without allocating a result Vec.
    ///
    /// Depth 0 means count all descendants. Any other value limits
    /// the count to that many levels below.
    pub fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.count_downline(idx, depth))
    }

    /// Counts nodes in the subtree under a binary position (0=left, 1=right).
    ///
    /// The count includes the child at the given position and all of
    /// its descendants.
    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing — arena and slots map out of sync");

        if position > 1 {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: 2,
            });
        }

        match node_slots[position] {
            Some(child_idx) => Ok(1 + self.arena.count_subtree(child_idx)),
            None => Ok(0),
        }
    }

    /// Checks whether `user_id` is a descendant of `ancestor_id`.
    ///
    /// A node is not considered a descendant of itself.
    pub fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError> {
        let ancestor_idx = self.arena.resolve(ancestor_id)?;
        if user_id == ancestor_id {
            return Ok(false);
        }
        let user_idx = self.arena.resolve(user_id)?;
        Ok(self.arena.is_descendant_of(user_idx, ancestor_idx))
    }

    // --- Sponsor traversals ---

    /// Returns the sponsor of a node, or None if the node has no sponsor (root).
    pub fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsor(idx))
    }

    /// Walks upward following sponsor links.
    ///
    /// Returns sponsors in order from immediate sponsor to the root sponsor.
    /// The starting node is not included.
    ///
    /// Depth 0 means walk all the way. Any other value limits the walk.
    pub fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_sponsor_upline(idx, depth))
    }

    /// Returns the direct recruits of a node.
    pub fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsored(idx))
    }

    /// Provides read access to the arena for commission calculators
    /// and other crate-internal consumers.
    #[allow(dead_code)] // Used by BinaryTree commission calculator (future task)
    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
    }
}

impl crate::tree::navigator::TreeNavigator for BinaryTree {
    fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        self.get_parent(user_id)
    }
    fn get_children(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        self.get_children(user_id)
    }
    fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        self.get_upline(user_id, depth)
    }
    fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        self.get_downline(user_id, depth)
    }
    fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        self.get_position(user_id)
    }
    fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        self.get_branch(user_id, position)
    }
    fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
        self.count_downline(user_id, depth)
    }
    fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        self.count_branch(user_id, position)
    }
    fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError> {
        self.is_descendant_of(user_id, ancestor_id)
    }
    fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        self.get_sponsor(user_id)
    }
    fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        self.get_sponsor_upline(user_id, depth)
    }
    fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        self.get_sponsored(user_id)
    }
}

impl std::fmt::Debug for BinaryTree {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let root_id = self.arena.root.map(|idx| self.arena.node(idx).user_id);
        write!(
            f,
            "BinaryTree {{ nodes: {}, root: {:?} }}",
            self.arena.node_count(),
            root_id
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::test_helpers::test_uuid;

    #[test]
    fn add_root_to_empty_tree() {
        let mut tree = BinaryTree::new();
        let result = tree.add_root(test_uuid(1), 1000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_root_sets_depth_zero() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let node = tree.get_node(test_uuid(1)).unwrap();
        assert_eq!(node.depth, 0);
    }

    #[test]
    fn add_root_twice_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_root(test_uuid(2), 2000);
        assert!(matches!(result, Err(TreeError::RootAlreadyExists)));
    }

    #[test]
    fn add_node_left_position() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_node_right_position() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_node_both_positions() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 2);
        assert_eq!(children[0].user_id, test_uuid(2));
        assert_eq!(children[1].user_id, test_uuid(3));
    }

    #[test]
    fn add_node_position_occupied_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        let result = tree.add_node(test_uuid(3), test_uuid(1), 0, test_uuid(1), 3000);
        assert!(matches!(result, Err(TreeError::PositionOccupied { .. })));
    }

    #[test]
    fn add_node_invalid_position_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 2, test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::PositionOutOfRange { .. })));
    }

    #[test]
    fn add_node_sets_depth_from_parent() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 1, test_uuid(1), 3000)
            .unwrap();
        let node = tree.get_node(test_uuid(3)).unwrap();
        assert_eq!(node.depth, 2);
    }

    #[test]
    fn add_duplicate_user_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(1), test_uuid(1), 0, test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::UserAlreadyExists(_))));
    }

    #[test]
    fn add_node_right_only_leaves_left_empty() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000)
            .unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 1);
        assert_eq!(children[0].user_id, test_uuid(2));
    }

    #[test]
    fn sponsor_set_on_add_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        let sponsor = tree.get_sponsor(test_uuid(2)).unwrap();
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));
    }

    #[test]
    fn remove_leaf_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        let result = tree.remove_node(test_uuid(2));
        assert!(result.is_ok());
    }

    #[test]
    fn remove_node_with_children_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        let result = tree.remove_node(test_uuid(1));
        assert!(matches!(result, Err(TreeError::HasChildren(_, 1))));
    }

    #[test]
    fn remove_and_readd_same_position() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        let result = tree.add_node(test_uuid(3), test_uuid(1), 0, test_uuid(1), 3000);
        assert!(result.is_ok());
    }

    #[test]
    fn removed_slot_is_reused() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0, test_uuid(1), 3000)
            .unwrap();
        // Arena slot reused
        assert_eq!(tree.arena.nodes.len(), 2);
    }

    #[test]
    fn single_node_tree() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        assert!(tree.get_parent(test_uuid(1)).unwrap().is_none());
        assert!(tree.get_children(test_uuid(1)).unwrap().is_empty());
        assert!(tree.get_upline(test_uuid(1), 0).unwrap().is_empty());
        assert!(tree.get_downline(test_uuid(1), 0).unwrap().is_empty());
        assert_eq!(tree.count_downline(test_uuid(1), 0).unwrap(), 0);
    }

    #[test]
    fn operations_on_empty_tree_fail() {
        let tree = BinaryTree::new();
        assert!(matches!(
            tree.get_parent(test_uuid(1)),
            Err(TreeError::UserNotFound(_))
        ));
    }

    #[test]
    fn deep_chain_1000_nodes_alternating() {
        use crate::tree::test_helpers::test_uuid_u16;
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid_u16(0), 0).unwrap();
        for i in 1..=1000u16 {
            let position = (i % 2) as usize;
            tree.add_node(
                test_uuid_u16(i),
                test_uuid_u16(i - 1),
                position,
                test_uuid_u16(0),
                i as i64,
            )
            .unwrap();
        }
        let downline = tree.get_downline(test_uuid_u16(0), 0).unwrap();
        assert_eq!(downline.len(), 1000);
        let upline = tree.get_upline(test_uuid_u16(1000), 0).unwrap();
        assert_eq!(upline.len(), 1000);
    }

    #[test]
    fn get_position_root() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        let pos = tree.get_position(test_uuid(1)).unwrap();
        assert_eq!(pos.position, 0);
        assert!(pos.parent_user_id.is_none());
        assert_eq!(pos.depth, 0);
    }

    #[test]
    fn get_position_left_child() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 0);
        assert_eq!(pos.parent_user_id, Some(test_uuid(1)));
    }

    #[test]
    fn get_position_right_child() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000)
            .unwrap();
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 1);
    }

    #[test]
    fn get_position_downline_counts_by_slot() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 1, test_uuid(1), 5000)
            .unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        assert_eq!(pos.downline_counts[&0], 2);
        assert_eq!(pos.downline_counts[&1], 0);
    }

    #[test]
    fn get_branch_left() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 4000)
            .unwrap();
        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        let ids: Vec<Uuid> = branch.iter().map(|n| n.user_id).collect();
        assert!(ids.contains(&test_uuid(2)));
        assert!(ids.contains(&test_uuid(4)));
        assert!(!ids.contains(&test_uuid(3)));
    }

    #[test]
    fn is_descendant_of_works() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 1, test_uuid(1), 3000)
            .unwrap();
        assert!(tree.is_descendant_of(test_uuid(3), test_uuid(1)).unwrap());
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(3)).unwrap());
    }
}
