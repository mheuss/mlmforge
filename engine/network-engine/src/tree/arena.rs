use std::collections::{HashMap, VecDeque};

use serde::{Deserialize, Serialize};
use uuid::Uuid;

use super::error::TreeError;
use super::node::{Node, NodeIndex};
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
}
