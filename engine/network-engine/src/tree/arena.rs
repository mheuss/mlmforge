use std::collections::{HashMap, HashSet, VecDeque};

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::snapshot::SnapshotConsistencyError;
use crate::types::TreePosition;

/// Shared arena storage for all tree types.
///
/// Owns the contiguous node Vec, UUID-to-index map, free list, and root.
/// Tree type wrappers (UnilevelTree, BinaryTree) delegate storage and
/// traversal operations to Arena while enforcing their own shape constraints.
#[derive(Serialize, Deserialize)]
pub(crate) struct Arena {
    pub(crate) nodes: Vec<Node>,
    pub(crate) index: HashMap<Uuid, NodeIndex>,
    pub(crate) free_list: Vec<NodeIndex>,
    pub(crate) root: Option<NodeIndex>,
}

impl Arena {
    pub(crate) fn new() -> Self {
        Self {
            nodes: Vec::new(),
            index: HashMap::new(),
            free_list: Vec::new(),
            root: None,
        }
    }

    /// Prove every stored `NodeIndex` is in range and points at a live slot.
    pub(crate) fn validate_restored(&self) -> Result<(), SnapshotConsistencyError> {
        let node_count = self.nodes.len();

        // index -> nodes
        //
        // Keep the lowest-user_id fault rather than returning on the first one
        // found: self.index is a HashMap with a randomized hasher, so returning
        // early makes which offender gets named vary between runs on the same
        // input. Still one pass.
        let mut fault: Option<(Uuid, SnapshotConsistencyError)> = None;
        for (user_id, idx) in &self.index {
            // Liveness before equality. A tombstone's user_id is Uuid::nil(),
            // so an index entry of nil -> tombstoned slot satisfies the
            // equality check and the node pass then skips the tombstone. The
            // entry would survive both passes.
            let found = match self.nodes.get(idx.0) {
                None => Some(SnapshotConsistencyError::IndexSlotOutOfRange {
                    user_id: *user_id,
                    slot: idx.0,
                    node_count,
                }),
                Some(slot) if slot.user_id == Uuid::nil() => {
                    Some(SnapshotConsistencyError::IndexSlotTombstoned {
                        user_id: *user_id,
                        slot: idx.0,
                    })
                }
                Some(slot) if slot.user_id != *user_id => {
                    Some(SnapshotConsistencyError::IndexSlotMismatch {
                        user_id: *user_id,
                        slot: idx.0,
                        found: slot.user_id,
                    })
                }
                Some(_) => None,
            };
            if let Some(err) = found {
                if fault.as_ref().is_none_or(|(held, _)| user_id < held) {
                    fault = Some((*user_id, err));
                }
            }
        }
        if let Some((_, err)) = fault {
            return Err(err);
        }

        // nodes -> index, and every edge on every live node
        for (slot, node) in self.nodes.iter().enumerate() {
            if node.user_id == Uuid::nil() {
                // A tombstone is cleared to nil with empty fields. Hold a
                // restored one to that shape here rather than trusting the
                // liveness checks elsewhere to keep it unreachable.
                let stale = if node.parent.is_some() {
                    Some("parent")
                } else if node.sponsor.is_some() {
                    Some("sponsor")
                } else if !node.children.is_empty() {
                    Some("child")
                } else if !node.sponsored.is_empty() {
                    Some("sponsored node")
                } else {
                    None
                };
                if let Some(field) = stale {
                    return Err(SnapshotConsistencyError::TombstoneNotCleared { slot, field });
                }
                continue;
            }
            if self.index.get(&node.user_id) != Some(&NodeIndex(slot)) {
                return Err(SnapshotConsistencyError::NodeNotIndexed {
                    slot,
                    user_id: node.user_id,
                });
            }
            self.check_edge("parent", slot, node.parent)?;
            self.check_edge("sponsor", slot, node.sponsor)?;
            for child in &node.children {
                self.check_edge("children", slot, Some(*child))?;
            }
            for sponsored in &node.sponsored {
                self.check_edge("sponsored", slot, Some(*sponsored))?;
            }
        }

        // depth
        //
        // Runs after the edge pass, which is what establishes that a parent
        // index is in range and live. A downline walk subtracts a start depth
        // from a node's own, so a child shallower than its parent underflows.
        for (slot, node) in self.nodes.iter().enumerate() {
            if node.user_id == Uuid::nil() {
                continue;
            }
            match node.parent {
                None => {
                    if node.depth != 0 {
                        return Err(SnapshotConsistencyError::NodeDepthNotZero {
                            slot,
                            user_id: node.user_id,
                            depth: node.depth,
                        });
                    }
                }
                Some(parent) => {
                    // checked_add, not +1: slot order can reach a child before
                    // the parent whose own depth this pass would reject, and a
                    // forged parent at u32::MAX would overflow here first.
                    let parent_depth = self.nodes[parent.0].depth;
                    if Some(node.depth) != parent_depth.checked_add(1) {
                        return Err(SnapshotConsistencyError::NodeDepthMismatch {
                            slot,
                            user_id: node.user_id,
                            depth: node.depth,
                            parent_slot: parent.0,
                            parent_depth,
                        });
                    }
                }
            }
        }

        // free_list
        let mut seen = HashSet::new();
        for idx in &self.free_list {
            let slot =
                self.nodes
                    .get(idx.0)
                    .ok_or(SnapshotConsistencyError::FreeSlotOutOfRange {
                        slot: idx.0,
                        node_count,
                    })?;
            if slot.user_id != Uuid::nil() {
                return Err(SnapshotConsistencyError::FreeSlotNotTombstoned {
                    slot: idx.0,
                    found: slot.user_id,
                });
            }
            if !seen.insert(idx.0) {
                return Err(SnapshotConsistencyError::FreeSlotRepeated { slot: idx.0 });
            }
        }

        // root
        if let Some(root) = self.root {
            let slot = self
                .nodes
                .get(root.0)
                .ok_or(SnapshotConsistencyError::RootOutOfRange {
                    slot: root.0,
                    node_count,
                })?;
            if slot.user_id == Uuid::nil() {
                return Err(SnapshotConsistencyError::RootTombstoned { slot: root.0 });
            }
        }

        Ok(())
    }

