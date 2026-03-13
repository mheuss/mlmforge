# 012: Stairstep Compensation Configuration

## The Problem

Stairstep breakaway is the most successful compensation structure in the direct selling industry. It built Amway, Herbalife, and most of the largest MLM companies. The core mechanic: when a downline leader advances to a qualifying rank, their group "breaks away." The upline stops earning level commissions on that group's individual orders and instead earns a differential override on the group's total volume.

This creates a progression from level-based earning (early career) to override-based earning (leadership), which drives both personal selling and leader development.

This document covers the configurable options specific to stairstep commission calculation.

## Structure

Same tree as unilevel. Unlimited width, unlimited depth. No separate tree implementation needed. The differentiator is entirely in the commission walk. Level commissions work identically to unilevel (decision 009). Breakaway and differential overrides add a second layer on top.

## Commission Options

### Level Commissions (Pre-Breakaway)

Before any downline leaders break away, stairstep works exactly like unilevel. The same rate table, broad commission percent, volume-to-dollar multiplier, and commissionable depth options apply. See decision 009.

These level commissions continue to apply to the portion of the downline that has not broken away. A distributor earns level commissions on their personal group and differential overrides on their breakaway groups.

### Breakaway Configuration

| Option | Type | What it controls |
|--------|------|-----------------|
| **Threshold rank** | rank ref | The rank at which a downline leader's group breaks away. A distributor whose rank ordinal is greater than or equal to the threshold rank's ordinal is considered broken away. A senior_director breaks away when the threshold is director. |
| **Group volume excludes breakaway** | boolean (default: true) | Whether breakaway group volume is excluded from the upline's GV for rank qualification purposes. This is a rank qualification concern enforced upstream of the commission calculator. The calculator receives snapshots with final ranks already determined and does not use this flag directly. Should always be true. Without this exclusion, there is no meaningful distinction between stairstep and unilevel with override bonuses. The volume boundary at breakaway is what creates the economic incentive structure. |

**Before breakaway:** The upline earns level commissions on every order in their downline. The downline leader's volume counts toward the upline's group volume for rank qualification.

**After breakaway:** The downline leader's group volume is excluded from the upline's GV. The upline earns differential overrides on the breakaway group's total volume.

### Differential Override

The override is the difference between the sponsor's rank rate and the breakaway leader's rank rate, applied to the breakaway group's total volume. This is what makes stairstep "stairstep."

| Option | Type | What it controls |
|--------|------|-----------------|
| **Override calculation** | `differential` or `fixed_override` | How the override percentage is determined. |
| **Rank rates** | rank x rate map | Base rate per rank. Used to calculate the differential. |
| **Min override** | float (default: 0.0) | Floor for the override percentage. Standard: 0 (never negative). |

**Differential mode (standard):** Your rank rate minus their rank rate. Applied to the breakaway group's total volume.

| Scenario | Your rank rate | Their rank rate | Your override |
|----------|---------------|----------------|--------------|
| You outrank them | 15% | 10% | 5% |
| Same rank | 12% | 12% | 0% |
| They outrank you | 10% | 15% | 0% (capped at min_override) |

Example: You are rank 8 (15%). Your breakaway leader is rank 6 (10%). Their group generated 50,000 GV this period. Your override: (15% - 10%) x 50,000 = $2,500.

**Fixed override mode:** A fixed percentage per rank regardless of the breakaway leader's rank. Simpler but less common. Used when the company wants predictable override income.

### Generation Overrides (Multi-Tiered Breakaway)

Breakaway is multi-tiered. When a leader within a breakaway group also achieves the threshold rank, they create another generation boundary. Each same-or-higher rank leader creates a new generation.

From your perspective:
- **Generation 1:** Your first breakaway leader's group.
- **Generation 2:** A breakaway within Generation 1.
- **Generation 3:** A breakaway within Generation 2.

You earn a different override percentage on each generation's group volume. Rates typically decrease with depth.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Max generations** | integer | How many generations deep you can earn overrides. |
| **Generation rates** | generation x rate map | Override percentage per generation. Gen 1 = 5%, Gen 2 = 3%, Gen 3 = 2%, Gen 4 = 1%. |
| **Boundary rank** | rank ref | Minimum rank that creates a generation boundary. Typically the same as the breakaway threshold rank. |

**This is the same generation counting model used by standalone generation plans (decision 013).** One implementation serves both. Stairstep adds generation counting after breakaway. The standalone generation plan uses it as the primary commission model.

### Compression

Same shared mechanism as unilevel and matrix (decision 009). Skip unqualified nodes in the upline walk for level commissions. Does not affect differential override calculation (overrides are on group volume totals, not individual orders).

## Stairstep-Specific Considerations

**Pruning eligibility.** The legacy system only allowed pruning of non-Good-status distributors from stairstep structures. This should be configurable. Different companies have different rules about when a distributor can be removed. Some require a formal cancellation process regardless of status.

**Group volume definition.** Group volume for stairstep = sum of all personal volume from the distributor and their downline, excluding any sub-groups that have broken away. Each breakaway creates a volume boundary. The engine must recalculate these boundaries whenever a rank change creates or removes a breakaway.

## What This Enables

- A complete stairstep plan configurable through the breakaway rank threshold, differential rates, and generation overrides.
- Level commissions and differential overrides coexist. Pre-breakaway earning transitions naturally to post-breakaway earning as a distributor's organization matures.
- Generation counting is a shared implementation with the standalone generation plan type, reducing code duplication and ensuring consistent behavior.
