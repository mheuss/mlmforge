# 013: Generation Compensation Configuration

## The Problem

Generation plans pay commissions based on "generations" of qualified leaders, not tree depth. Where a unilevel plan counts tree levels (1, 2, 3...), a generation plan counts leadership boundaries. A new generation starts each time you encounter a downline distributor holding a qualifying rank.

This rewards deep leadership development. There is no fixed depth limit. A distributor's earning depth grows as their leaders build leaders who build leaders.

Generation plans are sometimes called "gap commission" plans because the earner is paid on the "gap" (group) between themselves and the next qualified leader.

This document covers the configurable options specific to generation commission calculation.

## Structure

Same tree as unilevel. Unlimited width, unlimited depth. No separate tree implementation needed. The same tree serves unilevel, stairstep, and generation plans.

## How Generations Work

A generation is the group of distributors between two boundary-rank leaders. The commission walk counts these boundaries instead of tree levels.

**Reference example:**

```
Alice (rank 8)              <- Generation boundary
+-- Bob (rank 3)
|   +-- Carol (rank 2)
|   |   +-- Dave (rank 8)   <- Generation boundary
|   |       +-- Eve (rank 1)
|   |       +-- Frank (rank 8)  <- Generation boundary
|   +-- Grace (rank 4)
+-- Henry (rank 1)
```

From Alice's perspective (boundary rank = 8):
- **Generation 1:** Bob, Carol, Henry, Grace. Everyone between Alice and the next rank-8 leader.
- **Generation 2:** Eve. Between Dave and Frank.
- **Generation 3:** Starts below Frank.

Alice earns a configured percentage on each generation's total volume. Dave and Frank each earn their own generation commissions from their perspective.

This example is specified as the first integration test case for generation commission implementation.

## Commission Options

### Generation Rates

| Option | Type | What it controls |
|--------|------|-----------------|
| **Max generations** | integer | Maximum number of generations to pay on. |
| **Generation rates** | generation x rate map | Override percentage per generation. Gen 1 = 10%, Gen 2 = 7%, Gen 3 = 5%, Gen 4 = 3%. |
| **Volume-to-dollar multiplier** | float | CV to currency conversion. Can differ from the level commission multiplier. |

**The formula:**

```
commission = generation_group_volume x volume_to_dollar_multiplier x generation_rate[generation_number]
```

Where `generation_group_volume` is the total PV of all distributors within that generation (between two boundary leaders).

### Boundary Rank

| Option | Type | What it controls |
|--------|------|-----------------|
| **Boundary rank** | rank ref | Minimum rank that creates a generation boundary. When the walk encounters a distributor at or above this rank, one generation ends and the next begins. |

### Boundary Mode

Two modes determine what rank creates a boundary:

| Mode | How it works | Effect |
|------|-------------|--------|
| **Threshold rank** | A fixed rank creates boundaries for everyone. All earners see the same generation structure. A Gold creates a boundary whether the earner is Gold, Diamond, or Double Diamond. | Simpler. One walk can serve multiple earners. Better performance. |
| **Same rank** | The boundary rank equals the earner's own rank. A Diamond only sees other Diamonds (and above) as boundaries. A Gold sees Golds, Diamonds, and above. | Higher-ranked leaders see fewer boundaries, meaning larger generation pools and bigger payouts. Rewards advancement more aggressively. |

**Same rank mode implications:** The boundary check at each node depends on which earner you are calculating for, not just the node's rank versus a fixed threshold. This turns a single walk into a per-earner calculation. The performance cost is bounded by `max_generations`. The walk stops after that many boundaries regardless.

### Empty Generation Handling

When two boundary-rank leaders are direct parent-child with no one between them, there is an "empty generation" with zero volume.

| Option | Values | What it controls |
|--------|--------|-----------------|
| **Empty generation consumes number** | boolean (default: true) | Whether empty generations advance the generation counter. |

**Consume (default):** The empty generation takes a generation number. Deeper volumes shift to lower-paying tiers. Penalizes top-heavy structures where leaders cluster together.

**Skip (false):** Empty generations do not advance the counter. Deeper volumes stay at higher-paying tiers. Rewards deep building regardless of leader clustering.

### Combined Level and Generation Mode

Many generation plans pay BOTH level commissions and generation overrides on the same volume events.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Level commissions enabled** | boolean | Whether standard level commissions (tree depth, like unilevel) also apply. |
| **Commissionable depth** | integer | Max depth for level commissions (if enabled). Uses the standard rate table from decision 009. |
| **Rate table** | rank x level grid | Level commission rates (if enabled). Same format as unilevel. |

When both are enabled: Level commissions reward the immediate upline (levels 1-5). Generation commissions reward the leadership pipeline (generations 1-4). Both run on the same volume events but use different walk logic. They are independent calculations.

### Infinity Bonus in Generation Context

The infinity bonus (unlimited depth until hitting a same-rank blocker) is particularly natural in generation plans. Earn on unlimited generations until hitting a same-rank blocker.

This shares the "blocker" concept with the standard infinity bonus from decision 008. The difference is what defines a "step": tree depth (unilevel infinity) versus generation boundary (generation infinity). The engine uses the same walk mechanism with different step criteria.

## Shared Implementation with Stairstep

Generation counting is shared between:
1. **Standalone generation plan** (this document). Generation counting is the primary commission model.
2. **Stairstep generation bonus** (decision 012). Generation counting is applied after breakaway.

Both use the same walk, the same boundary detection, and the same per-generation percentage table. One implementation, two entry points. Stairstep provides the boundary rank via its breakaway configuration. Standalone generation provides it via the generation-specific configuration.

## Edge Cases

No legacy implementation exists for generation plans. The algorithm must be validated from industry research. Key edge cases to test:

1. **No boundaries in downline.** Everything is Generation 1. The walk finds no boundaries. Earner receives Gen 1 rate on all downline volume.
2. **Consecutive boundaries.** Two qualified leaders parent-child with no one between them. Creates an empty generation (behavior determined by the `empty_generation_consumes_number` setting).
3. **Volume generator is a boundary leader.** They do not earn on their own volume (consistent with all plan types), but they create a boundary for earners above them.
4. **Max generations reached mid-walk.** Earners above the cutoff receive nothing on deeper volume.

Property-based test requirements:
- Total commissions paid on a volume event should equal the sum of all generation percentages for all qualified earners in the walk.
- Every volume event should produce at least one earner (assuming at least one boundary-rank leader exists above the generator).
- Generation number should never exceed `max_generations` for any earner.

## What This Enables

- A complete generation plan configurable through boundary rank, boundary mode, generation rates, and optional level commissions.
- Same-rank boundary mode gives companies an aggressive advancement incentive that threshold mode cannot replicate.
- Empty generation handling is configurable because industry practice varies.
- The shared implementation with stairstep generation bonus eliminates code duplication and ensures both entry points produce identical results for identical inputs.
