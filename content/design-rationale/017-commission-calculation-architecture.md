# 017: Commission Calculation Architecture

## The Problem

The commission engine needs to calculate earnings across multiple compensation plan types. Unilevel came first. Binary, matrix, stairstep, generation, streamline, and board plan followed, and all seven now exist. Each plan type has fundamentally different mechanics. Unilevel walks a tree upward counting levels. Binary pairs left and right leg volume. Matrix fills a fixed-width grid.

Despite these differences, the calculators share structural concerns. What data do they receive? What do they return? How do they handle missing data? How do they separate input facts from business rules? Getting these decisions wrong in the first calculator means fixing them across all seven later.

These decisions apply to all commission calculators. They were identified during the unilevel calculator design but are intentionally plan-type-agnostic.

## Decisions

### Snapshot = Facts, Calculator = Rules

Callers provide raw observable data in `DistributorSnapshot`. Personal volume, rank, status, activity flags. The calculator derives all eligibility, depth limits, and skip decisions from the `CompensationPlan` config. No derived fields in the snapshot.

The caller does not need to understand commission rules. The calculator does not need to know where data came from. Eligibility logic lives in one place and cannot drift from the config. If a plan changes its eligibility criteria, only the config changes. The snapshot structure stays the same.

We considered including derived fields like `is_eligible` or `max_depth` in the snapshot. This pushes commission logic into the caller. Two systems would need to agree on eligibility rules. That agreement would break silently when one side changes.

### Flat Earnings List as Output

Five of the seven calculators succeed with a `Vec<CommissionEarning>`: unilevel, matrix, stairstep, generation, and streamline. The table below gives all seven exact signatures. Each entry is self-contained with the earner, the volume source, the level, the rate, and the dollar amount. No pre-grouping. No nesting. No aggregation.

Two do not, because they carry state a flat list cannot hold.

| Calculator | Returns |
|------------|---------|
| unilevel, matrix, stairstep, generation, streamline | `Result<Vec<CommissionEarning>, CalculationError>` |
| `calculate_binary_pairing` | `Result<BinaryCalculationResult, CalculationError>`, which is `earnings: Vec<BinaryCommissionEarning>` plus `carry_forward` |
| `calculate_board_commissions` | `BoardCommissionResult`, which is `earnings: Vec<BoardCycleEarning>` plus `updated_cycle_counts`. Not a `Result`, because it cannot fail |

Binary has to return post-payout leg volumes for every distributor, earners and non-earners alike, or the next period cannot resume. Board plan has to return updated cycle counts for the same reason. Both are period state, not earnings. Decision [022](022-shared-commission-walk.md) treats the same split as one reason binary stays out of the shared walk. The flat-list rule below is about the earnings themselves, and it holds inside all three shapes.

An earner can appear more than once for the same volume source. Stairstep pays a level commission and an override on one source, and multi-tier overrides can select the same ancestor for several tiers. `sort_earnings` carries a level tiebreaker for exactly this reason, and says so in its own doc comment. `(earner_id, source_id)` is not an identity.

> **Envelope changing under [029](029-commission-provenance-on-the-wire.md).** Phase B of that arc replaces the bare array with `{earnings, walks, plan}`, and adds a `walk` reference to each earning. It has not landed: the five level-based calculators still succeed with a bare `Vec<CommissionEarning>` today, and the worker serializes that directly. Everything this section says about the earnings themselves survives the change. The list inside `earnings` stays flat, self-contained, and ungrouped exactly as described here. Only the envelope around it moves.

Consumers aggregate however they need. A payout system sums by earner. An audit report groups by source. A dashboard shows level breakdowns. None of these consumption patterns should constrain the calculator's output format.

We considered returning grouped results (by earner, by level, by source). Every grouping assumes a consumption pattern. The flat list is the most flexible. Grouping is cheap to do downstream.

### Prep + Walk Two-Phase Pattern

All calculators run a prep pass before the main calculation loop. The prep pass evaluates eligibility, counts active legs, determines per-distributor depth limits, and caches the results. The walk phase uses O(1) lookups against the cache.

Active leg counting requires querying the tree for each distributor's children and checking each child's eligibility. Doing this inline during the walk would repeat the work for every volume source. A single prep pass pays the cost once.

The two phases also separate concerns. Prep answers "who can earn and how deep?" Walk answers "who earns what from this volume?" Testing each phase in isolation is straightforward.

### Matrix Reuses the Unilevel Walk

