use std::collections::{HashMap, VecDeque};
use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::config::matrix::SpilloverDirection;
use crate::types::TreePosition;

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
pub struct MatrixTree {
    arena: Arena,
    width: u8,
    #[allow(dead_code)] // Used by later tasks in this feature branch.
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
            .ok_or(TreeError::SubtreeFull(sponsor_id))?;

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

    // --- Node removal ---

    /// Removes a node from the tree using the given pruning mode.
    ///
    /// PromoteEarliest: promotes the earliest-enrolled child to
    /// fill the removed node's slot, then repositions remaining
    /// children under the promoted node.
    ///
    /// HoldingTank: moves the removed node and its entire subtree
    /// to the holding tank for manual re-placement.
    pub fn remove_node(
        &mut self,
        user_id: Uuid,
        mode: PruningMode,
    ) -> Result<RemovalResult, TreeError> {
        let idx = self.arena.resolve(user_id)?;

        // Cannot remove root.
        if self.arena.root == Some(idx) {
            return Err(TreeError::CannotRemoveRoot);
        }

        match mode {
            PruningMode::PromoteEarliest => self.remove_promote_earliest(idx, user_id),
            PruningMode::HoldingTank => self.remove_to_holding_tank(idx, user_id),
        }
    }

    /// PromoteEarliest removal: if the node is a leaf, simply detach it.
    /// If it has children, promote the earliest-enrolled child into the
    /// removed node's position, then reposition remaining children.
    fn remove_promote_earliest(
        &mut self,
        idx: NodeIndex,
        user_id: Uuid,
    ) -> Result<RemovalResult, TreeError> {
        let parent_idx = self
            .arena
            .node(idx)
            .parent
            .expect("remove_promote_earliest called on root -- should be caught earlier");

        // Find this node's slot position in parent.
        let parent_slot_pos = self.find_slot_position(parent_idx, idx);

        let child_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing for node -- arena and slots map out of sync")
            .clone();
        let occupied_children: Vec<NodeIndex> = child_slots.iter().flatten().copied().collect();

        // Leaf node: simple removal.
        if occupied_children.is_empty() {
            self.detach_and_tombstone(idx, user_id, parent_idx, parent_slot_pos);
            return Ok(RemovalResult {
                removed: user_id,
                promoted: None,
                repositioned: Vec::new(),
                moved_to_tank: Vec::new(),
            });
        }

        // Find earliest-enrolled child.
        let promoted_idx = *occupied_children
            .iter()
            .min_by_key(|&&child_idx| self.arena.node(child_idx).enrolled_at)
            .expect("occupied_children is non-empty");
        let promoted_user_id = self.arena.node(promoted_idx).user_id;

        // Remaining children (not the promoted one).
        let remaining: Vec<NodeIndex> = occupied_children
            .iter()
            .copied()
            .filter(|&c| c != promoted_idx)
            .collect();

        // Step 1: Detach promoted child from removed node's slots.
        let promoted_slot_pos = self.find_slot_position(idx, promoted_idx);
        let removed_slots = self
            .slots
            .get_mut(&idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        removed_slots[promoted_slot_pos] = None;

        // Step 2: Move promoted node into removed node's slot under parent.
        let parent_slots = self
            .slots
            .get_mut(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        parent_slots[parent_slot_pos] = Some(promoted_idx);
        self.rebuild_children(parent_idx);

        // Update promoted node's parent to point to removed node's parent.
        self.arena.node_mut(promoted_idx).parent = Some(parent_idx);

        // Step 3: Move remaining children under promoted node.
        // First, transfer any existing children from the removed node's slots
        // to the promoted node's slots.
        let mut repositioned_ids = Vec::new();

        // Get the promoted node's current slots (its own children from before).
        // We need to merge remaining siblings into the promoted node's subtree.
        for &remaining_child in &remaining {
            // Try to place in promoted node's direct slots first.
            let promoted_slots = self
                .slots
                .get(&promoted_idx)
                .expect("slots entry missing for node -- arena and slots map out of sync")
                .clone();

            let direct_slot = promoted_slots.iter().position(|s| s.is_none());
            if let Some(slot_pos) = direct_slot {
                let promoted_slots_mut = self
                    .slots
                    .get_mut(&promoted_idx)
                    .expect("slots entry missing for node -- arena and slots map out of sync");
                promoted_slots_mut[slot_pos] = Some(remaining_child);
                self.arena.node_mut(remaining_child).parent = Some(promoted_idx);
            } else {
                // Promoted node's slots are full. Use BFS spillover within
                // promoted node's subtree.
                let (target_parent, target_pos) = self
                    .find_spillover_slot(promoted_idx)
                    .expect("promoted subtree should have open slots for remaining siblings");
                let target_slots = self
                    .slots
                    .get_mut(&target_parent)
                    .expect("slots entry missing for node -- arena and slots map out of sync");
                target_slots[target_pos] = Some(remaining_child);
                self.rebuild_children(target_parent);
                self.arena.node_mut(remaining_child).parent = Some(target_parent);
            }

            repositioned_ids.push(self.arena.node(remaining_child).user_id);
        }

        // Rebuild promoted node's children after all placements.
        self.rebuild_children(promoted_idx);

        // Step 4: Clean up removed node.
        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena
                .node_mut(sponsor_idx)
                .sponsored
                .retain(|&s| s != idx);
        }
        self.slots.remove(&idx);
        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);

        // Step 5: Recalculate depths for promoted node and all its descendants.
        self.recalculate_depths(promoted_idx);

        Ok(RemovalResult {
            removed: user_id,
            promoted: Some(promoted_user_id),
            repositioned: repositioned_ids,
            moved_to_tank: Vec::new(),
        })
    }