    /// Prove a child-slot map entry is in range and names a live slot.
    pub(crate) fn check_slot_index(&self, slot: usize) -> Result<(), SnapshotConsistencyError> {
        let node = self
            .nodes
            .get(slot)
            .ok_or(SnapshotConsistencyError::ChildSlotOutOfRange {
                slot,
                node_count: self.nodes.len(),
            })?;
        if node.user_id == Uuid::nil() {
            return Err(SnapshotConsistencyError::ChildSlotTombstoned { slot });
        }
        Ok(())
    }

    /// Prove every live node has a child-slot entry.
    pub(crate) fn check_every_live_node_has_a_slot<V>(
        &self,
        slots: &HashMap<NodeIndex, V>,
    ) -> Result<(), SnapshotConsistencyError> {
        for (slot, node) in self.nodes.iter().enumerate() {
            // Skipping tombstones is load-bearing: every tree that has had a
            // node removed carries one, and they hold no slot entry.
            if node.user_id == Uuid::nil() {
                continue;
            }
            if !slots.contains_key(&NodeIndex(slot)) {
                return Err(SnapshotConsistencyError::SlotEntryMissing {
                    slot,
                    user_id: node.user_id,
                });
            }
        }
        Ok(())
    }

    /// Prove every live non-root node is a child in exactly one slot entry.
    pub(crate) fn check_every_live_node_is_slotted_once<C>(
        &self,
        slots: &HashMap<NodeIndex, C>,
    ) -> Result<(), SnapshotConsistencyError>
    where
        for<'a> &'a C: IntoIterator<Item = &'a Option<NodeIndex>>,
    {
        // The walk records only which child repeated, holding the lowest.
        // Naming the parents from the walk would name whichever two hash
        // order reached first, which varies per run once three parents are
        // involved.
        let mut first_seen: HashSet<NodeIndex> = HashSet::new();
        let mut repeat: Option<NodeIndex> = None;
        for children in slots.values() {
            for child in children.into_iter().flatten() {
                if !first_seen.insert(*child) && repeat.is_none_or(|held| child.0 < held.0) {
                    repeat = Some(*child);
                }
            }
        }
        if let Some(child) = repeat {
            // Rejection path only. Collect every parent naming this child so
            // the message is the same on every run, and always return: falling
            // through would accept the payload that reached here.
            let mut parents: Vec<usize> = slots
                .iter()
                .filter(|(_, children)| children.into_iter().flatten().any(|c| *c == child))
                .map(|(parent, _)| parent.0)
                .collect();
            parents.sort_unstable();
            return match (parents.first().copied(), parents.get(1).copied()) {
                (Some(first_parent), Some(second_parent)) => {
                    Err(SnapshotConsistencyError::ChildSlotRepeated {
                        slot: child.0,
                        first_parent,
                        second_parent,
                    })
                }
                // One parent names it, so it sits in two of that parent's slots.
                (Some(parent), None) => {
                    Err(SnapshotConsistencyError::ChildSlottedTwiceUnderOneParent {
                        slot: child.0,
                        parent,
                    })
                }
                // `repeat` is only set from a child found in `slots`, so the
                // collect above cannot come back empty.
                (None, _) => {
                    unreachable!(
                        "repeated child {} matched no parent in the slot map",
                        child.0
                    )
                }
            };
        }

        // self.nodes is a Vec, so this walks in slot order and the first miss
        // is the same one on every run.
        for (slot, node) in self.nodes.iter().enumerate() {
            if node.user_id == Uuid::nil() || self.root == Some(NodeIndex(slot)) {
                continue;
            }
            if !first_seen.contains(&NodeIndex(slot)) {
                return Err(SnapshotConsistencyError::LiveNodeNotSlotted {
                    slot,
                    user_id: node.user_id,
                });
            }
        }
        Ok(())
    }

