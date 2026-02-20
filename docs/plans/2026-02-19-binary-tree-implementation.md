# Binary Tree Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the binary tree structure in Rust, extracting shared foundations (arena, traversals, test helpers, sponsor edges) that both unilevel and binary use, and updating the worker integration boundary for multi-tree support.

**Architecture:** Five phases. Phase 1 extracts shared arena and helpers from unilevel. Phase 2 retrofits unilevel to use the shared foundation and adds sponsor edges. Phase 3 builds the binary tree on the shared foundation. Phase 4 updates the worker and Go integration boundary for named tree instances. Phase 5 extracts the TreeNavigator trait from both implementations.

**Tech Stack:** Rust (serde, proptest, thiserror), Go (encoding/json), NDJSON protocol

**Status:** In Progress
**Progress:** 7 complete, 0 implemented, 5 pending

---

## Phase 1: Extract Shared Foundations

### Task 1: Create shared test helpers module [Pending]

**Files:**
- Create: `engine/network-engine/src/tree/test_helpers.rs`
- Modify: `engine/network-engine/src/tree/mod.rs`

**Step 1: Create the test helpers module**

Create `engine/network-engine/src/tree/test_helpers.rs`:

```rust
use uuid::Uuid;

/// Deterministic UUID for tests. The byte value makes failures readable.
pub fn test_uuid(n: u8) -> Uuid {
    Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
}

/// Deterministic UUID from a u16. Needed for tests with more than
/// 255 nodes (deep chain, wide fan).
pub fn test_uuid_u16(n: u16) -> Uuid {
    let bytes = n.to_le_bytes();
    Uuid::from_bytes([bytes[0], bytes[1], 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
}
```

**Step 2: Register the module in mod.rs**

Add to `engine/network-engine/src/tree/mod.rs`:

```rust
pub mod error;
pub mod node;
pub mod unilevel;

#[cfg(test)]
pub(crate) mod test_helpers;
```

**Step 3: Update unilevel tests to use shared helpers**

In `engine/network-engine/src/tree/unilevel.rs`, replace the local `test_uuid` and `test_uuid_u16` functions in the test module with imports:

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::tree::test_helpers::{test_uuid, test_uuid_u16};

    // Remove the local test_uuid and test_uuid_u16 function definitions.
    // All test functions remain unchanged.
```

**Step 4: Run tests**

Run: `cargo test -p network-engine`
Expected: All 44 existing unilevel tests pass. No behavior change.

**Step 5: Commit**

```
test(tree): extract shared test helpers to test_helpers.rs
```

---

### Task 2: Add sponsor edges to Node struct [Pending]

**Files:**
- Modify: `engine/network-engine/src/tree/node.rs`

**Step 1: Add sponsor fields to Node**

Update `engine/network-engine/src/tree/node.rs`:

```rust
use uuid::Uuid;

/// Index into the arena's node Vec.
///
/// Lightweight handle (one `usize`). Not a pointer.
/// Only meaningful within the tree that created it.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct NodeIndex(pub(crate) usize);

/// A node in the tree arena.
///
/// Stores two sets of relationships as arena indices:
/// - Placement topology: `parent` / `children` — who is above/below in the tree.
/// - Sponsor topology: `sponsor` / `sponsored` — who recruited whom.
///
/// Both edge types use arena indices for cache-friendly traversal.
/// The tree stores sponsor edges as data for traversal but does not
/// use them to make placement decisions (decision 020).
///
/// Public fields (`user_id`, `depth`, `enrolled_at`) are the read-only
/// surface for consumers who receive `&Node` from traversal methods.
/// Structural fields are crate-internal because they hold arena indices
/// that are meaningless outside the tree.
#[derive(Debug, Clone)]
pub struct Node {
    pub user_id: Uuid,
    pub(crate) parent: Option<NodeIndex>,
    pub(crate) children: Vec<NodeIndex>,
    pub(crate) sponsor: Option<NodeIndex>,
    pub(crate) sponsored: Vec<NodeIndex>,
    pub depth: u32,
    /// Unix timestamp in seconds when the user was enrolled.
    pub enrolled_at: i64,
}
```

**Step 2: Update all Node construction sites in unilevel.rs**

Every place that creates a `Node` must add `sponsor: None, sponsored: Vec::new()`. There are three sites:

1. `add_root` (~line 50): Add `sponsor: None, sponsored: Vec::new()`
2. `add_node` (~line 102): Add `sponsor: None, sponsored: Vec::new()`
3. `remove_node` tombstone (~line 179): Add `sponsor: None, sponsored: Vec::new()`

This is temporary — Task 4 will add real sponsor wiring. For now, just keep the code compiling.

**Step 3: Run tests**

Run: `cargo test -p network-engine`
Expected: All tests pass. Sponsor fields default to empty.

**Step 4: Commit**

```
refactor(tree): add sponsor edges to Node struct
```

---

### Task 3: Create shared Arena struct [Pending]

**Files:**
- Create: `engine/network-engine/src/tree/arena.rs`
- Modify: `engine/network-engine/src/tree/mod.rs`

**Step 1: Write tests for Arena**

Create `engine/network-engine/src/tree/arena.rs` with the Arena struct and tests. The Arena extracts storage, alloc, resolve, and tombstone from UnilevelTree.

```rust
use std::collections::{HashMap, VecDeque};
use uuid::Uuid;

use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::types::TreePosition;