    /// HoldingTank removal: moves the node and its entire subtree
    /// to the holding tank for manual re-placement.
    fn remove_to_holding_tank(
        &mut self,
        idx: NodeIndex,
        user_id: Uuid,
    ) -> Result<RemovalResult, TreeError> {
        let parent_idx = self
            .arena
            .node(idx)
            .parent
            .expect("remove_to_holding_tank called on root -- should be caught earlier");

        let parent_slot_pos = self.find_slot_position(parent_idx, idx);

        // BFS collect all descendants of the removed node.
        let mut descendants = Vec::new();
        let mut queue = VecDeque::new();
        for &child_idx in &self.arena.node(idx).children {
            queue.push_back(child_idx);
        }
        while let Some(current) = queue.pop_front() {
            descendants.push(current);
            for &child_idx in &self.arena.node(current).children {
                queue.push_back(child_idx);
            }
        }

        // Collect IDs that are being removed (the node + all descendants).
        let mut removed_set: Vec<NodeIndex> = vec![idx];
        removed_set.extend(&descendants);

        let mut moved_to_tank = Vec::new();

        // Process descendants first (leaves before parents doesn't matter
        // since we're doing bulk removal, but we process in BFS order).
        for &desc_idx in &descendants {
            let desc = self.arena.node(desc_idx);
            let desc_user_id = desc.user_id;
            let desc_sponsor = desc.sponsor;
            let desc_enrolled_at = desc.enrolled_at;

            // Add to holding tank.
            self.holding_tank.push(HoldingTankEntry {
                user_id: desc_user_id,
                sponsor: desc_sponsor,
                enrolled_at: desc_enrolled_at,
            });
            moved_to_tank.push(desc_user_id);

            // Remove from sponsor's sponsored list if the sponsor is
            // not also being removed.
            if let Some(sponsor_idx) = desc_sponsor {
                if !removed_set.contains(&sponsor_idx) {
                    self.arena
                        .node_mut(sponsor_idx)
                        .sponsored
                        .retain(|&s| s != desc_idx);
                }
            }

            // Clean up.
            self.slots.remove(&desc_idx);
            self.arena.index.remove(&desc_user_id);
            self.arena.tombstone(desc_idx);
        }

        // Now handle the removed node itself.
        let node = self.arena.node(idx);
        let node_sponsor = node.sponsor;
        let node_enrolled_at = node.enrolled_at;

        self.holding_tank.push(HoldingTankEntry {
            user_id,
            sponsor: node_sponsor,
            enrolled_at: node_enrolled_at,
        });
        moved_to_tank.push(user_id);

        // Remove from sponsor's sponsored list.
        if let Some(sponsor_idx) = node_sponsor {
            if !removed_set.contains(&sponsor_idx) {
                self.arena
                    .node_mut(sponsor_idx)
                    .sponsored
                    .retain(|&s| s != idx);
            }
        }

        // Detach from parent.
        let parent_slots = self
            .slots
            .get_mut(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        parent_slots[parent_slot_pos] = None;
        self.rebuild_children(parent_idx);

        // Tombstone.
        self.slots.remove(&idx);
        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);

        Ok(RemovalResult {
            removed: user_id,
            promoted: None,
            repositioned: Vec::new(),
            moved_to_tank,
        })
    }

