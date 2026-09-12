use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::snapshot::SnapshotConsistencyError;
use crate::types::TreePosition;

/// Arena-backed binary tree.
///
/// Each node has at most two children: position 0 (left) and
/// position 1 (right). Placement requires an explicit position.
/// The tree validates positions but never picks alternatives
/// (decision 020).
#[derive(Serialize, Deserialize)]
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

    /// Prove a restored tree's stored indexes are in range and live, and that
    /// every live node has a child slot entry.
    ///
    /// The slot map and the arena edges are each checked alone. That the two
    /// agree is not checked. HEU-750.
    pub fn validate_restored(&self) -> Result<(), SnapshotConsistencyError> {
        self.arena.validate_restored()?;
        // Keep the lowest-slot fault rather than returning on the first one
        // found: self.slots is a HashMap with a randomized hasher, so
        // returning early makes which entry gets named vary between runs on
        // the same input. Still one pass.
        let mut fault: Option<(usize, SnapshotConsistencyError)> = None;
        for (parent, children) in &self.slots {
            let found = self.arena.check_slot_index(parent.0).err().or_else(|| {
                children
                    .iter()
                    .flatten()
                    .find_map(|child| self.arena.check_slot_index(child.0).err())
            });
            if let Some(err) = found {
                if fault.as_ref().is_none_or(|(held, _)| parent.0 < *held) {
                    fault = Some((parent.0, err));
                }
            }
        }
        if let Some((_, err)) = fault {
            return Err(err);
        }
        // A live node with no entry passes every check above and then panics
        // on the next query rather than returning a wrong answer.
        self.arena.check_every_live_node_has_a_slot(&self.slots)?;
        self.arena
            .check_every_live_node_is_slotted_once(&self.slots)?;
        Ok(())
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

    /// Internal helper for tests.
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
            .expect("slots entry missing for node -- arena and slots map out of sync");
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
                .expect("slots entry missing for node -- arena and slots map out of sync");
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
            .expect("slots entry missing for node -- arena and slots map out of sync");
        let mut children = Vec::with_capacity(2);
        if let Some(left) = slots[0] {
            children.push(left);
        }
        if let Some(right) = slots[1] {
            children.push(right);
        }
        self.arena.node_mut(parent_idx).children = children;
    }

    // --- Custom position methods (slot-based logic) ---

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
                .expect("slots entry missing for node -- arena and slots map out of sync");
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
            .expect("slots entry missing for node -- arena and slots map out of sync");
        pos.downline_counts.clear();
        for (slot_pos, slot) in node_slots.iter().enumerate() {
            let count = match slot {
                Some(child_idx) => self.arena.count_subtree(*child_idx),
                None => 0,
            };
            pos.downline_counts.insert(slot_pos, count);
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
            .expect("slots entry missing for node -- arena and slots map out of sync");

        if position > 1 {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: 2,
            });
        }

        match node_slots[position] {
            Some(child_idx) => Ok(self.arena.collect_subtree(child_idx)),
            None => Ok(vec![]),
        }
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
            .expect("slots entry missing for node -- arena and slots map out of sync");

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

    /// Provides read access to the arena for commission calculators
    /// and other crate-internal consumers.
    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
    }

    /// Provides read access to the binary slot map for commission
    /// calculators and other crate-internal consumers.
    pub(crate) fn slots(&self) -> &HashMap<NodeIndex, [Option<NodeIndex>; 2]> {
        &self.slots
    }
}

