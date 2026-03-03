use std::collections::HashMap;
use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::config::matrix::SpilloverDirection;

/// Entry in the holding tank for nodes removed via HoldingTank pruning
/// or awaiting manual placement.
#[derive(Debug, Clone)]
pub struct HoldingTankEntry {
    pub user_id: Uuid,
    pub sponsor: Option<NodeIndex>,
    pub enrolled_at: i64,
}

/// Result of a remove_node operation, describing what changed.
#[derive(Debug)]
pub struct RemovalResult {
    pub removed: Uuid,
    pub promoted: Option<Uuid>,
    pub repositioned: Vec<Uuid>,
    pub moved_to_tank: Vec<Uuid>,
}

/// Pruning mode for matrix node removal.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PruningMode {
    PromoteEarliest,
    HoldingTank,
}

/// Arena-backed matrix tree with fixed-width positional placement.
///
/// Each node has exactly `width` child slots (0..width-1).
/// Placement is either automatic (breadth-first spillover within
/// the sponsor's subtree) or explicit (admin override).
/// Depth is unlimited. Width is immutable after construction.
#[allow(dead_code)] // Fields used by later implementation tasks.
pub struct MatrixTree {
    arena: Arena,
    width: u8,
    spillover: SpilloverDirection,
    slots: HashMap<NodeIndex, Vec<Option<NodeIndex>>>,
    holding_tank: Vec<HoldingTankEntry>,
}

impl MatrixTree {
    pub fn new(width: u8, spillover: SpilloverDirection) -> Result<Self, TreeError> {
        if width < 2 {
            return Err(TreeError::InvalidWidth(width));
        }
        Ok(Self {
            arena: Arena::new(),
            width,
            spillover,
            slots: HashMap::new(),
            holding_tank: Vec::new(),
        })
    }

    pub fn set_root(&mut self, user_id: Uuid, enrolled_at: i64) -> Result<NodeIndex, TreeError> {
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
        self.slots.insert(idx, vec![None; self.width as usize]);
        Ok(idx)
    }

