//! Binary pairing commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::binary::{
    BinaryCommissionMode, CycleStepConfig, MultiPositionCapMode, PairingCalculation,
    VolumeAfterPayout,
};
use crate::config::eligibility::CommissionEligibility;
use crate::config::{BinaryStructureConfig, CompensationPlan};
use crate::decimal::{AuthoredDec, Dec, NumericError};
use crate::tree::binary::BinaryTree;
use crate::tree::node::NodeIndex;

use super::is_eligible;
use super::types::{
    BinaryCalculationResult, BinaryCommissionEarning, BinaryLeg, CalculationError,
    DistributorSnapshot, LegVolumes, VolumeSource,
};
use super::{finalize, walk};

/// Resolve a position_id to its owner_id.
///
/// When ownership is None (single-position mode) or the position isn't
/// in the map, the position is its own owner.
fn resolve_owner(position_id: &Uuid, ownership: Option<&HashMap<Uuid, Uuid>>) -> Uuid {
    ownership
        .and_then(|map| map.get(position_id))
        .copied()
        .unwrap_or(*position_id)
}

/// Phase 1, Step 1: Aggregate volume sources into per-position totals.
///
/// Totals are keyed by position_id (source_id). Snapshot validation
/// resolves through the ownership map when provided, so snapshots are
/// keyed by owner_id while volume is keyed by position_id.
fn aggregate_volume(
    tree: &BinaryTree,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    ownership: Option<&HashMap<Uuid, Uuid>>,
) -> Result<HashMap<Uuid, Dec>, CalculationError> {
    let mut totals: HashMap<Uuid, Dec> = HashMap::new();

    for source in volume {
        if source.cv_amount.is_negative() {
            return Err(CalculationError::InvalidCvAmount(
                source.source_id,
                source.cv_amount,
            ));
        }

        // Validate source exists in tree.
        if !tree.contains(source.source_id) {
            return Err(CalculationError::SourceNotInTree(source.source_id));
        }

        // Validate source exists in snapshots (keyed by owner_id).
        let owner = resolve_owner(&source.source_id, ownership);
        if !snapshots.contains_key(&owner) {
            return Err(CalculationError::SourceNotInSnapshot(owner));
        }

        let total = totals.entry(source.source_id).or_insert(Dec::ZERO);
        *total = total.try_add(source.cv_amount)?;
    }

    Ok(totals)
}

/// Evaluate eligibility for all distributors in the binary tree.
///
/// Returns a simple bool per distributor. Active leg tiers are a unilevel
/// concept (counting frontline children) and are intentionally not evaluated
/// for binary trees, where volume flows through exactly two legs.
fn evaluate_eligibility(
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    eligibility: &CommissionEligibility,
) -> HashMap<Uuid, bool> {
    snapshots
        .iter()
        .map(|(id, snap)| (*id, is_eligible(snap, eligibility)))
        .collect()
}

/// Phase 1, Step 3: Bottom-up volume accumulation.
///
/// Single O(N) post-order traversal. Returns per-distributor working
/// leg volumes (current period + carry-forward).
fn accumulate_leg_volumes(
    tree: &BinaryTree,
    volume_totals: &HashMap<Uuid, Dec>,
    carry_forward: &HashMap<Uuid, LegVolumes>,
) -> Result<HashMap<Uuid, LegVolumes>, NumericError> {
    let arena = tree.arena();
    let slots = tree.slots();

    // Build depth-sorted list of all live nodes (deepest first).
    let mut nodes_by_depth: Vec<(NodeIndex, Uuid, u32)> = arena
        .index
        .iter()
        .map(|(uid, &idx)| (idx, *uid, arena.node(idx).depth))
        .collect();
    nodes_by_depth.sort_by_key(|n| (std::cmp::Reverse(n.2), n.1));

    // Subtree totals: personal volume + children's subtree totals.
    let mut subtree_totals: HashMap<NodeIndex, Dec> = HashMap::new();

    for &(idx, uid, _depth) in &nodes_by_depth {
        let personal = volume_totals.get(&uid).copied().unwrap_or(Dec::ZERO);

        let children = &arena.node(idx).children;
        let child_sum = Dec::sum(
            children
                .iter()
                .map(|c| subtree_totals.get(c).copied().unwrap_or(Dec::ZERO)),
        )?;

        subtree_totals.insert(idx, personal.try_add(child_sum)?);
    }

    // Compute per-distributor leg volumes from slot positions.
    let mut leg_volumes: HashMap<Uuid, LegVolumes> = HashMap::new();

    for &(idx, uid, _depth) in &nodes_by_depth {
        let node_slots = slots.get(&idx).copied().unwrap_or([None, None]);

        let subtree_of = |slot: Option<NodeIndex>| {
            slot.and_then(|c| subtree_totals.get(&c).copied())
                .unwrap_or(Dec::ZERO)
        };
        let left_subtree = subtree_of(node_slots[0]);
        let right_subtree = subtree_of(node_slots[1]);

        let prior = carry_forward.get(&uid);
        let left = left_subtree.try_add(prior.map(|p| p.left).unwrap_or(Dec::ZERO))?;
        let right = right_subtree.try_add(prior.map(|p| p.right).unwrap_or(Dec::ZERO))?;

        leg_volumes.insert(uid, LegVolumes { left, right });
    }

    Ok(leg_volumes)
}

/// Refuse a negative carried leg volume on any supplied position.
fn check_carried_legs(carry_forward: &HashMap<Uuid, LegVolumes>) -> Result<(), CalculationError> {
    let mut positions: Vec<_> = carry_forward.keys().copied().collect();
    positions.sort();
    for position in positions {
        let prior = &carry_forward[&position];
        for (leg, volume) in [
            (BinaryLeg::Left, prior.left),
            (BinaryLeg::Right, prior.right),
        ] {
            if volume.is_negative() {
                return Err(CalculationError::NegativeCarriedLegVolume {
                    position,
                    leg,
                    volume,
                });
            }
        }
    }
    Ok(())
}

/// The positions in `legs`, in ascending id order.
fn sorted_positions(legs: &HashMap<Uuid, LegVolumes>) -> Vec<Uuid> {
    let mut positions: Vec<Uuid> = legs.keys().copied().collect();
    positions.sort_unstable();
    positions
}

/// Sorts `earnings` by `(earner_id, position_id)` and settles each owner's
/// earnings against `cap` at `minor_units` decimal places.
fn apply_aggregate_cap(
    earnings: &mut [BinaryCommissionEarning],
    cap: Dec,
    minor_units: u32,
) -> Result<(), NumericError> {
    earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.position_id.cmp(&b.position_id))
    });
    for group in earnings.chunk_by_mut(|a, b| a.earner_id == b.earner_id) {
        let raw: Vec<Dec> = group.iter().map(|e| e.dollar_amount).collect();
        if !Dec::sum_exceeds(&raw, cap) {
            let rounded: Vec<Dec> = raw.iter().map(|a| a.round_minor(minor_units)).collect();
            if !Dec::sum_exceeds(&rounded, cap) {
                for (e, amount) in group.iter_mut().zip(rounded) {
                    e.dollar_amount = amount;
                }
                continue;
            }
        }
        let shares = Dec::allocate_largest_remainder(&raw, cap, minor_units)?;
        for (e, amount) in group.iter_mut().zip(shares) {
            e.dollar_amount = amount;
            e.capped = true;
        }
    }
    Ok(())
}

/// Rounds every earning to `minor_units` decimal places, or settles each
/// owner's earnings against `aggregate_cap` when there is one.
fn finalize_earnings(
    earnings: &mut [BinaryCommissionEarning],
    aggregate_cap: Option<Dec>,
    minor_units: u32,
) -> Result<(), NumericError> {
    match aggregate_cap {
        Some(cap) => apply_aggregate_cap(earnings, cap, minor_units),
        None => {
            for e in earnings.iter_mut() {
                e.dollar_amount = e.dollar_amount.round_minor(minor_units);
            }
            Ok(())
        }
    }
}

/// Refuse a cap that is negative or not a whole number of the plan currency's
/// minor units.
fn check_cap(
    cap: Option<AuthoredDec>,
    plan: &CompensationPlan,
    minor_units: u32,
) -> Result<(), CalculationError> {
    match cap {
        Some(cap) if cap.is_negative() => Err(CalculationError::NegativeCap(cap)),
        Some(cap) if cap.dec().round_minor(minor_units) != cap.dec() => {
            Err(CalculationError::CapNotWholeMinorUnits {
                cap,
                currency: plan.volume.base_currency.clone(),
            })
        }
        _ => Ok(()),
    }
}

/// Calculate binary pairing commissions for a commission period.
///
/// Pure function. Takes a binary tree, config, distributor snapshots,
/// volume events, and prior carry-forward state. Returns earnings and
/// updated carry-forward state.
///
/// # Errors
///
/// Returns `CalculationError` if the cycle-step config is invalid, if the
/// pairing percent is outside [0, 1], if a snapshot names a rank the plan
/// does not define, if a snapshot's personal_volume is negative, if the owner
/// of any position in the tree has no snapshot, if a volume source is not
/// found in the tree, if a volume source has an invalid cv_amount, if a
/// carried leg volume is negative, if the plan's base currency is not in the
/// minor-unit table, if the cap is negative or not a whole number of the base
/// currency's minor units, or if an exact result cannot be held.
pub fn calculate_binary_pairing(
    tree: &BinaryTree,
    plan: &CompensationPlan,
    structure: &BinaryStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    carry_forward: &HashMap<Uuid, LegVolumes>,
    ownership: Option<&HashMap<Uuid, Uuid>>,
) -> Result<BinaryCalculationResult, CalculationError> {
    let pairing = match &structure.binary_commission.mode {
        BinaryCommissionMode::Pairing(config) => {
            if config.percent.is_negative() || config.percent.dec() > Dec::ONE {
                return Err(CalculationError::PairingPercentOutOfRange(config.percent));
            }
            config
        }
        BinaryCommissionMode::CycleStep(config) => {
            return calculate_binary_cycle_step(
                tree,
                plan,
                structure,
                config,
                snapshots,
                volume,
                carry_forward,
                ownership,
            );
        }
    };

    // Below the match on purpose. The Pairing arm's percent check and the
    // CycleStep arm's config validation each stay first on their own path,
    // and the CycleStep arm has already returned by here.
    let minor_units = finalize::plan_minor_units(plan)?;
    check_cap(pairing.cap_per_period, plan, minor_units)?;
    check_carried_legs(carry_forward)?;
    walk::validate_snapshots(
        plan,
        snapshots,
        tree.user_ids()
            .into_iter()
            .map(|position| resolve_owner(&position, ownership)),
    )?;

    let multiplier = structure
        .binary_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    // Phase 1: Prep
    let volume_totals = aggregate_volume(tree, snapshots, volume, ownership)?;
    let eligibility_cache = evaluate_eligibility(snapshots, &plan.eligibility);
    let working_legs = accumulate_leg_volumes(tree, &volume_totals, carry_forward)?;
    // Visit positions in id order so a failed calculation names the same operands every run.
    let positions = sorted_positions(&working_legs);

    // Aggregate mode skips the per-position cap, because capping first would
    // change each position's share of the aggregate cap.
    let use_aggregate_cap = matches!(
        pairing.multi_position_cap_mode,
        MultiPositionCapMode::Aggregate
    ) && ownership.is_some();

    // Phase 2: Calculate
    let mut earnings = Vec::new();

    for uid in &positions {
        let legs = &working_legs[uid];
        let owner = resolve_owner(uid, ownership);
        let eligible = eligibility_cache.get(&owner).copied().unwrap_or(false);
        if !eligible {
            continue;
        }

        let matched = legs.left.min(legs.right);
        if matched.is_zero() {
            continue;
        }

        let (min_leg, max_leg) = if legs.left <= legs.right {
            (legs.left, legs.right)
        } else {
            (legs.right, legs.left)
        };

        let ratio = match pairing.calculation {
            PairingCalculation::WeakerLeg => Dec::ONE,
            PairingCalculation::VolumeRatio => {
                if max_leg > Dec::ZERO {
                    min_leg.div_ratio(max_leg)?
                } else {
                    Dec::ZERO
                }
            }
        };

        let raw_amount = match pairing.calculation {
            PairingCalculation::WeakerLeg => {
                matched.try_mul(pairing.percent)?.try_mul(multiplier)?
            }
            PairingCalculation::VolumeRatio if max_leg > Dec::ZERO => {
                matched.mul_div_ratio(pairing.percent, multiplier, min_leg, max_leg)?
            }
            PairingCalculation::VolumeRatio => Dec::ZERO,
        };

        let (dollar_amount, capped) = if use_aggregate_cap {
            (raw_amount, false)
        } else {
            match pairing.cap_per_period.map(AuthoredDec::dec) {
                Some(cap) if raw_amount > cap => (cap, true),
                _ => (raw_amount, false),
            }
        };

        earnings.push(BinaryCommissionEarning {
            earner_id: owner,
            position_id: *uid,
            left_volume: legs.left,
            right_volume: legs.right,
            matched_volume: matched,
            ratio,
            percent: pairing.percent.dec(),
            dollar_amount,
            capped,
        });
    }

    // Phase 2b: finalize.
    let aggregate_cap = pairing
        .cap_per_period
        .filter(|_| use_aggregate_cap)
        .map(AuthoredDec::dec);
    finalize_earnings(&mut earnings, aggregate_cap, minor_units)?;

    // Phase 3: Post-payout carry-forward
    let mut new_carry_forward = HashMap::new();

    for uid in &positions {
        let legs = &working_legs[uid];
        let matched = legs.left.min(legs.right);
        let owner = resolve_owner(uid, ownership);
        let eligible = eligibility_cache.get(&owner).copied().unwrap_or(false);

        // Non-eligible distributors: nothing was matched/paid, so
        // matched is effectively 0 for carry-forward purposes.
        let effective_matched = if eligible { matched } else { Dec::ZERO };

        let (new_left, new_right) = match pairing.volume_after_payout {
            VolumeAfterPayout::FullFlush => (Dec::ZERO, Dec::ZERO),
            VolumeAfterPayout::NetOff => (
                legs.left.try_sub(effective_matched)?,
                legs.right.try_sub(effective_matched)?,
            ),
            VolumeAfterPayout::CarryForward => {
                if legs.left <= legs.right {
                    // Left is weaker
                    (Dec::ZERO, legs.right.try_sub(effective_matched)?)
                } else {
                    // Right is weaker
                    (legs.left.try_sub(effective_matched)?, Dec::ZERO)
                }
            }
        };

        // Apply carry_forward_cap if set.
        let (capped_left, capped_right) = match pairing.carry_forward_cap.map(AuthoredDec::dec) {
            Some(cap) => (new_left.min(cap), new_right.min(cap)),
            None => (new_left, new_right),
        };

        new_carry_forward.insert(
            *uid,
            LegVolumes {
                left: capped_left.max(Dec::ZERO),
                right: capped_right.max(Dec::ZERO),
            },
        );
    }

    // Sort earnings by earner_id then position_id for deterministic output.
    // HashMap iteration order is unstable, so without sorting the
    // earnings order varies across runs.
    earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.position_id.cmp(&b.position_id))
    });

    Ok(BinaryCalculationResult {
        earnings,
        carry_forward: new_carry_forward,
    })
}