impl_arena_delegations!(BinaryTree);
impl_tree_navigator!(BinaryTree);

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

    fn binary_pair() -> (BinaryTree, NodeIndex) {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 0)
            .unwrap();
        let child = tree.arena.resolve(test_uuid(2)).unwrap();
        (tree, child)
    }

    #[test]
    fn validate_restored_accepts_a_healthy_tree() {
        let (tree, _) = binary_pair();

        assert_eq!(tree.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_live_node_no_parent_slots() {
        let (mut tree, child) = binary_pair();
        for children in tree.slots.values_mut() {
            for slot in children.iter_mut() {
                if *slot == Some(child) {
                    *slot = None;
                }
            }
        }

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::LiveNodeNotSlotted {
                slot: child.0,
                user_id: test_uuid(2),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_in_two_parent_slots() {
        let (mut tree, child) = binary_pair();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 0)
            .unwrap();
        let other_parent = tree.arena.resolve(test_uuid(3)).unwrap();
        tree.slots.get_mut(&other_parent).unwrap()[0] = Some(child);

        assert!(
            matches!(
                tree.validate_restored(),
                Err(SnapshotConsistencyError::ChildSlotRepeated { slot, .. }) if slot == child.0
            ),
            "expected ChildSlotRepeated naming slot {}, got {:?}",
            child.0,
            tree.validate_restored()
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_in_two_slots_of_one_parent() {
        // One parent, two of its own slots. Naming it as two parents would be
        // a message that states something the walk did not observe.
        let (mut tree, child) = binary_pair();
        let root = tree.arena.root.expect("binary_pair sets a root");
        tree.slots.get_mut(&root).unwrap()[1] = Some(child);

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::ChildSlottedTwiceUnderOneParent {
                slot: child.0,
                parent: root.0,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_two_parents_when_three_hold_a_child() {
        // The walk records only the offending child. Naming parents from it
        // would report whichever two hash order reached first.
        for _ in 0..64 {
            let (mut tree, child) = binary_pair();
            tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 0)
                .unwrap();
            tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 0)
                .unwrap();
            let p3 = tree.arena.resolve(test_uuid(3)).unwrap();
            let p4 = tree.arena.resolve(test_uuid(4)).unwrap();
            tree.slots.get_mut(&p3).unwrap()[1] = Some(child);
            tree.slots.get_mut(&p4).unwrap()[0] = Some(child);
            let root = tree.arena.root.expect("binary_pair sets a root");
            let mut expected = [root.0, p3.0, p4.0];
            expected.sort_unstable();

            assert_eq!(
                tree.validate_restored(),
                Err(SnapshotConsistencyError::ChildSlotRepeated {
                    slot: child.0,
                    first_parent: expected[0],
                    second_parent: expected[1],
                }),
                "the two lowest parents should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_unslotted_node_every_run() {
        // The constructor is inside the loop. One tree repeats its own map
        // order, so looping over a single instance proves nothing.
        for _ in 0..64 {
            let (mut tree, low) = binary_pair();
            tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 0)
                .unwrap();
            let high = tree.arena.resolve(test_uuid(3)).unwrap();
            assert!(low.0 < high.0, "the fixture must put the lower slot first");
            for children in tree.slots.values_mut() {
                for slot in children.iter_mut() {
                    if *slot == Some(low) || *slot == Some(high) {
                        *slot = None;
                    }
                }
            }

            assert_eq!(
                tree.validate_restored(),
                Err(SnapshotConsistencyError::LiveNodeNotSlotted {
                    slot: low.0,
                    user_id: test_uuid(2),
                }),
                "the lowest slot should win regardless of hash order"
            );
        }
    }

    #[test]
    fn engine_output_slots_every_live_node_exactly_once() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        // add_node is (user_id, parent_id, position, sponsor_id, enrolled_at).
        for n in 2..=12u8 {
            let parent = test_uuid(1 + (n - 2) / 2);
            let position = usize::from((n - 2) % 2);
            tree.add_node(test_uuid(n), parent, position, test_uuid(1), n as i64)
                .unwrap();
        }
        tree.remove_node(test_uuid(12)).unwrap();
        assert!(
            tree.arena.nodes.iter().any(|n| n.user_id == Uuid::nil()),
            "the removal should have left a tombstone to skip"
        );

        crate::tree::test_helpers::assert_live_nodes_are_slotted_once(&tree.arena, &tree.slots);
    }

    #[test]
    fn validate_restored_surfaces_an_arena_fault() {
        // Proves the arena delegation is present.
        let (mut tree, _) = binary_pair();
        tree.arena.index.insert(test_uuid(3), NodeIndex(99));

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::IndexSlotOutOfRange {
                user_id: test_uuid(3),
                slot: 99,
                node_count: 2,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_slots_offender_every_run() {
        // self.slots is a HashMap with a randomized hasher. Fresh tree each
        // iteration, so each gets its own seed.
        for _ in 0..64 {
            let (mut tree, _) = binary_pair();
            tree.slots.insert(NodeIndex(98), [None, None]);
            tree.slots.insert(NodeIndex(99), [None, None]);

            assert_eq!(
                tree.validate_restored(),
                Err(SnapshotConsistencyError::ChildSlotOutOfRange {
                    slot: 98,
                    node_count: 2,
                }),
                "the lowest slot should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_accepts_a_tree_after_a_removal() {
        // A removal leaves a tombstone with no slots entry. The completeness
        // sweep must skip it, or every restore of a tree that has ever lost a
        // node is rejected. That is a false rejection of a valid snapshot.
        let (mut tree, _) = binary_pair();
        tree.remove_node(test_uuid(2)).unwrap();

        assert_eq!(tree.validate_restored(), Ok(()));
    }

    #[test]
    fn validate_restored_rejects_a_slots_key_past_the_end() {
        let (mut tree, _) = binary_pair();
        tree.slots.insert(NodeIndex(99), [None, None]);

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::ChildSlotOutOfRange {
                slot: 99,
                node_count: 2,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_value_past_the_end() {
        let (mut tree, _) = binary_pair();
        let root = tree.arena.resolve(test_uuid(1)).unwrap();
        tree.slots.insert(root, [Some(NodeIndex(99)), None]);

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::ChildSlotOutOfRange {
                slot: 99,
                node_count: 2,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_value_naming_a_tombstone() {
        // The arena has to be internally consistent, or its own edge check
        // fires first and this guard is never reached. So the root drops its
        // children edge and the index entry goes, leaving only the slots map
        // naming the dead slot. That is a forgeable restore payload.
        let (mut tree, child) = binary_pair();
        let root = tree.arena.resolve(test_uuid(1)).unwrap();
        tree.arena.index.remove(&test_uuid(2));
        tree.arena.nodes[root.0].children.clear();
        tree.arena.nodes[root.0].sponsored.clear();
        tree.arena.tombstone(child);
        tree.slots.remove(&child);
        tree.slots.insert(root, [Some(child), None]);

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::ChildSlotTombstoned { slot: child.0 })
        );
    }

    #[test]
    fn validate_restored_rejects_a_live_node_with_no_slots_entry() {
        // A bounds-only check misses this one, and it ends in a panic rather
        // than a wrong answer: this file's expect() calls assume the entry is
        // present.
        let (mut tree, child) = binary_pair();
        tree.slots.remove(&child);

        assert_eq!(
            tree.validate_restored(),
            Err(SnapshotConsistencyError::SlotEntryMissing {
                slot: child.0,
                user_id: test_uuid(2),
            })
        );
    }

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
    fn get_position_downline_counts_right_only_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        // Right child only — left slot is empty.
        tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000)
            .unwrap();
        // Add a child under the right child to give it a nonzero subtree.
        tree.add_node(test_uuid(3), test_uuid(2), 0, test_uuid(1), 3000)
            .unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        assert_eq!(pos.downline_counts.len(), 2, "both slots should be present");
        assert_eq!(pos.downline_counts[&0], 0, "empty left slot should be 0");
        assert_eq!(
            pos.downline_counts[&1], 1,
            "right slot has one descendant under it"
        );
    }

    #[test]
    fn get_position_downline_counts_leaf_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();

        // Node 2 is a leaf — both slots are empty.
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.downline_counts.len(), 2, "both slots should be present");
        assert_eq!(pos.downline_counts[&0], 0);
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

    #[test]
    fn snapshot_round_trip() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 4000)
            .unwrap();

        let json = serde_json::to_string(&tree).unwrap();
        let restored: BinaryTree = serde_json::from_str(&json).unwrap();

        // Verify all nodes exist in restored tree.
        assert!(restored.contains(test_uuid(1)));
        assert!(restored.contains(test_uuid(2)));
        assert!(restored.contains(test_uuid(3)));
        assert!(restored.contains(test_uuid(4)));

        // Verify binary structure is preserved.
        let children = restored.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 2);
        assert_eq!(children[0].user_id, test_uuid(2));
        assert_eq!(children[1].user_id, test_uuid(3));

        // Verify slot positions are preserved.
        let pos = restored.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 0);
        let pos = restored.get_position(test_uuid(3)).unwrap();
        assert_eq!(pos.position, 1);

        // Verify depths are preserved.
        let pos = restored.get_position(test_uuid(4)).unwrap();
        assert_eq!(pos.depth, 2);

        // Verify sponsor links are preserved.
        let sponsor = restored.get_sponsor(test_uuid(4)).unwrap();
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));
    }
}