/// Shared arena storage for all tree types.
///
/// Owns the contiguous node Vec, UUID-to-index map, free list, and root.
/// Tree type wrappers (UnilevelTree, BinaryTree) delegate storage and
/// traversal operations to Arena while enforcing their own shape constraints.
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
        &self.nodes[idx.0]
    }

    /// Mutable arena access by index.
    pub(crate) fn node_mut(&mut self, idx: NodeIndex) -> &mut Node {
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
    pub(crate) fn get_branch(&self, parent_idx: NodeIndex, position: usize) -> Result<Vec<&Node>, TreeError> {
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
    pub(crate) fn count_branch(&self, parent_idx: NodeIndex, position: usize) -> Result<usize, TreeError> {
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

    /// Counts all descendants of a node (not including the node itself).
    pub(crate) fn count_subtree(&self, start_idx: NodeIndex) -> usize {
        let mut count = 0;
        let mut queue = VecDeque::new();

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
        assert_eq!(arena.node(idx).user_id, Uuid::nil());
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
```

**Step 2: Register in mod.rs**

Update `engine/network-engine/src/tree/mod.rs`:

```rust
pub(crate) mod arena;
pub mod error;
pub mod node;
pub mod unilevel;

#[cfg(test)]
pub(crate) mod test_helpers;
```

**Step 3: Add sponsor_user_id to TreePosition**

Update `engine/network-engine/src/types.rs` to add `sponsor_user_id`:

```rust
pub struct TreePosition {
    pub user_id: Uuid,
    pub parent_user_id: Option<Uuid>,
    pub sponsor_user_id: Option<Uuid>,
    pub position: usize,
    pub depth: u32,
    pub child_count: usize,
    pub downline_counts: HashMap<usize, usize>,
    pub enrolled_at: i64,
}
```

**Step 4: Run tests**

Run: `cargo test -p network-engine`
Expected: All existing tests pass. New arena tests pass.

**Step 5: Commit**

```
refactor(tree): extract shared Arena struct with traversals and sponsor walks
```

---

### Task 4: Retrofit UnilevelTree to use Arena [Pending]

**Files:**
- Modify: `engine/network-engine/src/tree/unilevel.rs`

**Step 1: Rewrite UnilevelTree to wrap Arena**

Replace the internal storage in `UnilevelTree` with an `Arena`. Delegate all traversals. Keep the exact same public API signatures (except `add_node` gains `sponsor_id`).

```rust
use std::collections::HashMap;
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

    pub fn remove_node(&mut self, user_id: Uuid) -> Result<(), TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let child_count = self.arena.node(idx).children.len();

        if child_count > 0 {
            return Err(TreeError::HasChildren(user_id, child_count));
        }

        // Remove from parent's children list
        if let Some(parent_idx) = self.arena.node(idx).parent {
            self.arena.node_mut(parent_idx).children.retain(|&child_idx| child_idx != idx);
        }

        // Remove from sponsor's sponsored list
        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena.node_mut(sponsor_idx).sponsored.retain(|&s| s != idx);
        }

        if self.arena.root == Some(idx) {
            self.arena.root = None;
        }

        self.arena.index.remove(&user_id);
        self.arena.tombstone(idx);
        Ok(())
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
        let children = self.arena.node(idx)
            .children
            .iter()
            .map(|&child_idx| self.arena.node(child_idx))
            .collect();
        Ok(children)
    }

    pub fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_upline(idx, depth))
    }

    pub fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.bfs_downline(idx, depth))
    }

    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_position(idx))
    }

    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        self.arena.get_branch(idx, position)
    }

    pub fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.count_downline(idx, depth))
    }

    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        self.arena.count_branch(idx, position)
    }

    pub fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError> {
        let ancestor_idx = self.arena.resolve(ancestor_id)?;

        if user_id == ancestor_id {
            return Ok(false);
        }

        let user_idx = self.arena.resolve(user_id)?;
        Ok(self.arena.is_descendant_of(user_idx, ancestor_idx))
    }

    // --- Sponsor traversals ---

    pub fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsor(idx))
    }

    pub fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_sponsor_upline(idx, depth))
    }

    pub fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsored(idx))
    }

    /// Provides read access to the arena for commission calculators
    /// and other crate-internal consumers.
    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
    }
}
```

**Step 2: Update all unit tests for the new `add_node` signature**

Every test that calls `add_node` needs the `sponsor_id` parameter. In most unilevel tests, `sponsor_id == parent_id`. Update all test calls from:

```rust
tree.add_node(test_uuid(2), test_uuid(1), 2000).unwrap();
```

To:

```rust
tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
```

The test module remains in `unilevel.rs`. Every existing test gets the signature update. Add a few new tests for sponsor operations:

```rust
    #[test]
    fn sponsor_is_set_on_add_node() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
        let sponsor = tree.get_sponsor(test_uuid(2)).unwrap();
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));
    }

    #[test]
    fn sponsor_different_from_parent() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
        // Place user 3 under user 2, but sponsored by user 1
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(1), 3000).unwrap();
        let parent = tree.get_parent(test_uuid(3)).unwrap().unwrap();
        let sponsor = tree.get_sponsor(test_uuid(3)).unwrap().unwrap();
        assert_eq!(parent.user_id, test_uuid(2));
        assert_eq!(sponsor.user_id, test_uuid(1));
    }

    #[test]
    fn get_sponsored_returns_recruits() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 3000).unwrap();
        let sponsored = tree.get_sponsored(test_uuid(1)).unwrap();
        assert_eq!(sponsored.len(), 2);
    }

    #[test]
    fn get_sponsor_upline_walks_sponsor_chain() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 3000).unwrap();
        let upline = tree.get_sponsor_upline(test_uuid(3), 0).unwrap();
        assert_eq!(upline.len(), 2);
        assert_eq!(upline[0].user_id, test_uuid(2));
        assert_eq!(upline[1].user_id, test_uuid(1));
    }

    #[test]
    fn remove_node_clears_sponsor_link() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2000).unwrap();
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
```

**Step 3: Update property tests**

In `engine/network-engine/tests/unilevel_properties.rs`, update `build_random_tree` to pass `sponsor_id`. Use parent as sponsor for simplicity in property tests:

```rust
tree.add_node(uuid_from_index(i), uuid_from_index(parent_idx), uuid_from_index(parent_idx), i as i64)
```

Add two new property tests for sponsor consistency:

```rust
    /// Property 7: Sponsor-sponsored consistency.
    /// If A sponsors B, then B's sponsor is A and B appears in A's sponsored list.
    #[test]
    fn sponsor_consistency(
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

        for i in 1..node_count {
            let uid = uuid_from_index(i);
            if let Some(sponsor) = tree.get_sponsor(uid).unwrap() {
                let sponsored = tree.get_sponsored(sponsor.user_id).unwrap();
                prop_assert!(
                    sponsored.iter().any(|s| s.user_id == uid),
                    "Node {} not found in sponsor's sponsored list",
                    uid
                );
            }
        }
    }

    /// Property 8: Sponsor upline completeness.
    /// get_sponsor_upline(node, 0) reaches root (a node with no sponsor).
    #[test]
    fn sponsor_upline_completeness(
        node_count in 1usize..100,
        parent_choices in prop::collection::vec(0usize..1000, 0..100),
    ) {
        let tree = build_random_tree(node_count, &parent_choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let upline = tree.get_sponsor_upline(uid, 0).unwrap();

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_sponsor = tree.get_sponsor(last.user_id).unwrap();
                prop_assert!(
                    last_sponsor.is_none(),
                    "Last node in sponsor upline should have no sponsor"
                );
            }
        }
    }
