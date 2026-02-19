# 020: Tree Topology Separation

## The Problem

The Network Engine implements multiple tree structures (unilevel, binary, matrix). Each structure has different placement strategies. Unilevel auto-appends children. Binary requires left/right choice with spillover. Matrix uses forced placement within a fixed grid.

Where does placement logic live? Inside the tree data structure, or in the caller?

The unilevel tree was implemented first (decision 007) and makes no placement decisions. The caller passes a parent ID and the tree appends. But this was never stated as an architectural principle. Without an explicit decision, someone implementing binary might reasonably put spillover logic inside the tree struct.

## Decisions

### Trees Are Pure Topology

Tree structures enforce shape constraints and provide traversal operations. They do not contain placement logic, sponsor awareness, holding tank knowledge, or business rules.

A tree knows:
- Its structural constraints (unilevel: unbounded children; binary: max 2; matrix: fixed width)
- Parent-child relationships
- Depth, position indices, traversal

A tree does not know:
- Who sponsored a user (sponsor != placement parent)
- Why a user was placed at a given position
- Whether a holding tank exists
- What spillover strategy is active
- Whether per-user placement preferences are set

### The Caller Decides Placement

The Go platform layer (or a future placement service) is responsible for:
1. Determining which position a new user occupies
2. Resolving spillover (walking the subtree to find the first open slot)
3. Consulting per-user placement preferences
4. Managing holding tanks (parking users who need manual placement)
5. Calling `add_node(user_id, parent_id, position, enrolled_at)` with the final answer

The tree validates the request (position in range, not already occupied, user doesn't already exist) and wires it up. If the position is invalid, the tree returns an error. It never silently picks an alternative.

### Position Validation Is a Tree Responsibility

While placement decisions are the caller's job, position enforcement is the tree's job. Each tree type validates positions against its own constraints:

- **Unilevel:** Any position (append to end of children list). No position parameter needed.
- **Binary:** Position must be 0 (left) or 1 (right). Position must not already be occupied.
- **Matrix:** Position must be within the configured width. Position must not already be occupied.

This is validation, not decision-making. The tree says "that position is invalid" but never says "let me pick a better one."

## What We Considered

**Spillover inside the tree.** Binary trees could expose `add_node_with_spillover(user_id, sponsor_id, side)` that walks the subtree to find the first available slot. This is convenient but couples the tree to a specific placement strategy. Not all binary plans use the same spillover algorithm. Some companies override spillover for certain ranks. Baking one strategy into the tree forces all callers into that behavior.

**Placement trait on trees.** Define a `PlacementStrategy` trait that trees implement. This adds indirection without benefit. The Go layer already has the business context (user preferences, rank overrides, holding tank state) needed to make placement decisions. A Rust trait can't access that context without threading it through the protocol.

**Hybrid approach.** Expose both `add_node(exact position)` and `add_node_suggested(preferences)` on the tree. This muddies the API surface and creates two paths to the same mutation, which complicates testing and reasoning about state.

## What This Enables

- **Testable trees.** Tree tests verify topology and traversal without mocking placement services, sponsor lookups, or preference stores.
- **Swappable placement strategies.** A company can change from balanced spillover to power-leg spillover without touching the tree implementation.
- **Consistent API.** Every tree type has the same `add_node` contract: caller provides exact placement, tree validates and wires. No special cases per structure type.
- **Holding tank compatibility.** Per-structure holding tanks (decision 003) work naturally. A user sits in the binary holding tank while already placed on the unilevel. The tree doesn't need to know about this.