    /// Returns entries from the holding tank.
    ///
    /// When `sponsor_id` is Some, returns only entries whose sponsor
    /// matches (by resolving the sponsor_id to a NodeIndex). When None,
    /// returns all entries.
    pub fn get_holding_tank(&self, sponsor_id: Option<Uuid>) -> Vec<&HoldingTankEntry> {
        match sponsor_id {
            Some(id) => {
                let sponsor_idx = self.arena.resolve(id).ok();
                self.holding_tank
                    .iter()
                    .filter(|entry| entry.sponsor == sponsor_idx)
                    .collect()
            }
            None => self.holding_tank.iter().collect(),
        }
    }

    /// Places a user from the holding tank into the tree at a specific
    /// parent and position.
    ///
    /// Validates position, parent existence, slot availability, and that
    /// the user is in the holding tank. Restores the sponsor link if the
    /// original sponsor still exists in the tree.
    pub fn place_from_tank(
        &mut self,
        user_id: Uuid,
        parent_id: Uuid,
        position: u8,
    ) -> Result<(), TreeError> {
        if position >= self.width {
            return Err(TreeError::PositionOutOfRange {
                user_id: parent_id,
                position: position as usize,
                child_count: self.width as usize,
            });
        }

        let parent_idx = self.arena.resolve(parent_id)?;

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

        // Find user in holding tank.
        let tank_pos = self
            .holding_tank
            .iter()
            .position(|entry| entry.user_id == user_id)
            .ok_or(TreeError::UserNotInHoldingTank(user_id))?;
        let entry = self.holding_tank.remove(tank_pos);

        // Resolve sponsor: restore if the sponsor still exists (not tombstoned).
        let sponsor_idx = entry.sponsor.and_then(|idx| {
            let node = &self.arena.nodes[idx.0];
            if node.user_id != uuid::Uuid::nil() && self.arena.index.contains_key(&node.user_id) {
                Some(idx)
            } else {
                None
            }
        });

        let parent_depth = self.arena.node(parent_idx).depth;

        let node = Node {
            user_id,
            parent: Some(parent_idx),
            children: Vec::new(),
            sponsor: sponsor_idx,
            sponsored: Vec::new(),
            depth: parent_depth + 1,
            enrolled_at: entry.enrolled_at,
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

        // Re-add to sponsor's sponsored list.
        if let Some(sponsor_idx) = sponsor_idx {
            self.arena.node_mut(sponsor_idx).sponsored.push(idx);
        }

        Ok(())
    }

    /// Detaches a leaf node from the tree and tombstones it.
    fn detach_and_tombstone(
        &mut self,
        idx: NodeIndex,
        user_id: Uuid,
        parent_idx: NodeIndex,
        parent_slot_pos: usize,
    ) {
        // Clear parent slot.
        let parent_slots = self
            .slots
            .get_mut(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        parent_slots[parent_slot_pos] = None;
        self.rebuild_children(parent_idx);

        // Remove from sponsor's sponsored list.
        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena
                .node_mut(sponsor_idx)
                .sponsored
                .retain(|&s| s != idx);
        }

        // Tombstone.
        self.slots.remove(&idx);
        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);
    }

