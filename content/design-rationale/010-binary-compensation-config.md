# 010: Binary Compensation Configuration

## The Problem

Binary is the second most popular structure type. It works fundamentally differently from level-based plans. There is no upline walk. Each distributor accumulates volume in a left leg and a right leg. Commissions are calculated on the relationship between those two legs.

Binary plans generate excitement through spillover (excess recruits fill positions below other people) and team dynamics (balancing two legs requires coordination). They also carry higher regulatory risk because the spillover mechanic can look pyramid-like without genuine product sales.

This document covers the configurable options specific to binary commission calculation.

## Structure

Exactly two children per node: left (position 0) and right (position 1). This is a structural invariant enforced at the tree level. The tree rejects attempts to add a third child.

Most binary companies place the company itself at the root node. Distributors choose (or are assigned) a side when they join.

## Commission Options

### Pairing Bonus

The pairing bonus is the primary commission type for binary. Everything else layers on top. It pays based on the weaker leg's volume.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Percent** | float (0.0-1.0) | Percentage of payable volume. 10% pairing bonus means you earn 10% of the matched volume. |
| **Calculation mode** | `weaker_leg` or `volume_ratio` | How the payable volume is determined. |
| **Cap per period** | float | Maximum pairing bonus per distributor per period. 0 = no cap. |

**Weaker leg mode (default).** Pay on the lesser of the two legs.

```
payable_volume = min(left_volume, right_volume)
commission = payable_volume x percent
```

Example: Left = 8,000, right = 4,000, percent = 10%. Commission = 4,000 x 10% = $400.

**Volume ratio mode.** Pay on the weaker leg, scaled by the balance ratio. Penalizes imbalance more aggressively. A perfectly balanced tree earns the full rate. A lopsided tree earns less even on the weaker leg.

```
payable_volume = min(left_volume, right_volume)
ratio = min(left_volume, right_volume) / max(left_volume, right_volume)
commission = payable_volume x percent x ratio
```

Example: Left = 8,000, right = 4,000, percent = 10%. Ratio = 4,000 / 8,000 = 0.5. Commission = 4,000 x 10% x 0.5 = $200.

A 50/50 split: ratio = 1.0, full payout. A 20/80 split: ratio = 0.25, sharply reduced.

### Volume After Payout

What happens to leg volume after commissions are paid. This is the most consequential configuration choice in a binary plan.

| Mode | What happens | Effect |
|------|-------------|--------|
| **Full flush** | Both legs reset to zero. | Clean slate every period. Distributors must rebuild entirely. Most aggressive. Fastest company cash recovery. |
| **Net off** | Matched (paid) volume is subtracted from both legs. Remainder stays in both. | Both legs keep their unpaid excess. Moderate approach. Rewards balanced growth on both sides. |
| **Carry forward** | Matched volume is subtracted from the weaker leg (it goes to zero). The stronger leg keeps its excess. | The strong leg accumulates across periods. Creates momentum. The most common industry approach. Distributors build one "power leg" and let the excess carry. |

**Carry-forward cap.** When carry forward is enabled, a configurable maximum prevents infinite accumulation in the strong leg.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Carry forward cap** | float | Maximum volume that can carry in the strong leg. 0 = no cap. Without a cap, a distributor could build one massive leg and coast as trickle volume on the weak side matches against stored excess. |

### Cycle/Step Model (Legacy)

An alternative to percentage-based pairing. Volume thresholds trigger fixed-dollar payouts. This is an older approach. Most modern companies use percentage-based pairing.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Steps** | list of {threshold, amount} | Each step defines a volume threshold and a fixed dollar payout. When the weaker leg reaches the threshold, the step amount is paid. |
| **Volume after cycle** | `full_flush` or `carry_forward` | What happens to leg volume after all steps in a cycle are completed. Full flush resets both legs to zero. Carry forward subtracts the highest threshold from both legs and keeps the excess. |
| **Cap per period** | float or null | Maximum total cycle/step payout per distributor per period. Null = no cap. |
| **Carry forward cap** | float or null | Maximum volume that can carry on either leg into the next period. Null = no cap. Applied when computing next-period carry-forward. In FullFlush mode with a completed cycle, legs are already zero so the cap has no practical effect. |
| **Multi-position cap mode** | `per_position` or `aggregate` | How the cap applies when a distributor holds multiple positions. Per-position caps each position independently. Aggregate caps the combined total. |

Example: Step 1 at 1,000 volume = $50. Step 2 at 2,500 = $100. Step 3 at 5,000 = $200. Step 4 at 10,000 = $500. After step 4, the cycle completes. With `volume_after_cycle: full_flush`, both legs reset and the cycle restarts.

The percentage-based pairing and cycle/step models are two modes of the same configuration. Only one is active at a time.

### Volume Propagation

Binary volume propagation works differently from unilevel. When a descendant generates volume, every ancestor on the path to root determines which leg (left or right) the descendant occupies and accumulates to that leg.

This is not a simple "roll up to parent." Each ancestor sees the volume land in either its left subtree total or right subtree total. This is O(depth) per volume event.

Volume propagation is not configurable. It is a structural behavior of the binary tree. It is documented here because it is the foundation that makes the pairing bonus work.

## Placement Options

Binary placement is more complex than any other structure type because of spillover and per-user preferences.

| Option | Type | What it controls |
|--------|------|-----------------|
| **Default placement** | `balanced`, `left`, `right` | System-wide default. Balanced: place on the side with less volume. Left/right: always place on the specified side. |
| **Per-user preference** | boolean | Whether individual distributors can set their own placement preference, overriding the default. This is the "power leg" strategy: a distributor builds one side aggressively while the company/upline fills the other via spillover. |
| **Spillover enabled** | boolean | When a sponsor's two positions are full, new recruits spill down to the next available position on the chosen side. Spillover is the primary growth mechanic for binary and nearly every plan turns it on, but `spillover_enabled` is a required field, not an assumption. |

Spillover creates excitement because distributors see new people appearing in their downline without personally recruiting them. It also means sponsor and placement parent are almost always different people. The engine tracks both.

**Holding tank** is configurable for binary in `HoldingTankConfig.applicable_structures`, but the engine does not implement it. `get_holding_tank` and `place_from_tank` reject every non-matrix tree (`handlers/tree.rs:426-430`, `:477-481`). See [003](003-network-engine-design.md).

## Binary-Specific Gaps From Legacy

The legacy system had an empty stub for binary volume propagation and no pairing bonus implementation. The commission calculation was delegated to custom per-deployment code.

That gap is closed. `calculate_binary_pairing` (`commission/binary.rs:154`) handles both the pairing and cycle/step modes and returns `BinaryCalculationResult` with post-payout leg volumes for carry-forward. Placement preference and spillover are config the Go placement layer reads; the tree itself takes an explicit position.

## What This Enables

- A complete binary plan configurable through pairing mode, volume-after-payout mode, and carry-forward cap.
- Volume ratio mode gives companies a more aggressive balance incentive beyond the standard weaker-leg model.
- Legacy cycle/step plans are supportable without forcing all binary customers onto the percentage model.
- Per-user placement preferences support the power leg strategy that most binary companies use.
