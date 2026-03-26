//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
use super::walk;

/// Calculate unilevel commissions for a set of volume events.
///
/// Walks the upline from each volume source, applying the rate table,
/// compression, eligibility, and depth limits from the plan config.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data.
pub fn calculate_unilevel(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &UnilevelStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    let rank_ordinals = walk::build_rank_ordinals(plan);
    let eligibility_cache = walk::evaluate_eligibility(snapshots, tree, &plan.eligibility);

    let broad_pct = structure.level_commission.broad_commission_percent;
    walk::validate_broad_pct(broad_pct);

    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let threshold_ordinal = walk::resolve_threshold_ordinal(compression, &rank_ordinals);

    // Build pass-up context if configured. Use tree.user_ids() instead of
    // snapshot keys so skip sets cover all distributors in the tree, including
    // intermediate sponsors that may be missing from snapshots.
    let pass_up_context = structure
        .pass_up
        .as_ref()
        .map(|pu| walk::build_pass_up_context(tree, pu, &tree.user_ids()));

    let config = walk::LevelWalkConfig {
        max_depth: structure.level_commission.max_depth,
        broad_pct,
        multiplier,
        compression,
        threshold_ordinal,
        rank_ordinals: &rank_ordinals,
        rate_table: &structure.level_commission.rate_table,
        pass_up: pass_up_context.as_ref(),
        dynamic_thresholds: None,
    };

    let mut earnings =
        walk::walk_level_commissions(tree, &config, &eligibility_cache, snapshots, volume, |_| {
            false
        })?;

    walk::sort_earnings(&mut earnings);
    Ok(earnings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::is_eligible;
    use crate::commission::test_helpers::build_test_plan;
    use crate::config::PassUpConfig;
    use crate::config::commission::{CompressionConfig, CompressionMode, LevelCommissionConfig};
    use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
    use crate::config::rank::{DemotionPolicy, RankDefinition, RankQualification};
    use crate::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
    use crate::tree::test_helpers::test_uuid;
    use std::collections::BTreeMap;

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
            rank: "silver".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    fn test_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();

        let mut associate = BTreeMap::new();
        associate.insert(1, 0.05);
        associate.insert(2, 0.04);
        associate.insert(3, 0.03);
        table.insert("associate".to_string(), associate);

        let mut silver = BTreeMap::new();
        silver.insert(1, 0.07);
        silver.insert(2, 0.06);
        silver.insert(3, 0.05);
        silver.insert(4, 0.04);
        silver.insert(5, 0.03);
        table.insert("silver".to_string(), silver);

        table
    }

    fn test_structure(rate_table: BTreeMap<String, BTreeMap<u8, f64>>) -> UnilevelStructureConfig {
        UnilevelStructureConfig {
            name: "Test Unilevel".to_string(),
            level_commission: LevelCommissionConfig {
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth: 5,
                rate_table,
            },
            compression: None,
            pass_up: None,
        }
    }

    fn test_plan(eligibility: CommissionEligibility) -> CompensationPlan {
        let structure = test_structure(test_rate_table());
        test_plan_with_structure(eligibility, structure)
    }

    fn test_plan_with_structure(
        eligibility: CommissionEligibility,
        structure: UnilevelStructureConfig,
    ) -> CompensationPlan {
        let mut plan = build_test_plan(
            eligibility,
            StructureConfig::Unilevel(structure),
            "Test Unilevel",
        );
        // Unilevel tests need both associate and silver ranks.
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test Unilevel".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "silver".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test Unilevel".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    // --- is_eligible tests ---

    #[test]
    fn eligible_distributor_passes_all_checks() {
        let elig = default_eligibility();
        let snap = eligible_snapshot();
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_at_exact_minimum_pv() {
        let elig = default_eligibility(); // minimum_pv = 100.0
        let snap = DistributorSnapshot {
            personal_volume: 100.0,
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_below_minimum_pv() {
        let elig = default_eligibility();
        let snap = DistributorSnapshot {
            personal_volume: 50.0,
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_wrong_status() {
        let elig = default_eligibility();
        let snap = DistributorSnapshot {
            status: "suspended".to_string(),
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_when_status_list_empty() {
        let elig = CommissionEligibility {
            eligible_statuses: vec![],
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            status: "anything".to_string(),
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    #[test]
    fn ineligible_no_order_when_required() {
        let elig = CommissionEligibility {
            require_order_in_period: true,
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            has_order_in_period: false,
            ..eligible_snapshot()
        };
        assert!(!is_eligible(&snap, &elig));
    }

    #[test]
    fn eligible_no_order_when_not_required() {
        let elig = CommissionEligibility {
            require_order_in_period: false,
            ..default_eligibility()
        };
        let snap = DistributorSnapshot {
            has_order_in_period: false,
            ..eligible_snapshot()
        };
        assert!(is_eligible(&snap, &elig));
    }

    // --- calculate_unilevel tests ---

    #[test]
    fn basic_walk_three_node_chain() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // Volume source: leaf(3) generates 100 CV
        // Expected: mid(2) earns at level 1, root(1) earns at level 2
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                rank: "associate".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);

        // mid(2) is associate at level 1: 100 * 0.40 * 1.0 * 0.05 = 2.0
        let mid_earning = result.iter().find(|e| e.earner_id == test_uuid(2)).unwrap();
        assert_eq!(mid_earning.level, 1);
        assert_eq!(mid_earning.rate, 0.05);
        assert!((mid_earning.dollar_amount - 2.0).abs() < f64::EPSILON);

        // root(1) is silver at level 2: 100 * 0.40 * 1.0 * 0.06 = 2.4
        let root_earning = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(root_earning.level, 2);
        assert_eq!(root_earning.rate, 0.06);
        assert!((root_earning.dollar_amount - 2.4).abs() < f64::EPSILON);
    }

    #[test]
    fn walk_rank_not_in_rate_table() {
        // Distributor with rank "bronze" which has no entry in rate table
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "bronze".to_string(), // not in rate table
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert!(result.is_empty()); // no rate found, no earning
    }

    #[test]
    fn walk_level_not_in_rate_table() {
        // Associate only has rates for levels 1-3, walk goes to level 4
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 0)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), test_uuid(4), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(
                test_uuid(id),
                DistributorSnapshot {
                    rank: "associate".to_string(),
                    ..eligible_snapshot()
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Associate has rates for levels 1-3 only.
        assert_eq!(result.len(), 3);
        assert!(result.iter().all(|e| e.level <= 3));
    }

    #[test]
    fn walk_stops_at_max_depth() {
        // max_depth=2, tree is 5 deep
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.max_depth = 2;
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        for id in 1..=4 {
            snapshots.insert(
                test_uuid(id),
                DistributorSnapshot {
                    rank: "silver".to_string(),
                    ..eligible_snapshot()
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Only levels 1 and 2 should have earnings
        assert_eq!(result.len(), 2);
        assert!(result.iter().all(|e| e.level <= 2));
    }

    #[test]
    fn walk_dollar_amount_formula() {
        // Verify: cv * broad_pct * multiplier * rate
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.broad_commission_percent = 0.50;
        structure.level_commission.volume_to_dollar_multiplier = Some(0.80);
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 200.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        // 200.0 * 0.50 * 0.80 * 0.07 (silver level 1) = 5.6
        let expected = 200.0 * 0.50 * 0.80 * 0.07;
        assert!((result[0].dollar_amount - expected).abs() < f64::EPSILON);
    }

    #[test]
    fn walk_multiplier_falls_back_to_plan_level() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.level_commission.volume_to_dollar_multiplier = None; // fallback
        let mut plan = test_plan(default_eligibility());
        plan.volume.volume_to_dollar_multiplier = 0.75;

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // 100 * 0.40 * 0.75 * 0.07 = 2.1
        let expected = 100.0 * 0.40 * 0.75 * 0.07;
        assert!((result[0].dollar_amount - expected).abs() < f64::EPSILON);
    }

    // --- compression tests ---

    #[test]
    fn compression_skip_inactive_preserves_level() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is ineligible (low PV), compression enabled
        // Expected: root(1) earns at level 1 (not level 2)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: 0.0, // ineligible
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1);
    }

    #[test]
    fn compression_skip_below_rank() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is "associate" (ordinal 1), threshold is "silver" (ordinal 2)
        // mid gets compressed out, root earns at level 1
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                rank: "associate".to_string(), // below threshold
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1); // level preserved
    }

    #[test]
    fn no_compression_ineligible_forfeits_level() {
        // Tree: root(1) -> mid(2) -> leaf(3)
        // mid(2) is ineligible, no compression
        // Expected: root(1) earns at level 2 (level 1 forfeited)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let structure = test_structure(test_rate_table()); // no compression
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: 0.0, // ineligible
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 2); // level 1 forfeited
    }

    #[test]
    fn compression_missing_snapshot_compressed_out() {
        // mid(2) has no snapshot. With compression, they're skipped.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let mut structure = test_structure(test_rate_table());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        // test_uuid(2) intentionally missing
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1); // compressed, level preserved
    }

    #[test]
    fn no_compression_missing_snapshot_forfeits_level() {
        // mid(2) has no snapshot. Without compression, level forfeited.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        // test_uuid(2) missing
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].level, 2); // level 1 forfeited
    }

    // --- active leg tier depth limit tests ---

    #[test]
    fn active_leg_tier_limits_earning_depth() {
        // 5-node chain. Each node has 1 active leg (its child).
        // Tier: 1 active leg -> depth 2.
        // Volume from node 5.
        // node 4 at level 1, node 3 at level 2 (both earn, within depth 2)
        // node 2 at level 3, node 1 at level 4 (both skip, beyond depth 2)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 0)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 0)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), test_uuid(4), 0)
            .unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![ActiveLegTier {
                min_active_legs: 1,
                max_commission_depth: 2,
            }],
        };
        let structure = test_structure(test_rate_table());
        let plan = test_plan_with_structure(elig, structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(
                test_uuid(id),
                DistributorSnapshot {
                    rank: "silver".to_string(),
                    ..eligible_snapshot()
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Nodes 4 and 3 earn (levels 1 and 2). Nodes 2 and 1 are beyond depth 2.
        assert_eq!(result.len(), 2);
        assert!(result.iter().all(|e| e.level <= 2));
    }

    #[test]
    fn active_leg_tier_unlimited_earns_full_depth() {
        // root(1) has 3 active legs -> tier with depth 0 (unlimited)
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 0)
            .unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![ActiveLegTier {
                min_active_legs: 3,
                max_commission_depth: 0,
            }],
        };
        let structure = test_structure(test_rate_table());
        let plan = test_plan_with_structure(elig, structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(
                test_uuid(id),
                DistributorSnapshot {
                    rank: "silver".to_string(),
                    ..eligible_snapshot()
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // node 2 at level 1, node 1 at level 2. Both should earn.
        assert_eq!(result.len(), 2);
    }

    // --- error handling tests ---

    #[test]
    fn error_source_not_in_tree() {
        let tree = UnilevelTree::new();
        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let snapshots = HashMap::new();

        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::SourceNotInTree(id) if id == test_uuid(99)
        ));
    }

    #[test]
    fn error_source_not_in_snapshot() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let snapshots = HashMap::new(); // empty — source has no snapshot

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::SourceNotInSnapshot(id) if id == test_uuid(1)
        ));
    }

    #[test]
    fn error_negative_cv_amount() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: -50.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::InvalidCvAmount(id, _) if id == test_uuid(1)
        ));
    }

    #[test]
    fn error_nan_cv_amount() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::NAN,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::InvalidCvAmount(id, _) if id == test_uuid(1)
        ));
    }

    #[test]
    fn error_positive_infinity_cv_amount() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::INFINITY,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::InvalidCvAmount(id, _) if id == test_uuid(1)
        ));
    }

    #[test]
    fn error_negative_infinity_cv_amount() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::NEG_INFINITY,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume);

        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            CalculationError::InvalidCvAmount(id, _) if id == test_uuid(1)
        ));
    }

    // --- edge case tests ---

    #[test]
    fn empty_volume_returns_empty_earnings() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &[]).unwrap();

        assert!(result.is_empty());
    }

    #[test]
    fn root_as_source_no_upline() {
        // Root generates volume. No parent to walk to. No earnings.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());
        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert!(result.is_empty());
    }

    #[test]
    fn multiple_volume_sources_produce_separate_earnings() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: test_uuid(3),
                cv_amount: 200.0,
            },
        ];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // root earns from both sources
        assert_eq!(result.len(), 2);
        let from_2: Vec<_> = result
            .iter()
            .filter(|e| e.source_id == test_uuid(2))
            .collect();
        let from_3: Vec<_> = result
            .iter()
            .filter(|e| e.source_id == test_uuid(3))
            .collect();
        assert_eq!(from_2.len(), 1);
        assert_eq!(from_3.len(), 1);
        assert!((from_2[0].cv_amount - 100.0).abs() < f64::EPSILON);
        assert!((from_3[0].cv_amount - 200.0).abs() < f64::EPSILON);
    }

    #[test]
    fn realistic_scenario_acme_wellness() {
        // Tree structure:
        //   company(1) [gold]
        //     ├── leader_a(2) [silver]
        //     │   ├── rep_a1(4) [associate]
        //     │   │   └── rep_a1a(7) [associate]
        //     │   └── rep_a2(5) [associate]
        //     └── leader_b(3) [silver]
        //         └── rep_b1(6) [associate]
        //
        // Volume: rep_a1a(7) generates 100 CV
        // Walk from 7 upward:
        //   Level 1: rep_a1(4) — associate, rate 0.05
        //   Level 2: leader_a(2) — silver, rate 0.06
        //   Level 3: company(1) — gold, rate 0.06
        //
        // broad_commission_percent = 0.40, multiplier = 1.0

        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 0)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 0)
            .unwrap();
        tree.add_node(test_uuid(6), test_uuid(3), test_uuid(3), 0)
            .unwrap();
        tree.add_node(test_uuid(7), test_uuid(4), test_uuid(4), 0)
            .unwrap();

        let mut rate_table = BTreeMap::new();
        let mut associate_rates = BTreeMap::new();
        associate_rates.insert(1, 0.05);
        associate_rates.insert(2, 0.04);
        associate_rates.insert(3, 0.03);
        rate_table.insert("associate".to_string(), associate_rates);

        let mut silver_rates = BTreeMap::new();
        silver_rates.insert(1, 0.07);
        silver_rates.insert(2, 0.06);
        silver_rates.insert(3, 0.05);
        silver_rates.insert(4, 0.04);
        silver_rates.insert(5, 0.03);
        rate_table.insert("silver".to_string(), silver_rates);

        let mut gold_rates = BTreeMap::new();
        gold_rates.insert(1, 0.08);
        gold_rates.insert(2, 0.07);
        gold_rates.insert(3, 0.06);
        gold_rates.insert(4, 0.05);
        gold_rates.insert(5, 0.04);
        gold_rates.insert(6, 0.03);
        gold_rates.insert(7, 0.02);
        rate_table.insert("gold".to_string(), gold_rates);

        let structure = test_structure(rate_table);

        // Build plan with all three ranks (associate, silver, gold)
        let mut plan = test_plan_with_structure(default_eligibility(), structure.clone());
        plan.ranks.push(RankDefinition {
            name: "gold".to_string(),
            ordinal: 3,
            qualification: RankQualification {
                structures: vec![],
                required_products: vec![],
            },
            qualified_structures: vec!["Test Unilevel".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        });

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "gold".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        for id in 4..=7 {
            snapshots.insert(
                test_uuid(id),
                DistributorSnapshot {
                    rank: "associate".to_string(),
                    ..eligible_snapshot()
                },
            );
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(7),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 3);

        // rep_a1(4) at level 1: associate rate = 0.05
        let e4 = result.iter().find(|e| e.earner_id == test_uuid(4)).unwrap();
        assert_eq!(e4.level, 1);
        assert!((e4.dollar_amount - 100.0 * 0.40 * 1.0 * 0.05).abs() < f64::EPSILON);

        // leader_a(2) at level 2: silver rate = 0.06
        let e2 = result.iter().find(|e| e.earner_id == test_uuid(2)).unwrap();
        assert_eq!(e2.level, 2);
        assert!((e2.dollar_amount - 100.0 * 0.40 * 1.0 * 0.06).abs() < f64::EPSILON);

        // company(1) at level 3: gold rate = 0.06
        let e1 = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(e1.level, 3);
        assert!((e1.dollar_amount - 100.0 * 0.40 * 1.0 * 0.06).abs() < f64::EPSILON);
    }

    #[test]
    fn ineligible_source_still_generates_walk() {
        // Source doesn't need to be eligible. They generate volume,
        // they don't earn from it.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: 0.0, // ineligible, but still a valid source
                ..eligible_snapshot()
            },
        );

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // root should still earn from the volume
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
    }

    #[test]
    fn zero_cv_amount_produces_zero_earnings() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let structure = test_structure(test_rate_table());
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 0.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // cv_amount=0.0 is valid but produces no earnings (rate * 0 = 0, filtered out).
        assert!(
            result.is_empty() || result.iter().all(|e| e.dollar_amount == 0.0),
            "zero CV should produce zero or empty earnings"
        );
    }

    // --- pass-up integration tests ---

    fn pass_up_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();
        let mut silver = BTreeMap::new();
        silver.insert(1, 0.05);
        silver.insert(2, 0.04);
        silver.insert(3, 0.03);
        silver.insert(4, 0.02);
        silver.insert(5, 0.01);
        table.insert("silver".to_string(), silver);
        table
    }

    fn structure_with_pass_up(
        rate_table: BTreeMap<String, BTreeMap<u8, f64>>,
        pass_up: PassUpConfig,
    ) -> UnilevelStructureConfig {
        UnilevelStructureConfig {
            name: "Test".to_string(),
            level_commission: LevelCommissionConfig {
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth: 5,
                rate_table,
            },
            compression: None,
            pass_up: Some(pass_up),
        }
    }

    #[test]
    fn pass_up_skips_sponsor_for_passed_recruit() {
        // Tree: S(1)->A(2)->[R1(3, t=200), R2(4, t=300), R3(5, t=400)]
        // count=2, includes=false. Volume from R1.
        // A is skipped for R1 (passed up). S earns at level 1.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // R1
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 300)
            .unwrap(); // R2
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 400)
            .unwrap(); // R3

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 2,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3), // R1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // A should not earn (R1 is passed up from A).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(2)),
            "A should be skipped for passed-up recruit R1"
        );

        // S earns at level 1 (A was skipped without consuming a level).
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 1);
    }

    #[test]
    fn pass_up_retains_after_count() {
        // Same tree: S(1)->A(2)->[R1(3, t=200), R2(4, t=300), R3(5, t=400)]
        // count=2, includes=false. Volume from R3 (3rd recruit, retained).
        // A earns at level 1 (R3 is not passed up). S earns at level 2.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(2), test_uuid(2), 300)
            .unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), test_uuid(2), 400)
            .unwrap();

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 2,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(5), // R3
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // A earns at level 1 (R3 is retained, not passed up).
        let a_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(2))
            .expect("A should earn from retained recruit R3");
        assert_eq!(a_earning.level, 1);

        // S earns at level 2.
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 2);
    }

    #[test]
    fn pass_up_with_includes_commissions_skips_branch() {
        // Tree: S(1)->[B(5, t=50), A(2, t=100)]->R1(3)->D1(4)
        // count=1, includes=true. Volume from D1.
        //
        // Skip sets with includes_commissions=true:
        //   R1: {D1} (R1 sponsored D1)
        //   A:  {R1, D1} (A sponsored R1, subtree includes D1)
        //   S:  {B} (S's first recruit by enrolled_at is B, not A)
        //
        // Walk from D1: R1 skipped (D1 in set), A skipped (D1 in set),
        //   S earns at level 1 (D1 not in S's skip set).
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(5), test_uuid(1), test_uuid(1), 50)
            .unwrap(); // B (S's first recruit)
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A (S's second recruit, retained)
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // R1
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 300)
            .unwrap(); // D1

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: true,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4), // D1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // A should not earn (D1 is in A's skip set via includes_commissions).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(2)),
            "A should be skipped for volume from D1 (in passed-up branch)"
        );

        // R1 should not earn (D1 is R1's first recruit).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(3)),
            "R1 should be skipped for volume from its passed-up recruit D1"
        );

        // S earns at level 1 (R1 and A skipped without consuming levels).
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 1);
    }

    #[test]
    fn pass_up_without_includes_commissions_does_not_skip_branch() {
        // Tree: S(1)->[B(5, t=50), A(2, t=100)]->R1(3)->D1(4)
        // count=1, includes=false. Volume from D1.
        //
        // Skip sets with includes_commissions=false:
        //   R1: {D1} (R1 sponsored D1)
        //   A:  {R1} (only direct recruit, not subtree)
        //   S:  {B} (S's first recruit is B)
        //
        // Walk from D1: R1 skipped (D1 in set), A earns at level 1
        //   (D1 not in A's skip set), S at level 2.
        // Contrast with includes=true where A would also be skipped.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(1), test_uuid(1), 50)
            .unwrap(); // B (S's first recruit)
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A (S's second recruit, retained)
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 300)
            .unwrap();

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=5 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4), // D1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // R1 should not earn (D1 is R1's first recruit, in R1's skip set).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(3)),
            "R1 should be skipped for volume from its passed-up recruit D1"
        );

        // A earns at level 1 (D1 is not A's direct recruit, not in skip set).
        // R1 was skipped without consuming a level.
        let a_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(2))
            .expect("A should earn (D1 not in A's skip set)");
        assert_eq!(a_earning.level, 1);

        // S earns at level 2.
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 2);
    }

    #[test]
    fn pass_up_and_compression_both_active() {
        // Tree: S(1)->B(2, inactive)->A(3)->R1(4)
        // compression=SkipInactive, count=1, includes=false.
        // Volume from R1.
        // A skipped (pass-up, R1 is A's first recruit).
        // B skipped (compression, inactive).
        // S earns at level 1.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // B
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // A
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 300)
            .unwrap(); // R1

        let mut structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: false,
            },
        );
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot()); // S: eligible
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: 0.0, // B: ineligible (below min PV 100)
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(3), eligible_snapshot()); // A: eligible
        snapshots.insert(test_uuid(4), eligible_snapshot()); // R1: eligible

        let volume = vec![VolumeSource {
            source_id: test_uuid(4), // R1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // A skipped (pass-up), B skipped (compression). S earns at level 1.
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1);
    }

    #[test]
    fn pass_up_no_double_counting() {
        // Tree: S(1)->A(2)->R1(3), count=1. Volume from R1.
        // S earns exactly once (at level 1), not at multiple levels.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // R1

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=3 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3), // R1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // S should earn exactly once.
        let s_count = result
            .iter()
            .filter(|e| e.earner_id == test_uuid(1))
            .count();
        assert_eq!(
            s_count, 1,
            "S should earn exactly once, not at multiple levels"
        );

        // S earns at level 1 (A was skipped).
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 1);
    }

    #[test]
    fn pass_up_recursive_independent() {
        // Tree: S(1)->A(2)->R1(3)->R1a(4), count=1, includes=false.
        // Volume from R1a.
        // R1's first recruit is R1a, so R1 is skipped for R1a's volume (passed to A).
        // A's first recruit is R1, so A is skipped for R1's volume, but R1a is
        // NOT A's direct recruit, so A is NOT skipped for R1a's volume.
        // Result: R1 skipped, A earns at level 1, S at level 2.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // R1
        tree.add_node(test_uuid(4), test_uuid(3), test_uuid(3), 300)
            .unwrap(); // R1a

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        for id in 1..=4 {
            snapshots.insert(test_uuid(id), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4), // R1a
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // R1 skipped (R1a is R1's first recruit, passed up to A).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(3)),
            "R1 should be skipped for its first recruit R1a"
        );

        // A earns at level 1 (R1a is not A's direct recruit, not in A's skip set).
        let a_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(2))
            .expect("A should earn from R1a (not in A's skip set)");
        assert_eq!(a_earning.level, 1);

        // S earns at level 2.
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(s_earning.level, 2);
    }

    #[test]
    fn pass_up_none_is_noop() {
        // Standard tree with no pass_up. Normal walk behavior.
        // Tree: root(1)->mid(2)->leaf(3). Volume from leaf.
        // mid earns at level 1, root at level 2.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap();

        let structure = test_structure(test_rate_table()); // no pass_up
        let plan = test_plan(default_eligibility());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);

        // mid earns at level 1.
        let mid_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(2))
            .expect("mid should earn");
        assert_eq!(mid_earning.level, 1);

        // root earns at level 2.
        let root_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("root should earn");
        assert_eq!(root_earning.level, 2);
    }

    #[test]
    fn pass_up_works_with_missing_snapshot_for_sponsor() {
        // Tree: S(1) -> A(2) -> R1(3, t=200)
        // A has NO snapshot. Pass-up should still build A's skip set from
        // the tree structure (via user_ids()), so A is skipped for R1's
        // volume. Without compression, missing snapshot forfeits the level.
        // But pass-up fires before the snapshot lookup, so A is skipped
        // entirely without consuming a level. S earns at level 1.
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap(); // S
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 100)
            .unwrap(); // A
        tree.add_node(test_uuid(3), test_uuid(2), test_uuid(2), 200)
            .unwrap(); // R1

        let structure = structure_with_pass_up(
            pass_up_rate_table(),
            PassUpConfig {
                count: 1,
                includes_commissions: false,
            },
        );
        let plan = test_plan_with_structure(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(1), eligible_snapshot());
        // Deliberately omit A (test_uuid(2)) from snapshots.
        snapshots.insert(test_uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(3), // R1
            cv_amount: 100.0,
        }];

        let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // A should not earn (no snapshot, and pass-up skips before lookup).
        assert!(
            result.iter().all(|e| e.earner_id != test_uuid(2)),
            "A should not earn — skipped by pass-up before snapshot lookup"
        );

        // S earns at level 1 (A was skipped without consuming a level).
        let s_earning = result
            .iter()
            .find(|e| e.earner_id == test_uuid(1))
            .expect("S should earn");
        assert_eq!(
            s_earning.level, 1,
            "S should earn at level 1 because pass-up skipped A without consuming a level"
        );
    }
}