    #[cfg(test)]
    pub(crate) fn get_node(&self, user_id: Uuid) -> Result<&Node, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.node(idx))
    }

    /// Rebuilds a node's children Vec from its slot map.
    ///
    /// Children appear in slot order: 0 first, then 1, etc.
    /// Only occupied slots are included.
    fn rebuild_children(&mut self, parent_idx: NodeIndex) {
        let slots = self
            .slots
            .get(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        let children: Vec<NodeIndex> = slots.iter().flatten().copied().collect();
        self.arena.node_mut(parent_idx).children = children;
    }

    /// Adds a node at an explicit parent and position.
    ///
    /// This is the admin-controlled placement path. The caller
    /// specifies exactly which parent slot receives the new node.
    /// Position must be in 0..width-1, and the slot must be empty.
    pub fn add_node_at(
        &mut self,
        user_id: Uuid,
        sponsor_id: Uuid,
        parent_id: Uuid,
        position: u8,
        enrolled_at: i64,
    ) -> Result<NodeIndex, TreeError> {
        if self.arena.root.is_none() {
            return Err(TreeError::TreeEmpty);
        }
        if position >= self.width {
            return Err(TreeError::PositionOutOfRange {
                user_id: parent_id,
                position: position as usize,
                child_count: self.width as usize,
            });
        }
        if self.arena.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }
        let parent_idx = self.arena.resolve(parent_id)?;
        let sponsor_idx = self.arena.resolve(sponsor_id).map_err(|e| match e {
            TreeError::UserNotFound(id) => TreeError::SponsorNotFound(id),
            other => other,
        })?;

        let parent_slots = self
            .slots
            .get(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        if parent_slots[position as usize].is_some() {
            return Err(TreeError::PositionOccupied {
                user_id: parent_id,
                position: position as usize,
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
        self.slots.insert(idx, vec![None; self.width as usize]);

        let parent_slots = self
            .slots
            .get_mut(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        parent_slots[position as usize] = Some(idx);
        self.rebuild_children(parent_idx);

        self.arena.node_mut(sponsor_idx).sponsored.push(idx);
        Ok(idx)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::matrix::SpilloverDirection;
    use crate::tree::test_helpers::test_uuid;

    #[test]
    fn new_with_valid_width() {
        let tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst);
        assert!(tree.is_ok());
    }

    #[test]
    fn new_with_width_one_fails() {
        let tree = MatrixTree::new(1, SpilloverDirection::BreadthFirst);
        assert!(matches!(tree, Err(TreeError::InvalidWidth(1))));
    }

    #[test]
    fn new_with_width_zero_fails() {
        let tree = MatrixTree::new(0, SpilloverDirection::BreadthFirst);
        assert!(matches!(tree, Err(TreeError::InvalidWidth(0))));
    }

    #[test]
    fn new_with_width_two_succeeds() {
        let tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst);
        assert!(tree.is_ok());
    }

    #[test]
    fn set_root_succeeds() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        let result = tree.set_root(test_uuid(1), 1000);
        assert!(result.is_ok());
    }

    #[test]
    fn set_root_twice_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.set_root(test_uuid(2), 2000);
        assert!(matches!(result, Err(TreeError::RootAlreadyExists)));
    }

    #[test]
    fn set_root_initializes_slots() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let idx = tree.arena.resolve(test_uuid(1)).unwrap();
        let slots = tree.slots.get(&idx).unwrap();
        assert_eq!(slots.len(), 3);
        assert!(slots.iter().all(|s| s.is_none()));
    }

    #[test]
    fn set_root_depth_is_zero() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let node = tree.get_node(test_uuid(1)).unwrap();
        assert_eq!(node.depth, 0);
    }

    // --- add_node_at tests ---

    #[test]
    fn add_node_at_explicit_position() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000);
        assert!(result.is_ok());
    }

    #[test]
    fn add_node_at_sets_depth() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(2), 1, 3000)
            .unwrap();
        let node2 = tree.get_node(test_uuid(2)).unwrap();
        assert_eq!(node2.depth, 1);
        let node3 = tree.get_node(test_uuid(3)).unwrap();
        assert_eq!(node3.depth, 2);
    }

    #[test]
    fn add_node_at_sets_sponsor() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        // Add node2 under root, sponsored by root.
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        // Add node3 under root, but sponsored by node2 (sponsor != parent).
        tree.add_node_at(test_uuid(3), test_uuid(2), test_uuid(1), 1, 3000)
            .unwrap();
        let node3 = tree.get_node(test_uuid(3)).unwrap();
        let sponsor_idx = node3.sponsor.unwrap();
        let sponsor = tree.arena.node(sponsor_idx);
        assert_eq!(sponsor.user_id, test_uuid(2));
    }

    #[test]
    fn add_node_at_position_occupied_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        let result = tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 0, 3000);
        assert!(matches!(result, Err(TreeError::PositionOccupied { .. })));
    }

    #[test]
    fn add_node_at_invalid_position_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 3, 2000);
        assert!(matches!(
            result,
            Err(TreeError::PositionOutOfRange { position: 3, .. })
        ));
    }

    #[test]
    fn add_node_at_duplicate_user_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 1, 3000);
        assert!(matches!(result, Err(TreeError::UserAlreadyExists(_))));
    }

    #[test]
    fn add_node_at_parent_not_found_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(99), 0, 2000);
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }

    #[test]
    fn add_node_at_sponsor_not_found_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(99), test_uuid(1), 0, 2000);
        assert!(matches!(result, Err(TreeError::SponsorNotFound(_))));
    }

    #[test]
    fn add_node_at_on_empty_tree_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        let result = tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000);
        assert!(matches!(result, Err(TreeError::TreeEmpty)));
    }

    #[test]
    fn add_node_at_fills_correct_slot() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 2, 2000)
            .unwrap();
        let root_idx = tree.arena.resolve(test_uuid(1)).unwrap();
        let child_idx = tree.arena.resolve(test_uuid(2)).unwrap();
        let slots = tree.slots.get(&root_idx).unwrap();
        assert!(slots[0].is_none());
        assert!(slots[1].is_none());
        assert_eq!(slots[2], Some(child_idx));
    }

    #[test]
    fn add_node_at_all_positions() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 1, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(1), test_uuid(1), 2, 4000)
            .unwrap();
        let root_idx = tree.arena.resolve(test_uuid(1)).unwrap();
        let slots = tree.slots.get(&root_idx).unwrap();
        assert!(slots.iter().all(|s| s.is_some()));
        let children = tree.arena.node(root_idx).children.len();
        assert_eq!(children, 3);
    }
}