```

**Step 4: Update commission calculator**

The commission calculator in `engine/network-engine/src/commission/unilevel.rs` calls `tree.get_upline()`. Verify it compiles with the refactored tree. The `calculate_unilevel` function takes `&UnilevelTree` — the public API hasn't changed for traversals.

**Step 5: Run tests**

Run: `cargo test -p network-engine`
Expected: All existing tests pass with updated signatures. New sponsor tests pass. Property tests pass.

**Step 6: Add custom Debug impl**

Remove `derive(Debug)` from `UnilevelTree` (it's a wrapper struct, not currently derived). Add:

```rust
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
```

**Step 7: Run tests and commit**

Run: `cargo test -p network-engine`

```
refactor(tree): retrofit UnilevelTree to use shared Arena with sponsor edges
```

---

## Phase 2: Build Binary Tree

### Task 5: Create BinaryTree with add_root and add_node [Pending]

**Files:**
- Create: `engine/network-engine/src/tree/binary.rs`
- Modify: `engine/network-engine/src/tree/mod.rs`
- Modify: `engine/network-engine/src/tree/error.rs`

**Step 1: Add PositionOccupied error variant**

In `engine/network-engine/src/tree/error.rs`, add:

```rust
    #[error("position {position} already occupied for user {user_id}")]
    PositionOccupied { user_id: Uuid, position: usize },
```

**Step 2: Write failing tests for BinaryTree add_root and add_node**

Create `engine/network-engine/src/tree/binary.rs` with the test module first:

```rust
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
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000).unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        assert_eq!(children.len(), 2);
        assert_eq!(children[0].user_id, test_uuid(2));
        assert_eq!(children[1].user_id, test_uuid(3));
    }

    #[test]
    fn add_node_position_occupied_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 1, test_uuid(1), 3000).unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000).unwrap();
        let children = tree.get_children(test_uuid(1)).unwrap();
        // Only one child at position 1
        assert_eq!(children.len(), 1);
        assert_eq!(children[0].user_id, test_uuid(2));
    }

    #[test]
    fn sponsor_set_on_add_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        let sponsor = tree.get_sponsor(test_uuid(2)).unwrap();
        assert_eq!(sponsor.unwrap().user_id, test_uuid(1));
    }
}
```

**Step 3: Implement BinaryTree**

Add the implementation above the test module in `binary.rs`:

```rust
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

        // Check if position is occupied.
        // Children are stored in position order. Position 0 is index 0,
        // position 1 is index 1. If children.len() > position, that slot
        // is taken. If children.len() == 1 and position == 0, slot 0 is taken.
        let parent_children = &self.arena.node(parent_idx).children;
        if position < parent_children.len() {
            return Err(TreeError::PositionOccupied {
                user_id: parent_id,
                position,
            });
        }
        // If position == 1 and children.len() == 0, we need a placeholder
        // at position 0. We don't use placeholders — instead, we require
        // that position <= children.len(). Position 1 with no left child
        // is valid (sparse), so we pad with a sentinel approach.
        // Actually: store children as Vec, use the position as the index.
        // For binary, we pre-fill to length 2 on first child if needed.
        // Simpler approach: children Vec grows to accommodate position.

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

        // Ensure parent's children Vec is large enough, then place at position.
        let parent = self.arena.node_mut(parent_idx);
        while parent.children.len() <= position {
            parent.children.push(NodeIndex(usize::MAX)); // sentinel
        }
        parent.children[position] = idx;

        self.arena.node_mut(sponsor_idx).sponsored.push(idx);
        Ok(idx)
    }
```

**STOP.** The sentinel approach above is problematic. Sentinels (usize::MAX) would break traversals that iterate `children`. Let me reconsider.

Better approach: use the same `Vec<NodeIndex>` but treat it as position-indexed for binary. Position 0 = first element, position 1 = second element. "Occupied" means the Vec has an element at that index. To support right-only (position 1 without position 0), we can't just use Vec indices directly.

**Revised approach:** For binary trees, the children Vec stores actual children only, but each child records which position it occupies. We check occupancy by scanning children for their position. This keeps the Vec clean (no sentinels), and `get_children` returns only occupied positions.

Actually, the simplest correct approach: for binary, always maintain children as a Vec of length 0, 1, or 2. Position 0 is always index 0, position 1 is always index 1. To support "right child only" (position 1 without position 0), we need some way to represent an empty slot. Since `NodeIndex` is just a `usize`, we can use `Option<NodeIndex>` — but the children Vec uses `NodeIndex` directly.

**Final approach:** Binary tree uses a separate representation internally. Add a `left: Option<NodeIndex>` and `right: Option<NodeIndex>` pair stored on the `BinaryTree` struct itself as a per-node map, OR store position metadata alongside the node.

**Simplest correct approach consistent with design:** Keep `children: Vec<NodeIndex>` as-is. For binary, enforce:
- Position 0: insert at index 0
- Position 1: insert at index 1 (or index 0 if left is empty, and record the position separately)

This is getting complicated. The cleanest path: **for binary, children Vec is always ordered by insertion, and we store a `position_in_parent` field on the node itself.** But the Node struct is shared and doesn't have this field.

**Revised cleanest approach:** For binary placement, always insert at the correct position in the children Vec. If the right child is placed first, children = [right_child]. If left is placed second, insert at index 0: children = [left_child, right_child]. This preserves position == index.

Let me revise the `add_node` implementation:

```rust
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

        // Check position occupancy. For binary trees, we maintain
        // children Vec so that index == position. If position 1 is
        // placed first, we insert a sentinel at position 0 (Uuid::nil
        // NodeIndex). We'll need to handle this. OR: we maintain a
        // separate left/right tracking.
        //
        // Let's use a direct approach: BinaryTree maintains a
        // HashMap<NodeIndex, [Option<NodeIndex>; 2]> for binary
        // children separately from the Node's children Vec. But that
        // defeats the purpose of using the shared arena traversals.
        //
        // The traversals iterate node.children. For this to work,
        // children must contain only valid NodeIndex values in
        // position order.

        // Resolution: children Vec contains only occupied positions,
        // in left-then-right order. Track which positions are occupied
        // using a separate structure.

        ...
    }
