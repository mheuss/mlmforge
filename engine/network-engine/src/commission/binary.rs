//! Binary pairing commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::binary::{BinaryCommissionMode, PairingCalculation, VolumeAfterPayout};
use crate::config::eligibility::CommissionEligibility;
use crate::config::{BinaryStructureConfig, CompensationPlan};
use crate::tree::binary::BinaryTree;
use crate::tree::node::NodeIndex;

use super::is_eligible;
use super::types::{
    BinaryCalculationResult, BinaryCommissionEarning, CalculationError, DistributorSnapshot,
    LegVolumes, VolumeSource,
};

/// Phase 1, Step 1: Aggregate volume sources into per-distributor totals.
fn aggregate_volume(
    tree: &BinaryTree,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<HashMap<Uuid, f64>, CalculationError> {
    let mut totals: HashMap<Uuid, f64> = HashMap::new();

    for source in volume {
        if !source.cv_amount.is_finite() || source.cv_amount < 0.0 {
            return Err(CalculationError::InvalidCvAmount(
                source.source_id,
                source.cv_amount,
            ));
        }

        // Validate source exists in tree.
        if !tree.contains(source.source_id) {
            return Err(CalculationError::SourceNotInTree(source.source_id));
        }

        // Validate source exists in snapshots.
        if !snapshots.contains_key(&source.source_id) {
            return Err(CalculationError::SourceNotInSnapshot(source.source_id));
        }

        *totals.entry(source.source_id).or_insert(0.0) += source.cv_amount;
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
    volume_totals: &HashMap<Uuid, f64>,
    carry_forward: &HashMap<Uuid, LegVolumes>,
) -> HashMap<Uuid, LegVolumes> {
    let arena = tree.arena();
    let slots = tree.slots();

    // Build depth-sorted list of all live nodes (deepest first).
    let mut nodes_by_depth: Vec<(NodeIndex, Uuid, u32)> = arena
        .index
        .iter()
        .map(|(uid, &idx)| (idx, *uid, arena.node(idx).depth))
        .collect();
    nodes_by_depth.sort_by(|a, b| b.2.cmp(&a.2));

    // Subtree totals: personal volume + children's subtree totals.
    let mut subtree_totals: HashMap<NodeIndex, f64> = HashMap::new();

    for &(idx, uid, _depth) in &nodes_by_depth {
        let personal = volume_totals.get(&uid).copied().unwrap_or(0.0);

        let children = &arena.node(idx).children;
        let child_sum: f64 = children
            .iter()
            .map(|c| subtree_totals.get(c).copied().unwrap_or(0.0))
            .sum();

        subtree_totals.insert(idx, personal + child_sum);
    }

    // Compute per-distributor leg volumes from slot positions.
    let mut leg_volumes: HashMap<Uuid, LegVolumes> = HashMap::new();

    for &(idx, uid, _depth) in &nodes_by_depth {
        let node_slots = slots.get(&idx).copied().unwrap_or([None, None]);

        let left_subtree = node_slots[0]
            .map(|c| subtree_totals.get(&c).copied().unwrap_or(0.0))
            .unwrap_or(0.0);

        let right_subtree = node_slots[1]
            .map(|c| subtree_totals.get(&c).copied().unwrap_or(0.0))
            .unwrap_or(0.0);

        let prior = carry_forward.get(&uid);
        let left = left_subtree + prior.map(|p| p.left).unwrap_or(0.0);
        let right = right_subtree + prior.map(|p| p.right).unwrap_or(0.0);

        leg_volumes.insert(uid, LegVolumes { left, right });
    }

    leg_volumes
}

/// Calculate binary pairing commissions for a commission period.
///
/// Pure function. Takes a binary tree, config, distributor snapshots,
/// volume events, and prior carry-forward state. Returns earnings and
/// updated carry-forward state.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data, or has an invalid cv_amount.
pub fn calculate_binary_pairing(
    tree: &BinaryTree,
    plan: &CompensationPlan,
    structure: &BinaryStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    carry_forward: &HashMap<Uuid, LegVolumes>,
) -> Result<BinaryCalculationResult, CalculationError> {
    let pairing = match &structure.binary_commission.mode {
        BinaryCommissionMode::Pairing(config) => {
            debug_assert!(
                (0.0..=1.0).contains(&config.percent),
                "pairing.percent out of range: {}",
                config.percent
            );
            if !(0.0..=1.0).contains(&config.percent) {
                log::warn!(
                    "pairing percent {} is outside [0.0, 1.0]; commissions may be overstated",
                    config.percent
                );
            }
            config
        }
        BinaryCommissionMode::CycleStep(_) => {
            // CycleStep mode is not yet implemented. Returns an empty result
            // so callers receive a valid response rather than an error.
            // Tracked in BUGS_AND_TODOS.md as a deferred feature.
            log::warn!(
                "CycleStep binary commission mode is not yet implemented; returning empty result"
            );
            return Ok(BinaryCalculationResult {
                earnings: Vec::new(),
                carry_forward: HashMap::new(),
            });
        }
    };

    let multiplier = structure
        .binary_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    // Phase 1: Prep
    let volume_totals = aggregate_volume(tree, snapshots, volume)?;
    let eligibility_cache = evaluate_eligibility(snapshots, &plan.eligibility);
    let working_legs = accumulate_leg_volumes(tree, &volume_totals, carry_forward);

    // Phase 2: Calculate
    let mut earnings = Vec::new();

    for (uid, legs) in &working_legs {
        let eligible = eligibility_cache.get(uid).copied().unwrap_or(false);
        if !eligible {
            continue;
        }

        let matched = legs.left.min(legs.right);
        if matched == 0.0 {
            continue;
        }

        let (min_leg, max_leg) = if legs.left <= legs.right {
            (legs.left, legs.right)
        } else {
            (legs.right, legs.left)
        };

        let ratio = match pairing.calculation {
            PairingCalculation::WeakerLeg => 1.0,
            PairingCalculation::VolumeRatio => {
                if max_leg > 0.0 {
                    min_leg / max_leg
                } else {
                    0.0
                }
            }
        };

        let raw_amount = matched * pairing.percent * multiplier * ratio;

        let (dollar_amount, capped) = match pairing.cap_per_period {
            Some(cap) if raw_amount > cap => (cap, true),
            _ => (raw_amount, false),
        };

        earnings.push(BinaryCommissionEarning {
            earner_id: *uid,
            left_volume: legs.left,
            right_volume: legs.right,
            matched_volume: matched,
            ratio,
            percent: pairing.percent,
            dollar_amount,
            capped,
        });
    }

    // Phase 3: Post-payout carry-forward
    let mut new_carry_forward = HashMap::new();

    for (uid, legs) in &working_legs {
        let matched = legs.left.min(legs.right);
        let eligible = eligibility_cache.get(uid).copied().unwrap_or(false);

        // Non-eligible distributors: nothing was matched/paid, so
        // matched is effectively 0 for carry-forward purposes.
        let effective_matched = if eligible { matched } else { 0.0 };

        let (new_left, new_right) = match pairing.volume_after_payout {
            VolumeAfterPayout::FullFlush => (0.0, 0.0),
            VolumeAfterPayout::NetOff => (
                legs.left - effective_matched,
                legs.right - effective_matched,
            ),
            VolumeAfterPayout::CarryForward => {
                if legs.left <= legs.right {
                    // Left is weaker
                    (0.0, legs.right - effective_matched)
                } else {
                    // Right is weaker
                    (legs.left - effective_matched, 0.0)
                }
            }
        };

        // Apply carry_forward_cap if set.
        let (capped_left, capped_right) = match pairing.carry_forward_cap {
            Some(cap) => (new_left.min(cap), new_right.min(cap)),
            None => (new_left, new_right),
        };

        new_carry_forward.insert(
            *uid,
            LegVolumes {
                left: capped_left.max(0.0),
                right: capped_right.max(0.0),
            },
        );
    }

    // Sort earnings by earner_id for deterministic output.
    // HashMap iteration order is unstable, so without sorting the
    // earnings order varies across runs.
    earnings.sort_by(|a, b| a.earner_id.cmp(&b.earner_id));

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
        PairingCalculation, PairingConfig, VolumeAfterPayout,
    };
    use crate::config::{BinaryStructureConfig, CompensationPlan, StructureConfig};
    use crate::tree::binary::BinaryTree;
    use crate::tree::test_helpers::test_uuid;

    fn test_pairing_config() -> PairingConfig {
        PairingConfig {
            percent: 0.10,
            calculation: PairingCalculation::WeakerLeg,
            cap_per_period: None,
            volume_after_payout: VolumeAfterPayout::FullFlush,
            carry_forward_cap: None,
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // Root should earn: matched=500, ratio=1.0, 500 * 0.10 * 1.0 * 1.0 = 50.0
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.earner_id, test_uuid(1));
        assert_eq!(earning.left_volume, 500.0);
        assert_eq!(earning.right_volume, 500.0);
        assert_eq!(earning.matched_volume, 500.0);
        assert_eq!(earning.ratio, 1.0);
        assert_eq!(earning.percent, 0.10);
        assert_eq!(earning.dollar_amount, 50.0);
        assert!(!earning.capped);

        // FullFlush: all carry-forward should be zero.
        for legs in result.carry_forward.values() {
            assert_eq!(legs.left, 0.0);
            assert_eq!(legs.right, 0.0);
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
                cv_amount: 800.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 200.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.matched_volume, 200.0);
        assert_eq!(earning.dollar_amount, 20.0); // 200 * 0.10 * 1.0
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
                personal_volume: 0.0, // Below 100 PV minimum
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());
        snapshots.insert(test_uuid(4), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(4),
                cv_amount: 300.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 300.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // Root should see left=300 (from node 4, through ineligible node 2),
        // right=300. Earns 300 * 0.10 = 30.
        let root_earning = result
            .earnings
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("root should earn");
        assert_eq!(root_earning.left_volume, 300.0);
        assert_eq!(root_earning.right_volume, 300.0);
        assert_eq!(root_earning.dollar_amount, 30.0);

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
            cv_amount: 500.0,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
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
                cv_amount: 200.0,
            },
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 300.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 400.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // Left = 200 + 300 = 500, Right = 400. Matched = 400.
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.left_volume, 500.0);
        assert_eq!(earning.right_volume, 400.0);
        assert_eq!(earning.matched_volume, 400.0);
        assert_eq!(earning.dollar_amount, 40.0); // 400 * 0.10
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.ratio, 1.0);
        // 500 * 0.10 * 1.0 * 1.0 = 50.0
        assert_eq!(earning.dollar_amount, 50.0);
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
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 400.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.ratio, 0.25);
        // matched=100, 100 * 0.10 * 1.0 * 0.25 = 2.5
        assert_eq!(earning.dollar_amount, 2.5);
    }

    // --- Cap enforcement tests ---

    fn capped_structure(cap: f64) -> BinaryStructureConfig {
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
        let structure = capped_structure(1000.0);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let earning = &result.earnings[0];
        assert_eq!(earning.dollar_amount, 50.0); // 500 * 0.10, well under 1000 cap
        assert!(!earning.capped);
    }

    #[test]
    fn over_cap_clamped() {
        let structure = capped_structure(25.0);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let earning = &result.earnings[0];
        // raw_amount would be 50.0, capped to 25.0
        assert_eq!(earning.dollar_amount, 25.0);
        assert!(earning.capped);
    }

    #[test]
    fn at_exact_cap_not_flagged_as_capped() {
        let structure = capped_structure(50.0);
        let tree = three_node_tree();
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let earning = &result.earnings[0];
        // raw_amount = 50.0, cap = 50.0. Not exceeded, so not capped.
        assert_eq!(earning.dollar_amount, 50.0);
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
                cv_amount: 800.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 300.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // NetOff: left = 800 - 300 = 500, right = 300 - 300 = 0
        assert_eq!(root_cf.left, 500.0);
        assert_eq!(root_cf.right, 0.0);
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
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
        )
        .unwrap();

        // Node 2 is a leaf. Legs are both 0. No matched volume.
        // NetOff: 0-0=0, 0-0=0. Carry forward is zero.
        let node2_cf = result.carry_forward.get(&test_uuid(2)).unwrap();
        assert_eq!(node2_cf.left, 0.0);
        assert_eq!(node2_cf.right, 0.0);
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
                cv_amount: 800.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 300.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // CarryForward: weaker (right) zeroed, stronger (left) keeps excess.
        // left = 800 - 300 = 500, right = 0
        assert_eq!(root_cf.left, 500.0);
        assert_eq!(root_cf.right, 0.0);
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // Balanced: left is "weaker" (or equal). Both zero after payout.
        assert_eq!(root_cf.left, 0.0);
        assert_eq!(root_cf.right, 0.0);
    }

    #[test]
    fn carry_forward_cap_clamps_excess() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::Pairing(PairingConfig {
                    volume_after_payout: VolumeAfterPayout::CarryForward,
                    carry_forward_cap: Some(200.0),
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
                cv_amount: 1000.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 200.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        let root_cf = result.carry_forward.get(&test_uuid(1)).unwrap();
        // CarryForward: left excess = 1000-200 = 800, capped to 200.
        assert_eq!(root_cf.left, 200.0);
        assert_eq!(root_cf.right, 0.0);
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
                cv_amount: 200.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 100.0,
            },
        ];

        // Prior carry-forward: root had left=300, right=400.
        let mut prior_cf = HashMap::new();
        prior_cf.insert(
            test_uuid(1),
            LegVolumes {
                left: 300.0,
                right: 400.0,
            },
        );

        let result =
            calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &volume, &prior_cf)
                .unwrap();

        // Root legs: left = 200 + 300 = 500, right = 100 + 400 = 500.
        assert_eq!(result.earnings.len(), 1);
        let earning = &result.earnings[0];
        assert_eq!(earning.left_volume, 500.0);
        assert_eq!(earning.right_volume, 500.0);
        assert_eq!(earning.matched_volume, 500.0);
        assert_eq!(earning.dollar_amount, 50.0);
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
            cv_amount: 500.0,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
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
            cv_amount: 500.0,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        );

        assert!(matches!(
            result,
            Err(CalculationError::SourceNotInSnapshot(_))
        ));
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
            cv_amount: -100.0,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        );

        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    #[test]
    fn nan_cv_amount_returns_error() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: f64::NAN,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
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
            cv_amount: 500.0,
        }];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
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
                cv_amount: 400.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 400.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // Root: left leg includes node 6's volume (flows up through 4, 2).
        // left=400, right=400. Matched=400.
        let root_earning = result
            .earnings
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("root should earn");
        assert_eq!(root_earning.left_volume, 400.0);
        assert_eq!(root_earning.right_volume, 400.0);
        assert_eq!(root_earning.dollar_amount, 40.0);
    }

    #[test]
    fn distributor_in_tree_but_not_in_snapshot_treated_as_ineligible() {
        let tree = three_node_tree();
        let plan = test_plan(default_eligibility());
        let structure = test_binary_structure();

        // Only root has a snapshot. Children are in tree but not in snapshots.
        // No volume sources reference missing-snapshot nodes, so no error.
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &[], // No volume
            &HashMap::new(),
        )
        .unwrap();

        // No volume, no earnings. Should not error.
        assert!(result.earnings.is_empty());
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        // Carry-forward for a node not in the tree — should be silently ignored.
        let mut prior_cf = HashMap::new();
        prior_cf.insert(
            test_uuid(99),
            LegVolumes {
                left: 9999.0,
                right: 9999.0,
            },
        );

        let result =
            calculate_binary_pairing(&tree, &plan, &structure, &snapshots, &volume, &prior_cf)
                .unwrap();

        // Should not crash. Result should be same as without carry-forward.
        assert_eq!(result.earnings.len(), 1);
        assert_eq!(result.earnings[0].dollar_amount, 50.0);

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
                personal_volume: 50.0, // below 100 minimum
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
                cv_amount: 0.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 0.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // cv_amount=0.0 is valid. Zero matched volume produces no earnings.
        assert!(
            result.earnings.is_empty() || result.earnings.iter().all(|e| e.dollar_amount == 0.0),
            "zero CV should produce zero or empty earnings"
        );
    }

    // --- CycleStep mode tests ---

    #[test]
    fn cycle_step_mode_returns_empty_result() {
        let structure = BinaryStructureConfig {
            name: "Test Binary".to_string(),
            binary_commission: BinaryCommissionConfig {
                volume_to_dollar_multiplier: None,
                mode: BinaryCommissionMode::CycleStep(CycleStepConfig {
                    steps: vec![CycleStep {
                        threshold: 300.0,
                        amount: 25.0,
                    }],
                    flush_after_cycle: true,
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
                cv_amount: 500.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 500.0,
            },
        ];

        let result = calculate_binary_pairing(
            &tree,
            &plan,
            &structure,
            &snapshots,
            &volume,
            &HashMap::new(),
        )
        .unwrap();

        // CycleStep mode is not yet implemented. It returns an empty result.
        assert!(result.earnings.is_empty());
        assert!(result.carry_forward.is_empty());
    }

    // --- Property-based tests ---

    mod prop_tests {
        use super::*;
        use proptest::prelude::*;

        /// Generate a random volume amount (positive, finite).
        fn arb_volume() -> impl Strategy<Value = f64> {
            0.0..10000.0f64
        }

        /// Generate a pair of leg volumes for a balanced-to-unbalanced tree.
        fn arb_leg_pair() -> impl Strategy<Value = (f64, f64)> {
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
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
                ).unwrap();

                let total_payout: f64 = result.earnings.iter().map(|e| e.dollar_amount).sum();
                let matched = left_vol.min(right_vol);
                let theoretical_max = matched * 0.10 * 1.0; // percent * multiplier

                prop_assert!(
                    total_payout <= theoretical_max + 1e-10,
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
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert!(legs.left >= 0.0, "left carry-forward is negative: {}", legs.left);
                    prop_assert!(legs.right >= 0.0, "right carry-forward is negative: {}", legs.right);
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
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert_eq!(legs.left, 0.0);
                    prop_assert_eq!(legs.right, 0.0);
                }
            }

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
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
                let cap = 500.0;
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
                    &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
                ).unwrap();

                for legs in result.carry_forward.values() {
                    prop_assert!(
                        legs.left <= cap + 1e-10,
                        "left carry-forward {} exceeded cap {}",
                        legs.left, cap
                    );
                    prop_assert!(
                        legs.right <= cap + 1e-10,
                        "right carry-forward {} exceeded cap {}",
                        legs.right, cap
                    );
                }
            }
        }
    }
}