    /// Recalculates depth for a node and all its descendants via BFS.
    fn recalculate_depths(&mut self, start_idx: NodeIndex) {
        let parent_depth = self
            .arena
            .node(start_idx)
            .parent
            .map(|p| self.arena.node(p).depth)
            .unwrap_or(0);
        self.arena.node_mut(start_idx).depth = if self.arena.node(start_idx).parent.is_some() {
            parent_depth + 1
        } else {
            0
        };

        let mut queue = VecDeque::new();
        queue.push_back(start_idx);

        while let Some(current) = queue.pop_front() {
            let current_depth = self.arena.node(current).depth;
            let children = self.arena.node(current).children.clone();
            for child_idx in children {
                self.arena.node_mut(child_idx).depth = current_depth + 1;
                queue.push_back(child_idx);
            }
        }
    }

    /// Finds the slot position of a child within a parent's slots.
    fn find_slot_position(&self, parent_idx: NodeIndex, child_idx: NodeIndex) -> usize {
        let parent_slots = self
            .slots
            .get(&parent_idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");
        parent_slots
            .iter()
            .position(|s| *s == Some(child_idx))
            .expect("child not found in parent's slots -- tree is corrupt")
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
    /// For matrix trees, position is determined by the parent's slots
    /// map rather than children Vec index. Downline counts are keyed
    /// by slot position (0..width-1).
    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let mut pos = self.arena.get_position(idx);

        // For matrix, position is determined by slots, not children Vec index.
        if let Some(parent_idx) = self.arena.node(idx).parent {
            let parent_slots = self
                .slots
                .get(&parent_idx)
                .expect("slots entry missing for node -- arena and slots map out of sync");
            for (slot_pos, slot) in parent_slots.iter().enumerate() {
                if *slot == Some(idx) {
                    pos.position = slot_pos;
                    break;
                }
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

    /// Returns the subtree under a matrix slot position.
    ///
    /// Results include the child at the given position and all of
    /// its descendants, in BFS order.
    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");

        if position >= self.width as usize {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: self.width as usize,
            });
        }

        match node_slots[position] {
            Some(child_idx) => {
                let mut result = Vec::new();
                let mut queue = VecDeque::new();
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

    /// Counts nodes in the subtree under a matrix slot position.
    ///
    /// The count includes the child at the given position and all of
    /// its descendants.
    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self
            .slots
            .get(&idx)
            .expect("slots entry missing for node -- arena and slots map out of sync");

        if position >= self.width as usize {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: self.width as usize,
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

    /// Returns true if the tree contains a node with this user_id.
    pub fn contains(&self, user_id: Uuid) -> bool {
        self.arena.index.contains_key(&user_id)
    }

    /// Provides read access to the arena for commission calculators
    /// and other crate-internal consumers.
    #[allow(dead_code)] // Used by later tasks in this feature branch.
    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
    }
}

impl crate::tree::navigator::TreeNavigator for MatrixTree {
    fn contains(&self, user_id: Uuid) -> bool {
        self.contains(user_id)
    }
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

    // --- TreeNavigator / query tests ---

    #[test]
    fn get_position_root() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let pos = tree.get_position(test_uuid(1)).unwrap();
        assert_eq!(pos.position, 0);
        assert!(pos.parent_user_id.is_none());
        assert_eq!(pos.depth, 0);
        assert_eq!(pos.child_count, 0);
    }

    #[test]
    fn get_position_reports_correct_slot() {
        // Place node at slot 2 of a 3-wide tree.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 2, 2000)
            .unwrap();
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 2);
    }

    #[test]
    fn get_position_downline_counts_by_slot() {
        // 3-wide tree: root has 3 children, grandchild under slot 0.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 1, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(1), test_uuid(1), 2, 4000)
            .unwrap();
        // Grandchild under node2 (slot 0 of root).
        tree.add_node_at(test_uuid(5), test_uuid(2), test_uuid(2), 0, 5000)
            .unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        // Slot 0 has node2 which has 1 descendant (node5).
        assert_eq!(pos.downline_counts[&0], 1);
        // Slots 1 and 2 have nodes with no descendants.
        assert_eq!(pos.downline_counts[&1], 0);
        assert_eq!(pos.downline_counts[&2], 0);
    }

    #[test]
    fn get_position_open_slots_on_leaf() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();

        // Node 2 is a leaf. All 3 slot downline counts should be 0.
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.downline_counts.len(), 3);
        assert_eq!(pos.downline_counts[&0], 0);
        assert_eq!(pos.downline_counts[&1], 0);
        assert_eq!(pos.downline_counts[&2], 0);
    }

