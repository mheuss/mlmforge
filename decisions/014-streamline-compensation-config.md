# 014: Streamline Compensation Configuration

## The Problem

Streamline is architecturally different from the other five structure types. It uses linear chains called "streams" instead of trees. Each stream is a single-file line of distributors. Higher-ranked distributors earn additional streams, which is the primary growth and earning mechanic.

Streamline is the only structure type where compression was actually implemented in the legacy system (all others had it configured but never active). The compression model is "dynamic". Each level has its own rank threshold.

This document covers the configurable options specific to streamline commission calculation, including the monoline variant.

## Structure

Each stream is a linear chain. Width = 1. Each person has exactly one person above and one below. A distributor can have positions on multiple streams within the same structure. This one-to-many mapping (one user to many positions) is unique to streamline.

**Arena storage model.** Each stream is a separate arena instance with width=1 enforced. A `StreamlineStructure` wrapper manages a collection of stream arenas with a user-to-stream index. This keeps the arena model clean. No special-casing needed.

## Commission Options

### Dynamic Compression

Dynamic compression is the defining commission mechanic for streamline. Each level in the walk has its own minimum rank requirement. If the person at a position does not hold the required rank for that level, they are skipped.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Commissionable depth** | integer | Maximum levels the walk travels. Can be deep (17+ levels is common for streamline). |
| **Per-level configuration** | level x {min_rank, percent} | Each level defines two things: the minimum rank to earn at this level and the commission percentage. |

**How the walk works:**

1. Start from the volume generator.
2. Walk up the stream.
3. At each position, check: does this person hold at least the required rank for the current level?
4. If yes, they earn the level's percentage. Advance the level counter.
5. If no, skip them. The level counter does not advance.
6. Continue until max depth or top of stream.

**Critical behavior: skipping does not consume a level.** This is the same behavior as standard compression in unilevel/matrix/stairstep. The difference is that the skip criteria varies by level (each level has its own rank threshold) rather than being uniform (one status check for all levels).

**Example walk:**

| Position | Person | Rank | Level requires | Result |
|----------|--------|------|---------------|--------|
| 1 above | Alice | Silver | Level 1: Bronze | Earns at Level 1 (qualifies) |
| 2 above | Bob | Bronze | Level 2: Silver | Skipped (Bronze < Silver) |
| 3 above | Carol | Gold | Level 2: Silver | Earns at Level 2 (Bob was skipped) |
| 4 above | Dave | Bronze | Level 3: Gold | Skipped |
| 5 above | Eve | Diamond | Level 3: Gold | Earns at Level 3 (Dave was skipped) |

Carol earns at level 2, not level 3. Eve earns at level 3, not level 5. Rank controls your earning position, not just your earning percentage.

### Streams and Rank Expansion

As a distributor advances in rank, they receive additional streams. This is the primary growth incentive. More streams means more earning positions and more frontline volume sources.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Additional streams per rank** | rank x integer map | How many additional streams each rank grants. The total stream count is 1 (base) plus all additional streams up to and including the current rank. |

Example:

| Rank | Additional | Total Streams |
|------|-----------|---------------|
| Bronze | 0 | 1 |
| Silver | 1 | 2 |
| Gold | 3 | 4 |
| Platinum | 6 | 7 |
| Diamond | 20 | 21 |

Each stream operates independently. New enrollments can be directed into any of the sponsor's active streams. Each stream walks independently for commission calculation.

### Stream Freeze on Demotion

When a distributor is demoted and has more streams than their new rank allows, the excess streams are frozen.

| Behavior | What happens |
|----------|-------------|
| Existing positions remain | No tree restructuring. Nobody moves. |
| No new enrollments | Frozen streams do not accept new recruits. |
| Commission eligibility lost | The distributor does not earn on frozen streams. |
| Reversible | If the rank is regained, streams unfreeze immediately. |

No data is ever destroyed by rank fluctuation. This is the safest approach and avoids the operational danger of destructive tree modifications on every rank change.

### Stream Assignment

How new recruits are assigned to a sponsor's streams.

| Option | Values | What it controls |
|--------|--------|-----------------|
| **Assignment mode** | `sponsor_stream` or `round_robin` | Default assignment strategy. Sponsor stream: recruit joins the same stream as their sponsor. Round robin: recruits are distributed across the sponsor's streams evenly. |
| **Per-enrollment choice** | boolean | Whether the sponsor can explicitly choose which stream for a specific enrollment. The assignment mode is the fallback when no choice is made. |

Per-enrollment choice gives sponsors control over which streams to grow. A sponsor with 5 streams might want to focus new recruits into streams 2 and 3 to build momentum, while leaving stream 1 (which is already deep) alone.

## Monoline Variant

A monoline (single leg) plan is a degenerate case of streamline. Not a separate plan type. Not a separate tree type. It is a streamline configured with:

| Streamline option | Monoline setting |
|------------------|-----------------|
| Additional streams per rank | 0 for all ranks (only one stream ever) |
| Per-level min_rank | 0 or none (no rank gating at any level) |
| Per-level percent | Simple override percentages |
| Stream assignment | N/A (only one stream exists) |

This produces monoline behavior: one vertical line, first-come-first-served, override commissions on everyone below you. Everyone is in the same stream. New recruits are appended to the bottom.

No separate implementation is needed. The monoline.md discovery file redirects to the streamline file.

## Streamline-Specific Considerations

**Placement.** Append to the bottom of the assigned stream. No position choice beyond stream selection. No holding tank. No forced placement algorithm.

The legacy system supported chronological insertion by timestamp (splice into the middle of a stream). This is fragile. Updating parent pointers mid-chain creates ordering dependencies and race conditions under concurrent enrollment. Append-to-end is the primary placement model. Chronological ordering for edge cases requires proper locking at the application layer.

**Uncompressed mode.** The legacy system only supported the compressed (dynamic) commission walk for streamline. A classic (uncompressed) mode, where every person earns at their actual position's level rate regardless of rank, is lower priority but configurable:

| Option | Type | What it controls |
|--------|------|-----------------|
| **Uncompressed mode enabled** | boolean | Whether a classic (uncompressed) commission mode is available alongside or instead of dynamic compression. |

Most streamline plans use dynamic compression. Uncompressed mode is the exception.

## Implementation Priority

1. Multi-stream position tracking (the defining data model feature, resolved by separate arena per stream)
2. Dynamic compression walk (algorithm is proven from legacy, needs modernization)
3. Stream assignment logic (sponsor_stream + round_robin)
4. Rank-based stream expansion (ties rank advancement to earning positions)
5. Commission cap
6. Classic (uncompressed) mode (lower priority)

## What This Enables

- A complete streamline plan configurable through per-level rank thresholds, stream expansion per rank, assignment modes, and demotion freeze behavior.
- Monoline plans are a zero-code configuration of the same streamline engine.
- Stream freeze on demotion prevents destructive tree changes while maintaining correct commission behavior.
- Per-enrollment stream choice gives sponsors meaningful control over their earning structure.