```

**This task is getting complex. Let me simplify the implementation approach before continuing.**

The design says children use `Vec<NodeIndex>` with runtime checks. The problem is representing "right child without left child" in a position-indexed Vec. Two options:

**Option A:** Binary tree stores `slots: [Option<NodeIndex>; 2]` as a field on `BinaryTree` keyed by parent `NodeIndex`. The `children` Vec on the Node is populated from the slots for traversal. This adds complexity.

**Option B:** Binary tree always maintains the children Vec so that position 0 is always index 0 and position 1 is always index 1. Use a "null node" as a placeholder. The null node is allocated once and represents "empty slot." Traversals skip it.

**Option C (simplest):** Binary tree pushes children to the Vec in insertion order and looks them up by position using the node's position in the Vec. But: if right is placed first, it's at index 0 with position 1. Position != index.

**Revised final decision:** The design said `Vec<NodeIndex>` for consistency, but the "right child without left child" case makes Vec awkward. The binary tree should store children as `[Option<NodeIndex>; 2]` internally on a per-parent map in the BinaryTree struct, and populate the Node's children Vec from it. **Or**, accept that for binary, the children Vec always has length 0 or 2, never 1. If only one child exists, the other slot has a placeholder.

**ACTUALLY:** Re-reading the design discussion — we specifically chose `Vec<NodeIndex>` over `[Option<NodeIndex>; 2]` for consistency across tree types. The compromise: **binary never has a sparse children Vec.** When placing right child only:
1. Place right child at position 1: `children.len() == 0`, so insert at the end. `children = [right]`. But right is at index 0, not index 1.

This doesn't work. The design decision needs revision at implementation time. The simplest correct approach:

**Binary tree uses a `BinarySlots` map:**

```rust
pub struct BinaryTree {
    arena: Arena,
    /// Per-node binary child slots. Left = index 0, right = index 1.
    /// The Node's children Vec is kept in sync for traversals.
    slots: HashMap<NodeIndex, [Option<NodeIndex>; 2]>,
}
```

When placing a child, update slots and rebuild the node's children Vec from occupied slots (left first if present, then right if present). The children Vec always contains valid indices in position order. Position is determined by looking up the slots, not by Vec index.

Actually this is over-engineering. Let me go with the simplest thing: **binary enforces that left must be placed before right.** No, that's a business rule the tree shouldn't enforce.

**Final final approach.** The children Vec stores children in position order. For "right only," we need the child at index 0 to be the right child but with position 1. The tree needs to know which position each child occupies.

Add a `position_in_parent: Option<usize>` field to Node:

No, the Node struct is shared and this adds weight to unilevel nodes that don't need it.

**Resolved approach: padding Vec with insert.**

```rust
// Binary children: position 0 = Vec index 0, position 1 = Vec index 1.
// For right-only: pad children to [dummy, right_child].
// BUT: dummy would be traversed by BFS.
```

This keeps going in circles. The root issue is that `Vec<NodeIndex>` can't represent sparse fixed-position slots without sentinels or placeholders.

**The right answer is what the user initially suggested:** Use `[Option<NodeIndex>; 2]`. But we chose Vec for consistency. The consistency argument breaks down at implementation. Let me note this in the plan and use a pragmatic hybrid:

**Binary tree stores `left` and `right` as `Option<NodeIndex>` on the BinaryTree struct (not on Node), and rebuilds the Node's `children` Vec from them.** The children Vec only contains occupied slots, in left-then-right order. Arena traversals use children Vec and work correctly. Position lookups use the BinaryTree's left/right tracking.

Here is the revised BinaryTree implementation:

```rust
use std::collections::HashMap;
use uuid::Uuid;