    fn check_edge(
        &self,
        field: &'static str,
        slot: usize,
        target: Option<NodeIndex>,
    ) -> Result<(), SnapshotConsistencyError> {
        let Some(target) = target else {
            return Ok(());
        };
        let found = self
            .nodes
            .get(target.0)
            .ok_or(SnapshotConsistencyError::EdgeOutOfRange {
                field,
                slot,
                target: target.0,
                node_count: self.nodes.len(),
            })?;
        if found.user_id == Uuid::nil() {
            return Err(SnapshotConsistencyError::EdgeTombstoned {
                field,
                slot,
                target: target.0,
            });
        }
        Ok(())
    }

    /// Resolves a user ID to a NodeIndex.
    pub(crate) fn resolve(&self, user_id: Uuid) -> Result<NodeIndex, TreeError> {
        self.index
            .get(&user_id)
            .copied()
            .ok_or(TreeError::UserNotFound(user_id))
    }

    /// Direct arena access by index.
    pub(crate) fn node(&self, idx: NodeIndex) -> &Node {
        let n = &self.nodes[idx.0];
        debug_assert!(
            n.user_id != uuid::Uuid::nil(),
            "accessed tombstoned slot at index {}",
            idx.0
        );
        n
    }

    /// Mutable arena access by index.
    pub(crate) fn node_mut(&mut self, idx: NodeIndex) -> &mut Node {
        debug_assert!(
            self.nodes[idx.0].user_id != uuid::Uuid::nil(),
            "accessed tombstoned slot at index {}",
            idx.0
        );
        &mut self.nodes[idx.0]
    }

    /// Allocates a slot in the arena. Reuses tombstoned slots from the
    /// free list when available. Otherwise appends to the Vec.
    pub(crate) fn alloc_slot(&mut self, node: Node) -> NodeIndex {
        if let Some(free_idx) = self.free_list.pop() {
            self.nodes[free_idx.0] = node;
            free_idx
        } else {
            let idx = NodeIndex(self.nodes.len());
            self.nodes.push(node);
            idx
        }
    }

    /// Clears a slot and adds it to the free list for reuse.
    pub(crate) fn tombstone(&mut self, idx: NodeIndex) {
        debug_assert!(
            !self.free_list.contains(&idx),
            "double tombstone at index {}",
            idx.0
        );
        self.nodes[idx.0] = Node {
            user_id: Uuid::nil(),
            parent: None,
            children: Vec::new(),
            sponsor: None,
            sponsored: Vec::new(),
            depth: 0,
            enrolled_at: 0,
        };
        self.free_list.push(idx);
    }

    /// Returns the number of live nodes (total slots minus free slots).
    pub(crate) fn node_count(&self) -> usize {
        self.nodes.len() - self.free_list.len()
    }

    // --- Shared placement traversals ---

    /// Walks upward from a node toward the root, following placement parent links.
    ///
    /// Returns ancestors in order from immediate parent to root.
    /// The starting node is not included in the result.
    ///
    /// Depth 0 means walk all the way to root. Any other value limits
    /// the walk to that many levels up.
    pub(crate) fn walk_upline(&self, start_idx: NodeIndex, depth: u32) -> Vec<&Node> {
        let mut result = Vec::new();
        let mut current = self.nodes[start_idx.0].parent;
        let mut steps = 0u32;

        while let Some(parent_idx) = current {
            if depth > 0 && steps >= depth {
                break;
            }
            result.push(&self.nodes[parent_idx.0]);
            current = self.nodes[parent_idx.0].parent;
            steps += 1;
        }

        result
    }

