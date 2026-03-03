use std::collections::{HashMap, VecDeque};
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

    /// Finds the first open slot via BFS within a subtree.
    ///
    /// Starts at `start_idx` and scans each node's slots left to right.
    /// The first None slot wins. If all slots are full, enqueue children
    /// in position order and continue to the next level.
    fn find_spillover_slot(&self, start_idx: NodeIndex) -> Option<(NodeIndex, usize)> {
        let mut queue = VecDeque::new();
        queue.push_back(start_idx);

        while let Some(current) = queue.pop_front() {
            let slots = self
                .slots
                .get(&current)
                .expect("slots entry missing for node -- arena and slots map out of sync");

            for (pos, slot) in slots.iter().enumerate() {
                if slot.is_none() {
                    return Some((current, pos));
                }
            }

            // All slots full. Enqueue children in position order.
            for child_idx in slots.iter().flatten() {
                queue.push_back(*child_idx);
            }
        }

        None
    }

    /// Adds a node with automatic BFS spillover placement.
    ///
    /// The node is placed in the first available slot within the
    /// sponsor's subtree, found by breadth-first search. The sponsor
    /// becomes the node's sponsor, but the placement parent may differ.
    pub fn add_node(
        &mut self,
        user_id: Uuid,
        sponsor_id: Uuid,
        enrolled_at: i64,
    ) -> Result<NodeIndex, TreeError> {
        if self.arena.root.is_none() {
            return Err(TreeError::TreeEmpty);
        }
        if self.arena.index.contains_key(&user_id) {
            return Err(TreeError::UserAlreadyExists(user_id));
        }
        let sponsor_idx = self.arena.resolve(sponsor_id).map_err(|e| match e {
            TreeError::UserNotFound(id) => TreeError::SponsorNotFound(id),
            other => other,
        })?;

        let (parent_idx, position) = self
            .find_spillover_slot(sponsor_idx)
            .expect("BFS spillover found no open slot -- tree is full or corrupt");

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
        parent_slots[position] = Some(idx);
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

    // --- add_node (BFS spillover) tests ---

    #[test]
    fn add_node_places_in_sponsors_first_slot() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let node2 = tree.get_node(test_uuid(2)).unwrap();
        assert_eq!(node2.depth, 1);
        let parent_idx = node2.parent.unwrap();
        assert_eq!(tree.arena.node(parent_idx).user_id, test_uuid(1));
    }

    #[test]
    fn add_node_fills_sponsor_slots_left_to_right() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), 4000).unwrap();

        let root_idx = tree.arena.resolve(test_uuid(1)).unwrap();
        let slots = tree.slots.get(&root_idx).unwrap();
        let child_ids: Vec<Uuid> = slots
            .iter()
            .map(|s| tree.arena.node(s.unwrap()).user_id)
            .collect();
        assert_eq!(child_ids, vec![test_uuid(2), test_uuid(3), test_uuid(4)]);
    }

    #[test]
    fn add_node_spills_to_next_level() {
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        // Fill root's 2 slots.
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        // Next node should spill to node2's first slot.
        tree.add_node(test_uuid(4), test_uuid(1), 4000).unwrap();
        let node4 = tree.get_node(test_uuid(4)).unwrap();
        let parent_idx = node4.parent.unwrap();
        assert_eq!(tree.arena.node(parent_idx).user_id, test_uuid(2));
        assert_eq!(node4.depth, 2);
    }

    #[test]
    fn add_node_bfs_order_fills_level_before_going_deeper() {
        // 2-wide tree, 7 nodes total (root + 6).
        // Expected BFS layout:
        //        1 (root)
        //       / \
        //      2   3
        //     / \ / \
        //    4  5 6  7
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        for i in 2..=7u8 {
            tree.add_node(test_uuid(i), test_uuid(1), i as i64 * 1000)
                .unwrap();
        }

        // Verify parents: 2 and 3 under root.
        let node2 = tree.get_node(test_uuid(2)).unwrap();
        assert_eq!(tree.arena.node(node2.parent.unwrap()).user_id, test_uuid(1));
        let node3 = tree.get_node(test_uuid(3)).unwrap();
        assert_eq!(tree.arena.node(node3.parent.unwrap()).user_id, test_uuid(1));

        // Verify 4 and 5 under node2.
        let node4 = tree.get_node(test_uuid(4)).unwrap();
        assert_eq!(tree.arena.node(node4.parent.unwrap()).user_id, test_uuid(2));
        let node5 = tree.get_node(test_uuid(5)).unwrap();
        assert_eq!(tree.arena.node(node5.parent.unwrap()).user_id, test_uuid(2));

        // Verify 6 and 7 under node3.
        let node6 = tree.get_node(test_uuid(6)).unwrap();
        assert_eq!(tree.arena.node(node6.parent.unwrap()).user_id, test_uuid(3));
        let node7 = tree.get_node(test_uuid(7)).unwrap();
        assert_eq!(tree.arena.node(node7.parent.unwrap()).user_id, test_uuid(3));
    }

    #[test]
    fn add_node_sponsor_differs_from_placement_parent() {
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        // Fill root's slots with nodes sponsored by root.
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();
        // Next node: sponsored by root, but placed under node2 (spillover).
        tree.add_node(test_uuid(4), test_uuid(1), 4000).unwrap();
        let node4 = tree.get_node(test_uuid(4)).unwrap();
        let parent = tree.arena.node(node4.parent.unwrap());
        let sponsor = tree.arena.node(node4.sponsor.unwrap());
        assert_eq!(parent.user_id, test_uuid(2), "placement parent is node2");
        assert_eq!(sponsor.user_id, test_uuid(1), "sponsor is still root");
    }

    #[test]
    fn add_node_on_empty_tree_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 2000);
        assert!(matches!(result, Err(TreeError::TreeEmpty)));
    }

    #[test]
    fn add_node_sponsor_not_found_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(99), 2000);
        assert!(matches!(result, Err(TreeError::SponsorNotFound(_))));
    }

    #[test]
    fn add_node_duplicate_user_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        let result = tree.add_node(test_uuid(2), test_uuid(1), 3000);
        assert!(matches!(result, Err(TreeError::UserAlreadyExists(_))));
    }

    #[test]
    fn add_node_spillover_stays_in_sponsor_subtree() {
        // Build a 2-wide tree with two branches.
        // Sponsor node2 directly. Spillover must stay in node2's subtree.
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        // Place node2 and node3 under root.
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 1, 3000)
            .unwrap();
        // Fill node2's slots.
        tree.add_node_at(test_uuid(4), test_uuid(2), test_uuid(2), 0, 4000)
            .unwrap();
        tree.add_node_at(test_uuid(5), test_uuid(2), test_uuid(2), 1, 5000)
            .unwrap();
        // Now add via spillover under node2. It must go to node4 or node5,
        // not node3 (which is outside node2's subtree).
        tree.add_node(test_uuid(6), test_uuid(2), 6000).unwrap();
        let node6 = tree.get_node(test_uuid(6)).unwrap();
        let parent_id = tree.arena.node(node6.parent.unwrap()).user_id;
        assert!(
            parent_id == test_uuid(4) || parent_id == test_uuid(5),
            "spillover should place within sponsor's subtree, got parent {:?}",
            parent_id
        );
        // Specifically, BFS order means node4 slot 0 first.
        assert_eq!(parent_id, test_uuid(4));
    }
}