The matrix calculator uses the same level-based upline walk as unilevel. Same prep phase, same walk loop, same compression logic, same rate table lookup. The only structural difference is the effective depth ceiling: `min(matrix_params.height, level_commission.max_depth)` instead of just `max_depth`.

This was not a foregone conclusion. Matrix has forced placement, fixed width, and spillover. It would be reasonable to expect a fundamentally different calculation approach. But matrix commissions are level-based. The walk up the placement tree works the same way. The tree shape constrains how nodes are placed, not how commissions are calculated.

This reinforces the decision to keep calculators as standalone functions. The unilevel and matrix calculators share almost all their logic, but extracting a shared abstraction now would be premature. Wait for the third level-based calculator (generation or stairstep) to see if the pattern holds.

### No Shared Calculator Abstraction Yet

Each calculator is a standalone public function. No `CommissionCalculator` trait. No shared interface. The unilevel calculator is `calculate_unilevel`. The binary calculator is `calculate_binary_pairing`. Each takes the inputs it needs and returns its own shape, described in the Flat Earnings List section above. The five level-based calculators succeed with a `Vec<CommissionEarning>`, and that payload becomes `CommissionCalculationResult` when 029's phase B lands. The standalone-function decision is unaffected either way.

Binary calculation has fundamentally different inputs. It pairs volume from two legs rather than walking levels. We do not know what a shared interface would look like. Premature abstraction here would constrain future designs.

The project follows the rule of three. Extract common patterns when we have three concrete implementations. Two is not enough to see the real shape.

**Status update (HEU-200 complete):** Three calculators now exist: unilevel, matrix, and stairstep. The rule of three was satisfied. Shared logic was extracted into `commission/walk.rs` as generic functions over `TreeNavigator`. No `CommissionCalculator` trait was created — the duplication was internal (walk loop, prep phase), not external (calling convention). Callers already know which calculator to use from the config type, so polymorphic dispatch adds no value. The standalone public functions remain. See decision 022 for the full rationale.

### Compression Is Part of the Walk

Compression affects level counting during the walk itself. When compression is enabled and a node is skipped, the level counter does not increment. This changes the level number for every subsequent node in the upline path.

Compression cannot be applied as a post-processing step. The level assignments depend on which nodes were skipped. Post-processing would need to reconstruct the skip decisions, which means reimplementing the walk logic.

This means compression behavior is tightly coupled to the walk loop. Each plan type that supports compression must implement it inline. This is acceptable. Compression rules vary between plan types anyway.

### Defensive on Missing Data, Strict on Source Data

Volume sources and their distributors must exist in the tree and snapshot. If they do not, the calculator returns an error. These are the explicit inputs to the calculation. Bad inputs produce meaningless results.

Upline nodes missing from snapshots during a walk are treated as ineligible silently. The calculation continues. Missing upline data is a completeness issue, not an integrity issue. Halting an entire commission run because one distributor's snapshot is missing would be disproportionate.

Volume amounts are also validated: `cv_amount` must be finite and non-negative. NaN, positive infinity, negative infinity, and negative values all produce `InvalidCvAmount` errors. These are input integrity checks, not business rules. A non-finite CV amount is always a bug upstream.

This split reflects the difference between "the caller gave us bad input" and "the data has gaps we can safely work around." Strict on the former. Defensive on the latter.

## What This Enables

- **Consistent calculator behavior.** The five level-based calculators share an output contract, so code that consumes their results does not care which one produced them. They share a parameter shape too, five arguments in the same order, but not the types: the tree is a `&UnilevelTree`, `&MatrixTree`, or `&StreamlineEngine`, and the structure config differs per plan. Decision [022](022-shared-commission-walk.md) makes those differing types one of its reasons for having no calculator trait. Binary and board plan differ on the output side as well, and a caller has to know which it asked for.
- **Independent calculator development.** Each calculator is a standalone function with no shared abstraction to coordinate. Teams or sessions can build different calculators in parallel.
- **Clean testing boundaries.** Snapshots and volume in, earnings out, with binary and board plan also returning the period state they carry. No hidden state. No setup beyond providing the input data. Property-based testing works naturally against the flat earnings list inside every one of the three shapes.
- **Shared logic extracted, no trait.** HEU-200 pulled the common walk into `commission/walk.rs` as generic functions over `TreeNavigator`. No `CommissionCalculator` trait was created, and the standalone functions remain. They can still be wrapped in a trait later without changing their internals. See the status update above and decision 022.
