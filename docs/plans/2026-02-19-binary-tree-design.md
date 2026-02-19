# Binary Tree Design

## Goal

Implement a binary tree structure in Rust for the Network Engine. Binary is the second tree type after unilevel. This work also extracts shared foundations (arena, traversals, test helpers) that both tree types use, adds sponsor edges to the shared Node struct, and updates the worker integration boundary for multi-tree support.

## Context

The unilevel tree is implemented and working (decision 007). It uses arena-based storage with iterative BFS traversal. The binary tree follows the same patterns but enforces a two-child maximum per node and requires explicit position (0=left, 1=right) on placement.

Decision 020 (Tree Topology Separation) establishes that trees are pure topology. Placement logic (spillover, preferences, holding tanks) belongs to the caller. The tree validates positions but never picks alternatives.

### Key Decisions Made During Brainstorming

1. **Trees are pure topology, callers decide placement** (decision 020, committed).
2. **Explicit position parameter** for binary `add_node`. No "first empty slot" convenience.
3. **Fixed-size array model** — `children: [Option<NodeIndex>; 2]` was considered for compile-time enforcement but rejected in favor of `Vec<NodeIndex>` with runtime checks, for consistency across all tree types (matrix will use the same pattern).
4. **Bundle extraction tasks** with binary tree work. Shared foundations first, then binary on top.
5. **Volume stays in the commission calculator**, not the tree. Consistent with unilevel.
6. **Sponsor edges on the shared Node struct**. All tree types track sponsor relationships for sponsor-line traversals. Decision 020 updated: storing sponsor edges is data, not logic.
7. **Named tree instances** in the worker. `HashMap<String, TreeInstance>` supports plans with multiple structures of the same type.
8. **Approach C (composition)** — shared Arena struct with concrete Node type, no generics. Width enforcement at the tree level.
9. **TreeNavigator trait extracted last**, after both implementations exist. Generalize from real code.

## Architecture

### Shared Node Struct

The Node struct gains sponsor edges. All tree types use the same Node.

```rust
pub struct Node {
    pub user_id: Uuid,
    pub(crate) parent: Option<NodeIndex>,
    pub(crate) children: Vec<NodeIndex>,
    pub(crate) sponsor: Option<NodeIndex>,
    pub(crate) sponsored: Vec<NodeIndex>,
    pub depth: u32,
    pub enrolled_at: i64,
}
```

- `parent` / `children` — placement topology (existing)
- `sponsor` / `sponsored` — sponsor topology (new)
- For `add_root`, both `parent` and `sponsor` are `None`
- For `add_node`, both `parent_id` and `sponsor_id` are required parameters

The `TreePosition` output type gains `sponsor_user_id: Option<Uuid>`.

### Shared Arena

Extracted from `UnilevelTree` into `tree/arena.rs`. Pure storage and traversal, no shape constraints.

```rust
pub(crate) struct Arena {
    nodes: Vec<Node>,
    index: HashMap<Uuid, NodeIndex>,
    free_list: Vec<NodeIndex>,
    root: Option<NodeIndex>,
}
```

**Data access:**
- `resolve(user_id) -> Result<NodeIndex, TreeError>` — UUID to index lookup
- `node(&self, idx) -> &Node` — direct arena access
- `node_mut(&mut self, idx) -> &mut Node` — mutable arena access
- `root() -> Option<NodeIndex>` — root accessor

**Lifecycle:**
- `alloc_slot(node) -> NodeIndex` — allocate (reuses free list)
- `tombstone(idx)` — clear slot and add to free list
- `insert_index(user_id, idx)` / `remove_index(user_id)` — manage UUID map

**Shared traversals:**
- `bfs_walk(start_idx, depth) -> Vec<&Node>` — BFS over placement children
- `walk_upline(start_idx, depth) -> Vec<&Node>` — follow parent links upward
- `is_descendant_of(user_idx, ancestor_idx) -> bool` — walk parent links
- `count_subtree(start_idx) -> usize` — count descendants without allocation
- `get_sponsor(idx) -> Option<&Node>` — sponsor node
- `walk_sponsor_upline(start_idx, depth) -> Vec<&Node>` — follow sponsor links upward
- `get_sponsored(idx) -> Vec<&Node>` — direct recruits

Each tree type wraps `Arena` and delegates traversals. Tree-specific logic (width enforcement, position validation) lives in the wrapper.

### Binary Tree Struct

```rust
pub struct BinaryTree {
    arena: Arena,
}
```

Same public API shape as unilevel, with these differences:

- `add_node` requires explicit `position: usize` (0=left, 1=right) and `sponsor_id`
- Validates position is 0 or 1
- Validates position is not already occupied
- New error variant: `PositionOccupied { user_id: Uuid, position: usize }`

All traversals delegate to the shared Arena.

### Unilevel Retrofit

