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
    #[allow(dead_code)] // Used by later implementation tasks (add_node, remove_node).
    fn rebuild_children(&mut self, parent_idx: NodeIndex) {
        let slots = self
            .slots
            .get(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        let children: Vec<NodeIndex> = slots.iter().flatten().copied().collect();
        self.arena.node_mut(parent_idx).children = children;
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
}
