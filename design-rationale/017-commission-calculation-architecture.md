# 017: Commission Calculation Architecture

## The Problem

The commission engine needs to calculate earnings across multiple compensation plan types. Unilevel is the first. Binary, matrix, stairstep, generation, and streamline follow later. Each plan type has fundamentally different mechanics. Unilevel walks a tree upward counting levels. Binary pairs left and right leg volume. Matrix fills a fixed-width grid.

Despite these differences, the calculators share structural concerns. What data do they receive? What do they return? How do they handle missing data? How do they separate input facts from business rules? Getting these decisions wrong in the first calculator means fixing them across all six later.

These decisions apply to all commission calculators. They were identified during the unilevel calculator design but are intentionally plan-type-agnostic.

## Decisions

### Snapshot = Facts, Calculator = Rules

Callers provide raw observable data in `DistributorSnapshot`. Personal volume, rank, status, activity flags. The calculator derives all eligibility, depth limits, and skip decisions from the `CompensationPlan` config. No derived fields in the snapshot.

The caller does not need to understand commission rules. The calculator does not need to know where data came from. Eligibility logic lives in one place and cannot drift from the config. If a plan changes its eligibility criteria, only the config changes. The snapshot structure stays the same.

We considered including derived fields like `is_eligible` or `max_depth` in the snapshot. This pushes commission logic into the caller. Two systems would need to agree on eligibility rules. That agreement would break silently when one side changes.

### Flat Earnings List as Output

All calculators return `Vec<CommissionEarning>`. One entry per earner-per-source. Each entry is self-contained with the earner, the volume source, the level, the rate, and the dollar amount. No pre-grouping. No nesting. No aggregation.

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

Each calculator is a standalone public function. No `CommissionCalculator` trait. No shared interface. The unilevel calculator is `calculate_unilevel`. The binary calculator will be `calculate_binary`. Each takes the inputs it needs and returns `Vec<CommissionEarning>`.

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

- **Consistent calculator behavior.** All plan types follow the same input/output contract. Code that consumes commission results works regardless of which calculator produced them.
- **Independent calculator development.** Each calculator is a standalone function with no shared abstraction to coordinate. Teams or sessions can build different calculators in parallel.
- **Clean testing boundaries.** Snapshot-in, earnings-out. No hidden state. No setup beyond providing the input data. Property-based testing works naturally against the flat output list.
- **Trait extraction ready.** Three calculators (unilevel, matrix, stairstep) now exist. The common patterns are visible. HEU-200 tracks extracting a shared abstraction. The standalone functions can be wrapped in a trait without changing their internals.