use super::arena::Arena;
use super::error::TreeError;
use super::node::{Node, NodeIndex};
use crate::types::TreePosition;

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
        let slots = self.slots.get_mut(&parent_idx).unwrap();
        slots[position] = Some(idx);
        self.rebuild_children(parent_idx);

        self.arena.node_mut(sponsor_idx).sponsored.push(idx);
        Ok(idx)
    }

    pub fn remove_node(&mut self, user_id: Uuid) -> Result<(), TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let child_count = self.arena.node(idx).children.len();

        if child_count > 0 {
            return Err(TreeError::HasChildren(user_id, child_count));
        }

        if let Some(parent_idx) = self.arena.node(idx).parent {
            let slots = self.slots.get_mut(&parent_idx).unwrap();
            for slot in slots.iter_mut() {
                if *slot == Some(idx) {
                    *slot = None;
                }
            }
            self.rebuild_children(parent_idx);
        }

        if let Some(sponsor_idx) = self.arena.node(idx).sponsor {
            self.arena.node_mut(sponsor_idx).sponsored.retain(|&s| s != idx);
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
        let slots = self.slots[&parent_idx];
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
        let children = self.arena.node(idx)
            .children
            .iter()
            .map(|&child_idx| self.arena.node(child_idx))
            .collect();
        Ok(children)
    }

    pub fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_upline(idx, depth))
    }

    pub fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.bfs_downline(idx, depth))
    }

    pub fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let mut pos = self.arena.get_position(idx);

        // For binary, position is determined by slots, not children Vec index.
        // Override position from the slots map.
        if let Some(parent_idx) = self.arena.node(idx).parent {
            let parent_slots = self.slots.get(&parent_idx).unwrap();
            if parent_slots[0] == Some(idx) {
                pos.position = 0;
            } else if parent_slots[1] == Some(idx) {
                pos.position = 1;
            }
        }

        // Override downline_counts to use slot positions, not children Vec indices.
        let node_slots = self.slots.get(&idx).unwrap();
        pos.downline_counts.clear();
        for (slot_pos, slot) in node_slots.iter().enumerate() {
            if let Some(child_idx) = slot {
                pos.downline_counts.insert(slot_pos, self.arena.count_subtree(*child_idx));
            }
        }

        Ok(pos)
    }

    /// Returns the subtree under a binary position (0=left, 1=right).
    pub fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self.slots.get(&idx).unwrap();

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
            None => Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: node_slots.iter().filter(|s| s.is_some()).count(),
            }),
        }
    }

    pub fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.count_downline(idx, depth))
    }

    pub fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        let node_slots = self.slots.get(&idx).unwrap();

        if position > 1 {
            return Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: 2,
            });
        }

        match node_slots[position] {
            Some(child_idx) => {
                // Count the child + all its descendants
                Ok(1 + self.arena.count_subtree(child_idx))
            }
            None => Err(TreeError::PositionOutOfRange {
                user_id,
                position,
                child_count: node_slots.iter().filter(|s| s.is_some()).count(),
            }),
        }
    }

    pub fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError> {
        let ancestor_idx = self.arena.resolve(ancestor_id)?;
        if user_id == ancestor_id {
            return Ok(false);
        }
        let user_idx = self.arena.resolve(user_id)?;
        Ok(self.arena.is_descendant_of(user_idx, ancestor_idx))
    }

    // --- Sponsor traversals ---

    pub fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsor(idx))
    }

    pub fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.walk_sponsor_upline(idx, depth))
    }

    pub fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError> {
        let idx = self.arena.resolve(user_id)?;
        Ok(self.arena.get_sponsored(idx))
    }

    pub(crate) fn arena(&self) -> &Arena {
        &self.arena
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
```

**Step 4: Register in mod.rs**

```rust
pub(crate) mod arena;
pub mod binary;
pub mod error;
pub mod node;
pub mod unilevel;

#[cfg(test)]
pub(crate) mod test_helpers;
```

**Step 5: Run tests**

Run: `cargo test -p network-engine`
Expected: All tests pass.

**Step 6: Commit**

```
feat(tree): implement BinaryTree with position-indexed placement
```

---

### Task 6: Binary tree edge case tests [Pending]

**Files:**
- Modify: `engine/network-engine/src/tree/binary.rs`

**Step 1: Add edge case tests**

Add to the test module in `binary.rs`:

```rust
    #[test]
    fn remove_leaf_node() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        let result = tree.remove_node(test_uuid(2));
        assert!(result.is_ok());
    }

    #[test]
    fn remove_node_with_children_fails() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        let result = tree.remove_node(test_uuid(1));
        assert!(matches!(result, Err(TreeError::HasChildren(_, 1))));
    }

    #[test]
    fn remove_and_readd_same_position() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        let result = tree.add_node(test_uuid(3), test_uuid(1), 0, test_uuid(1), 3000);
        assert!(result.is_ok());
    }

    #[test]
    fn removed_slot_is_reused() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.remove_node(test_uuid(2)).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0, test_uuid(1), 3000).unwrap();
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

    use crate::tree::test_helpers::test_uuid_u16;

    #[test]
    fn deep_chain_1000_nodes_alternating() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid_u16(0), 0).unwrap();
        for i in 1..=1000u16 {
            let position = (i % 2) as usize; // alternate left/right
            tree.add_node(
                test_uuid_u16(i),
                test_uuid_u16(i - 1),
                position,
                test_uuid_u16(0), // root sponsors everyone
                i as i64,
            ).unwrap();
        }
        let downline = tree.get_downline(test_uuid_u16(0), 0).unwrap();
        assert_eq!(downline.len(), 1000);
        let upline = tree.get_upline(test_uuid_u16(1000), 0).unwrap();
        assert_eq!(upline.len(), 1000);
    }

    #[test]
    fn get_position_left_child() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 0);
        assert_eq!(pos.parent_user_id, Some(test_uuid(1)));
    }

    #[test]
    fn get_position_right_child() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 1, test_uuid(1), 2000).unwrap();
        let pos = tree.get_position(test_uuid(2)).unwrap();
        assert_eq!(pos.position, 1);
    }

    #[test]
    fn get_position_downline_counts_by_slot() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 4000).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 1, test_uuid(1), 5000).unwrap();

        let pos = tree.get_position(test_uuid(1)).unwrap();
        // Left leg (position 0): user 2 has 2 descendants (4, 5)
        assert_eq!(pos.downline_counts[&0], 2);
        // Right leg (position 1): user 3 has 0 descendants
        assert_eq!(pos.downline_counts[&1], 0);
    }

    #[test]
    fn get_branch_left() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000).unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(1), 4000).unwrap();
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
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 1, test_uuid(1), 3000).unwrap();
        assert!(tree.is_descendant_of(test_uuid(3), test_uuid(1)).unwrap());
        assert!(!tree.is_descendant_of(test_uuid(1), test_uuid(3)).unwrap());
    }
```

**Step 2: Run tests**

Run: `cargo test -p network-engine`

**Step 3: Commit**

```
test(tree): add binary tree edge case tests
```

---

### Task 7: Binary tree property tests [Pending]

**Files:**
- Create: `engine/network-engine/tests/binary_properties.rs`

**Step 1: Write property tests**

Create `engine/network-engine/tests/binary_properties.rs` with all 10 property tests:

```rust
use network_engine::tree::binary::BinaryTree;
use proptest::prelude::*;
use uuid::Uuid;

fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_be_bytes();
    Uuid::from_bytes(bytes)
}

/// Builds a random binary tree. Each non-root node picks a random parent
/// and a random position (0 or 1). If the chosen position is occupied,
/// tries the other. If both are full, picks a different parent.
fn build_random_binary_tree(node_count: usize, choices: &[(usize, usize)]) -> BinaryTree {
    let mut tree = BinaryTree::new();
    if node_count == 0 {
        return tree;
    }

    tree.add_root(uuid_from_index(0), 0).unwrap();

    for i in 1..node_count {
        let (parent_hint, pos_hint) = if i - 1 < choices.len() {
            (choices[i - 1].0 % i, choices[i - 1].1 % 2)
        } else {
            (0, 0)
        };

        // Try the hinted parent and position first, then search for an open slot.
        let mut placed = false;
        for offset in 0..i {
            let parent_idx = (parent_hint + offset) % i;
            let parent_id = uuid_from_index(parent_idx);
            for pos_offset in 0..2 {
                let position = (pos_hint + pos_offset) % 2;
                if tree
                    .add_node(uuid_from_index(i), parent_id, position, uuid_from_index(0), i as i64)
                    .is_ok()
                {
                    placed = true;
                    break;
                }
            }
            if placed {
                break;
            }
        }
        assert!(placed, "Could not place node {}", i);
    }

    tree
}