    /// Walks downward from a node in BFS order, following placement children.
    ///
    /// Returns descendants in breadth-first order. The starting node
    /// is not included in the result.
    ///
    /// Depth 0 means walk all levels. Any other value limits the walk
    /// to that many levels below the starting node.
    pub(crate) fn bfs_downline(&self, start_idx: NodeIndex, depth: u32) -> Vec<&Node> {
        let start_depth = self.nodes[start_idx.0].depth;
        let mut result = Vec::new();
        let mut queue = VecDeque::new();

        for &child_idx in &self.nodes[start_idx.0].children {
            queue.push_back(child_idx);
        }

        while let Some(idx) = queue.pop_front() {
            let node = &self.nodes[idx.0];
            let relative_depth = node.depth - start_depth;

            result.push(node);

            if depth == 0 || relative_depth < depth {
                for &child_idx in &node.children {
                    queue.push_back(child_idx);
                }
            }
        }

        result
    }

    /// Counts descendants without allocating a result Vec.
    pub(crate) fn count_downline(&self, start_idx: NodeIndex, depth: u32) -> usize {
        let start_depth = self.nodes[start_idx.0].depth;
        let mut count = 0;
        let mut queue = VecDeque::new();

        for &child_idx in &self.nodes[start_idx.0].children {
            queue.push_back(child_idx);
        }

        while let Some(idx) = queue.pop_front() {
            let node = &self.nodes[idx.0];
            let relative_depth = node.depth - start_depth;

            count += 1;

            if depth == 0 || relative_depth < depth {
                for &child_idx in &node.children {
                    queue.push_back(child_idx);
                }
            }
        }

        count
    }

