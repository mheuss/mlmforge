# 021: Sponsor vs. Placement in Commission Calculations

## The Problem

Trees store two edge types: placement edges (parent/children) and sponsor edges (sponsor/sponsored). Decision 020 establishes that trees store both for cache-friendly traversal. But it does not specify which edge type commission calculators should use for each purpose.

This matters in matrix plans. Forced placement with spillover means placement parent and sponsor are routinely different people. A distributor sponsored by Alice might be placed under Bob because Alice's slots were full. The two edge types diverge.

The matrix commission calculator had a bug where `count_active_legs` used `get_children` (placement children) instead of `get_sponsored` (personally sponsored recruits). The function returned the wrong count for any distributor whose placement children differed from their sponsored recruits. Active leg tier depth limits were computed from the wrong population.

The bug passed code review and all tests. It was caught by an automated review tool citing the eligibility docs. This means the distinction is subtle enough to need an explicit rule.

## The Decision

Commission calculators use two different edge types for two different purposes.

| Purpose | Edge type | Method | Why |
|---------|-----------|--------|-----|
| **Upline walk** (who earns from this volume) | Placement | `get_upline` | Commissions flow up the placement tree. This is where the distributor sits in the structure. |
| **Active leg counting** (how deep can this distributor earn) | Sponsor | `get_sponsored` | Active leg tiers count personally sponsored frontline distributors. Sponsoring is a recruiting action. Placement is a structural outcome. |

The rule: **placement edges determine commission flow. Sponsor edges determine personal qualification.**

This applies to all tree types that track both edge types. In unilevel trees, the two are often identical because there is no forced placement. The distinction still holds. Code should use the semantically correct method even when the results happen to match.

## Why This Is Easy to Get Wrong

Three factors conspire to hide the bug:

1. **Unilevel sets the pattern.** The unilevel calculator was built first. In unilevel trees, sponsor and placement parent are typically the same person. `get_children` returns the right answer by coincidence. When the matrix calculator was built by following the unilevel pattern, the coincidence broke.

2. **Tests use simple trees.** Most unit tests build chains where each node is both the placement child and the sponsored recruit of the node above. The bug only surfaces in trees with spillover, where a sponsor's recruits are placed under someone else.

3. **The function name is ambiguous.** `count_active_legs` suggests counting legs, which sounds like children. The eligibility docs define active legs as "personally sponsored frontline distributors," but the function name doesn't signal that distinction.

## What This Means for Future Calculators

Every commission calculator that supports active leg tiers must use `get_sponsored`, not `get_children`. Stairstep, generation, and streamline have since been built and all comply. They route through the shared walk, and `count_active_legs` in `commission/walk.rs` calls `get_sponsored`, so the rule is inherited rather than restated per calculator.

The same principle extends to any commission logic that evaluates personal recruiting activity. If the question is "what did this distributor do?" the answer comes from sponsor edges. If the question is "where does this distributor sit?" the answer comes from placement edges.

## What This Enables

- **Correct active leg tiers in matrix.** Distributors are evaluated on their personal recruiting, not on who the system placed beneath them.
- **Consistent semantics across plan types.** The rule is the same regardless of tree structure. No per-plan-type special cases.
- **Testable distinction.** Tests that exercise spillover scenarios will catch future regressions. The matrix calculator's `walk_follows_placement_not_sponsor` test already verifies the walk uses placement. Active leg tests should verify sponsor usage.
