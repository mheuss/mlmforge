# 011: Matrix Compensation Configuration

## The Problem

Matrix plans use a fixed-width tree with a fixed commission depth. A "3x9" matrix is 3 wide and pays 9 levels down. Positions fill algorithmically. The company controls the shape. This creates spillover excitement (new recruits appear below you without your effort) and structural guarantees (everyone has the same number of slots).

Matrix also has a cycling variant called the board plan, where small matrices split when full.

This document covers the configurable options specific to matrix commission calculation, including the board plan variant.

## Structure

| Option | Type | What it controls |
|--------|------|-----------------|
| **Width** | integer (>= 2) | Maximum children per node. A 3-wide matrix means each person has exactly 3 slots below them. |
| **Height** | integer (>= 1) | Maximum commissionable depth. Caps how many levels down the commission walk pays. Does not bound tree growth. |

A 3x9 matrix has 29,524 theoretical positions in one distributor's commissionable window — the root plus nine levels. A 5x10 has around 12 million. These counts size the commission walk, not the tree. The admin UI should warn when configured dimensions create an unreasonable number of positions (threshold: 1,000,000).

Width is a structural invariant enforced at the tree level. The tree rejects children beyond the width.

Height is not a tree bound. The matrix tree grows to any depth. Every distributor has their own height-deep commission window into one shared genealogy, so a tree that stopped at the configured height would prevent anyone below that level from enrolling. A 3x9 matrix company could not place distributor 29,525.

The board plan is different. A board is a genuinely bounded container, which is why it cycles when it fills. That is a separate structure type with its own storage. See the board plan section below.

## Commission Options

### Level Commission Rate Table

Matrix uses the same level-based commission walk as unilevel. The rate table is a grid of rank and level. The formula is identical:

```
commission = CV x broad_commission_percent x volume_to_dollar_multiplier x rate_table[rank][level]
```

All options from decision 009 (rate table, broad commission percent, volume-to-dollar multiplier, commissionable depth) apply. The effective commission depth is `min(commissionable_depth, height)`. Whichever is smaller wins. Neither is rejected for exceeding the other.

### Compression

Same as unilevel compression (decision 009). Standard compression skips unqualified distributors in the upline walk. The next qualified person earns at the skipped level's rate. Skipping does not consume a level.

This is the same shared compression mechanism used across all level-based structure types.

## Placement Options

Forced placement is the defining mechanic for matrix. The system decides where recruits go.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Spillover direction** | `breadth_first` | How the matrix fills. Fill each level completely before the next. Balanced growth. Industry standard. |

`depth_first` is not available. The schema enum accepts only `breadth_first`, and `MatrixTree::new` rejects `SpilloverDirection::DepthFirst` with `UNSUPPORTED_SPILLOVER` (`tree/matrix.rs:57-59`). The variant exists in the Rust enum as a placeholder for a plan type nobody has asked for yet.

Forced placement means sponsor and placement parent are almost always different people. The engine tracks both.

**Holding tank** is available for matrix structures.

## Pruning

When a node is removed from the matrix and has children, two behaviors are available:

| Mode | What happens | When to use |
|------|-------------|-------------|
| **Promote earliest** | The earliest child (by enrollment date) inherits the removed node's position. Remaining children are repositioned under the inheritor. No admin action needed. | Default behavior. Works well when removals are rare and the order of inheritance is not contentious. |
| **Holding tank** | All children are moved to the holding tank. Admin decides where to re-place them. | When the company wants full control over post-removal placement. Safest option but requires admin action. |

Both modes are intended to emit domain events for audit, and volume redistribution from repositioning should flag the affected period's commission calculation. Neither is wired: there is no event bus, and no production code appends a tree event. See [000](000-architecture-overview.md).

## Matrix-Specific Bonuses

### Matrix Completion Bonus

**Not implemented.** The config block parses and nothing reads it. See the
banner in [008](008-common-compensation-config.md), which covers every bonus
type.

Earned when a matrix level is fully filled or the entire structure is complete. This is a defining feature for matrix plans.

Detection requires the tree to track fill state: positions filled at each level versus the theoretical maximum of `width ^ level`.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Per-level amounts** | level x amount | Bonus when a specific level is fully filled. "Level 1 full: $50. Level 2 full: $200. Level 3 full: $500." |
| **Full matrix bonus** | float | Bonus when the entire matrix structure is complete. 0 = no full-matrix bonus. |

Per-level completion is more practical than full-matrix completion for large matrices. A 3x9 matrix has 29,524 positions. Full completion is unrealistic. Per-level triggers are the real incentive driver.

### Position Bonus

**Not implemented**, same as the completion bonus above.

Earned when a personally sponsored recruit is placed in the matrix.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Amount** | float | Per-placement bonus. |
| **Type** | `fixed` or `percentage` | Fixed dollar amount or percentage of the new recruit's signup product. |
| **Sponsored only** | boolean (default: true) | Whether only personally sponsored placements qualify. When true, spillover placements do not generate a position bonus. |

The sponsored-only distinction is critical. In forced matrix, most positions are filled by spillover, not personal sponsorship. Without this flag, everyone earns position bonuses on spillover placements they had nothing to do with. This is also why sponsor vs. placement parent tracking is mandatory.

## Board Plan (Revolving Matrix) Variant

A board plan is a cycling matrix variant implemented as its own structure type (`board_plan`). It uses lightweight flat arrays instead of the shared matrix tree. Boards are configured with a small size (commonly 2x2 or 3x3). When a board fills completely, it cycles.

### How Cycling Works

1. The top position "cycles out" and earns a cycle commission.
2. The board splits into two new boards, each headed by one of the original second-level members.
3. The cycled-out member re-enters at the bottom of a new board (re-entry).

### Board Plan Options

| Option | Type | What it controls |
|--------|------|-----------------|
| **Cycle commission** | float | Amount earned when cycling out of a board. |
| **Re-entry enabled** | boolean | Whether cycled-out members automatically re-enter a new board. |
| **Re-entry position** | `bottom` or `sponsor_board` | Where re-entered members are placed. Bottom: lowest available position in any board. Sponsor board: placed in the same board as their sponsor. |
| **Max cycles per period** | integer (>= 1) | Cap on how many times a member can cycle per period. Prevents runaway earnings on fast-filling boards. |
| **Stall threshold periods** | integer (>= 1) | Periods a board can go without filling before it is flagged as stalled. |
| **Inactive compression** | boolean | Whether inactive members are compressed out when a board cycles. |
| **Max cascade depth** | integer (>= 1) | How far a single cycle may cascade into further cycles. Optional. |

There is no separate "cycling enabled" switch. A structure is a board plan by
being typed `board_plan`; presence of the `board_cycling` block configures it.

### Board Plan Status

**Implemented in HEU-30.** Configuration schema fully populated. Engine handles board lifecycle, cycling, re-entry, stall detection, and inactive compression.

Board plan dimensions are capped at width 2-5 and height 1-4. A 5x4 board has 781 positions, which is already impractically large for cycling.

Board plan structures require a companion unilevel structure for sponsor-based commissions.

Board cycling adds board splitting, re-entry tracking, and cycle event recording. Board plan companies face higher regulatory scrutiny because the cycling mechanic can resemble a pyramid scheme when not paired with genuine product sales requirements. Every cycle event must be recorded as a domain event for audit.

## What This Enables

- Standard matrix plans are fully configurable through width, height, rate table, compression, and placement direction.
- Board plan cycling handles board lifecycle, splitting, re-entry, stall detection, and inactive compression.
