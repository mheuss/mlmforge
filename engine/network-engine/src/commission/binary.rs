//! Binary pairing commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::binary::{BinaryCommissionMode, PairingCalculation, VolumeAfterPayout};
use crate::config::eligibility::CommissionEligibility;
use crate::config::{BinaryStructureConfig, CompensationPlan};
use crate::tree::binary::BinaryTree;
use crate::tree::node::NodeIndex;

use super::types::{
    BinaryCalculationResult, BinaryCommissionEarning, CalculationError, DistributorSnapshot,
    LegVolumes, VolumeSource,
};

/// Check if a distributor meets basic commission eligibility.
///
/// Same logic as unilevel. Binary ignores active_leg_tiers.
fn is_eligible(snapshot: &DistributorSnapshot, eligibility: &CommissionEligibility) -> bool {
    if snapshot.personal_volume < eligibility.minimum_pv {
        return false;
    }

    if eligibility.require_order_in_period && !snapshot.has_order_in_period {
        return false;
    }

    if !eligibility.eligible_statuses.is_empty()
        && !eligibility.eligible_statuses.contains(&snapshot.status)
    {
        return false;
    }

    true
}

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
        let arena = tree.arena();
        if !arena.index.contains_key(&source.source_id) {
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

/// Phase 1, Step 2: Evaluate eligibility for all distributors.
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
        BinaryCommissionMode::Pairing(config) => config,
        BinaryCommissionMode::CycleStep(_) => {
            // CycleStep is deferred. Return empty result.
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

    Ok(BinaryCalculationResult {
        earnings,
        carry_forward: new_carry_forward,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::binary::{
        BinaryCommissionConfig, BinaryCommissionMode, PairingCalculation, PairingConfig,
        VolumeAfterPayout,
    };
    use crate::config::bonus::BonusConfig;
    use crate::config::payout::{CapEnforcement, CapsConfig, PayoutConfig, PayoutMethod};
    use crate::config::period::{PeriodConfig, PeriodLength};
    use crate::config::placement::PlacementConfig;
    use crate::config::rank::{
        DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
    };
    use crate::config::volume::VolumeConfig;
    use crate::config::{BinaryStructureConfig, CompensationPlan, StructureConfig};
    use crate::tree::binary::BinaryTree;

    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([n, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
    }

    fn default_eligibility() -> CommissionEligibility {
        CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![],
        }
    }

    fn eligible_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "associate".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

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
        CompensationPlan {
            name: "Test Plan".to_string(),
            version: 1,
            structures: vec![StructureConfig::Binary(structure)],
            period: PeriodConfig {
                length: PeriodLength::Month,
                start_date: chrono::NaiveDate::from_ymd_opt(2026, 3, 1).unwrap(),
                payout_lag_days: 14,
            },
            volume: VolumeConfig {
                inhibit_signup_volume: false,
                base_currency: "USD".to_string(),
                volume_to_dollar_multiplier: 1.0,
                deduct_qualifying_volume: false,
            },
            ranks: vec![RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test Binary".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            }],
            rank_tracking: RankTrackingConfig {
                track_achieved_rank: false,
            },
            rank_features: RankFeaturesConfig {
                constraints_enabled: false,
                overrides_enabled: false,
            },
            eligibility,
            bonuses: BonusConfig {
                matching: None,
                sponsor: None,
                fast_start: None,
                rank_advancement: None,
                leadership_development: None,
                infinity: None,
                lifestyle: None,
                pool: None,
                matrix_completion: None,
                position: None,
                board_cycling: None,
                pass_up: None,
            },
            payout: PayoutConfig {
                currency: "USD".to_string(),
                minimum_payout: 50.0,
                allow_partial_payout: true,
                payment_methods: vec![PayoutMethod {
                    method_type: "bank_transfer".to_string(),
                    fee: 2.50,
                }],
            },
            caps: CapsConfig {
                per_distributor_cap: None,
                company_payout_cap_percent: 0.42,
                enforcement: CapEnforcement::ProRata,
                enable_clawback: false,
            },
            placement: PlacementConfig {
                donated_placement: None,
                holding_tank: None,
                binary_placement: None,
            },
        }
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
        for (_, legs) in &result.carry_forward {
            assert_eq!(legs.left, 0.0);
            assert_eq!(legs.right, 0.0);
        }
    }
}