    #[test]
    fn get_branch_returns_subtree() {
        // 3-wide tree: root -> [node2, node3, node4], node2 -> [node5]
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 1, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(1), test_uuid(1), 2, 4000)
            .unwrap();
        tree.add_node_at(test_uuid(5), test_uuid(2), test_uuid(2), 0, 5000)
            .unwrap();

        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        let ids: Vec<Uuid> = branch.iter().map(|n| n.user_id).collect();
        assert!(ids.contains(&test_uuid(2)));
        assert!(ids.contains(&test_uuid(5)));
        assert!(!ids.contains(&test_uuid(3)));
        assert!(!ids.contains(&test_uuid(4)));
    }

    #[test]
    fn get_branch_empty_slot_returns_empty() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        // Slot 1 is empty.
        let branch = tree.get_branch(test_uuid(1), 1).unwrap();
        assert!(branch.is_empty());
    }

    #[test]
    fn get_branch_invalid_position_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.get_branch(test_uuid(1), 3);
        assert!(matches!(
            result,
            Err(TreeError::PositionOutOfRange { position: 3, .. })
        ));
    }

    #[test]
    fn count_branch_matches_get_branch_len() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(2), test_uuid(2), 1, 3000)
            .unwrap();

        let branch = tree.get_branch(test_uuid(1), 0).unwrap();
        let count = tree.count_branch(test_uuid(1), 0).unwrap();
        assert_eq!(count, branch.len());
        assert_eq!(count, 2);
    }

    #[test]
    fn contains_works() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        assert!(tree.contains(test_uuid(1)));
        assert!(!tree.contains(test_uuid(99)));
    }

    #[test]
    fn get_upline_and_downline() {
        // Chain: root -> node2 -> node3
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();

        let upline = tree.get_upline(test_uuid(3), 0).unwrap();
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(2));
        assert_eq!(upline[1].user_id, test_uuid(1));

        let downline = tree.get_downline(test_uuid(1), 0).unwrap();
        assert_eq!(downline.len(), 2);
        assert_eq!(downline[0].user_id, test_uuid(2));
        assert_eq!(downline[1].user_id, test_uuid(3));
    }

    #[test]
    fn is_descendant_of_works() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();

        assert!(tree.is_descendant_of(test_uuid(3), test_uuid(1)).unwrap());
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(3)).unwrap());
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(1)).unwrap());
    }

    #[test]
    fn sponsor_traversals() {
        // root sponsors node2, node2 sponsors node3 (placed under root via spillover).
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();

        // get_sponsor
        let sponsor = tree.get_sponsor(test_uuid(3)).unwrap().unwrap();
        assert_eq!(sponsor.user_id, test_uuid(2));

        // get_sponsored
        let sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert_eq!(sponsored.len(), 1);
        assert_eq!(sponsored[0].user_id, test_uuid(2));

        // get_sponsor_upline
        let upline = tree.get_sponsor_upline(test_uuid(3), 0).unwrap();
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(2));
        assert_eq!(upline[1].user_id, test_uuid(1));
    }

    // --- Pruning (PromoteEarliest) tests ---

    #[test]
    fn remove_leaf_node_promote_earliest() {
        // Remove a leaf node. No promotion needed.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        let result = tree
            .remove_node(test_uuid(2), PruningMode::PromoteEarliest)
            .unwrap();
        assert_eq!(result.removed, test_uuid(2));
        assert!(result.promoted.is_none());
        assert!(result.repositioned.is_empty());
        assert!(!tree.contains(test_uuid(2)));
    }

    #[test]
    fn remove_node_promotes_earliest_child() {
        // root -> [node2, node3]
        // node2 -> [node4, node5]
        // Remove node2. node4 enrolled first, so node4 is promoted.
        // node5 is repositioned under node4.
        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(1), test_uuid(1), 1, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(2), test_uuid(2), 0, 4000)
            .unwrap();
        tree.add_node_at(test_uuid(5), test_uuid(2), test_uuid(2), 1, 5000)
            .unwrap();

        let result = tree
            .remove_node(test_uuid(2), PruningMode::PromoteEarliest)
            .unwrap();

        assert_eq!(result.removed, test_uuid(2));
        assert_eq!(result.promoted, Some(test_uuid(4)));
        assert!(result.repositioned.contains(&test_uuid(5)));
        assert!(!tree.contains(test_uuid(2)));

        // node4 should now be under root at slot 0.
        let pos4 = tree.get_position(test_uuid(4)).unwrap();
        assert_eq!(pos4.parent_user_id, Some(test_uuid(1)));
        assert_eq!(pos4.position, 0);

        // node5 should be under node4.
        let parent5 = tree.get_parent(test_uuid(5)).unwrap().unwrap();
        assert_eq!(parent5.user_id, test_uuid(4));
    }

    #[test]
    fn remove_node_promote_recalculates_depth() {
        // Chain: root -> node2 -> node3 -> node4
        // Remove node2. node3 promoted to root's slot.
        // node4 under node3. Depths must be recalculated.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(2), test_uuid(2), 0, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(3), test_uuid(3), 0, 4000)
            .unwrap();

        // Before: node2=depth1, node3=depth2, node4=depth3
        assert_eq!(tree.get_node(test_uuid(4)).unwrap().depth, 3);

        tree.remove_node(test_uuid(2), PruningMode::PromoteEarliest)
            .unwrap();

        // After: node3=depth1 (promoted to root's slot), node4=depth2
        assert_eq!(tree.get_node(test_uuid(3)).unwrap().depth, 1);
        assert_eq!(tree.get_node(test_uuid(4)).unwrap().depth, 2);
    }

    #[test]
    fn remove_root_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.remove_node(test_uuid(1), PruningMode::PromoteEarliest);
        assert!(matches!(result, Err(TreeError::CannotRemoveRoot)));
    }

    #[test]
    fn remove_nonexistent_user_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        let result = tree.remove_node(test_uuid(99), PruningMode::PromoteEarliest);
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }

    #[test]
    fn remove_node_promote_preserves_sponsor_links() {
        // root sponsors node2, node2 sponsors node3 and node4.
        // Remove node2. Sponsor links for node3/node4 should stay.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(2), test_uuid(2), 0, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(2), test_uuid(2), 1, 4000)
            .unwrap();

        tree.remove_node(test_uuid(2), PruningMode::PromoteEarliest)
            .unwrap();

        // node3 and node4's sponsors should still be node2's idx, but
        // since node2 is removed, what matters is the sponsor is preserved
        // in the node data (it's an idx that now points to a tombstone).
        // The important check: root's sponsored list no longer contains node2.
        let root_sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert!(
            !root_sponsored.iter().any(|n| n.user_id == test_uuid(2)),
            "removed node should be cleared from sponsor's sponsored list"
        );
    }

    // --- Pruning (HoldingTank) tests ---

    #[test]
    fn remove_leaf_to_holding_tank() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        let result = tree
            .remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();
        assert_eq!(result.removed, test_uuid(2));
        assert!(result.promoted.is_none());
        assert!(result.repositioned.is_empty());
        assert!(result.moved_to_tank.contains(&test_uuid(2)));
        assert!(!tree.contains(test_uuid(2)));

        let tank = tree.get_holding_tank(None);
        assert_eq!(tank.len(), 1);
        assert_eq!(tank[0].user_id, test_uuid(2));
    }

    #[test]
    fn remove_node_moves_subtree_to_holding_tank() {
        // root -> [node2]
        // node2 -> [node3, node4]
        // node3 -> [node5]
        // Remove node2: nodes 2, 3, 4, 5 all go to tank.
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 0, 2000)
            .unwrap();
        tree.add_node_at(test_uuid(3), test_uuid(2), test_uuid(2), 0, 3000)
            .unwrap();
        tree.add_node_at(test_uuid(4), test_uuid(2), test_uuid(2), 1, 4000)
            .unwrap();
        tree.add_node_at(test_uuid(5), test_uuid(3), test_uuid(3), 0, 5000)
            .unwrap();

        let result = tree
            .remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();

        // 3 descendants + the node itself = 4 moved to tank.
        assert_eq!(result.moved_to_tank.len(), 4);
        assert!(!tree.contains(test_uuid(2)));
        assert!(!tree.contains(test_uuid(3)));
        assert!(!tree.contains(test_uuid(4)));
        assert!(!tree.contains(test_uuid(5)));

        let tank = tree.get_holding_tank(None);
        assert_eq!(tank.len(), 4);
    }

    #[test]
    fn holding_tank_preserves_sponsor() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3000).unwrap();

        // node3's sponsor is node2.
        let sponsor_idx_before = tree.arena.resolve(test_uuid(2)).unwrap();

        tree.remove_node(test_uuid(3), PruningMode::HoldingTank)
            .unwrap();

        let tank = tree.get_holding_tank(None);
        assert_eq!(tank.len(), 1);
        assert_eq!(tank[0].sponsor, Some(sponsor_idx_before));
    }

    #[test]
    fn holding_tank_preserves_enrolled_at() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();

        let tank = tree.get_holding_tank(None);
        assert_eq!(tank[0].enrolled_at, 2000);
    }

    #[test]
    fn remove_to_tank_clears_parent_slot() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node_at(test_uuid(2), test_uuid(1), test_uuid(1), 1, 2000)
            .unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();

        let root_idx = tree.arena.resolve(test_uuid(1)).unwrap();
        let slots = tree.slots.get(&root_idx).unwrap();
        assert!(slots[1].is_none(), "slot 1 should be cleared after removal");
    }

    // --- place_from_tank tests ---

    #[test]
    fn place_from_tank_succeeds() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();
        tree.place_from_tank(test_uuid(2), test_uuid(1), 0).unwrap();

        assert!(tree.contains(test_uuid(2)));
        let parent = tree.get_parent(test_uuid(2)).unwrap().unwrap();
        assert_eq!(parent.user_id, test_uuid(1));
    }

    #[test]
    fn place_from_tank_removes_from_tank() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();
        assert_eq!(tree.get_holding_tank(None).len(), 1);

        tree.place_from_tank(test_uuid(2), test_uuid(1), 0).unwrap();
        assert!(tree.get_holding_tank(None).is_empty());
    }

    #[test]
    fn place_from_tank_restores_sponsor() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();
        tree.place_from_tank(test_uuid(2), test_uuid(1), 0).unwrap();

        let sponsor = tree.get_sponsor(test_uuid(2)).unwrap().unwrap();
        assert_eq!(sponsor.user_id, test_uuid(1));

        // Sponsor's sponsored list should contain the re-placed node.
        let sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert!(sponsored.iter().any(|n| n.user_id == test_uuid(2)));
    }

    #[test]
    fn place_from_tank_preserves_enrolled_at() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();
        tree.place_from_tank(test_uuid(2), test_uuid(1), 0).unwrap();

        let node = tree.get_node(test_uuid(2)).unwrap();
        assert_eq!(node.enrolled_at, 2000);
    }

    #[test]
    fn place_from_tank_user_not_in_tank_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();

        let result = tree.place_from_tank(test_uuid(99), test_uuid(1), 0);
        assert!(matches!(result, Err(TreeError::UserNotInHoldingTank(_))));
    }

    #[test]
    fn place_from_tank_position_occupied_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 3000).unwrap();

        // Remove node3 to holding tank.
        tree.remove_node(test_uuid(3), PruningMode::HoldingTank)
            .unwrap();

        // Try to place at slot 0 where node2 is.
        let result = tree.place_from_tank(test_uuid(3), test_uuid(1), 0);
        assert!(matches!(result, Err(TreeError::PositionOccupied { .. })));
    }

    #[test]
    fn place_from_tank_invalid_position_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();

        let result = tree.place_from_tank(test_uuid(2), test_uuid(1), 3);
        assert!(matches!(
            result,
            Err(TreeError::PositionOutOfRange { position: 3, .. })
        ));
    }

    #[test]
    fn place_from_tank_parent_not_found_fails() {
        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.set_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();

        tree.remove_node(test_uuid(2), PruningMode::HoldingTank)
            .unwrap();

        let result = tree.place_from_tank(test_uuid(2), test_uuid(99), 0);
        assert!(matches!(result, Err(TreeError::UserNotFound(_))));
    }
}
