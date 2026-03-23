# 007: Unilevel Tree Implementation

## The Problem

The unilevel tree is the first concrete tree type for the network engine. It needs to handle trees with millions of nodes, support all operations from the `TreeNavigator` interface, and prove that the generalized position-indexed model from decision 003 works in practice.

The implementation choices here set the pattern for binary and matrix trees.

## Decisions

### Arena Storage

All nodes live in a contiguous `Vec<Node>`. Each node is referenced by a `NodeIndex` wrapper around `usize`. A `HashMap<Uuid, NodeIndex>` provides O(1) lookup by user ID.

Tree walks follow arena indices directly. No hash lookups during traversal. Nodes inserted in similar timeframes end up physically near each other in memory. This matches how downline walks work in practice.

We considered `petgraph` and a pure `HashMap<Uuid, Node>` approach. `petgraph` adds dependency weight and API surface we do not need. Pure HashMap loses cache locality during tree walks, which is the hot path.

### UUID User IDs

User IDs are `Uuid` (128-bit) throughout the system. This is a system-wide decision that constrains the Identity context.

The alternative was `u64` internal IDs with a boundary mapping table. The mapping table would cost more memory (100+ MB at 1M users) than the 8 extra bytes per ID reference that UUID adds. Tree walks use arena indices regardless. The hot path performance is identical.

### Iterative BFS Traversal

All downline and branch walks use iterative BFS with `VecDeque`. No recursion. BFS gives level-ordered results, which matches how distributors think about their organization.

Iterative traversal is stack-safe at any depth. A recursive approach would blow the stack on deep chains (1000+ nodes). The implementation handles both 1000-node deep chains and 1000-child wide fans without issues.

Upline walks follow parent links directly. O(d) where d is depth. No queue needed.

### Tombstone Deletion with Free List

Deleted nodes are tombstoned. Their arena slots go on a free list for reuse by the next `add_root` or `add_node` call.

Node removal is rare in MLM trees. Distributors are deactivated, not deleted. When removal does happen, it is leaf-only. Removing a node with children returns an error to prevent orphaned subtrees.

This avoids the complexity of arena compaction. The trade-off is that tombstoned slots consume memory until reused. For MLM workloads where removal is uncommon, this is acceptable.

### Position-Indexed Model

Child position equals the index in the parent's `children` Vec. Position 0 is the first enrolled child, position 1 is the second. Width is unbounded for unilevel.

Binary trees will use positions 0 (left) and 1 (right). Matrix trees will use positions 0 through width-1. The same `get_branch(user, position)` call works across all tree types.

This validates the generalized tree model from decision 003. No tree-type-specific queries are needed.

## What We Considered

**petgraph.** Full-featured graph library. More API surface and dependency weight than needed for tree-specific operations. Arena storage is simpler and faster for our use case.

**HashMap-only storage.** Each node stores child UUIDs instead of indices. Simpler data model but every traversal step requires a hash lookup. Unacceptable for million-node tree walks.

**Recursive traversal.** Simpler code but stack overflow risk on deep chains. MLM trees can be arbitrarily deep. Stack safety is not optional.

**Arena compaction on delete.** Moves nodes to fill gaps left by deletions. Complex to implement correctly (requires updating all indices). Not worth it when deletions are rare.

## What This Enables

- **Pattern for future tree types.** Binary and matrix implementations follow the same arena + index + BFS approach.
- **Predictable performance.** Arena storage and iterative traversal give consistent performance regardless of tree shape.
- **Safe at scale.** No stack overflow, no hash lookup overhead during walks, O(1) user lookup by UUID.
- **Clean abstraction boundary.** The position-indexed model means consumers do not need to know which tree type they are querying.