Unilevel gets updated to use the shared Arena:
- Wraps `Arena` instead of owning storage directly
- Delegates all traversals to Arena methods
- `add_node` signature gains `sponsor_id` parameter
- Custom `Debug` impl replaces `derive(Debug)` — prints node count and root user ID

### Worker Integration

Worker state changes from a single optional tree to named instances:

```rust
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,
    pub trees: HashMap<String, TreeInstance>,
}

pub enum TreeInstance {
    Unilevel(UnilevelTree),
    Binary(BinaryTree),
}
```

**New operation:** `create_tree` — creates a named tree instance of a given type.

**Changed operations:** All tree operations gain a `structure` parameter naming the target tree instance. `add_root` and `add_node` gain `sponsor_id`. Binary `add_node` gains `position`.

**New operations for sponsor traversal:** `get_sponsor`, `get_sponsor_upline`, `get_sponsored`.

**New error codes:** `POSITION_OCCUPIED`, `INVALID_POSITION`.

**Breaking changes:** `add_root`, `add_node`, and all tree query operations gain new required parameters. Go `EngineClient` and contract test fixtures need updating.

### TreeNavigator Trait

Extracted last, after both tree types are working. Covers shared operations:
- `get_parent`, `get_children`, `get_upline`, `get_downline`, `get_position`
- `get_branch`, `count_downline`, `count_branch`, `is_descendant_of`
- `get_sponsor`, `get_sponsor_upline`, `get_sponsored`

Generalized from the two concrete implementations.

## Testing

### Shared Test Helpers

Extracted to `tree/test_helpers.rs` (behind `#[cfg(test)]`):
- `test_uuid(n: u8) -> Uuid`
- `test_uuid_u16(n: u16) -> Uuid`

### Property Tests (Both Tree Types)

Six mandatory property tests from `docs/development/network-engine.md`:
1. Parent-child consistency — bidirectional references
2. Depth consistency — depth = parent's depth + 1, root = 0
3. Upline completeness — `get_upline` returns exactly `depth` nodes, ends at root
4. Downline containment — every node in downline satisfies `is_descendant_of`
5. Count matches collection — `count_downline == get_downline.len()`
6. Branch partitioning — union of all branches == full downline, no duplicates

Sponsor edge property tests (both types):
7. Sponsor-sponsored consistency — if A sponsors B, B appears in A's `sponsored` list and B's `sponsor` points to A
8. Sponsor upline completeness — `get_sponsor_upline` length equals sponsor chain depth to root

### Binary-Specific Property Tests

9. Max two children — no node ever has more than 2 children
10. Position integrity — if a node is at position P, it is at `children[P]`

### Binary Edge Case Unit Tests

- Empty tree, single-node tree
- Left-only child, right-only child, both children
- Deep chain (1000 nodes, alternating left/right)
- Attempt to add third child fails
- Attempt to add to occupied position fails
- Remove left leaf, then re-add at same position

## Execution Sequence

**Phase 1: Extract shared foundations**
1. Create `tree/arena.rs` — move arena storage, alloc, free list, resolve, tombstone
2. Create `tree/test_helpers.rs` — move `test_uuid`, `test_uuid_u16`
3. Add sponsor edges to `Node` (`sponsor`, `sponsored`)
4. Add shared BFS walker and upline walker to `Arena`
5. Add sponsor-line traversals to `Arena`

**Phase 2: Retrofit unilevel**
6. Refactor `UnilevelTree` to wrap `Arena` and delegate traversals
7. Update `add_node` signature to accept `sponsor_id`
8. Add custom `Debug` impl (replace derive)
9. All existing unilevel tests must still pass. Add sponsor-related tests.
10. Property tests updated for sponsor edge consistency

**Phase 3: Build binary tree**
11. Create `tree/binary.rs` with `BinaryTree` wrapping `Arena`
12. Implement `add_root`, `add_node` (with position + sponsor), `remove_node`
13. Delegate shared operations to `Arena`
14. Add `PositionOccupied` error variant to `TreeError`
15. Full unit test suite + property tests
16. Edge case tests (left-only, right-only, deep alternating chain)

**Phase 4: Worker integration**
17. Change `WorkerState` to `HashMap<String, TreeInstance>`
18. Add `create_tree` operation
19. Update all existing handlers to accept `structure` param
20. Add `sponsor_id` to `add_node` / `add_root` handlers
21. Add sponsor-line operation handlers
22. Update Go `EngineClient` and contract test fixtures

**Phase 5: TreeNavigator trait**
23. Extract trait from two concrete implementations
24. Covers shared operations plus sponsor operations

## Not In Scope

- Binary commission calculator (separate task)
- Spillover/placement logic (Go layer, per decision 020)
- Holding tank implementation (Go layer)
- Go-side placement service
- Other tree types (matrix, stairstep, streamline)