/// Calculate binary cycle/step commissions for a commission period.
///
/// Pays fixed dollar amounts when both legs reach volume thresholds.
/// Steps are evaluated in ascending order. All qualifying steps pay
/// (not just the highest). When the highest step is reached, the cycle
/// completes and volume is handled according to `volume_after_cycle`.
///
/// Shares the prep phase (volume aggregation, eligibility, leg
/// accumulation) with the pairing calculator.
#[allow(clippy::too_many_arguments)]
fn calculate_binary_cycle_step(
    tree: &BinaryTree,
    plan: &CompensationPlan,
    _structure: &BinaryStructureConfig,
    config: &CycleStepConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    carry_forward: &HashMap<Uuid, LegVolumes>,
    ownership: Option<&HashMap<Uuid, Uuid>>,
) -> Result<BinaryCalculationResult, CalculationError> {
    // Validate config (catches duplicate thresholds, sorts steps).
    let mut validated_config = config.clone();
    validated_config
        .validate()
        .map_err(CalculationError::ConfigError)?;
    let minor_units = finalize::plan_minor_units(plan)?;
    check_cap(validated_config.cap_per_period, plan, minor_units)?;

    check_carried_legs(carry_forward)?;
    walk::validate_snapshots(
        plan,
        snapshots,
        tree.user_ids()
            .into_iter()
            .map(|position| resolve_owner(&position, ownership)),
    )?;

    // Phase 1: Prep (shared with pairing calculator)
    let volume_totals = aggregate_volume(tree, snapshots, volume, ownership)?;
    let eligibility_cache = evaluate_eligibility(snapshots, &plan.eligibility);
    let working_legs = accumulate_leg_volumes(tree, &volume_totals, carry_forward)?;
    // Visit positions in id order so a failed calculation names the same operands every run.
    let positions = sorted_positions(&working_legs);

    // Steps are sorted by validate() above. Use the validated copy.
    let steps = &validated_config.steps;

    // Aggregate mode skips the per-position cap, because capping first would
    // change each position's share of the aggregate cap.
    let use_aggregate_cap = matches!(
        validated_config.multi_position_cap_mode,
        MultiPositionCapMode::Aggregate
    ) && ownership.is_some();

    // Phase 2: Calculate
    // Track post-cycle leg volumes for carry-forward computation.
    let mut post_cycle_legs: HashMap<Uuid, (Dec, Dec)> = HashMap::new();
    let mut earnings = Vec::new();

    for uid in &positions {
        let legs = &working_legs[uid];
        let owner = resolve_owner(uid, ownership);
        let eligible = eligibility_cache.get(&owner).copied().unwrap_or(false);

        if !eligible {
            // Ineligible: preserve original leg volumes for carry-forward.
            post_cycle_legs.insert(*uid, (legs.left, legs.right));
            continue;
        }

        let original_left = legs.left;
        let original_right = legs.right;
        let mut left = legs.left;
        let mut right = legs.right;
        let mut total_earnings = Dec::ZERO;

        // Compute full cycle payout (all steps qualify) and highest threshold.
        let sum_of_amounts = Dec::sum(steps.iter().map(|s| s.amount.dec()))?;
        let highest = steps
            .last()
            .expect("validate() guarantees non-empty steps")
            .threshold
            .dec();

        // Closed-form full-cycle calculation for CarryForward mode.
        // Avoids O(cycles × steps) loop for large volumes.
        let matched = left.min(right);
        if matched >= highest {
            match validated_config.volume_after_cycle {
                VolumeAfterPayout::FullFlush => {
                    // At most one complete cycle. Pay all steps, zero legs.
                    total_earnings = total_earnings.try_add(sum_of_amounts)?;
                    left = Dec::ZERO;
                    right = Dec::ZERO;
                }
                VolumeAfterPayout::CarryForward => {
                    // Compute how many full cycles fit in O(1).
                    let full_cycles = matched.floor_div(highest)?;
                    total_earnings =
                        total_earnings.try_add(full_cycles.try_mul(sum_of_amounts)?)?;
                    let consumed = full_cycles.try_mul(highest)?;
                    left = left.try_sub(consumed)?;
                    right = right.try_sub(consumed)?;
                }
                VolumeAfterPayout::NetOff => {
                    unreachable!("net_off is validated out for cycle step")
                }
            }
        }

        // Handle one remaining partial cycle (if any volume remains).
        let remaining_matched = left.min(right);
        if remaining_matched >= steps[0].threshold.dec() {
            for step in steps.iter() {
                if remaining_matched >= step.threshold.dec() {
                    total_earnings = total_earnings.try_add(step.amount)?;
                }
            }
            // Partial cycle: no volume subtraction.
        }

        // Store post-cycle leg volumes for carry-forward.
        post_cycle_legs.insert(*uid, (left, right));

        // No earnings to emit if nothing was earned.
        if total_earnings.is_zero() {
            continue;
        }

        let (dollar_amount, capped) = if use_aggregate_cap {
            (total_earnings, false)
        } else {
            match validated_config.cap_per_period.map(AuthoredDec::dec) {
                Some(cap) if total_earnings > cap => (cap, true),
                _ => (total_earnings, false),
            }
        };

        earnings.push(BinaryCommissionEarning {
            earner_id: owner,
            position_id: *uid,
            left_volume: original_left,
            right_volume: original_right,
            matched_volume: original_left.min(original_right),
            ratio: Dec::ZERO,
            percent: Dec::ZERO,
            dollar_amount,
            capped,
        });
    }

    // Phase 2b: finalize.
    let aggregate_cap = validated_config
        .cap_per_period
        .filter(|_| use_aggregate_cap)
        .map(AuthoredDec::dec);
    finalize_earnings(&mut earnings, aggregate_cap, minor_units)?;

    // Phase 3: Post-payout carry-forward
    let mut new_carry_forward = HashMap::new();

    for uid in working_legs.keys() {
        let (new_left, new_right) = post_cycle_legs
            .get(uid)
            .copied()
            .unwrap_or((Dec::ZERO, Dec::ZERO));

        // Apply carry_forward_cap if set.
        let (capped_left, capped_right) =
            match validated_config.carry_forward_cap.map(AuthoredDec::dec) {
                Some(cap) => (new_left.min(cap), new_right.min(cap)),
                None => (new_left, new_right),
            };

        new_carry_forward.insert(
            *uid,
            LegVolumes {
                left: capped_left.max(Dec::ZERO),
                right: capped_right.max(Dec::ZERO),
            },
        );
    }

    // Sort earnings by earner_id then position_id for deterministic output.
    earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.position_id.cmp(&b.position_id))
    });

    Ok(BinaryCalculationResult {
        earnings,
        carry_forward: new_carry_forward,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::{
        build_test_plan, default_eligibility, eligible_snapshot,
    };
    use crate::config::binary::{
        BinaryCommissionConfig, BinaryCommissionMode, CycleStep, CycleStepConfig,
        MultiPositionCapMode, PairingCalculation, PairingConfig, VolumeAfterPayout,
    };
    use crate::config::{BinaryStructureConfig, CompensationPlan, StructureConfig};
    use crate::test_support::like;
    use crate::tree::binary::BinaryTree;
    use crate::tree::test_helpers::test_uuid;
    use crate::{assert_d, d};

    fn test_pairing_config() -> PairingConfig {
        PairingConfig {
            percent: d!(0.10),
            calculation: PairingCalculation::WeakerLeg,
            cap_per_period: None,
            volume_after_payout: VolumeAfterPayout::FullFlush,
            carry_forward_cap: None,
            multi_position_cap_mode: MultiPositionCapMode::PerPosition,
        }
    }

    fn test_binary_structure() -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(test_pairing_config()),
            },
        }
    }

    fn test_plan(eligibility: CommissionEligibility) -> CompensationPlan {
        test_plan_with_structure(eligibility, test_binary_structure())
    }

    fn test_plan_with_structure(
        eligibility: CommissionEligibility,
        structure: BinaryStructureConfig,
    ) -> CompensationPlan {
        build_test_plan(
            eligibility,
            StructureConfig::Binary(structure),
            "Test Binary",
        )
    }

    /// Helper: build a 3-node binary tree (root with left and right children).
    fn three_node_tree() -> BinaryTree {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree
    }

    // --- Basic pairing tests ---

    #[test]
    fn balanced_legs_weaker_leg_full_flush() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left child generates 500 CV, right child generates 500 CV.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Root should earn: matched=500, ratio=1.0, 500 * 0.10 * 1.0 * 1.0 = 50.0
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.earner_id, test_uuid(1));
        assert_d!(earning.left_volume, 500.0);
        assert_d!(earning.right_volume, 500.0);
        assert_d!(earning.matched_volume, 500.0);
        assert_d!(earning.ratio, 1.0);
        assert_d!(earning.percent, 0.10);
        assert_d!(earning.dollar_amount, 50.0);
        assert!(!earning.capped);

        // FullFlush: all carry-forward should be zero.
        for legs in result.carry_forward.values() {
            assert_d!(legs.left, 0.0);
            assert_d!(legs.right, 0.0);
        }
    }

    #[test]
    fn unbalanced_legs_pays_on_weaker() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left=800, Right=200. Weaker leg is 200.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(800.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(200.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.matched_volume, 200.0);
        assert_d!(earning.dollar_amount, 20.0); // 200 * 0.10 * 1.0
    }

    #[test]
    fn ineligible_distributor_earns_nothing_but_volume_accumulates() {
        // Tree: root(1) -> left(2) -> left(4), right(3)
        // Node 2 is ineligible. Node 4's volume should still flow up
        // through node 2 to be counted in root's left leg.
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(2), 4000)
            .unwrap();

        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: d!(0.0), // Below 100 PV minimum
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(300.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(300.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Root should see left=300 (from node 4, through ineligible node 2),
        // right=300. Earns 300 * 0.10 = 30.
        let root_earning = result
            .earnings
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("root should earn");
        assert_d!(root_earning.left_volume, 300.0);
        assert_d!(root_earning.right_volume, 300.0);
        assert_d!(root_earning.dollar_amount, 30.0);

        // Node 2 should NOT be in earnings (ineligible).
        assert!(result.earnings.iter().all(|e| e.earner_id != test_uuid(2)));
    }

    #[test]
    fn no_earning_for_zero_matched_volume() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        // Only a left child, no right.
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();

        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(500.0),
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // No matched volume (right leg is zero), so no earning.
        assert!(result.earnings.is_empty());
    }

    #[test]
    fn multiple_volume_sources_same_distributor_aggregated() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Two volume sources for the same left-child distributor.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(300.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(400.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Left = 200 + 300 = 500, Right = 400. Matched = 400.
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.left_volume, 500.0);
        assert_d!(earning.right_volume, 400.0);
        assert_d!(earning.matched_volume, 400.0);
        assert_d!(earning.dollar_amount, 40.0); // 400 * 0.10
    }

    // --- VolumeRatio tests ---

    fn volume_ratio_structure() -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    calculation: PairingCalculation::VolumeRatio,
                    ..test_pairing_config()
                }),
            },
        }
    }

    #[test]
    fn volume_ratio_balanced_legs_ratio_is_one() {
        let tree = three_node_tree();
        let structure = volume_ratio_structure();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.ratio, 1.0);
        // 500 * 0.10 * 1.0 * 1.0 = 50.0
        assert_d!(earning.dollar_amount, 50.0);
    }

    #[test]
    fn volume_ratio_unbalanced_legs_applies_penalty() {
        let tree = three_node_tree();
        let structure = volume_ratio_structure();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left=100, Right=400. Ratio = 100/400 = 0.25
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(100.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(400.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.ratio, 0.25);
        // matched=100, 100 * 0.10 * 1.0 * 0.25 = 2.5
        assert_d!(earning.dollar_amount, 2.5);
    }

    // --- Cap enforcement tests ---

    fn capped_structure(cap: AuthoredDec) -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    cap_per_period: Some(cap),
                    ..test_pairing_config()
                }),
            },
        }
    }

    #[test]
    fn under_cap_not_clamped() {
        let structure = capped_structure(d!(1000.0));
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 50.0); // 500 * 0.10, well under 1000 cap
        assert!(!earning.capped);
    }

    #[test]
    fn over_cap_clamped() {
        let structure = capped_structure(d!(25.0));
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let earning = &result.earnings[0];
        // raw_amount would be 50.0, capped to 25.0
        assert_d!(earning.dollar_amount, 25.0);
        assert!(earning.capped);
    }

    #[test]
    fn at_exact_cap_not_flagged_as_capped() {
        let structure = capped_structure(d!(50.0));
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let earning = &result.earnings[0];
        // raw_amount = 50.0, cap = 50.0. Not exceeded, so not capped.
        assert_d!(earning.dollar_amount, 50.0);
        assert!(!earning.capped);
    }

    // --- NetOff carry-forward tests ---

    fn net_off_structure() -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    volume_after_payout: VolumeAfterPayout::NetOff,
                    ..test_pairing_config()
                }),
            },
        }
    }

    #[test]
    fn net_off_subtracts_matched_from_both_legs() {
        let tree = three_node_tree();
        let structure = net_off_structure();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left=800, Right=300. Matched=300.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(800.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(300.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // NetOff: left = 800 - 300 = 500, right = 300 - 300 = 0
        assert_d!(root_cf.left, 500.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn net_off_non_earner_keeps_full_volume() {
        let tree = three_node_tree();
        let structure = net_off_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Node 2 is a leaf. Legs are both 0. No matched volume.
        // NetOff: 0-0=0, 0-0=0. Carry forward is zero.
        let node2_cf = result.carry_forward.get(&test_uuid(2)).unwrap();
        assert_d!(node2_cf.left, 0.0);
        assert_d!(node2_cf.right, 0.0);
    }

    // --- CarryForward mode tests ---

    fn carry_forward_structure() -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    volume_after_payout: VolumeAfterPayout::CarryForward,
                    ..test_pairing_config()
                }),
            },
        }
    }

    #[test]
    fn carry_forward_weaker_leg_zeroed_stronger_keeps_excess() {
        let tree = three_node_tree();
        let structure = carry_forward_structure();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left=800, Right=300. Matched=300. Left is stronger.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(800.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(300.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // CarryForward: weaker (right) zeroed, stronger (left) keeps excess.
        // left = 800 - 300 = 500, right = 0
        assert_d!(root_cf.left, 500.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn carry_forward_balanced_legs_both_zero() {
        let tree = three_node_tree();
        let structure = carry_forward_structure();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // Balanced: left is "weaker" (or equal). Both zero after payout.
        assert_d!(root_cf.left, 0.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn carry_forward_cap_clamps_excess() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    volume_after_payout: VolumeAfterPayout::CarryForward,
                    carry_forward_cap: Some(d!(200.0)),
                    ..test_pairing_config()
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Left=1000, Right=200. Matched=200. Excess left = 800.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1000.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(200.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // CarryForward: left excess = 1000-200 = 800, capped to 200.
        assert_d!(root_cf.left, 200.0);
        assert_d!(root_cf.right, 0.0);
    }

    // --- Prior carry-forward tests ---

    #[test]
    fn prior_carry_forward_adds_to_current_volume() {
        let tree = three_node_tree();
        let structure = test_binary_structure(); // FullFlush
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Current period: left=200, right=100.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(100.0),
            },
        ];

        // Prior carry-forward: root had left=300, right=400.
        let mut prior_cf = HashMap::new();
        prior_cf.insert(
            test_uuid(1),
            LegVolumes {
                left: d!(300.0),
                right: d!(400.0),
            },
        );

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &prior_cf, None,
        )
        .unwrap();

        // Root legs: left = 200 + 300 = 500, right = 100 + 400 = 500.
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.left_volume, 500.0);
        assert_d!(earning.right_volume, 500.0);
        assert_d!(earning.matched_volume, 500.0);
        assert_d!(earning.dollar_amount, 50.0);
    }

    // --- Error handling tests ---

    #[test]
    fn source_not_in_tree_returns_error() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());
        snapshots.insert(test_uuid(99), eligible_snapshot()); // Not in tree

        let volume = vec![VolumeSource {
            source_id: test_uuid(99), // Not in tree
            cv_amount: d!(500.0),
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        );

        assert!(matches!(result, Err(CalculationError::SourceNotInTree(_))));
    }

    #[test]
    fn source_not_in_snapshot_returns_error() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        // Node 2 is in tree but NOT in snapshots.
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2), // In tree, not in snapshots
            cv_amount: d!(500.0),
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(2))
        );
    }

    #[test]
    fn negative_cv_amount_returns_error() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(-100.0),
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        );

        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    // --- Edge case tests ---

    #[test]
    fn empty_tree_returns_empty_result() {
        let tree = BinaryTree::new();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &HashMap::new(),
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert!(result.earnings.is_empty());
        assert!(result.carry_forward.is_empty());
    }

    #[test]
    fn single_node_tree_no_earnings() {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();

        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        // Root generates volume but has no children. Both legs are zero.
        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: d!(500.0),
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert!(result.earnings.is_empty());
        // Root should still have a carry-forward entry.
        assert!(result.carry_forward.contains_key(&test_uuid(1)));
    }

    #[test]
    fn deep_binary_tree() {
        // Build a 4-level deep binary tree:
        //       1
        //      / \
        //     2   3
        //    / \
        //   4   5
        //  /
        // 6
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 1, test_uuid(2), 5000)
            .unwrap();
        tree.add_node(test_uuid(6), test_uuid(4), 0, test_uuid(4), 6000)
            .unwrap();

        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        for i in 1..=6u8 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        // Volume at the deepest node (6) and at node 3 (right leg of root).
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(400.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(400.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Root: left leg includes node 6's volume (flows up through 4, 2).
        // left=400, right=400. Matched=400.
        let root_earning = result
            .earnings
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("root should earn");
        assert_d!(root_earning.left_volume, 400.0);
        assert_d!(root_earning.right_volume, 400.0);
        assert_d!(root_earning.dollar_amount, 40.0);
    }

    #[test]
    fn distributor_in_tree_but_not_in_snapshot_is_rejected() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        // Only root has a snapshot. Children are in tree but not in snapshots.
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[],
            &HashMap::new(),
            None,
        );

        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(test_uuid(2))
        );
    }

    #[test]
    fn a_position_needs_its_owner_snapshot_not_its_own() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let owner = test_uuid(9);
        let ownership = HashMap::from([(test_uuid(2), owner), (test_uuid(3), owner)]);

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(owner, eligible_snapshot());

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[],
            &HashMap::new(),
            Some(&ownership),
        );
        assert!(result.is_ok(), "got {result:?}");
    }

    #[test]
    fn a_missing_owner_snapshot_is_reported_under_the_owner_id() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let owner = test_uuid(9);
        let ownership = HashMap::from([(test_uuid(2), owner), (test_uuid(3), owner)]);

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[],
            &HashMap::new(),
            Some(&ownership),
        );
        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(owner)
        );
    }

    #[test]
    fn cycle_step_refuses_a_position_owner_with_no_snapshot() {
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let owner = test_uuid(9);
        let ownership = HashMap::from([(test_uuid(2), owner), (test_uuid(3), owner)]);

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[],
            &HashMap::new(),
            Some(&ownership),
        );
        assert_eq!(
            result.unwrap_err(),
            CalculationError::DistributorNotInSnapshot(owner)
        );
    }

    #[test]
    fn aggregate_volume_rejects_a_source_whose_owner_has_no_snapshot() {
        let tree = three_node_tree();
        let owner = test_uuid(9);
        let ownership = HashMap::from([(test_uuid(2), owner)]);
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
        ]);
        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(500.0),
        }];

        assert_eq!(
            aggregate_volume(&tree, &snapshots, &volume, Some(&ownership)),
            Err(CalculationError::SourceNotInSnapshot(owner))
        );
    }

    #[test]
    fn stale_carry_forward_for_removed_node_ignored() {
        let tree = three_node_tree();
        let structure = test_binary_structure();
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        // Carry-forward for a node not in the tree — should be silently ignored.
        let mut prior_cf = HashMap::new();
        prior_cf.insert(
            test_uuid(99),
            LegVolumes {
                left: d!(9999.0),
                right: d!(9999.0),
            },
        );

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &prior_cf, None,
        )
        .unwrap();

        // Should not crash. Result should be same as without carry-forward.
        assert_eq!(result.earnings.len(), 1);
        assert_d!(result.earnings[0].dollar_amount, 50.0);

        // Stale node should not appear in output carry-forward.
        assert!(!result.carry_forward.contains_key(&test_uuid(99)));
    }

    // --- evaluate_eligibility direct tests ---

    #[test]
    fn evaluate_eligibility_eligible_distributor() {
        let elig = default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = evaluate_eligibility(&snapshots, &elig);

        assert_eq!(result.get(&test_uuid(1)), Some(&true));
    }

    #[test]
    fn evaluate_eligibility_low_pv_returns_false() {
        let elig = default_eligibility(); // minimum_pv = 100.0
        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                personal_volume: d!(50.0), // below 100 minimum
                ..eligible_snapshot()
            },
        );

        let result = evaluate_eligibility(&snapshots, &elig);

        assert_eq!(result.get(&test_uuid(1)), Some(&false));
    }

    #[test]
    fn evaluate_eligibility_wrong_status_returns_false() {
        let elig = default_eligibility(); // eligible_statuses = ["active"]
        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                status: "suspended".to_string(),
                ..eligible_snapshot()
            },
        );

        let result = evaluate_eligibility(&snapshots, &elig);

        assert_eq!(result.get(&test_uuid(1)), Some(&false));
    }

    // --- cv_amount boundary tests ---

    #[test]
    fn zero_cv_amount_produces_zero_earnings() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(0.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(0.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // cv_amount=0.0 is valid. Zero matched volume produces no earnings.
        assert!(
            result.earnings.is_empty()
                || result
                    .earnings
                    .iter()
                    .all(|e| e.dollar_amount == like(&e.dollar_amount, "0.0")),
            "zero CV should produce zero or empty earnings"
        );
    }

    // --- CycleStep calculator tests ---

    fn test_cycle_step_structure(
        steps: Vec<CycleStep>,
        volume_after_cycle: VolumeAfterPayout,
    ) -> BinaryStructureConfig {
        BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps,
                    volume_after_cycle,
                    cap_per_period: None,
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        }
    }

    #[test]
    fn calculate_binary_pairing_rejects_a_rank_not_in_the_ladder() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    #[test]
    fn calculate_binary_pairing_rejects_a_negative_personal_volume() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                personal_volume: d!(-0.5),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert!(
            matches!(
                err,
                CalculationError::InvalidPersonalVolume(id, pv)
                    if id == test_uuid(1) && pv == d!(-0.5)
            ),
            "got {err:?}"
        );
    }

    /// Pins the rank check above the per-source checks, per design decision 2.
    /// This input is bad on both counts: only the check order decides which
    /// error surfaces.
    #[test]
    fn binary_unknown_rank_wins_over_invalid_cv() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(-50.0),
        }];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    /// Pins the cycle-step rank check above `aggregate_volume`. The pairing
    /// arm is pinned on both sides by its own two tests; without this the
    /// cycle-step arm is pinned below `validate` only.
    #[test]
    fn cycle_step_unknown_rank_wins_over_invalid_cv() {
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(-50.0),
        }];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    /// Pins the cycle-step rank check below CycleStepConfig::validate. Hoisting
    /// both call sites into one above the mode match would invert this and no
    /// other test would notice.
    #[test]
    fn cycle_step_config_error_wins_over_unknown_rank() {
        // Empty steps: validate() rejects this on its own.
        let structure = test_cycle_step_structure(vec![], VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: d!(500.0),
        }];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        // Pins which guard fired, so the docblock above stays true if the
        // validation order inside CycleStepConfig::validate changes.
        match err {
            CalculationError::ConfigError(msg) => {
                assert!(msg.contains("at least one step"), "wrong guard: {msg}")
            }
            other => panic!("expected ConfigError, got {other:?}"),
        }
    }

    /// The CycleStep arm returns out of the mode match into its own function,
    /// so a guard placed below that match reaches the pairing arm only. This
    /// covers the other arm.
    #[test]
    fn pairing_in_cycle_step_mode_rejects_a_rank_not_in_the_ladder() {
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "diamond".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert_eq!(
            err,
            CalculationError::UnknownSnapshotRank(test_uuid(1), "diamond".to_string())
        );
    }

    #[test]
    fn pairing_in_cycle_step_mode_rejects_a_negative_personal_volume() {
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                personal_volume: d!(-0.5),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap_err();

        assert!(
            matches!(
                err,
                CalculationError::InvalidPersonalVolume(id, pv)
                    if id == test_uuid(1) && pv == d!(-0.5)
            ),
            "got {err:?}"
        );
    }

    #[test]
    fn cycle_step_single_step_both_legs_meet() {
        // Both legs at 500, step at 300/$25. Earns $25.
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.earner_id, test_uuid(1));
        assert_d!(earning.dollar_amount, 25.0);
        assert_d!(earning.left_volume, 500.0);
        assert_d!(earning.right_volume, 500.0);
        assert_d!(earning.matched_volume, 500.0);
        assert_d!(earning.percent, 0.0);
        assert_d!(earning.ratio, 0.0);
        assert!(!earning.capped);

        // FullFlush after complete cycle: carry-forward legs should be zero.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 0.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn cycle_step_single_step_one_leg_below() {
        // Left at 500, right at 200, step at 300/$25. Earns nothing.
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(200.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // matched = min(500, 200) = 200 < 300 threshold. No step qualifies.
        assert!(
            result.earnings.is_empty()
                || result
                    .earnings
                    .iter()
                    .all(|e| e.dollar_amount == like(&e.dollar_amount, "0.0")),
            "should earn nothing when weaker leg is below threshold"
        );

        // Legs carry forward unchanged (no cycle started).
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 500.0);
        assert_d!(root_cf.right, 200.0);
    }

    #[test]
    fn cycle_step_multiple_steps_all_qualify() {
        // Both legs at 1500, steps at 300/$25, 600/$50, 1200/$100.
        // All three steps qualify. Earns $25 + $50 + $100 = $175.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 175.0);

        // FullFlush: legs zeroed after complete cycle.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 0.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn cycle_step_multiple_steps_partial() {
        // Both legs at 800, steps at 300/$25, 600/$50, 1200/$100.
        // Steps 1 and 2 qualify (800 >= 300, 800 >= 600). Step 3 does not (800 < 1200).
        // Earns $25 + $50 = $75. Incomplete cycle, no flush.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(800.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(800.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 75.0);

        // Incomplete cycle: legs carry forward unchanged.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 800.0);
        assert_d!(root_cf.right, 800.0);
    }

    // --- CycleStep carry-forward and flush tests ---

    #[test]
    fn cycle_step_full_flush_zeroes_both_legs() {
        // Both legs at 1500, steps at 300/$25, 600/$50, 1200/$100.
        // All steps qualify, cycle completes. FullFlush zeroes carry-forward.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        assert_d!(result.earnings[0].dollar_amount, 175.0);

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 0.0);
        assert_d!(root_cf.right, 0.0);
    }

    #[test]
    fn cycle_step_carry_forward_subtracts_highest() {
        // Both legs at 1500, steps at 300/$25, 600/$50, 1200/$100.
        // All steps qualify, cycle completes. CarryForward subtracts 1200,
        // leaving 300 on each leg. 300 >= first step threshold (300), so a
        // second partial cycle runs: step 1 qualifies ($25), step 2 does not.
        // Total: $175 + $25 = $200. Carry-forward: 300 each (incomplete
        // second cycle, no subtraction).
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        // $175 from the first complete cycle + $25 from the second partial cycle.
        assert_d!(result.earnings[0].dollar_amount, 200.0);

        // 1500 - 1200 = 300 on each leg. Second cycle incomplete, no subtraction.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 300.0);
        assert_d!(root_cf.right, 300.0);
    }

    #[test]
    fn cycle_step_mid_period_cycling() {
        // Both legs at 2000, steps at 300/$25, 600/$50, 1200/$100.
        // CarryForward mode. First cycle earns $175, subtracts 1200, leaving 800 each.
        // Second cycle: 800 >= 300 ($25), 800 >= 600 ($50), 800 < 1200 (no).
        // Incomplete second cycle, no subtraction. Total: $175 + $75 = $250.
        // Carry-forward: 800 each.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(2000.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(2000.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        assert_d!(result.earnings[0].dollar_amount, 250.0);

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 800.0);
        assert_d!(root_cf.right, 800.0);
    }

    #[test]
    fn cycle_step_carry_forward_from_prior_period() {
        // Prior carry-forward has 500 on each leg. New volume adds 200 each side.
        // Total: 700 each. Steps at 300/$25, 600/$50, 1200/$100.
        // Steps 1 and 2 qualify ($75). Incomplete cycle. Carry-forward: 700 each.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(200.0),
            },
        ];

        let mut prior_carry = HashMap::new();
        prior_carry.insert(
            test_uuid(1),
            LegVolumes {
                left: d!(500.0),
                right: d!(500.0),
            },
        );

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &prior_carry,
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        assert_d!(result.earnings[0].dollar_amount, 75.0);

        // Incomplete cycle: legs carry forward unchanged at 700.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 700.0);
        assert_d!(root_cf.right, 700.0);
    }

    #[test]
    fn cycle_step_carry_forward_cap_applied() {
        // Both legs at 1500, steps at 300/$25, 600/$50, 1200/$100.
        // CarryForward: first cycle completes ($175), subtracts 1200, leaving
        // 300. Second partial cycle earns $25 (300 >= 300). Total: $200.
        // Post-cycle legs: 300 each. carry_forward_cap = 200 clamps to 200.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps,
                    volume_after_cycle: VolumeAfterPayout::CarryForward,
                    cap_per_period: None,
                    carry_forward_cap: Some(d!(200.0)),
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        // $175 from the first complete cycle + $25 from the second partial cycle.
        assert_d!(result.earnings[0].dollar_amount, 200.0);

        // 1500 - 1200 = 300, clamped to 200 by carry_forward_cap.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 200.0);
        assert_d!(root_cf.right, 200.0);
    }

    #[test]
    fn cycle_step_ineligible_preserves_volume() {
        // Root is ineligible (PV below minimum). Both legs at 500, step at 300/$25.
        // No earnings. Both legs carry forward at 500 (unchanged).
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                personal_volume: d!(0.0), // Below 100 PV minimum
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // Root is ineligible, so no earnings from root.
        let root_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == test_uuid(1))
            .collect();
        assert!(root_earnings.is_empty());

        // Volume preserved in carry-forward.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 500.0);
        assert_d!(root_cf.right, 500.0);
    }

    #[test]
    fn cycle_step_unsorted_steps_still_correct() {
        // Steps passed in descending order. Defensive sort should reorder.
        // Both legs at 1500, CarryForward. Same result as sorted input:
        // $200 ($175 first cycle + $25 second partial) and carry-forward 300.
        let steps = vec![
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        // Same as carry_forward_subtracts_highest: $175 + $25 partial = $200.
        assert_d!(result.earnings[0].dollar_amount, 200.0);

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 300.0);
        assert_d!(root_cf.right, 300.0);
    }

    #[test]
    fn cycle_step_per_period_cap() {
        // Both legs at 5000, steps at 300/$25, 600/$50, 1200/$100. CarryForward.
        // Cycle 1: all qualify ($175), subtract 1200 -> 3800 each.
        // Cycle 2: all qualify ($175), subtract 1200 -> 2600 each.
        // Cycle 3: all qualify ($175), subtract 1200 -> 1400 each.
        // Cycle 4: all qualify ($175), subtract 1200 -> 200 each.
        // 200 < 300, no more cycles. Total uncapped = $700.
        // Cap = $400, so earnings clamped to $400.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps,
                    volume_after_cycle: VolumeAfterPayout::CarryForward,
                    cap_per_period: Some(d!(400.0)),
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(5000.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(5000.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 400.0);
        assert!(earning.capped);

        // Carry-forward should reflect post-cycle legs (200 each),
        // not affected by the cap.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 200.0);
        assert_d!(root_cf.right, 200.0);
    }

    #[test]
    fn cycle_step_multi_position_per_position_cap() {
        // Two positions owned by same person. Each has balanced 1500 CV legs.
        // Steps: 300/$25, 600/$50, 1200/$100. CarryForward.
        // Per position: cycle 1 all qualify ($175), subtract 1200, remaining 300.
        // Cycle 2: 300 >= 300 ($25), 300 < 600. Incomplete, no subtraction.
        // Total per position = $200. Cap per position = $150.
        // Each position capped independently to $150.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps,
                    volume_after_cycle: VolumeAfterPayout::CarryForward,
                    cap_per_period: Some(d!(150.0)),
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                }),
            },
        };
        let tree = multi_position_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        // 1500 CV under each leg of each position.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(1500.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(1500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        let owner_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner_a)
            .collect();
        assert_eq!(owner_earnings.len(), 2);

        // Each position independently capped to $150.
        for e in &owner_earnings {
            assert_d!(
                e.dollar_amount,
                150.0,
                "each position should be capped to 150.0, got {}",
                e.dollar_amount
            );
            assert!(e.capped);
        }
    }

    #[test]
    fn cycle_step_multi_position_aggregate_cap() {
        // Two positions owned by same person. Each has balanced 1200 CV legs.
        // Steps: 300/$25, 600/$50, 1200/$100. FullFlush.
        // Per position: all steps qualify ($175), cycle completes, flush to 0.
        // No more cycles. Total per position = $175. Owner total = $350.
        // Aggregate cap = $300. Pro-rata: each gets 300 * (175/350) = $150.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
            CycleStep {
                threshold: d!(1200.0),
                amount: d!(100.0),
            },
        ];
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps,
                    volume_after_cycle: VolumeAfterPayout::FullFlush,
                    cap_per_period: Some(d!(300.0)),
                    carry_forward_cap: None,
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                }),
            },
        };
        let tree = multi_position_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        // 1200 CV under each leg of each position.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(1200.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(1200.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(1200.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(1200.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        let owner_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner_a)
            .collect();
        assert_eq!(owner_earnings.len(), 2);

        let total = Dec::sum(owner_earnings.iter().map(|e| e.dollar_amount)).unwrap();
        assert_d!(
            total,
            300.0,
            "aggregate total should be 300.0, got {}",
            total
        );

        // Pro-rata: each position had equal raw earnings ($175),
        // so each gets half the cap: $150.
        for e in &owner_earnings {
            assert_d!(
                e.dollar_amount,
                150.0,
                "each position should earn 150.0, got {}",
                e.dollar_amount
            );
            assert!(e.capped);
        }
    }

    #[test]
    fn cycle_step_zero_volume_no_crash() {
        // Both legs at zero volume. No earnings, no crash.
        let steps = vec![
            CycleStep {
                threshold: d!(300.0),
                amount: d!(25.0),
            },
            CycleStep {
                threshold: d!(600.0),
                amount: d!(50.0),
            },
        ];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        // Zero volume on both legs.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(0.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(0.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        // No earnings when both legs are zero.
        assert!(result.earnings.is_empty());

        // Carry-forward should have zero on both legs.
        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        assert_d!(root_cf.left, 0.0);
        assert_d!(root_cf.right, 0.0);
    }

    // --- Multi-position ownership resolution tests ---

    #[test]
    fn multi_position_earner_id_is_owner() {
        // Two positions (2, 3) owned by same person (owner_A).
        // Both earnings should have earner_id = owner_A, distinct position_ids.
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(1), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.earner_id, owner_a);
        assert_eq!(earning.position_id, test_uuid(1));
    }

    #[test]
    fn multi_position_eligibility_uses_owner_snapshot() {
        // pos1 (test_uuid(1)) owned by owner_A. Snapshot exists for
        // owner_A (eligible), NOT for pos1. Pos1 should earn because
        // eligibility resolves through owner_A.
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(1), owner_a);

        // No snapshot for test_uuid(1), only for owner_a.
        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        // Owner_a earns through position 1.
        assert_eq!(result.earnings.len(), 1);
        assert_eq!(result.earnings[0].earner_id, owner_a);
        assert_d!(result.earnings[0].dollar_amount, 50.0);
    }

    #[test]
    fn multi_position_no_ownership_map_is_identity() {
        // Standard test, no ownership map. earner_id == position_id.
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        assert_eq!(result.earnings[0].earner_id, test_uuid(1));
        assert_eq!(result.earnings[0].position_id, test_uuid(1));
    }

    #[test]
    fn multi_position_position_missing_from_map_falls_back() {
        // Ownership map exists but doesn't include test_uuid(1).
        // test_uuid(1) should fall back to self-ownership.
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        // Map only has an entry for test_uuid(99) — unrelated.
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(99), test_uuid(200));

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        // Falls back to self-ownership: earner_id == position_id.
        assert_eq!(result.earnings.len(), 1);
        assert_eq!(result.earnings[0].earner_id, test_uuid(1));
        assert_eq!(result.earnings[0].position_id, test_uuid(1));
    }

    #[test]
    fn multi_position_carry_forward_keyed_by_position() {
        // Two positions (2, 3) owned by same person. Run with
        // CarryForward mode and unbalanced legs. Verify carry-forward
        // is keyed by position_id, not owner_id.
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        // Children under pos 2
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 1, test_uuid(2), 5000)
            .unwrap();
        // Children under pos 3
        tree.add_node(test_uuid(6), test_uuid(3), 0, test_uuid(3), 6000)
            .unwrap();
        tree.add_node(test_uuid(7), test_uuid(3), 1, test_uuid(3), 7000)
            .unwrap();

        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    volume_after_payout: VolumeAfterPayout::CarryForward,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        // Unbalanced: pos2 left=1000, right=500; pos3 left=200, right=800
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(1000.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(800.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        // Carry-forward should be keyed by position_id (2, 3), not owner_id.
        assert!(result.carry_forward.contains_key(&test_uuid(2)));
        assert!(result.carry_forward.contains_key(&test_uuid(3)));

        // pos2: left=1000, right=500, matched=500. CarryForward: left keeps
        // excess (1000-500=500), right zeroed.
        let cf2 = &result.carry_forward[&test_uuid(2)];
        assert_d!(
            cf2.left,
            500.0,
            "pos2 left carry: expected 500.0, got {}",
            cf2.left
        );
        assert_d!(
            cf2.right,
            0,
            "pos2 right carry: expected 0.0, got {}",
            cf2.right
        );

        // pos3: left=200, right=800, matched=200. CarryForward: right keeps
        // excess (800-200=600), left zeroed.
        let cf3 = &result.carry_forward[&test_uuid(3)];
        assert_d!(
            cf3.left,
            0,
            "pos3 left carry: expected 0.0, got {}",
            cf3.left
        );
        assert_d!(
            cf3.right,
            600.0,
            "pos3 right carry: expected 600.0, got {}",
            cf3.right
        );
    }

    // --- Multi-position aggregate cap tests ---

    /// Build a binary tree with 2 positions under root, each having
    /// one child generating volume:
    ///
    /// ```text
    ///        root (1)
    ///       /        \
    ///    pos_a (2)   pos_b (3)
    ///    /    \       /    \
    ///  (4)   (5)    (6)   (7)
    /// ```
    fn multi_position_tree() -> BinaryTree {
        let mut tree = BinaryTree::new();
        tree.add_root(test_uuid(1), 1000).unwrap();
        // Two positions under root
        tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
            .unwrap();
        // Children under pos_a
        tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(2), 4000)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 1, test_uuid(2), 5000)
            .unwrap();
        // Children under pos_b
        tree.add_node(test_uuid(6), test_uuid(3), 0, test_uuid(3), 6000)
            .unwrap();
        tree.add_node(test_uuid(7), test_uuid(3), 1, test_uuid(3), 7000)
            .unwrap();
        tree
    }

    /// Runs the two-position aggregate-cap fixture, owned by `test_uuid(100)`,
    /// at the given pairing percent.
    fn aggregate_pairing_with_percent(
        percent: AuthoredDec,
    ) -> Result<BinaryCalculationResult, CalculationError> {
        let tree = multi_position_tree();

        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent,
                    cap_per_period: Some(d!(500.0)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        // Snapshots keyed by owner_id. All volume-generating nodes need
        // entries too (they're position == owner in single-position).
        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        // 3000 CV under each leg of each position.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(3000.0),
            },
        ];

        calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
    }

    #[test]
    fn a_negative_pairing_percent_is_refused_under_an_aggregate_cap() {
        assert_eq!(
            aggregate_pairing_with_percent(d!(-0.10)).unwrap_err(),
            CalculationError::PairingPercentOutOfRange(d!(-0.10))
        );
    }

    #[test]
    fn a_pairing_percent_above_one_is_refused() {
        assert_eq!(
            aggregate_pairing_with_percent(d!(1.01)).unwrap_err(),
            CalculationError::PairingPercentOutOfRange(d!(1.01))
        );
    }

    #[test]
    fn pairing_percents_of_zero_and_one_are_accepted() {
        assert!(aggregate_pairing_with_percent(d!(0)).is_ok());
        assert!(aggregate_pairing_with_percent(d!(1)).is_ok());
    }

    /// Runs a two-position aggregate-cap pairing whose earnings span 28 decimal places.
    fn wide_span_aggregate_pairing(
        cap: AuthoredDec,
    ) -> Result<BinaryCalculationResult, CalculationError> {
        let tree = multi_position_tree();
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: Some(d!(0.33333333)),
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(0.3333333333),
                    cap_per_period: Some(cap),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);
        let mut snapshots = HashMap::new();
        for id in [100, 1, 4, 5, 6, 7] {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(0.0000000001),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(0.0000000001),
            },
        ];
        calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
    }

    /// The earning for `position`, as its dollar amount and capped flag.
    fn position_payout(result: &BinaryCalculationResult, position: u8) -> (Dec, bool) {
        let e = result
            .earnings
            .iter()
            .find(|e| e.position_id == test_uuid(position))
            .unwrap();
        (e.dollar_amount, e.capped)
    }

    #[test]
    fn an_aggregate_cap_over_wide_span_earnings_is_allocated() {
        let result = wide_span_aggregate_pairing(d!(5)).unwrap();
        assert_eq!(position_payout(&result, 2), (d!(5), true));
        assert_eq!(position_payout(&result, 3), (d!(0), true));
    }

    #[test]
    fn wide_span_earnings_under_an_aggregate_cap_are_only_rounded() {
        let result = wide_span_aggregate_pairing(d!(500)).unwrap();
        assert_eq!(position_payout(&result, 2), (d!(11.11), false));
        assert_eq!(position_payout(&result, 3), (d!(0), false));
    }

    #[test]
    fn multi_position_aggregate_cap_scales_pro_rata() {
        // pos_a (2) and pos_b (3) are both owned by owner_A (test_uuid(100)).
        // Each position has balanced legs generating 3000 CV each side.
        // Per-position earning: 3000 * 0.10 = 300. Owner total = 600.
        // Aggregate cap = 500. Each should be scaled to 250.
        let owner_a = test_uuid(100);
        let result = aggregate_pairing_with_percent(d!(0.10)).unwrap();

        // Find earnings for the two positions owned by owner_a.
        let owner_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner_a)
            .collect();
        assert_eq!(owner_earnings.len(), 2);

        let total = Dec::sum(owner_earnings.iter().map(|e| e.dollar_amount)).unwrap();
        assert_d!(
            total,
            500.0,
            "aggregate total should be 500.0, got {}",
            total
        );

        // Pro-rata: each position should get 250.0.
        for e in &owner_earnings {
            assert_d!(
                e.dollar_amount,
                250.0,
                "each position should earn 250.0, got {}",
                e.dollar_amount
            );
            assert!(e.capped);
        }
    }

    #[test]
    fn multi_position_per_position_cap_independent() {
        // Same setup as above but PerPosition mode. Cap = 500.
        // Each position earns 300. Neither exceeds 500, so neither is capped.
        let tree = multi_position_tree();

        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    cap_per_period: Some(d!(500.0)),
                    multi_position_cap_mode: MultiPositionCapMode::PerPosition,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(3000.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        let owner_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner_a)
            .collect();
        assert_eq!(owner_earnings.len(), 2);

        // Each earns 300.0, neither capped at 500.
        for e in &owner_earnings {
            assert_d!(
                e.dollar_amount,
                300.0,
                "each position should earn 300.0, got {}",
                e.dollar_amount
            );
            assert!(!e.capped);
        }
    }

    #[test]
    fn multi_position_aggregate_cap_preserves_proportions_with_unequal_earnings() {
        // Two positions owned by same person. Unequal raw earnings.
        // pos_a: left=200, right=200 -> matched=200, raw=200*0.10=20
        // pos_b: left=3000, right=3000 -> matched=3000, raw=3000*0.10=300
        // Owner total raw = 320. Aggregate cap = 100.
        // Pro-rata: pos_a = 100 * (20/320) = 6.25, pos_b = 100 * (300/320) = 93.75
        // Without the fix, per-position cap would fire first (but both are under
        // 100, so it wouldn't change anything in this case). Test with cap < total
        // to exercise the pro-rata path.
        let tree = multi_position_tree();

        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    cap_per_period: Some(d!(100.0)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let owner_a = test_uuid(100);
        let mut ownership = HashMap::new();
        ownership.insert(test_uuid(2), owner_a);
        ownership.insert(test_uuid(3), owner_a);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner_a, eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());
        snapshots.insert(test_uuid(5), eligible_snapshot());
        snapshots.insert(test_uuid(6), eligible_snapshot());
        snapshots.insert(test_uuid(7), eligible_snapshot());

        // pos_a (2): balanced 200 each side. pos_b (3): balanced 3000 each side.
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(200.0),
            },
            VolumeSource {
                source_id: test_uuid(6),
                cv_amount: d!(3000.0),
            },
            VolumeSource {
                source_id: test_uuid(7),
                cv_amount: d!(3000.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();

        let owner_earnings: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner_a)
            .collect();
        assert_eq!(owner_earnings.len(), 2);

        let total = Dec::sum(owner_earnings.iter().map(|e| e.dollar_amount)).unwrap();
        assert_d!(
            total,
            100.0,
            "aggregate total should be 100.0, got {}",
            total
        );

        // Pro-rata from raw: pos_a raw=20, pos_b raw=300. Total=320.
        // pos_a share = 100 * 20/320 = 6.25
        // pos_b share = 100 * 300/320 = 93.75
        let pos_a = owner_earnings
            .iter()
            .find(|e| e.position_id == test_uuid(2))
            .unwrap();
        let pos_b = owner_earnings
            .iter()
            .find(|e| e.position_id == test_uuid(3))
            .unwrap();

        assert_d!(
            pos_a.dollar_amount,
            6.25,
            "pos_a should get 6.25, got {}",
            pos_a.dollar_amount
        );
        assert_d!(
            pos_b.dollar_amount,
            93.75,
            "pos_b should get 93.75, got {}",
            pos_b.dollar_amount
        );
    }

    #[test]
    fn multi_position_aggregate_cap_ignored_without_ownership() {
        // Aggregate mode but ownership is None. Should fall back to
        // per-position cap behavior.
        let tree = three_node_tree();

        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    cap_per_period: Some(d!(30.0)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500.0),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500.0),
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None, // No ownership map
        )
        .unwrap();

        // Root earns: 500 * 0.10 = 50.0, capped at 30.0 per-position.
        assert_eq!(result.earnings.len(), 1);
        assert_d!(
            result.earnings[0].dollar_amount,
            30.0,
            "should be capped at 30.0 per-position, got {}",
            result.earnings[0].dollar_amount
        );
        assert!(result.earnings[0].capped);
    }

    // --- Property-based tests ---

    mod prop_tests {
        use super::*;
        use crate::test_strategies::cents;
        use proptest::prelude::*;

        /// Generate a random volume amount (non-negative, whole cents).
        fn arb_volume() -> impl Strategy<Value = AuthoredDec> {
            cents(0, 1_000_000)
        }

        /// Generate a pair of leg volumes for a balanced-to-unbalanced tree.
        fn arb_leg_pair() -> impl Strategy<Value = (AuthoredDec, AuthoredDec)> {
            (arb_volume(), arb_volume())
        }

        proptest! {
            #[test]
            fn total_payout_never_exceeds_theoretical_max(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let tree = three_node_tree();
                let structure = test_binary_structure(); // 10%, WeakerLeg, FullFlush
                let plan = test_plan(default_eligibility());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                let total_payout = Dec::sum(result.earnings.iter().map(|e| e.dollar_amount)).unwrap();
                let matched = left_vol.min(right_vol);
                let percent: Dec = d!(0.10);
                let theoretical_max = matched.dec().try_mul(percent).unwrap().round_minor(2);

                prop_assert!(
                    total_payout <= theoretical_max,
                    "total payout {} exceeded theoretical max {}",
                    total_payout,
                    theoretical_max
                );
            }

            #[test]
            fn carry_forward_always_non_negative(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let tree = three_node_tree();
                let structure = carry_forward_structure();
                let plan = test_plan_with_structure(default_eligibility(), structure.clone());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert!(legs.left >= d!(0.0), "left carry-forward is negative: {}", legs.left);
                    prop_assert!(legs.right >= d!(0.0), "right carry-forward is negative: {}", legs.right);
                }
            }

            #[test]
            fn full_flush_always_zero_carry_forward(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let tree = three_node_tree();
                let structure = test_binary_structure(); // FullFlush
                let plan = test_plan(default_eligibility());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert_eq!(legs.left, like(&legs.left, "0.0"));
                    prop_assert_eq!(legs.right, like(&legs.right, "0.0"));
                }
            }

            /// In single-position mode (ownership=None), each position owns itself,
            /// so no duplicate earner_ids should appear. Multi-position mode with
            /// shared ownership intentionally allows duplicate earner_ids.
            #[test]
            fn no_duplicate_earner_ids(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let tree = three_node_tree();
                let structure = test_binary_structure();
                let plan = test_plan(default_eligibility());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                let mut seen = std::collections::HashSet::new();
                for earning in &result.earnings {
                    prop_assert!(
                        seen.insert(earning.earner_id),
                        "duplicate earner_id: {}",
                        earning.earner_id
                    );
                }
            }

            #[test]
            fn carry_forward_cap_never_exceeded(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let cap = d!(500.0);
                let structure = BinaryStructureConfig {
                    name: "Test Binary".to_string(),
                    binary_commission: BinaryCommissionConfig {
                        volume_to_dollar_multiplier: None,
                        mode: BinaryCommissionMode::Pairing(PairingConfig {
                            volume_after_payout: VolumeAfterPayout::CarryForward,
                            carry_forward_cap: Some(cap),
                            ..test_pairing_config()
                        }),
                    },
                };
                let tree = three_node_tree();
                let plan = test_plan_with_structure(default_eligibility(), structure.clone());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert!(
                        legs.left <= cap.dec(),
                        "left carry-forward {} exceeded cap {}",
                        legs.left, cap
                    );
                    prop_assert!(
                        legs.right <= cap.dec(),
                        "right carry-forward {} exceeded cap {}",
                        legs.right, cap
                    );
                }
            }

            /// With cap_per_period set, no single earner's payout should
            /// exceed that cap.
            #[test]
            fn cap_per_period_never_exceeded(
                (left_vol, right_vol) in arb_leg_pair()
            ) {
                let cap = d!(25.0);
                let structure = capped_structure(cap);
                let tree = three_node_tree();
                let plan = test_plan_with_structure(default_eligibility(), structure.clone());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                let volume = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_vol },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_vol },
                ];

                let result = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
                ).unwrap();

                for earning in &result.earnings {
                    prop_assert!(
                        earning.dollar_amount <= cap.dec(),
                        "earner {} paid {} which exceeds cap_per_period {}",
                        earning.earner_id, earning.dollar_amount, cap
                    );
                }
            }

            /// Carry-forward from period 1 feeds into period 2 correctly.
            /// The stronger leg's excess volume should accumulate and
            /// remain non-negative across periods.
            #[test]
            fn carry_forward_preserved_across_periods(
                left_p1 in cents(0, 500_000),
                right_p1 in cents(0, 500_000),
                left_p2 in cents(0, 500_000),
                right_p2 in cents(0, 500_000),
            ) {
                let tree = three_node_tree();
                let structure = carry_forward_structure();
                let plan = test_plan_with_structure(default_eligibility(), structure.clone());

                let mut snapshots = HashMap::new();
                snapshots.insert(test_uuid(1), eligible_snapshot());
                snapshots.insert(test_uuid(2), eligible_snapshot());
                snapshots.insert(test_uuid(3), eligible_snapshot());

                // Period 1
                let volume_p1 = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_p1 },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_p1 },
                ];

                let result_p1 = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume_p1, &HashMap::new(), None,
                ).unwrap();

                // All carry-forward values from period 1 must be non-negative.
                for legs in result_p1.carry_forward.values() {
                    prop_assert!(legs.left >= d!(0.0), "P1 left carry-forward negative: {}", legs.left);
                    prop_assert!(legs.right >= d!(0.0), "P1 right carry-forward negative: {}", legs.right);
                }

                // Period 2: feed carry-forward from period 1 as input.
                let volume_p2 = vec![
                    VolumeSource { source_id: test_uuid(2), cv_amount: left_p2 },
                    VolumeSource { source_id: test_uuid(3), cv_amount: right_p2 },
                ];

                let result_p2 = calculate_binary_pairing(
                    &tree, &plan, &structure, &snapshots, &volume_p2, &result_p1.carry_forward, None,
                ).unwrap();

                // All carry-forward values from period 2 must be non-negative.
                for legs in result_p2.carry_forward.values() {
                    prop_assert!(legs.left >= d!(0.0), "P2 left carry-forward negative: {}", legs.left);
                    prop_assert!(legs.right >= d!(0.0), "P2 right carry-forward negative: {}", legs.right);
                }

                let both = || result_p1.earnings.iter().chain(result_p2.earnings.iter());
                let total_paid = Dec::sum(both().map(|e| e.dollar_amount)).unwrap();
                let percent: Dec = d!(0.10);
                let rounded_max = Dec::sum(
                    both().map(|e| e.matched_volume.try_mul(percent).unwrap().round_minor(2)),
                )
                .unwrap();
                prop_assert!(
                    total_paid <= rounded_max,
                    "two-period total payout {} exceeded the sum of rounded earning maxima {}",
                    total_paid, rounded_max
                );

                // Matched volume across both periods stays within the input volume.
                let total_left = left_p1.dec().try_add(left_p2).unwrap();
                let total_right = right_p1.dec().try_add(right_p2).unwrap();
                let total_matched = Dec::sum(both().map(|e| e.matched_volume)).unwrap();
                prop_assert!(
                    total_matched <= total_left.min(total_right),
                    "two-period matched volume {} exceeded the input volume {}",
                    total_matched, total_left.min(total_right)
                );
            }
        }
    }

    #[test]
    fn apply_aggregate_cap_gives_the_same_amounts_in_every_input_order() {
        let owner = test_uuid(100);
        let earning = |position: u8, amount: Dec| BinaryCommissionEarning {
            earner_id: owner,
            position_id: test_uuid(position),
            left_volume: d!(0.0),
            right_volume: d!(0.0),
            matched_volume: d!(0.0),
            ratio: d!(1.0),
            percent: d!(0.10),
            dollar_amount: amount,
            capped: false,
        };
        let base = [
            earning(1, d!(0.2)),
            earning(2, d!(0.1)),
            earning(3, d!(0.3)),
        ];
        let pinned: [Dec; 3] = [d!(0.03), d!(0.02), d!(0.05)];
        for order in [
            [0, 1, 2],
            [0, 2, 1],
            [1, 0, 2],
            [1, 2, 0],
            [2, 0, 1],
            [2, 1, 0],
        ] {
            let mut earnings: Vec<_> = order.iter().map(|&i| base[i].clone()).collect();
            apply_aggregate_cap(&mut earnings, d!(0.1), 2).unwrap();
            earnings.sort_by_key(|e| e.position_id);
            let amounts: Vec<Dec> = earnings.iter().map(|e| e.dollar_amount).collect();
            assert_eq!(
                amounts,
                pinned.to_vec(),
                "input order {order:?} gave {amounts:?}"
            );
            assert!(
                earnings.iter().all(|e| e.capped),
                "input order {order:?} left an earning uncapped"
            );
        }
    }

    #[test]
    fn cycle_step_pays_when_ten_ten_plus_twenty_twenty_meets_thirty_thirty() {
        let steps = vec![CycleStep {
            threshold: d!(30.30),
            amount: d!(25),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(10.10),
            },
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(20.20),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(30.30),
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();
        assert_eq!(result.earnings.len(), 1);
        assert_d!(result.earnings[0].dollar_amount, 25);
    }

    #[test]
    fn cycle_step_total_of_ten_ten_plus_twenty_twenty_sits_exactly_at_a_thirty_thirty_cap() {
        let steps = vec![
            CycleStep {
                threshold: d!(100),
                amount: d!(10.10),
            },
            CycleStep {
                threshold: d!(200),
                amount: d!(20.20),
            },
        ];
        let mut structure = test_cycle_step_structure(steps, VolumeAfterPayout::FullFlush);
        if let BinaryCommissionMode::CycleStep(config) = &mut structure.binary_commission.mode {
            config.cap_per_period = Some(d!(30.30));
        }
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(500),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(500),
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();
        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 30.30);
        assert!(!earning.capped);
    }

    #[test]
    fn pairing_of_one_hundred_at_seven_percent_does_not_exceed_a_seven_cap() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(0.07),
                    cap_per_period: Some(d!(7)),
                    ..test_pairing_config()
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(100),
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();
        let earning = &result.earnings[0];
        assert_d!(earning.dollar_amount, 7);
        assert!(!earning.capped);
    }

    #[test]
    fn a_negative_carried_left_leg_is_refused_naming_the_position() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let carry = HashMap::from([(
            test_uuid(1),
            LegVolumes {
                left: d!(-5),
                right: d!(0),
            },
        )]);
        let err = calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &[], &carry, None)
            .unwrap_err();
        assert_eq!(
            err,
            CalculationError::NegativeCarriedLegVolume {
                position: test_uuid(1),
                leg: BinaryLeg::Left,
                volume: d!(-5),
            }
        );
        assert_eq!(
            err.to_string(),
            format!(
                "carried left leg volume for position {} is -5; it must not be negative",
                test_uuid(1)
            )
        );
    }

    #[test]
    fn negative_carried_legs_name_the_lowest_position_and_its_left_leg() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        for _ in 0..32 {
            // Rebuilt inside the loop, so each pass gets a fresh iteration order.
            let carry = HashMap::from([
                (
                    test_uuid(3),
                    LegVolumes {
                        left: d!(-1),
                        right: d!(-2),
                    },
                ),
                (
                    test_uuid(2),
                    LegVolumes {
                        left: d!(-3),
                        right: d!(-4),
                    },
                ),
            ]);
            let err =
                calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &[], &carry, None)
                    .unwrap_err();
            assert_eq!(
                err,
                CalculationError::NegativeCarriedLegVolume {
                    position: test_uuid(2),
                    leg: BinaryLeg::Left,
                    volume: d!(-3),
                }
            );
        }
    }

    #[test]
    fn a_negative_carried_right_leg_is_refused_in_cycle_step_mode() {
        let steps = vec![CycleStep {
            threshold: d!(100),
            amount: d!(10),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let carry = HashMap::from([(
            test_uuid(2),
            LegVolumes {
                left: d!(0),
                right: d!(-0.01),
            },
        )]);
        let err = calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &[], &carry, None)
            .unwrap_err();
        assert!(matches!(
            err,
            CalculationError::NegativeCarriedLegVolume {
                leg: BinaryLeg::Right,
                ..
            }
        ));
    }

    #[test]
    fn a_negative_carried_leg_on_a_removed_position_is_refused() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let carry = HashMap::from([(
            test_uuid(99),
            LegVolumes {
                left: d!(-1),
                right: d!(0),
            },
        )]);
        let err = calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &[], &carry, None)
            .unwrap_err();
        assert!(matches!(
            err,
            CalculationError::NegativeCarriedLegVolume {
                leg: BinaryLeg::Left,
                ..
            }
        ));
    }

    #[test]
    fn a_zero_carried_leg_is_accepted() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let carry = HashMap::from([(
            test_uuid(1),
            LegVolumes {
                left: d!(0),
                right: d!(0),
            },
        )]);
        assert!(
            calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &[], &carry, None)
                .is_ok()
        );
    }

    fn one_third_ratio_payout(cap: Option<AuthoredDec>) -> BinaryCalculationResult {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(0.3),
                    calculation: PairingCalculation::VolumeRatio,
                    cap_per_period: cap,
                    ..test_pairing_config()
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(300),
            },
        ];
        calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap()
    }

    #[test]
    fn a_one_third_ratio_payout_divides_last() {
        for cap in [None, Some(d!(10.01)), Some(d!(10))] {
            let earning = &one_third_ratio_payout(cap).earnings[0];
            assert_eq!(earning.ratio, d!(0.3333333333333333333333333333));
            assert_eq!(earning.dollar_amount, d!(10), "cap {cap:?}");
            assert!(!earning.capped, "cap {cap:?}");
        }
    }

    #[test]
    fn a_one_third_ratio_payout_just_over_the_cap_is_capped() {
        let earning = &one_third_ratio_payout(Some(d!(9.99))).earnings[0];
        assert_eq!(earning.ratio, d!(0.3333333333333333333333333333));
        assert_eq!(earning.dollar_amount, d!(9.99));
        assert!(earning.capped);
    }

    #[test]
    fn a_one_third_ratio_payout_gives_the_same_bytes_every_run() {
        let first = serde_json::to_string(&one_third_ratio_payout(Some(d!(10))).earnings).unwrap();
        for _ in 0..20 {
            assert_eq!(
                serde_json::to_string(&one_third_ratio_payout(Some(d!(10))).earnings).unwrap(),
                first
            );
        }
    }

    #[test]
    fn weaker_leg_reports_a_ratio_of_exactly_one() {
        let structure = test_binary_structure();
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: d!(300),
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();
        assert_eq!(result.earnings[0].ratio, Dec::ONE);
    }

    mod aggregate_cap_split {
        use super::*;
        use crate::test_strategies::cents;
        use proptest::prelude::*;

        fn earning(owner: u8, position: u8, amount: Dec) -> BinaryCommissionEarning {
            BinaryCommissionEarning {
                earner_id: test_uuid(owner),
                position_id: test_uuid(position),
                left_volume: Dec::ZERO,
                right_volume: Dec::ZERO,
                matched_volume: Dec::ZERO,
                ratio: Dec::ONE,
                percent: Dec::ZERO,
                dollar_amount: amount,
                capped: false,
            }
        }

        fn amounts(earnings: &[BinaryCommissionEarning]) -> Vec<Dec> {
            earnings.iter().map(|e| e.dollar_amount).collect()
        }

        #[test]
        fn three_earnings_of_one_split_a_cap_of_two_by_largest_remainder() {
            let mut earnings = vec![
                earning(1, 2, d!(1)),
                earning(1, 3, d!(1)),
                earning(1, 4, d!(1)),
            ];
            apply_aggregate_cap(&mut earnings, d!(2), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.67), d!(0.67), d!(0.66)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn three_equal_shares_of_a_two_cent_cap_give_one_one_and_zero() {
            let mut earnings = vec![
                earning(1, 2, d!(1)),
                earning(1, 3, d!(1)),
                earning(1, 4, d!(1)),
            ];
            apply_aggregate_cap(&mut earnings, d!(0.02), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.01), d!(0.01), d!(0)]);
        }

        #[test]
        fn a_raw_total_below_the_cap_whose_rounding_exceeds_it_is_allocated() {
            let mut earnings = vec![
                earning(1, 2, d!(0.005)),
                earning(1, 3, d!(0.005)),
                earning(1, 4, d!(0.005)),
            ];
            apply_aggregate_cap(&mut earnings, d!(0.02), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.01), d!(0.01), d!(0)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn a_raw_total_exactly_at_the_cap_whose_rounding_exceeds_it_is_allocated() {
            let mut earnings = vec![
                earning(1, 2, d!(0.005)),
                earning(1, 3, d!(0.005)),
                earning(1, 4, d!(0.010)),
            ];
            apply_aggregate_cap(&mut earnings, d!(0.02), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.01), d!(0), d!(0.01)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn unequal_shares_over_the_cap_go_to_the_largest_remainders() {
            let mut earnings = vec![
                earning(1, 2, d!(0.70)),
                earning(1, 3, d!(0.20)),
                earning(1, 4, d!(0.13)),
            ];
            apply_aggregate_cap(&mut earnings, d!(1.00), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.68), d!(0.19), d!(0.13)]);
        }

        #[test]
        fn unequal_shares_under_the_cap_whose_rounding_exceeds_it_are_allocated() {
            let mut earnings = vec![
                earning(1, 2, d!(0.335)),
                earning(1, 3, d!(0.335)),
                earning(1, 4, d!(0.325)),
            ];
            apply_aggregate_cap(&mut earnings, d!(1.00), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.34), d!(0.34), d!(0.32)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn a_raw_total_over_the_cap_is_allocated_even_when_its_rounded_sum_would_overflow() {
            let mut earnings = vec![
                earning(1, 2, d!(0.005)),
                earning(1, 3, d!(0.995)),
                earning(1, 4, d!(10000000000000000000000000000)),
            ];
            apply_aggregate_cap(&mut earnings, d!(1), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0), d!(0), d!(1)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn a_raw_total_over_the_cap_whose_rounding_fits_is_allocated() {
            let mut earnings = vec![
                earning(1, 2, d!(0.674)),
                earning(1, 3, d!(0.674)),
                earning(1, 4, d!(0.674)),
            ];
            apply_aggregate_cap(&mut earnings, d!(2.02), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0.68), d!(0.67), d!(0.67)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn a_zero_earning_under_a_zero_cap_is_rounded_not_capped() {
            let mut earnings = vec![earning(1, 2, d!(0))];
            apply_aggregate_cap(&mut earnings, d!(0), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0)]);
            assert!(!earnings[0].capped);
        }

        #[test]
        fn a_group_whose_raw_total_does_not_fit_a_dec_is_allocated() {
            let mut earnings = vec![
                earning(1, 2, d!(0.005)),
                earning(1, 3, d!(10000000000000000000000000000)),
            ];
            apply_aggregate_cap(&mut earnings, d!(1), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(0), d!(1)]);
            assert!(earnings.iter().all(|e| e.capped));
        }

        #[test]
        fn totals_whose_rounding_stays_within_the_cap_are_only_rounded() {
            let mut earnings = vec![earning(1, 2, d!(1.004)), earning(1, 3, d!(0.985))];
            apply_aggregate_cap(&mut earnings, d!(2), 2).unwrap();
            assert_eq!(amounts(&earnings), vec![d!(1), d!(0.99)]);
            assert!(earnings.iter().all(|e| !e.capped));
        }

        #[test]
        fn a_cap_at_the_envelope_edge_splits_exactly() {
            let cap: Dec = d!(999999999999999.99);
            let mut earnings = vec![
                earning(1, 2, d!(600000000000000)),
                earning(1, 3, d!(500000000000000.5)),
                earning(1, 4, d!(123456789012345.67)),
            ];
            apply_aggregate_cap(&mut earnings, cap, 2).unwrap();
            assert_eq!(Dec::sum(amounts(&earnings)).unwrap(), cap);
            assert!(earnings.iter().all(|e| !e.dollar_amount.is_negative()));
        }

        /// A cent amount, or a cent amount divided by three to 13 places.
        fn share_amount() -> impl Strategy<Value = Dec> {
            prop_oneof![
                cents::<Dec>(0, 10_000_000),
                cents::<Dec>(0, 10_000_000).prop_map(|c| {
                    let three: Dec = d!(3);
                    c.mul_div_ratio(Dec::ONE, Dec::ONE, Dec::ONE, three)
                        .unwrap()
                }),
            ]
        }

        proptest! {
            #[test]
            fn each_earners_capped_shares_sum_exactly_to_the_cap(
                amounts in prop::collection::vec((0u8..4, share_amount()), 1..12),
                cap in cents::<Dec>(0, 5_000_000),
            ) {
                let mut earnings: Vec<_> = amounts
                    .iter()
                    .enumerate()
                    .map(|(i, (owner, amount))| earning(*owner + 1, 100 + i as u8, *amount))
                    .collect();
                let mut raw: HashMap<Uuid, Vec<Dec>> = HashMap::new();
                for e in &earnings {
                    raw.entry(e.earner_id).or_default().push(e.dollar_amount);
                }
                let raw_by_position: HashMap<Uuid, Dec> =
                    earnings.iter().map(|e| (e.position_id, e.dollar_amount)).collect();
                apply_aggregate_cap(&mut earnings, cap, 2).unwrap();
                for (owner, raw_amounts) in raw {
                    let raw_total = Dec::sum(raw_amounts.iter().copied()).unwrap();
                    let rounded_total =
                        Dec::sum(raw_amounts.iter().map(|a| a.round_minor(2))).unwrap();
                    let shares: Vec<_> = earnings.iter().filter(|e| e.earner_id == owner).collect();
                    let total = Dec::sum(shares.iter().map(|e| e.dollar_amount)).unwrap();
                    prop_assert!(shares.iter().all(|e| !e.dollar_amount.is_negative()));
                    prop_assert!(shares.iter().all(|e| e.dollar_amount.round_minor(2) == e.dollar_amount));
                    if raw_total > cap || rounded_total > cap {
                        prop_assert_eq!(total, cap);
                        prop_assert!(shares.iter().all(|e| e.capped));
                        let unit: Dec = d!(0.01);
                        for e in &shares {
                            let floor = raw_by_position[&e.position_id]
                                .mul_div_trunc(cap, raw_total, 2)
                                .unwrap();
                            prop_assert!(
                                e.dollar_amount == floor
                                    || e.dollar_amount == floor.try_add(unit).unwrap()
                            );
                        }
                    } else {
                        for e in &shares {
                            prop_assert_eq!(
                                e.dollar_amount,
                                raw_by_position[&e.position_id].round_minor(2)
                            );
                        }
                        prop_assert!(shares.iter().all(|e| !e.capped));
                    }
                }
            }
        }
    }

    fn volume_ratio_payout(
        percent: AuthoredDec,
        multiplier: Option<AuthoredDec>,
        left: AuthoredDec,
        right: AuthoredDec,
    ) -> Dec {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: multiplier,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent,
                    calculation: PairingCalculation::VolumeRatio,
                    ..test_pairing_config()
                }),
            },
        };
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let snapshots = HashMap::from([
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(2), eligible_snapshot()),
            (test_uuid(3), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: left,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: right,
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            None,
        )
        .unwrap();
        result.earnings[0].dollar_amount
    }

    #[test]
    fn pairing_names_the_same_failing_position_on_every_run() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: Some(d!(20)),
                mode: BinaryCommissionMode::Pairing(test_pairing_config()),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let mut seen = std::collections::BTreeSet::new();
        // Rebuilt inside the loop so each pass samples a fresh HashMap order.
        for _ in 0..32 {
            let tree = three_node_tree();
            let snapshots = HashMap::from([
                (test_uuid(1), eligible_snapshot()),
                (test_uuid(2), eligible_snapshot()),
                (test_uuid(3), eligible_snapshot()),
            ]);
            let carry_forward = HashMap::from([
                (
                    test_uuid(2),
                    LegVolumes {
                        left: d!(70000000000000000000000000000),
                        right: d!(70000000000000000000000000000),
                    },
                ),
                (
                    test_uuid(3),
                    LegVolumes {
                        left: d!(60000000000000000000000000000),
                        right: d!(60000000000000000000000000000),
                    },
                ),
            ]);
            let err = calculate_binary_pairing(
                &tree,
                &plan,
                &structure,
                &snapshots,
                &[],
                &carry_forward,
                None,
            )
            .unwrap_err();
            seen.insert(err.to_string());
        }
        assert_eq!(seen.len(), 1, "distinct errors across runs: {seen:?}");
        let only = seen.first().unwrap();
        assert!(only.contains("7000000000000000000000000000"), "{only}");
    }

    #[test]
    fn leg_accumulation_names_the_same_failing_position_on_every_run() {
        let structure = test_binary_structure();
        let plan = test_plan(default_eligibility());
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(1),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(2),
            },
        ];
        let mut seen = std::collections::BTreeSet::new();
        // Rebuilt inside the loop so each pass samples a fresh HashMap order.
        for _ in 0..32 {
            let mut tree = BinaryTree::new();
            tree.add_root(test_uuid(1), 1000).unwrap();
            tree.add_node(test_uuid(2), test_uuid(1), 0, test_uuid(1), 2000)
                .unwrap();
            tree.add_node(test_uuid(3), test_uuid(1), 1, test_uuid(1), 3000)
                .unwrap();
            tree.add_node(test_uuid(4), test_uuid(2), 0, test_uuid(2), 4000)
                .unwrap();
            tree.add_node(test_uuid(5), test_uuid(3), 0, test_uuid(3), 5000)
                .unwrap();
            let snapshots: HashMap<Uuid, DistributorSnapshot> = (1..=5)
                .map(|i| (test_uuid(i), eligible_snapshot()))
                .collect();
            let carry_forward = HashMap::from([
                (
                    test_uuid(2),
                    LegVolumes {
                        left: d!(79228162514264337593543950335),
                        right: Dec::ZERO,
                    },
                ),
                (
                    test_uuid(3),
                    LegVolumes {
                        left: d!(79228162514264337593543950334),
                        right: Dec::ZERO,
                    },
                ),
            ]);
            let err = calculate_binary_pairing(
                &tree,
                &plan,
                &structure,
                &snapshots,
                &volume,
                &carry_forward,
                None,
            )
            .unwrap_err();
            seen.insert(err.to_string());
        }
        assert_eq!(seen.len(), 1, "distinct errors across runs: {seen:?}");
        let only = seen.first().unwrap();
        assert!(only.contains("79228162514264337593543950335"), "{only}");
    }

    #[test]
    fn a_volume_ratio_payout_is_rounded_to_a_cent() {
        assert_d!(volume_ratio_payout(d!(0.10), None, d!(1), d!(3)), 0.03);
    }

    #[test]
    fn a_cap_that_is_not_whole_cents_is_refused() {
        let tree = three_node_tree();
        let structure = capped_structure(d!(2.005));
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let mut snapshots = HashMap::new();
        for n in 1..=3 {
            snapshots.insert(test_uuid(n), eligible_snapshot());
        }
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap_err();
        assert_eq!(
            err,
            CalculationError::CapNotWholeMinorUnits {
                cap: d!(2.005),
                currency: "USD".to_string()
            }
        );
    }

    #[test]
    fn a_cycle_step_cap_that_is_not_whole_cents_is_refused() {
        let tree = three_node_tree();
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let mut structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        if let BinaryCommissionMode::CycleStep(cfg) = &mut structure.binary_commission.mode {
            cfg.cap_per_period = Some(d!(2.005));
        }
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &HashMap::new(),
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap_err();
        assert_eq!(
            err,
            CalculationError::CapNotWholeMinorUnits {
                cap: d!(2.005),
                currency: "USD".to_string()
            }
        );
    }

    #[test]
    fn a_volume_ratio_payout_rounds_from_its_thirteen_place_value() {
        // The exact payout is 0.004999999999975…, just under half a cent.
        assert_d!(
            volume_ratio_payout(d!(0.10), None, d!(1), d!(20.0000000001)),
            0.01
        );
    }

    #[test]
    fn a_negative_per_position_cap_is_refused() {
        let tree = three_node_tree();
        let structure = capped_structure(d!(-1));
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &HashMap::new(),
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap_err();
        assert_eq!(err, CalculationError::NegativeCap(d!(-1)));
    }

    #[test]
    fn a_negative_aggregate_cap_is_refused() {
        let tree = multi_position_tree();
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    cap_per_period: Some(d!(-1)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let ownership = HashMap::from([(test_uuid(2), test_uuid(100))]);
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &HashMap::new(),
            &[],
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap_err();
        assert_eq!(err, CalculationError::NegativeCap(d!(-1)));
    }

    #[test]
    fn an_unknown_plan_currency_is_refused_by_binary_cycle_step() {
        let tree = three_node_tree();
        let steps = vec![CycleStep {
            threshold: d!(300.0),
            amount: d!(25.0),
        }];
        let structure = test_cycle_step_structure(steps, VolumeAfterPayout::CarryForward);
        let mut plan = test_plan_with_structure(default_eligibility(), structure.clone());
        plan.volume.base_currency = "ZZZ".to_string();
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &HashMap::new(),
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap_err();
        assert_eq!(err, CalculationError::UnknownCurrency("ZZZ".to_string()));
    }

    #[test]
    fn an_unknown_plan_currency_is_refused_by_binary_pairing() {
        let tree = three_node_tree();
        let mut plan = test_plan(default_eligibility());
        plan.volume.base_currency = "ZZZ".to_string();
        let err = calculate_binary_pairing(
            &tree,
            &plan,
            &test_binary_structure(),
            &HashMap::new(),
            &[],
            &HashMap::new(),
            None,
        )
        .unwrap_err();
        assert_eq!(err, CalculationError::UnknownCurrency("ZZZ".to_string()));
    }

    #[test]
    fn volume_ratio_pays_legs_of_nine_hundred_trillion_at_ten_percent() {
        let paid = volume_ratio_payout(
            d!(0.1),
            None,
            d!(900_000_000_000_000),
            d!(900_000_000_000_000),
        );
        assert_d!(paid, 90_000_000_000_000);
    }

    #[test]
    fn volume_ratio_pays_a_ten_place_percent_and_multiplier() {
        let paid = volume_ratio_payout(
            d!(0.0333333333),
            Some(d!(0.8333333333)),
            d!(1700.37),
            d!(2500.11),
        );
        assert_d!(paid, 32.12);
    }

    #[test]
    fn aggregate_total_of_one_hundred_at_seven_percent_does_not_exceed_a_seven_cap() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(0.07),
                    cap_per_period: Some(d!(7)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let tree = multi_position_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let owner = test_uuid(100);
        let ownership = HashMap::from([(test_uuid(2), owner), (test_uuid(3), owner)]);
        let snapshots = HashMap::from([
            (owner, eligible_snapshot()),
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(4), eligible_snapshot()),
            (test_uuid(5), eligible_snapshot()),
            (test_uuid(6), eligible_snapshot()),
            (test_uuid(7), eligible_snapshot()),
        ]);
        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: d!(100),
            },
            VolumeSource {
                source_id: test_uuid(5),
                cv_amount: d!(100),
            },
        ];
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();
        let earning = result
            .earnings
            .iter()
            .find(|e| e.position_id == test_uuid(2))
            .expect("position 2 matches 100 against 100");
        assert_d!(earning.dollar_amount, 7);
        assert!(!earning.capped);
    }

    #[test]
    fn two_non_terminating_volume_ratio_payouts_add_under_one_aggregate_owner() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    percent: d!(1),
                    calculation: PairingCalculation::VolumeRatio,
                    cap_per_period: Some(d!(100)),
                    multi_position_cap_mode: MultiPositionCapMode::Aggregate,
                    ..test_pairing_config()
                }),
            },
        };
        let tree = multi_position_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());
        let owner = test_uuid(100);
        let ownership = HashMap::from([(test_uuid(2), owner), (test_uuid(3), owner)]);
        let snapshots = HashMap::from([
            (owner, eligible_snapshot()),
            (test_uuid(1), eligible_snapshot()),
            (test_uuid(4), eligible_snapshot()),
            (test_uuid(5), eligible_snapshot()),
            (test_uuid(6), eligible_snapshot()),
            (test_uuid(7), eligible_snapshot()),
        ]);
        let volume = [(4, d!(10)), (5, d!(30)), (6, d!(20)), (7, d!(30))]
            .map(|(node, cv_amount)| VolumeSource {
                source_id: test_uuid(node),
                cv_amount,
            })
            .to_vec();
        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
            Some(&ownership),
        )
        .unwrap();
        let mut owned: Vec<_> = result
            .earnings
            .iter()
            .filter(|e| e.earner_id == owner)
            .map(|e| (e.position_id, e.dollar_amount, e.capped))
            .collect();
        owned.sort();
        assert_eq!(
            owned,
            [
                (test_uuid(2), d!(3.33), false),
                (test_uuid(3), d!(13.33), false),
            ]
        );
        assert_eq!(
            Dec::sum(owned.iter().map(|(_, amount, _)| *amount)).unwrap(),
            d!(16.66)
        );
    }
}