proptest! {
    /// Property 1: Parent-child consistency.
    #[test]
    fn parent_child_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();

            for child in &children {
                let parent = tree.get_parent(child.user_id).unwrap();
                prop_assert_eq!(parent.unwrap().user_id, uid);
            }

            if let Some(parent) = tree.get_parent(uid).unwrap() {
                let siblings = tree.get_children(parent.user_id).unwrap();
                prop_assert!(siblings.iter().any(|s| s.user_id == uid));
            }
        }
    }

    /// Property 2: Depth consistency.
    #[test]
    fn depth_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();

            if let Some(parent_uid) = pos.parent_user_id {
                let parent_pos = tree.get_position(parent_uid).unwrap();
                prop_assert_eq!(pos.depth, parent_pos.depth + 1);
            } else {
                prop_assert_eq!(pos.depth, 0);
            }
        }
    }

    /// Property 3: Upline completeness.
    #[test]
    fn upline_completeness(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            let upline = tree.get_upline(uid, 0).unwrap();

            prop_assert_eq!(upline.len(), pos.depth as usize);

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_parent = tree.get_parent(last.user_id).unwrap();
                prop_assert!(last_parent.is_none());
            }
        }
    }

    /// Property 4: Downline containment.
    #[test]
    fn downline_containment(
        node_count in 1usize..30,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..30),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let downline = tree.get_downline(uid, 0).unwrap();

            for desc in &downline {
                prop_assert!(tree.is_descendant_of(desc.user_id, uid).unwrap());
            }
        }
    }

    /// Property 5: Count matches collection.
    #[test]
    fn count_matches_collection(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
        depth in 0u32..10,
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let count = tree.count_downline(uid, depth).unwrap();
            let downline = tree.get_downline(uid, depth).unwrap();
            prop_assert_eq!(count, downline.len());
        }
    }

    /// Property 6: Branch partitioning.
    #[test]
    fn branch_partitioning(
        node_count in 1usize..30,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..30),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let full_downline = tree.get_downline(uid, 0).unwrap();

            let mut branch_union: Vec<Uuid> = Vec::new();
            for pos in 0..2 {
                if let Ok(branch) = tree.get_branch(uid, pos) {
                    for node in &branch {
                        branch_union.push(node.user_id);
                    }
                }
            }

            branch_union.sort();
            let mut downline_ids: Vec<Uuid> = full_downline.iter().map(|n| n.user_id).collect();
            downline_ids.sort();

            prop_assert_eq!(branch_union, downline_ids);
        }
    }

    /// Property 7: Max two children.
    #[test]
    fn max_two_children(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let children = tree.get_children(uid).unwrap();
            prop_assert!(children.len() <= 2, "Node {} has {} children", uid, children.len());
        }
    }

    /// Property 8: Position integrity.
    /// Every node's position is 0 or 1 (except root at 0).
    #[test]
    fn position_integrity(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let pos = tree.get_position(uid).unwrap();
            prop_assert!(pos.position <= 1, "Node {} has position {}", uid, pos.position);
        }
    }

    /// Property 9: Sponsor-sponsored consistency.
    #[test]
    fn sponsor_consistency(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 1..node_count {
            let uid = uuid_from_index(i);
            if let Some(sponsor) = tree.get_sponsor(uid).unwrap() {
                let sponsored = tree.get_sponsored(sponsor.user_id).unwrap();
                prop_assert!(sponsored.iter().any(|s| s.user_id == uid));
            }
        }
    }

    /// Property 10: Sponsor upline completeness.
    #[test]
    fn sponsor_upline_completeness(
        node_count in 1usize..50,
        choices in prop::collection::vec((0usize..1000, 0usize..2), 0..50),
    ) {
        let tree = build_random_binary_tree(node_count, &choices);

        for i in 0..node_count {
            let uid = uuid_from_index(i);
            let upline = tree.get_sponsor_upline(uid, 0).unwrap();

            if !upline.is_empty() {
                let last = upline.last().unwrap();
                let last_sponsor = tree.get_sponsor(last.user_id).unwrap();
                prop_assert!(last_sponsor.is_none());
            }
        }
    }
}
```

**Step 2: Run tests**

Run: `cargo test -p network-engine`
Expected: All 10 property tests pass.

**Step 3: Commit**

```
test(tree): add binary tree property tests
```

---

## Phase 3: Worker Integration

### Task 8: Update WorkerState for named tree instances [Pending]

**Files:**
- Modify: `engine/network-engine-worker/src/state.rs`
- Modify: `engine/network-engine-worker/src/main.rs`
- Modify: `engine/network-engine-worker/src/handlers.rs`

**Step 1: Define TreeInstance enum and update WorkerState**

Update `state.rs`:

```rust
use std::collections::HashMap;
use network_engine::config::CompensationPlan;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::unilevel::UnilevelTree;

pub enum TreeInstance {
    Unilevel(UnilevelTree),
    Binary(BinaryTree),
}

#[derive(Default)]
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,
    pub trees: HashMap<String, TreeInstance>,
}
```

Note: `HashMap` implements `Default` (empty map), so the `#[derive(Default)]` still works. `TreeInstance` doesn't need `Default`.

Wait — `#[derive(Default)]` requires all fields to implement Default. `Option<CompensationPlan>` does, `HashMap<String, TreeInstance>` does. So this works.

**Step 2: Add create_tree handler and update dispatch**

Add `create_tree` to dispatch in `main.rs`:

```rust
"create_tree" => handlers::handle_create_tree(state, request),
```

Add handler in `handlers.rs`:

```rust
pub fn handle_create_tree(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match params.get("structure").and_then(|v| v.as_str()) {
        Some(s) => s.to_string(),
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing structure name",
            );
        }
    };
    let tree_type = match params.get("tree_type").and_then(|v| v.as_str()) {
        Some(t) => t,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing tree_type (unilevel or binary)",
            );
        }
    };

    let instance = match tree_type {
        "unilevel" => TreeInstance::Unilevel(UnilevelTree::new()),
        "binary" => TreeInstance::Binary(BinaryTree::new()),
        _ => {
            return Response::error(
                request.id.clone(),
                "INVALID_PARAMS",
                format!("unknown tree_type: {}", tree_type),
            );
        }
    };

    state.trees.insert(structure, instance);
    Response::success(request.id.clone(), serde_json::json!({"created": true}))
}
```

**Step 3: Update all tree handlers to use named instances**

Each handler needs to:
1. Parse `structure` from params
2. Look up the tree instance in `state.trees`
3. Match on `TreeInstance::Unilevel` or `TreeInstance::Binary`
4. Call the appropriate method

Add a helper to look up trees:

```rust
fn get_tree<'a>(state: &'a WorkerState, params: &serde_json::Value, request_id: &str) -> Result<&'a TreeInstance, Response> {
    let structure = params.get("structure").and_then(|v| v.as_str()).ok_or_else(|| {
        Response::error(request_id.to_string(), "MISSING_PARAM", "missing structure name")
    })?;
    state.trees.get(structure).ok_or_else(|| {
        Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", structure),
        )
    })
}

fn get_tree_mut<'a>(state: &'a mut WorkerState, structure: &str, request_id: &str) -> Result<&'a mut TreeInstance, Response> {
    state.trees.get_mut(structure).ok_or_else(|| {
        Response::error(
            request_id.to_string(),
            "STRUCTURE_NOT_FOUND",
            format!("no tree named '{}'", structure),
        )
    })
}
```

Update each handler. Example for `handle_add_node`:

```rust
pub fn handle_add_node(state: &mut WorkerState, request: &Request) -> Response {
    let params = match parse_params(request) {
        Ok(p) => p,
        Err(resp) => return resp,
    };
    let structure = match params.get("structure").and_then(|v| v.as_str()) {
        Some(s) => s.to_string(),
        None => return Response::error(request.id.clone(), "MISSING_PARAM", "missing structure"),
    };
    let user_id = match parse_uuid(&params, "user_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let parent_id = match parse_uuid(&params, "parent_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let sponsor_id = match parse_uuid(&params, "sponsor_id", &request.id) {
        Ok(id) => id,
        Err(resp) => return resp,
    };
    let enrolled_at = match params.get("enrolled_at").and_then(|v| v.as_i64()) {
        Some(ts) => ts,
        None => {
            return Response::error(
                request.id.clone(),
                "MISSING_PARAM",
                "missing or invalid enrolled_at",
            );
        }
    };

    let tree = match get_tree_mut(state, &structure, &request.id) {
        Ok(t) => t,
        Err(resp) => return resp,
    };

    match tree {
        TreeInstance::Unilevel(t) => {
            match t.add_node(user_id, parent_id, sponsor_id, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => Response::error(request.id.clone(), "TREE_ERROR", e.to_string()),
            }
        }
        TreeInstance::Binary(t) => {
            let position = match params.get("position").and_then(|v| v.as_u64()) {
                Some(p) => p as usize,
                None => {
                    return Response::error(
                        request.id.clone(),
                        "MISSING_PARAM",
                        "missing position (required for binary)",
                    );
                }
            };
            match t.add_node(user_id, parent_id, position, sponsor_id, enrolled_at) {
                Ok(_) => Response::success(request.id.clone(), serde_json::json!({"added": true})),
                Err(e) => tree_error_to_response(&request.id, e),
            }
        }
    }
}
```

Add a helper to map TreeError to appropriate error codes:

```rust
fn tree_error_to_response(request_id: &str, e: TreeError) -> Response {
    let code = match &e {
        TreeError::PositionOccupied { .. } => "POSITION_OCCUPIED",
        TreeError::PositionOutOfRange { .. } => "INVALID_POSITION",
        _ => "TREE_ERROR",
    };
    Response::error(request_id.to_string(), code, e.to_string())
}
```

Update all remaining handlers similarly. Each query handler looks up the tree by name and dispatches to the correct type.

**Step 4: Add sponsor-line handlers to dispatch**

In `main.rs`:

```rust
"get_sponsor" => handlers::handle_get_sponsor(state, request),
"get_sponsor_upline" => handlers::handle_get_sponsor_upline(state, request),
"get_sponsored" => handlers::handle_get_sponsored(state, request),
```

Implement these handlers following the same pattern as `get_parent` / `get_upline` / `get_children`.

**Step 5: Update get_position handler**

The `get_position` response now includes `sponsor_user_id`:

```rust
serde_json::json!({
    "user_id": pos.user_id.to_string(),
    "parent_user_id": parent_user_id,
    "sponsor_user_id": sponsor_user_id,
    "position": pos.position,
    "depth": pos.depth,
    "child_count": pos.child_count,
    "downline_counts": downline_counts,
    "enrolled_at": pos.enrolled_at,
})
```

**Step 6: Run Rust tests**

Run: `cargo test --workspace`
Expected: All pass.

**Step 7: Commit**

```
feat(worker): update worker for named tree instances and sponsor operations
```

---

### Task 9: Update Go EngineClient [Pending]

**Files:**
- Modify: `internal/networkengine/engine_client.go`
- Modify: `internal/networkengine/wire_types.go`

**Step 1: Update wire types**

Add to `wire_types.go`:

```go
// EnginePosition gains SponsorUserID.
type EnginePosition struct {
    UserID         string         `json:"user_id"`
    ParentUserID   *string        `json:"parent_user_id"`
    SponsorUserID  *string        `json:"sponsor_user_id"`
    Position       int            `json:"position"`
    Depth          uint32         `json:"depth"`
    ChildCount     int            `json:"child_count"`
    DownlineCounts map[string]int `json:"downline_counts"`
    EnrolledAt     int64          `json:"enrolled_at"`
}
```

**Step 2: Update EngineClient methods**

All tree methods gain a `structure` parameter. `AddRoot` and `AddNode` gain `sponsorID`. `AddNode` gains optional `position`.

```go
// CreateTree creates a named tree instance.
func (c *EngineClient) CreateTree(ctx context.Context, structure, treeType string) error {
    _, err := c.call(ctx, "create_tree", map[string]string{
        "structure": structure,
        "tree_type": treeType,
    })
    return err
}

// AddRoot creates the root node of a named tree.
func (c *EngineClient) AddRoot(ctx context.Context, structure, userID string, enrolledAt int64) error {
    _, err := c.call(ctx, "add_root", map[string]any{
        "structure":   structure,
        "user_id":     userID,
        "enrolled_at": enrolledAt,
    })
    return err
}

// AddNode adds a child node. For binary trees, position is required.
func (c *EngineClient) AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error {
    params := map[string]any{
        "structure":   structure,
        "user_id":     userID,
        "parent_id":   parentID,
        "sponsor_id":  sponsorID,
        "enrolled_at": enrolledAt,
    }
    for _, opt := range opts {
        opt(params)
    }
    _, err := c.call(ctx, "add_node", params)
    return err
}

// AddNodeOption configures optional parameters for AddNode.
type AddNodeOption func(map[string]any)

// WithPosition sets the child position (required for binary trees).
func WithPosition(position int) AddNodeOption {
    return func(params map[string]any) {
        params["position"] = position
    }
}
```

Update all query methods to accept `structure` as the first parameter.

**Step 3: Add sponsor methods**

```go
func (c *EngineClient) GetSponsor(ctx context.Context, structure, userID string) (*EngineNode, error) { ... }
func (c *EngineClient) GetSponsorUpline(ctx context.Context, structure, userID string, depth uint32) ([]EngineNode, error) { ... }
func (c *EngineClient) GetSponsored(ctx context.Context, structure, userID string) ([]EngineNode, error) { ... }
```

Follow the same pattern as `GetParent` / `GetUpline` / `GetChildren`.

**Step 4: Run Go tests**

Run: `go test ./internal/networkengine/...`
Expected: Tests need updating for new signatures. Update tests to pass `structure` parameter.

**Step 5: Commit**

```
feat(networkengine): update Go EngineClient for named trees and sponsor operations
```

---

### Task 10: Update contract test fixtures [Pending]

**Files:**
- Modify: `engine/testdata/contracts/add_root.json`
- Create: `engine/testdata/contracts/create_tree.json`
- Create: `engine/testdata/contracts/add_node_binary.json`
- Create: `engine/testdata/contracts/position_occupied.json`
- Modify: `engine/testdata/contracts/no_tree_error.json`

