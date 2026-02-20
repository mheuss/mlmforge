# Binary Commission Calculator Design

**Status:** Approved
**Created:** 2026-02-20

## Goal

Build a Rust function that calculates binary pairing commissions for a commission period. Pure Rust, no FFI or EventStore integration. Takes a binary tree, config, distributor snapshots, volume events, and prior carry-forward state. Returns earnings and updated carry-forward state.

## Scope

**In scope:** Pairing mode only (WeakerLeg and VolumeRatio calculations), all three volume_after_payout modes (FullFlush, NetOff, CarryForward), cap_per_period, carry_forward_cap.

**Out of scope:** CycleStep mode (deferred to backlog), wire protocol integration, rank-based pairing percentages (flat percent for now per existing PairingConfig).

## Input Types

### Reused from unilevel (no changes)

- `DistributorSnapshot` — rank, personal_volume, status, has_order_in_period
- `VolumeSource` — source_id, cv_amount
- `CompensationPlan` — shared plan config
- `CommissionEligibility` — min PV, require_order, statuses. Active_leg_tiers are ignored (binary has no level depth).

### New: carry-forward state

```rust
/// Per-distributor leg volumes carried from the previous period.
pub struct LegVolumes {
    pub left: f64,
    pub right: f64,
}
```

Passed as `HashMap<Uuid, LegVolumes>`. Empty map for the first period. The calculator adds current-period volume on top of carried values.

### Function signature

```rust
pub fn calculate_binary_pairing(
    tree: &BinaryTree,
    plan: &CompensationPlan,
    structure: &BinaryStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    carry_forward: &HashMap<Uuid, LegVolumes>,
) -> Result<BinaryCalculationResult, CalculationError>
```

## Output Types

### BinaryCommissionEarning

One entry per distributor who earned a pairing bonus. No entry for zero matched volume or ineligible distributors.

```rust
pub struct BinaryCommissionEarning {
    pub earner_id: Uuid,
    pub left_volume: f64,
    pub right_volume: f64,
    pub matched_volume: f64,
    pub ratio: f64,          // 1.0 for WeakerLeg, min/max for VolumeRatio
    pub percent: f64,        // the pairing percent applied
    pub dollar_amount: f64,  // after multiplier, after cap
    pub capped: bool,        // true if cap_per_period reduced this earning
}
```

Dollar amount formula:

```
matched = min(left, right)
ratio = if VolumeRatio { min / max } else { 1.0 }
raw_amount = matched * percent * ratio * volume_to_dollar_multiplier
dollar_amount = min(raw_amount, cap_per_period)  // if cap exists
```

### BinaryCalculationResult

```rust
pub struct BinaryCalculationResult {
    pub earnings: Vec<BinaryCommissionEarning>,
    pub carry_forward: HashMap<Uuid, LegVolumes>,
}
```

The carry_forward map reflects post-payout state. Every distributor in the tree gets an entry (not just earners), because non-earners still accumulate volume that carries.

## Algorithm

### Phase 1: Prep

Three steps, each O(N).

**Step 1: Aggregate volume sources.** Build `HashMap<Uuid, f64>` from `&[VolumeSource]`. Sum cv_amount per source_id. Validate each source exists in the tree and has a snapshot (same error handling as unilevel).

**Step 2: Evaluate eligibility.** Reuse the same logic as unilevel's prep phase. For each distributor in snapshots, check min_pv, require_order, statuses. Cache as `HashMap<Uuid, bool>`. Skip active_leg_tiers entirely.

**Step 3: Bottom-up volume accumulation.** Single post-order traversal of the tree.

1. Get all nodes sorted by depth descending (leaves first).
2. Each node gets a subtree_total = their personal volume from the aggregated map (0 if none) + left child's subtree_total + right child's subtree_total.
3. After the pass, compute per-distributor leg volumes:
   - left_volume = left_child.subtree_total + carry_forward.left (0 if no left child or no carry)
   - right_volume = right_child.subtree_total + carry_forward.right (0 if no right child or no carry)

Store as `HashMap<Uuid, LegVolumes>`. This is the working leg volume map for the calculation phase.

This approach is O(N) total for the entire tree. Each node is visited exactly once. The arena's contiguous storage makes this cache-friendly. For 500K nodes, this is milliseconds.

### Phase 2: Calculate

Iterate all distributors in the working leg volume map. For each:

1. Skip ineligible. If not in the eligibility cache or not eligible, skip. Their volume still exists in the tree (already accumulated in phase 1), but they earn nothing.

2. Compute pairing bonus:
   ```
   matched = min(left, right)
   if matched == 0.0: skip, no earning

   ratio = match calculation_mode {
       WeakerLeg => 1.0,
       VolumeRatio => if max > 0.0 { min / max } else { 0.0 },
   }

   raw_amount = matched * percent * volume_to_dollar_multiplier * ratio
   ```

