# 022: Shared Commission Walk

> **Status: known drift.** Written when four calculators existed. There are now
> seven, and generation and streamline are built rather than upcoming. The
> decision itself holds: `walk.rs` is still `pub(crate)` generic functions over
> `TreeNavigator`, there is still no `CommissionCalculator` trait, and all five
> level-based calculators route through the shared walk. Counts and tense below
> are stale.

## The Problem

The unilevel, matrix, and stairstep calculators duplicated ~200 lines of identical logic: eligibility evaluation, active leg counting, compression handling, rate table lookup, and the upline walk loop. When compression behavior needed updating, the change had to be made in three files. The rule of three (decision 017) was satisfied, triggering extraction.

The question was not whether to extract, but how. Two approaches were on the table: a `CommissionCalculator` trait on the public API, or generic functions over the existing `TreeNavigator` trait.

## The Decision

Shared logic lives in `commission/walk.rs` as `pub(crate)` functions generic over `T: TreeNavigator`. No `CommissionCalculator` trait exists. Each calculator remains a standalone public function that builds config and delegates to the shared walk.

### Why Not a Trait

A `CommissionCalculator` trait would unify the calling convention. But the calling convention is not where duplication lived. The duplication was internal: the walk loop, eligibility prep, compression checks, and rate lookups.

A trait also doesn't fit naturally:

- **Different config types.** Unilevel takes `UnilevelStructureConfig`, matrix takes `MatrixStructureConfig`, stairstep takes `StairstepStructureConfig`. Unifying these behind a trait requires wrapper structs or enum dispatch. Both add indirection for no behavioral gain.
- **Binary doesn't fit.** It uses pairing mechanics, returns `BinaryCalculationResult`, and has carry-forward state. A trait that covers five of seven calculators is a leaky abstraction. Board plan does not fit either: it takes cycle events rather than snapshots and returns `BoardCommissionResult`.
- **Callers already know the type.** The Go layer picks the calculator based on the config structure type. There is no scenario where someone has "a calculator" but doesn't know which kind.

A trait can be added later if a consumer needs polymorphic dispatch. The standalone functions can be wrapped without changing their internals.

### How the Shared Walk Works

`walk_level_commissions<T: TreeNavigator>` is the core function. It takes:

- A tree (any type implementing `TreeNavigator`)
- A `LevelWalkConfig` struct bundling walk parameters (max depth, broad percent, multiplier, compression config, rate table)
- An eligibility cache (built by `evaluate_eligibility<T>`)
- Distributor snapshots and volume sources
- A `should_stop: impl Fn(Uuid) -> bool` callback

The callback takes `Uuid`, not `&Node`. The closure only needs the ID for set membership checks. Passing `Uuid` keeps captures simple. If a future use case needs node data in the stop decision, widen the signature then.

### Plan-Specific Behavior

Each calculator injects its unique logic through two mechanisms:

1. **`LevelWalkConfig` fields.** Matrix computes `max_depth = min(height, max_depth)` before constructing the config. Unilevel and stairstep pass `max_depth` directly.
2. **`should_stop` callback.** Unilevel and matrix pass `|_| false`. Stairstep passes a closure that stops at group boundaries: `|id| group_leaders[id] != source_leader`.

Walk 2 (stairstep differential/generation overrides) stays in `stairstep.rs`. It is unique to stairstep and not a candidate for extraction.

## What This Means for Future Calculators

Generation and streamline have since been built and both follow this pattern. A future level-based calculator should too:

1. Build a `LevelWalkConfig` with plan-specific parameters
2. Call `walk::evaluate_eligibility` for the prep phase
3. Call `walk::walk_level_commissions` with an appropriate `should_stop` callback
4. Add any plan-specific post-walk logic (like stairstep's Walk 2)
5. Call `walk::sort_earnings` after combining all earnings

If a future calculator has a fundamentally different walk mechanic (like binary's pairing), it stays standalone. The shared walk is for level-based upline walks only.

## What This Enables

- **Single source for walk logic.** Compression, eligibility, depth limits, and rate lookups are defined once.
- **New calculators are thin.** A new level-based calculator is ~30 lines of config setup plus any unique post-walk logic.
- **Consistent behavior.** All level-based calculators handle compression, missing snapshots, and edge cases identically because they use the same code path.