**Step 1: Update existing fixtures**

Update `add_root.json` to include `structure`:

```json
{
    "description": "add_root creates the tree root node",
    "request": {
        "id": "c-3",
        "op": "add_root",
        "params": {
            "structure": "primary",
            "user_id": "00000000-0000-0000-0000-000000000001",
            "enrolled_at": 1000
        }
    },
    "expected_response": {
        "id": "c-3",
        "ok": true,
        "result": {"added": true}
    }
}
```

Note: `add_root` now requires a pre-existing tree. Add a `setup` field or use multi-step fixtures. The contract test runner may need updating to support setup steps.

**Simpler approach:** Create a `create_tree.json` fixture that tests create_tree independently. For `add_root.json`, the contract test already creates a fresh worker per fixture. Update the contract test runner to support a `setup` array of requests that run before the test request.

**Step 2: Add new fixtures**

Create `create_tree.json`:
```json
{
    "description": "create_tree creates a named tree instance",
    "request": {
        "id": "ct-1",
        "op": "create_tree",
        "params": {"structure": "test", "tree_type": "binary"}
    },
    "expected_response": {
        "id": "ct-1",
        "ok": true,
        "result": {"created": true}
    }
}
```

Create `add_node_binary.json` with setup:
```json
{
    "description": "add_node places a child at a binary position",
    "setup": [
        {"id": "s-1", "op": "create_tree", "params": {"structure": "test", "tree_type": "binary"}},
        {"id": "s-2", "op": "add_root", "params": {"structure": "test", "user_id": "00000000-0000-0000-0000-000000000001", "enrolled_at": 1000}}
    ],
    "request": {
        "id": "c-bin-1",
        "op": "add_node",
        "params": {
            "structure": "test",
            "user_id": "00000000-0000-0000-0000-000000000002",
            "parent_id": "00000000-0000-0000-0000-000000000001",
            "sponsor_id": "00000000-0000-0000-0000-000000000001",
            "position": 0,
            "enrolled_at": 2000
        }
    },
    "expected_response": {
        "id": "c-bin-1",
        "ok": true,
        "result": {"added": true}
    }
}
```

**Step 3: Update contract test runners**

Update both Go (`contract_test.go`) and Rust (`contract_tests.rs`) contract test runners to process the `setup` array before sending the test request.

**Step 4: Run contract tests**

Run: `cargo test --workspace && go test ./internal/networkengine/...`

**Step 5: Commit**

```
test(contracts): update fixtures for named trees and binary operations
```

---

## Phase 4: TreeNavigator Trait

### Task 11: Extract TreeNavigator trait [Pending]

**Files:**
- Create: `engine/network-engine/src/tree/navigator.rs`
- Modify: `engine/network-engine/src/tree/mod.rs`
- Modify: `engine/network-engine/src/tree/unilevel.rs`
- Modify: `engine/network-engine/src/tree/binary.rs`

**Step 1: Define the trait**

Create `engine/network-engine/src/tree/navigator.rs`:

```rust
use uuid::Uuid;

use super::error::TreeError;
use super::node::Node;
use crate::types::TreePosition;

/// Shared interface for all tree types.
///
/// Covers placement traversals, sponsor traversals, and position queries.
/// Each tree type implements this trait. The worker can use `dyn TreeNavigator`
/// to dispatch operations without matching on tree type for every query.
pub trait TreeNavigator {
    fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError>;
    fn get_children(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError>;
    fn get_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_downline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_position(&self, user_id: Uuid) -> Result<TreePosition, TreeError>;
    fn get_branch(&self, user_id: Uuid, position: usize) -> Result<Vec<&Node>, TreeError>;
    fn count_downline(&self, user_id: Uuid, depth: u32) -> Result<usize, TreeError>;
    fn count_branch(&self, user_id: Uuid, position: usize) -> Result<usize, TreeError>;
    fn is_descendant_of(&self, user_id: Uuid, ancestor_id: Uuid) -> Result<bool, TreeError>;
    fn get_sponsor(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError>;
    fn get_sponsor_upline(&self, user_id: Uuid, depth: u32) -> Result<Vec<&Node>, TreeError>;
    fn get_sponsored(&self, user_id: Uuid) -> Result<Vec<&Node>, TreeError>;
}
```

**Step 2: Implement for both tree types**

In `unilevel.rs`:

```rust
impl crate::tree::navigator::TreeNavigator for UnilevelTree {
    // Each method delegates to the existing impl.
    fn get_parent(&self, user_id: Uuid) -> Result<Option<&Node>, TreeError> { self.get_parent(user_id) }
    // ... all other methods
}
```

Same for `BinaryTree` in `binary.rs`.

**Step 3: Register in mod.rs**

```rust
pub(crate) mod arena;
pub mod binary;
pub mod error;
pub mod navigator;
pub mod node;
pub mod unilevel;

#[cfg(test)]
pub(crate) mod test_helpers;
```

**Step 4: Optionally simplify worker handlers**

The worker can now use `&dyn TreeNavigator` for query handlers instead of matching on each tree type. This reduces duplication. The mutation handlers (`add_node`) still need to match because signatures differ per tree type.

**Step 5: Run tests**

Run: `cargo test --workspace`

**Step 6: Commit**

```
refactor(tree): extract TreeNavigator trait from unilevel and binary
```

---

## Phase 5: Cleanup

### Task 12: Update BUGS_AND_TODOS.md and documentation [Pending]

**Files:**
- Modify: `BUGS_AND_TODOS.md`
- Modify: `docs/development/network-engine.md` (if exists)
- Modify: `decisions/INDEX.md`

**Step 1: Mark deferred items as resolved**

In `BUGS_AND_TODOS.md`, move these from Backlog to Resolved:
- Extract shared test helpers into common `#[cfg(test)]` module
- Extract shared arena logic into `tree/arena.rs`
- Define `TreeNavigator` trait
- Add custom Debug impl to tree types
- Implement Binary tree structure in Rust
- Implement sponsor/placement split in tree Node struct

Add to Resolved section with descriptions of what was done.

**Step 2: Update network-engine.md**

If `docs/development/network-engine.md` has notes about deferred work, update them.

**Step 3: Update downline_counts backlog item**

The `downline_counts` HashMap vs Vec discussion can be revisited now. Note that binary uses HashMap with position keys 0 and 1, which is sparse (right-only has key 1 but not 0). HashMap remains the right choice.

**Step 4: Commit**

```
docs: update backlog and docs for binary tree completion
```
