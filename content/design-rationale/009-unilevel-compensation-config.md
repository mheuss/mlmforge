# 009: Unilevel Compensation Configuration

## The Problem

The unilevel structure is the simplest tree shape: unlimited width, unlimited depth. Each sponsor recruits as many people as they want, all placed directly below them. The same tree serves unilevel, stairstep, and generation plans. The differentiator is always the commission walk, not the tree shape.

This document covers the configurable options specific to unilevel commission calculation.

## Structure

Unlimited width. No forced placement. No position choice. New recruits are placed directly under their sponsor.

Regulators prefer unilevel because its simplicity makes commission flows transparent. The downside is weak incentive for depth building. Most distributors recruit wide (many direct recruits) rather than deep (helping recruits build their own teams).

## Commission Options

### Level Commission Rate Table

The rate table is a grid of rank and level. Each cell holds the percentage a distributor of that rank earns at that level distance from the purchase.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Commissionable depth** | integer | Maximum levels the commission walk travels up from a purchase. A depth of 7 means up to 7 people in the upline can earn. Higher ranks may unlock deeper levels through active leg tiers (see decision 008). |
| **Rate table** | rank x level grid | The percentage each rank earns at each level. Indexed as `[rank][level]`. A Bronze at level 1 might earn 5%. A Diamond at level 5 might earn 2%. Ranks without entries earn nothing. Levels without entries earn nothing. |
| **Broad commission percent** | float (0.0-1.0) | A base multiplier applied before the rank/level percentage. Scales the entire rate table uniformly. Most plans set this to 1.0 (100%, no effect). Useful for scaling all commissions without editing every cell. |
| **Volume-to-dollar multiplier** | float (> 0) | Converts CV points to currency for this structure. Overrides the plan-level default when a structure needs a different conversion factor. |

**The formula:**

```
commission = CV x broad_commission_percent x volume_to_dollar_multiplier x rate_table[rank][level]
```

The volume generator does not earn commission on their own volume. The upline walk starts at the first parent.

**Example rate table:**

| | Level 1 | Level 2 | Level 3 | Level 4 | Level 5 |
|---|---------|---------|---------|---------|---------|
| Bronze | 5% | 2% | -- | -- | -- |
| Silver | 7% | 4% | 2% | -- | -- |
| Gold | 8% | 5% | 3% | 2% | -- |
| Diamond | 10% | 6% | 4% | 3% | 2% |

A Gold distributor 3 levels above a purchase earns 3%. A Bronze distributor earns on levels 1-2 only.

### Compression

Compression changes who earns when someone in the upline does not qualify. Without compression, an unqualified person at level 3 still occupies that level. The person at level 4 earns the level 4 rate. With compression, the unqualified person is skipped and the next qualified person earns the level 3 rate instead.

| Option | Values | What it controls |
|--------|--------|-----------------|
| **Enabled** | boolean | Whether compression is active on this structure. |
| **Mode** | `skip_inactive` or `skip_below_rank` | What makes someone "unqualified." Skip inactive: skip anyone not in the eligible statuses set. Skip below rank: skip anyone below a specified rank threshold. |
| **Skip below rank** | rank ref | The rank threshold for skip_below_rank mode. Anyone below this rank is skipped. |

**Critical behavior: skipping does not consume a level.** If levels 3, 4, and 5 are all unqualified, the next qualified person earns at level 3's rate, not level 6. Compression preserves the full commissionable depth.

Without compression, the company keeps the money that would have gone to unqualified people. With compression, that money flows to qualified people above. Compression pays out more total commissions.

### Australian X-Up (Pass-Up) Variant

A configuration option on unilevel where a new distributor's first X recruits are "passed up" to their sponsor. The distributor earns nothing on those recruits. After completing the pass-up requirement, the distributor keeps all future recruits.

There is no `enabled` flag. Presence of the `pass_up` block turns it on; omit
the block to turn it off.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Count** | integer (1-255) | Number of recruits passed to sponsor. A "2-up" plan passes the first 2 recruits. |
| **Includes commissions** | boolean | Whether commissions from passed-up recruits also go to the sponsor. When false, only the recruits themselves are passed up (they appear in the sponsor's downline), but commissions still flow normally. |

This creates a mentorship investment: the sponsor benefits from the new distributor's early recruiting, which incentivizes the sponsor to help them get started. After the pass-up, everything the distributor builds is theirs.

Only applicable to unilevel. `pass_up` is a field on `UnilevelCommission` and has no equivalent on any other structure type.

### Donated Placement

A sponsor can place their recruit under a different person in their own downline instead of directly under themselves. This splits two relationships:

- **Sponsor** (who recruited). Drives matching bonuses, introducer bonuses, fast start bonuses.
- **Placement parent** (where they sit in the tree). Drives level-based commission walks.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Enabled** | boolean | Whether donated placement is available. |
| **Restriction** | `own_downline` or `direct_children_only` | Where the recruit can be placed. Own downline: anywhere below the sponsor. Direct children only: only directly under one of the sponsor's existing recruits. |

Use cases: Strengthen a weak leg. Reward a performing downline leader with a new recruit placed in their branch. Companies need to know the actual recruiter regardless of placement, for compliance and disputes.

The sponsor relationship is always tracked separately from tree position. The engine maintains two traversal paths: tree walk for level commissions, sponsor walk for sponsor bonuses.

## Unilevel-Specific Bonuses

All shared bonuses from decision 008 apply. No additional structure-specific bonuses are unique to unilevel.

The matching bonus, infinity bonus, and fast start bonus are particularly common in unilevel plans. Leadership development bonus rewards mentorship in the wide-but-shallow structure that unilevel tends to produce.

## What This Enables

- A complete unilevel plan can be configured with the rate table, compression choice, and optional pass-up/donated placement settings.
- The same tree implementation serves unilevel, stairstep, and generation plans. Only the commission walk changes.
- Compression is a shared walk behavior. The same implementation applies to matrix and stairstep with identical skip-without-consuming behavior.