    /// Returns all nodes in the subtree under a specific child position.
    ///
    /// Results include the child at the given position and all of
    /// its descendants, in BFS order.
    pub(crate) fn get_branch(
        &self,
        parent_idx: NodeIndex,
        position: usize,
    ) -> Result<Vec<&Node>, TreeError> {
        let node = &self.nodes[parent_idx.0];

        if position >= node.children.len() {
            return Err(TreeError::PositionOutOfRange {
                user_id: node.user_id,
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

    /// Counts nodes in the subtree under a specific child position.
    pub(crate) fn count_branch(
        &self,
        parent_idx: NodeIndex,
        position: usize,
    ) -> Result<usize, TreeError> {
        let node = &self.nodes[parent_idx.0];

        if position >= node.children.len() {
            return Err(TreeError::PositionOutOfRange {
                user_id: node.user_id,
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

    /// Counts all descendants of a node, not including the node itself.
    ///
    /// Equivalent to `count_downline(idx, 0)` but reads more clearly
    /// when used for branch counting where depth limits don't apply.
    pub(crate) fn count_subtree(&self, start_idx: NodeIndex) -> usize {
        self.count_downline(start_idx, 0)
    }

    /// Collects a node and all its descendants in BFS order.
    ///
    /// Unlike `bfs_downline`, this includes the starting node itself.
    /// Used by slot-based trees (binary, matrix) for branch collection.
    pub(crate) fn collect_subtree(&self, root_idx: NodeIndex) -> Vec<&Node> {
        let mut result = Vec::new();
        let mut queue = VecDeque::new();
        queue.push_back(root_idx);
        while let Some(current) = queue.pop_front() {
            result.push(self.node(current));
            for &child_idx in &self.node(current).children {
                queue.push_back(child_idx);
            }
        }
        result
    }

    /// Checks whether user_idx is a descendant of ancestor_idx.
    pub(crate) fn is_descendant_of(&self, user_idx: NodeIndex, ancestor_idx: NodeIndex) -> bool {
        if user_idx == ancestor_idx {
            return false;
        }

        let mut current_idx = user_idx;
        loop {
            match self.nodes[current_idx.0].parent {
                Some(parent_idx) => {
                    if parent_idx == ancestor_idx {
                        return true;
                    }
                    current_idx = parent_idx;
                }
                None => return false,
            }
        }
    }

    /// Computes a full position snapshot for a user.
    pub(crate) fn get_position(&self, idx: NodeIndex) -> TreePosition {
        let node = &self.nodes[idx.0];

        let parent_user_id = node
            .parent
            .map(|parent_idx| self.nodes[parent_idx.0].user_id);

        let sponsor_user_id = node
            .sponsor
            .map(|sponsor_idx| self.nodes[sponsor_idx.0].user_id);

        let position = if let Some(parent_idx) = node.parent {
            self.nodes[parent_idx.0]
                .children
                .iter()
                .position(|&child_idx| child_idx == idx)
                .expect("node not found in parent's children list — tree is corrupt")
        } else {
            0
        };

        let mut downline_counts = HashMap::new();
        for (child_pos, &child_idx) in node.children.iter().enumerate() {
            let count = self.count_subtree(child_idx);
            downline_counts.insert(child_pos, count);
        }

        TreePosition {
            user_id: node.user_id,
            parent_user_id,
            sponsor_user_id,
            position,
            depth: node.depth,
            child_count: node.children.len(),
            downline_counts,
            enrolled_at: node.enrolled_at,
        }
    }

    // --- Sponsor traversals ---

    /// Returns the sponsor of a node, or None if the node has no sponsor (root).
    pub(crate) fn get_sponsor(&self, idx: NodeIndex) -> Option<&Node> {
        self.nodes[idx.0]
            .sponsor
            .map(|sponsor_idx| &self.nodes[sponsor_idx.0])
    }

    /// Walks upward following sponsor links.
    ///
    /// Returns sponsors in order from immediate sponsor to the root sponsor.
    /// The starting node is not included.
    ///
    /// Depth 0 means walk all the way. Any other value limits the walk.
    pub(crate) fn walk_sponsor_upline(&self, start_idx: NodeIndex, depth: u32) -> Vec<&Node> {
        let mut result = Vec::new();
        let mut current = self.nodes[start_idx.0].sponsor;
        let mut steps = 0u32;

        while let Some(sponsor_idx) = current {
            if depth > 0 && steps >= depth {
                break;
            }
            result.push(&self.nodes[sponsor_idx.0]);
            current = self.nodes[sponsor_idx.0].sponsor;
            steps += 1;
        }

        result
    }

    /// Returns the direct recruits of a node (the sponsored Vec).
    pub(crate) fn get_sponsored(&self, idx: NodeIndex) -> Vec<&Node> {
        self.nodes[idx.0]
            .sponsored
            .iter()
            .map(|&child_idx| &self.nodes[child_idx.0])
            .collect()
    }
}

/// Generates shared UUID-accepting query methods that delegate to Arena.
///
/// All tree types share these exact implementations. Methods that vary
/// per tree type (get_position, get_branch, count_branch) are NOT
/// included and must be implemented manually.
macro_rules! impl_arena_delegations {
    ($tree_type:ty) => {
        impl $tree_type {
            /// Returns true if the tree contains a node with this user_id.
            pub fn contains(&self, user_id: Uuid) -> bool {
                self.arena.index.contains_key(&user_id)
            }

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

            /// Counts descendants without allocating a result Vec.
            ///
            /// Depth 0 means count all descendants. Any other value limits
            /// the count to that many levels below.
            pub fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
                let idx = self.arena.resolve(user_id)?;
                Ok(self.arena.count_downline(idx, depth))
            }

            /// Checks whether `user_id` is a descendant of `ancestor_id`.
            ///
            /// A node is not considered a descendant of itself.
            pub fn is_descendant_of(
                &self,
                user_id: Uuid,
                ancestor_id: Uuid,
            ) -> Result<bool, TreeError> {
                let ancestor_idx = self.arena.resolve(ancestor_id)?;
                if user_id == ancestor_id {
                    return Ok(false);
                }
                let user_idx = self.arena.resolve(user_id)?;
                Ok(self.arena.is_descendant_of(user_idx, ancestor_idx))
            }

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
            pub fn get_sponsor_upline(
                &self,
                user_id: Uuid,
                depth: u32,
            ) -> Result<Vec<&Node>, TreeError> {
                let idx = self.arena.resolve(user_id)?;
                Ok(self.arena.walk_sponsor_upline(idx, depth))
            }

            /// Returns the direct recruits of a node.
            pub fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
                let idx = self.arena.resolve(user_id)?;
                Ok(self.arena.get_sponsored(idx))
            }
        }
    };
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::test_helpers::test_uuid;

    fn make_node(user_id: Uuid, parent: Option<NodeIndex>, depth: u32) -> Node {
        Node {
            user_id,
            parent,
            children: Vec::new(),
            sponsor: None,
            sponsored: Vec::new(),
            depth,
            enrolled_at: 0,
        }
    }

    #[test]
    fn resolve_existing_user() {
        let mut arena = Arena::new();
        let idx = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        arena.index.insert(test_uuid(1), idx);
        assert_eq!(arena.resolve(test_uuid(1)).unwrap(), idx);
    }

    #[test]
    fn resolve_missing_user_fails() {
        let arena = Arena::new();
        assert!(matches!(
            arena.resolve(test_uuid(99)),
            Err(TreeError::UserNotFound(_))
        ));
    }

    #[test]
    fn alloc_reuses_free_slots() {
        let mut arena = Arena::new();
        let idx = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        arena.tombstone(idx);
        let idx2 = arena.alloc_slot(make_node(test_uuid(2), None, 0));
        assert_eq!(idx, idx2);
        assert_eq!(arena.nodes.len(), 1);
    }

    #[test]
    fn tombstone_clears_node() {
        let mut arena = Arena::new();
        let idx = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        arena.tombstone(idx);
        // Access the raw slot directly to avoid the debug_assert in node().
        assert_eq!(arena.nodes[idx.0].user_id, Uuid::nil());
    }

    #[test]
    fn node_count_excludes_tombstoned() {
        let mut arena = Arena::new();
        let idx1 = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        arena.alloc_slot(make_node(test_uuid(2), Some(idx1), 1));
        assert_eq!(arena.node_count(), 2);
        arena.tombstone(idx1);
        assert_eq!(arena.node_count(), 1);
    }

    #[test]
    fn walk_upline_to_root() {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        let child = arena.alloc_slot(make_node(test_uuid(2), Some(root), 1));
        let grandchild = arena.alloc_slot(make_node(test_uuid(3), Some(child), 2));
        let upline = arena.walk_upline(grandchild, 0);
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(2));
        assert_eq!(upline[1].user_id, test_uuid(1));
    }

    #[test]
    fn walk_upline_with_depth_limit() {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        let child = arena.alloc_slot(make_node(test_uuid(2), Some(root), 1));
        let grandchild = arena.alloc_slot(make_node(test_uuid(3), Some(child), 2));
        let upline = arena.walk_upline(grandchild, 1);
        assert_eq!(upline.len(), 1);
        assert_eq!(upline[0].user_id, test_uuid(2));
    }

    #[test]
    fn bfs_downline_returns_all_descendants() {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        let c1 = arena.alloc_slot(make_node(test_uuid(2), Some(root), 1));
        let c2 = arena.alloc_slot(make_node(test_uuid(3), Some(root), 1));
        arena.alloc_slot(make_node(test_uuid(4), Some(c1), 2));
        arena.node_mut(root).children = vec![c1, c2];
        arena.node_mut(c1).children = vec![NodeIndex(3)];
        let downline = arena.bfs_downline(root, 0);
        assert_eq!(downline.len(), 3);
    }

    #[test]
    fn is_descendant_of_self_returns_false() {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        assert!(!arena.is_descendant_of(root, root));
    }

    #[test]
    fn sponsor_traversal() {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(make_node(test_uuid(1), None, 0));
        let mut child_node = make_node(test_uuid(2), Some(root), 1);
        child_node.sponsor = Some(root);
        let child = arena.alloc_slot(child_node);
        arena.node_mut(root).sponsored.push(child);

        let sponsor = arena.get_sponsor(child);
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));

        let sponsored = arena.get_sponsored(root);
        assert_eq!(sponsored.len(), 1);
        assert_eq!(sponsored[0].user_id, test_uuid(2));

        let upline = arena.walk_sponsor_upline(child, 0);
        assert_eq!(upline.len(), 1);
        assert_eq!(upline[0].user_id, test_uuid(1));
    }

    fn live_arena() -> Arena {
        let mut arena = Arena::new();
        let root = arena.alloc_slot(Node {
            user_id: test_uuid(1),
            parent: None,
            children: vec![],
            sponsor: None,
            sponsored: vec![],
            depth: 0,
            enrolled_at: 0,
        });
        arena.index.insert(test_uuid(1), root);
        arena.root = Some(root);
        arena
    }

    #[test]
    fn validate_restored_accepts_a_consistent_arena() {
        assert!(live_arena().validate_restored().is_ok());
    }

    #[test]
    fn validate_restored_rejects_an_index_slot_past_the_end() {
        let mut arena = live_arena();
        arena.index.insert(test_uuid(2), NodeIndex(99));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::IndexSlotOutOfRange {
                user_id: test_uuid(2),
                slot: 99,
                node_count: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_an_index_slot_holding_someone_else() {
        let mut arena = live_arena();
        arena.index.insert(test_uuid(2), NodeIndex(0));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::IndexSlotMismatch {
                user_id: test_uuid(2),
                slot: 0,
                found: test_uuid(1),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_live_node_with_no_index_entry() {
        let mut arena = live_arena();
        arena.index.clear();

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::NodeNotIndexed {
                slot: 0,
                user_id: test_uuid(1),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_parent_edge_past_the_end() {
        let mut arena = live_arena();
        arena.nodes[0].parent = Some(NodeIndex(99));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::EdgeOutOfRange {
                field: "parent",
                slot: 0,
                target: 99,
                node_count: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_edge_naming_a_tombstone() {
        let mut arena = live_arena();
        let dead = arena.alloc_slot(Node {
            user_id: test_uuid(2),
            parent: None,
            children: vec![],
            sponsor: None,
            sponsored: vec![],
            depth: 1,
            enrolled_at: 0,
        });
        arena.tombstone(dead);
        arena.nodes[0].children = vec![dead];

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::EdgeTombstoned {
                field: "children",
                slot: 0,
                target: dead.0,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_nil_index_entry_on_a_tombstone() {
        // Both ids are Uuid::nil(), so an equality-only check passes and the
        // node pass skips tombstones. Liveness has to be tested first.
        let mut arena = live_arena();
        let dead = arena.alloc_slot(Node {
            user_id: test_uuid(2),
            parent: None,
            children: vec![],
            sponsor: None,
            sponsored: vec![],
            depth: 1,
            enrolled_at: 0,
        });
        arena.tombstone(dead);
        arena.index.insert(Uuid::nil(), dead);

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::IndexSlotTombstoned {
                user_id: Uuid::nil(),
                slot: dead.0,
            })
        );
    }

    #[test]
    fn validate_restored_checks_every_edge_field() {
        // The implementation checks parent, sponsor, children and sponsored.
        // Assert all four rather than two, so a copy-paste omission fails here
        // rather than in a restored snapshot.
        for field in ["parent", "sponsor", "children", "sponsored"] {
            let mut arena = live_arena();
            match field {
                "parent" => arena.nodes[0].parent = Some(NodeIndex(99)),
                "sponsor" => arena.nodes[0].sponsor = Some(NodeIndex(99)),
                "children" => arena.nodes[0].children = vec![NodeIndex(99)],
                _ => arena.nodes[0].sponsored = vec![NodeIndex(99)],
            }
            assert_eq!(
                arena.validate_restored(),
                Err(SnapshotConsistencyError::EdgeOutOfRange {
                    field,
                    slot: 0,
                    target: 99,
                    node_count: 1,
                }),
                "{field} was not checked"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_tombstone_that_kept_any_edge() {
        // The implementation checks parent, sponsor, children and sponsored in
        // one if/else-if chain. Assert all four, so deleting an arm fails here
        // rather than in a restored snapshot. This also pins the four field
        // strings, which the operator reads back in the message.
        for (field, expected) in [
            ("parent", "parent"),
            ("sponsor", "sponsor"),
            ("children", "child"),
            ("sponsored", "sponsored node"),
        ] {
            let mut arena = live_arena();
            let dead = arena.alloc_slot(make_node(test_uuid(2), None, 1));
            arena.tombstone(dead);
            match field {
                "parent" => arena.nodes[dead.0].parent = Some(NodeIndex(0)),
                "sponsor" => arena.nodes[dead.0].sponsor = Some(NodeIndex(0)),
                "children" => arena.nodes[dead.0].children = vec![NodeIndex(0)],
                _ => arena.nodes[dead.0].sponsored = vec![NodeIndex(0)],
            }

            assert_eq!(
                arena.validate_restored(),
                Err(SnapshotConsistencyError::TombstoneNotCleared {
                    slot: dead.0,
                    field: expected,
                }),
                "a tombstone keeping {field} was not rejected"
            );
        }
    }

    #[test]
    fn validate_restored_names_the_same_offender_every_run() {
        // The index is a HashMap with a randomized hasher, so a per-run
        // iteration order would make a two-fault arena report either one. A
        // rejection an operator cannot reproduce is worse than a slower check.
        // Fresh arena each iteration, so each gets its own hasher seed.
        for _ in 0..64 {
            let mut arena = live_arena();
            arena.index.insert(test_uuid(3), NodeIndex(98));
            arena.index.insert(test_uuid(2), NodeIndex(99));

            assert_eq!(
                arena.validate_restored(),
                Err(SnapshotConsistencyError::IndexSlotOutOfRange {
                    user_id: test_uuid(2),
                    slot: 99,
                    node_count: 1,
                }),
                "the lowest user_id should win regardless of hash order"
            );
        }
    }

    #[test]
    fn validate_restored_rejects_a_free_slot_past_the_end() {
        let mut arena = live_arena();
        arena.free_list.push(NodeIndex(99));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::FreeSlotOutOfRange {
                slot: 99,
                node_count: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_root_naming_a_tombstone() {
        // In range and dead, rather than out of range.
        let mut arena = live_arena();
        let dead = arena.alloc_slot(make_node(test_uuid(2), None, 1));
        arena.tombstone(dead);
        arena.root = Some(dead);

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::RootTombstoned { slot: dead.0 })
        );
    }

    #[test]
    fn validate_restored_rejects_a_free_slot_that_is_not_a_tombstone() {
        let mut arena = live_arena();
        arena.free_list.push(NodeIndex(0));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::FreeSlotNotTombstoned {
                slot: 0,
                found: test_uuid(1),
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_repeated_free_slot() {
        // alloc_slot pops from free_list, so a repeat hands one slot to two
        // nodes and the second silently overwrites the first.
        let mut arena = live_arena();
        let dead = arena.alloc_slot(Node {
            user_id: test_uuid(2),
            parent: None,
            children: vec![],
            sponsor: None,
            sponsored: vec![],
            depth: 1,
            enrolled_at: 0,
        });
        arena.tombstone(dead);
        arena.free_list.push(dead);

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::FreeSlotRepeated { slot: dead.0 })
        );
    }

    #[test]
    fn validate_restored_rejects_a_root_past_the_end() {
        let mut arena = live_arena();
        arena.root = Some(NodeIndex(99));

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::RootOutOfRange {
                slot: 99,
                node_count: 1,
            })
        );
    }

    #[test]
    fn validate_restored_accepts_an_empty_arena() {
        assert!(Arena::new().validate_restored().is_ok());
    }

    fn arena_with_a_child(child_depth: u32) -> Arena {
        let mut arena = live_arena();
        let root = arena.root.expect("live_arena sets a root");
        let child = arena.alloc_slot(make_node(test_uuid(2), Some(root), child_depth));
        arena.index.insert(test_uuid(2), child);
        arena.nodes[root.0].children.push(child);
        arena
    }

    #[test]
    fn validate_restored_accepts_a_child_one_below_its_parent() {
        assert!(arena_with_a_child(1).validate_restored().is_ok());
    }

    #[test]
    fn validate_restored_rejects_a_child_shallower_than_its_parent() {
        // The subtraction in a downline walk underflows on this shape.
        let mut arena = arena_with_a_child(1);
        let mid = NodeIndex(1);
        let leaf = arena.alloc_slot(make_node(test_uuid(3), Some(mid), 0));
        arena.index.insert(test_uuid(3), leaf);
        arena.nodes[mid.0].children.push(leaf);

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::NodeDepthMismatch {
                slot: 2,
                user_id: test_uuid(3),
                depth: 0,
                parent_slot: 1,
                parent_depth: 1,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_child_deeper_than_one_below_its_parent() {
        assert_eq!(
            arena_with_a_child(2).validate_restored(),
            Err(SnapshotConsistencyError::NodeDepthMismatch {
                slot: 1,
                user_id: test_uuid(2),
                depth: 2,
                parent_slot: 0,
                parent_depth: 0,
            })
        );
    }

    #[test]
    fn validate_restored_rejects_a_parentless_node_below_the_surface() {
        // Adding a child to this one overflows depth + 1.
        let mut arena = live_arena();
        arena.nodes[0].depth = u32::MAX;

        assert_eq!(
            arena.validate_restored(),
            Err(SnapshotConsistencyError::NodeDepthNotZero {
                slot: 0,
                user_id: test_uuid(1),
                depth: u32::MAX,
            })
        );
    }

    #[test]
    fn validate_restored_names_the_same_depth_offender_every_run() {
        let mut arena = arena_with_a_child(1);
        let root = arena.root.expect("live_arena sets a root");
        let second = arena.alloc_slot(make_node(test_uuid(3), Some(root), 9));
        arena.index.insert(test_uuid(3), second);
        arena.nodes[root.0].children.push(second);
        arena.nodes[1].depth = 9;

        for _ in 0..8 {
            assert_eq!(
                arena.validate_restored(),
                Err(SnapshotConsistencyError::NodeDepthMismatch {
                    slot: 1,
                    user_id: test_uuid(2),
                    depth: 9,
                    parent_slot: 0,
                    parent_depth: 0,
                })
            );
        }
    }

    #[test]
    fn validate_restored_ignores_the_depth_on_a_tombstone() {
        let mut arena = arena_with_a_child(1);
        let dead = arena.alloc_slot(make_node(test_uuid(4), None, 42));
        arena.tombstone(dead);

        assert!(arena.validate_restored().is_ok());
    }
}
