# Unilevel Commission Calculator Design

**Status:** Approved
**Created:** 2026-02-17

## Goal

Build a Rust function that calculates unilevel commissions for a commission period. Pure Rust, no FFI or EventStore integration. Takes a tree, config, distributor data, and volume events. Returns a flat list of earnings.

## Scope

Rust calculation only. The Go integration boundary (FFI or gRPC), commission event types, and EventStore writes are separate future work.

## Input Types

### DistributorSnapshot

Point-in-time facts about a distributor for a commission period. Contains only observable data. No derived fields.

```rust
pub struct DistributorSnapshot {
    pub rank: String,
    pub personal_volume: f64,
    pub status: String,
    pub has_order_in_period: bool,
}
```

The caller builds a `HashMap<Uuid, DistributorSnapshot>` from whatever data source they have. The calculator doesn't care where the data came from.

### VolumeSource

A volume event that triggers commission calculation. One walk per volume source.

```rust
pub struct VolumeSource {
    pub source_id: Uuid,
    pub cv_amount: f64,
}
```

### Function Signature

```rust
pub fn calculate_unilevel(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &UnilevelStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError>
```

## Output Types

### CommissionEarning

One entry per earner-per-source.

```rust
pub struct CommissionEarning {
    pub earner_id: Uuid,
    pub source_id: Uuid,
    pub level: u8,
    pub rate: f64,
    pub cv_amount: f64,
    pub dollar_amount: f64,
}
```

The formula for `dollar_amount`:

```
cv_amount * broad_commission_percent * volume_to_dollar_multiplier * rate
```

Where `volume_to_dollar_multiplier` comes from the structure config if set, otherwise falls back to the plan-level `VolumeConfig.volume_to_dollar_multiplier`.

An empty vec is valid. It means nobody earned anything.

## Algorithm

### Phase 1: Prep Pass

A single pass over all distributors to build an internal eligibility cache. No tree walking happens yet.

For each distributor in the snapshot map:

1. Check basic eligibility against `CommissionEligibility` config:
   - `personal_volume >= min_personal_volume`
   - `has_order_in_period` (if `require_order_in_period` is true)
   - `status` is in `eligible_statuses` (empty list means all eligible)

2. Count active legs. Query the tree for direct children. Count how many are themselves eligible (passed step 1).

3. Determine max earning depth from `active_leg_tiers`:
   - Walk tiers from highest `min_active_legs` to lowest
   - First tier where `active_leg_count >= min_active_legs` sets `max_commission_depth`
   - `max_commission_depth` of 0 means unlimited
   - If no tier matches, use config `max_depth` as default

4. Store the result in an internal map:

```rust
struct EligibilityResult {
    eligible: bool,
    max_earning_depth: Option<u8>,  // None = use config max_depth
}
```

Active leg tiers are optional. If `active_leg_tiers` is empty, all eligible distributors earn up to `max_depth` from the rate table config. The tier system is an additional constraint, not a replacement.

### Phase 2: Walk

For each `VolumeSource`, walk from the source distributor upward through the tree.

1. Start at the source node. The source generates volume but doesn't earn from their own volume. Begin walking at their parent.

2. Walk upline, maintaining a level counter starting at 1.

3. At each node in the upline:

   a. **Compression** (if enabled):
   - `SkipInactive`: if the distributor is not eligible, skip them. Do not increment the level counter. Continue to next upline node.
   - `SkipBelowRank`: if the distributor's rank is below `rank_threshold`, skip them. Do not increment the level counter.
   - If compression is disabled and the distributor is ineligible, they don't earn but the level counter increments. The commission at that level is forfeited.

   b. **Depth limits:**
   - If `level > max_depth` (from config), stop the walk.
   - If the distributor has a `max_earning_depth` from active leg tiers and `level > max_earning_depth`, this distributor doesn't earn. But the walk continues. The next upline node might have a higher depth allowance.

   c. **Rate lookup:**
   - Find `rate_table[distributor_rank][level]`
   - If the rank isn't in the table, or the level isn't in that rank's entries, the distributor earns nothing at this level. The level counter still increments.

   d. **Calculate earning:**
   - `dollar_amount = cv_amount * broad_commission_percent * volume_to_dollar_multiplier * rate`
   - Emit a `CommissionEarning` entry.

   e. Increment level counter. Move to next upline node.

4. Stop when you hit the root (no more upline) or `level > max_depth`.

### Key Behavior: Compression vs No Compression

With compression enabled, skipping an ineligible node does not consume a level. This preserves the full commissionable depth regardless of how many nodes are skipped. Without compression, ineligible nodes create gaps. The level is consumed and the commission is forfeited.

### Key Behavior: Per-Distributor Depth vs Config Depth

The `max_depth` from config is a hard ceiling on the walk itself. The `max_earning_depth` from active leg tiers is a per-distributor earning limit. A distributor past their personal depth limit doesn't earn, but the walk continues. Someone above them with a higher tier can still earn at that level.

## Error Handling

- **Source not in tree:** Return `CalculationError`. Data integrity problem.
- **Source not in snapshot:** Return `CalculationError`. Data integrity problem.
- **Upline node missing from snapshot:** Treat as ineligible. Log warning. The calculation continues.
- **Rate not found in table:** No earning at that level. Walk continues. This is normal. Not every rank earns at every level.

## Architectural Decisions (ADR-017)

These decisions apply to all future commission calculators, not just unilevel. To be extracted into a formal ADR during implementation.

### Decision 1: Snapshot = facts, calculator = rules

Callers provide raw observable data in `DistributorSnapshot`. The calculator derives all eligibility, depth limits, and skip decisions from the `CompensationPlan` config. No derived fields in the snapshot.

**Why:** Keeps the data boundary clean. The caller doesn't need to understand commission rules. The calculator doesn't need to know where data came from. Eligibility logic lives in one place and can't drift from the config.

### Decision 2: Flat earnings list as output

All calculators return `Vec<CommissionEarning>`. One entry per earner-per-source. Consumers aggregate however they need. No pre-grouping.

**Why:** Simplest possible output. Self-contained entries are easy to audit, test, and aggregate downstream. Pre-grouping assumes a consumption pattern.

### Decision 3: Prep + walk two-phase pattern

All calculators run a prep pass (eligibility evaluation, leg counting, caching) before the main calculation loop. This avoids repeated derivation during walks.

**Why:** Active leg counting and eligibility checks are expensive if repeated per-walk. A single prep pass caches all derived data. The walk phase uses O(1) lookups.

### Decision 4: No shared calculator abstraction yet

Each calculator is a standalone public function. No `CommissionCalculator` trait. Extract common patterns when we have three concrete implementations.

**Why:** Binary calculation has fundamentally different inputs (pairing, not level walking). We don't know what the shared interface looks like yet. Premature abstraction here would constrain future designs. Three cases is the threshold for extraction.

### Decision 5: Compression is part of the walk

Compression affects level counting during the walk itself. It cannot be applied as a post-processing step.

**Why:** Whether a skipped node consumes a level depends on compression being enabled. This changes the level number for every subsequent node in the walk. Post-processing can't reconstruct this.

### Decision 6: Defensive on missing data, strict on source data

Missing volume sources are errors (the caller gave us bad input). Missing upline nodes during a walk are treated as ineligible with a warning (the calculation continues when it safely can).

**Why:** Volume sources are the explicit input to the calculation. If they're wrong, the results are meaningless. Upline nodes missing from snapshots are a data completeness issue that shouldn't halt an entire commission run. Treating them as ineligible is safe and conservative.