3. Apply cap. If cap_per_period is set and raw_amount exceeds it, clamp and set capped = true.

4. Emit BinaryCommissionEarning.

### Phase 3: Post-Payout Carry-Forward

After all earnings are computed, build the output carry-forward map. For every distributor in the tree (not just earners):

| Mode | Left leg after | Right leg after |
|------|---------------|-----------------|
| FullFlush | 0 | 0 |
| NetOff | left - matched | right - matched |
| CarryForward | 0 if left is weaker, else left - matched | 0 if right is weaker, else right - matched |

Apply carry_forward_cap if set: clamp each leg to the cap.

Non-eligible distributors still get carry-forward entries. Their volume accumulated but was not paid out. Whether it carries depends on the mode. In FullFlush, everyone flushes. In CarryForward and NetOff, unpaid distributors keep their full leg volumes (nothing was matched, so nothing is subtracted).

## Eligibility

Reuses the shared `CommissionEligibility` config. Same rules as unilevel for basic eligibility (min PV, require_order_in_period, eligible_statuses).

Active_leg_tiers do not apply. Binary has no level depth to unlock.

Cross-structure qualification (e.g., "must be Gold rank to earn on binary") is a Go-layer concern. Go filters which distributors are eligible for which structures based on the rank's `qualified_structures` list. The Rust calculator receives only already-qualified distributors.

Rank-based pairing percentages are deferred. The current PairingConfig has a single flat percent. If needed later, this is an additive change to the config and an isolated change to the calculator function.

## Error Handling

Same philosophy as unilevel (ADR-017 decision 6).

| Situation | Behavior |
|-----------|----------|
| Volume source not in tree | Return CalculationError::SourceNotInTree |
| Volume source not in snapshot | Return CalculationError::SourceNotInSnapshot |
| Invalid cv_amount (negative, NaN) | Return CalculationError::InvalidCvAmount |
| Distributor in tree but not in snapshot | Treat as ineligible. Volume still accumulates through them. |
| Carry-forward entry for user not in tree | Ignore silently. Stale data from a removed node. |
| Zero volume in both legs | No earning emitted. Carry-forward reflects the mode. |

No new error variants needed. The existing CalculationError enum covers all strict cases.

## Architectural Decisions

These extend ADR-017. Binary follows all six existing decisions plus these additions.

### Decision 7: Position-blind calculation

The calculator computes earnings per tree node (position), not per distributor (person). Multi-position ownership (one person owning multiple nodes) is a Go-layer aggregation concern. The calculator does not know or care who owns a position. This keeps the Rust engine focused on topology and computation.

### Decision 8: Carry-forward is caller-provided state

The calculator is a pure function. It receives carry-forward state as input and returns updated state as output. No internal persistence. The caller (Go) stores carry-forward between periods. This matches the snapshot pattern: callers provide facts, the calculator applies rules.

### Decision 9: Bottom-up accumulation over per-distributor walks

Leg volumes are computed in a single O(N) post-order traversal, not by walking each distributor's subtrees individually (which would be O(N^2)). Each node is visited once. The subtree_total propagates up naturally through the binary structure.

### Decision 10: Shared eligibility, structure-specific calculation

Binary reuses CommissionEligibility for basic qualification but ignores active_leg_tiers. The eligibility evaluation function can be shared across calculator types with a flag or by having callers skip the leg-tier step. Calculator-specific logic (pairing, volume accumulation) stays in the calculator function.

## Testing Strategy

### Unit tests

- Basic pairing: balanced legs, unbalanced legs, zero volume
- WeakerLeg vs VolumeRatio calculation modes
- All three volume_after_payout modes with carry-forward verification
- Cap enforcement (under cap, at cap, over cap)
- Carry-forward cap clamping
- Eligibility filtering (ineligible distributor's volume still accumulates)
- Carry-forward from prior period added to current volume
- Empty tree, single node, deep binary tree
- Multiple volume sources for same distributor (aggregation)

### Property-based tests

- Total payout never exceeds sum(matched_volume) * percent * multiplier
- Carry-forward leg volumes are always non-negative
- Carry-forward cap is never exceeded
- FullFlush always produces zero carry-forward
- No duplicate earner_ids in output

## Refactor Risk Assessment

The shared types (DistributorSnapshot, CommissionEligibility, VolumeSource) are safe to build on. Adding fields later is cheap. Cross-structure concerns are Go-layer. Calculator functions are isolated per ADR-017 decision 4.

The new types introduced here (LegVolumes, BinaryCommissionEarning, BinaryCalculationResult) are the contracts that need care. They define how Go consumes binary results and stores carry-forward state.
