//! Matrix commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::{CompensationPlan, MatrixStructureConfig};
use crate::tree::matrix::MatrixTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
use super::walk;

/// Calculate matrix level commissions for a set of volume events.
///
/// Walks the placement-tree upline from each volume source, applying the
/// rate table, compression, eligibility, and depth limits. The effective
/// depth ceiling is `min(matrix_params.height, level_commission.max_depth)`.
///
/// # Errors
///
/// Returns [`CalculationError::TreeConfigMismatch`] when the tree's `width` or
/// `spillover` disagrees with `structure.matrix_params` — checked before any
/// other work, so it precedes volume/snapshot validation. Otherwise returns
/// `CalculationError` if a volume source is not found in the tree or snapshot
/// data, or has an invalid `cv_amount`.
pub fn calculate_matrix(
    tree: &MatrixTree,
    plan: &CompensationPlan,
    structure: &MatrixStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    // Guard: the tree must have the topology the plan structure declares.
    // Tree width/spillover are set at create_tree time from op params, not from
    // the config, so nothing else reconciles them. Paying against a mismatched
    // tree would compute commissions on the wrong shape. See HEU-525.
    let params = &structure.matrix_params;
    if tree.width() != params.width || tree.spillover() != params.spillover {
        return Err(CalculationError::TreeConfigMismatch {
            structure: structure.name.clone(),
            expected_width: params.width,
            actual_width: tree.width(),
            expected_spillover: params.spillover,
            actual_spillover: tree.spillover(),
        });
    }

    let rank_ordinals = walk::build_rank_ordinals(plan);
    let eligibility_cache = walk::evaluate_eligibility(snapshots, tree, &plan.eligibility);

    // Matrix-specific: effective depth is min(height, max_depth)
    let max_depth = structure
        .level_commission
        .max_depth
        .min(structure.matrix_params.height);

    let broad_pct = structure.level_commission.broad_commission_percent;
    walk::validate_broad_pct(broad_pct);

    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let threshold_ordinal = walk::resolve_threshold_ordinal(compression, &rank_ordinals);

    let config = walk::LevelWalkConfig {
        max_depth,
        broad_pct,
        multiplier,
        compression,
        threshold_ordinal,
        rank_ordinals: &rank_ordinals,
        rate_table: &structure.level_commission.rate_table,
        pass_up: None,
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
    use crate::commission::test_helpers::build_test_plan;
    use crate::config::commission::{CompressionConfig, CompressionMode, LevelCommissionConfig};
    use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
    use crate::config::matrix::{MatrixStructureParams, SpilloverDirection};
    use crate::config::rank::{DemotionPolicy, RankDefinition, RankQualification};
    use crate::config::{MatrixStructureConfig, StructureConfig};
    use crate::tree::test_helpers::test_uuid;
    use std::collections::BTreeMap;

    fn test_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();
        let mut associate = BTreeMap::new();
        associate.insert(1, 0.05);
        associate.insert(2, 0.04);
        associate.insert(3, 0.03);
        associate.insert(4, 0.02);
        associate.insert(5, 0.01);
        table.insert("associate".to_string(), associate);
        table
    }

    fn multi_rank_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = test_rate_table();
        let mut silver = BTreeMap::new();
        silver.insert(1, 0.07);
        silver.insert(2, 0.06);
        silver.insert(3, 0.05);
        silver.insert(4, 0.04);
        silver.insert(5, 0.03);
        table.insert("silver".to_string(), silver);
        table
    }

    fn test_matrix_structure(width: u8, height: u8, max_depth: u8) -> MatrixStructureConfig {
        MatrixStructureConfig {
            name: "Test Matrix".to_string(),
            matrix_params: MatrixStructureParams {
                width,
                height,
                spillover: SpilloverDirection::BreadthFirst,
            },
            level_commission: LevelCommissionConfig {
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth,
                rate_table: test_rate_table(),
            },
            compression: None,
            pruning: None,
        }
    }

    fn test_plan(structure: MatrixStructureConfig) -> CompensationPlan {
        let eligibility = crate::commission::test_helpers::default_eligibility();
        build_test_plan(
            eligibility,
            StructureConfig::Matrix(structure),
            "Test Matrix",
        )
    }

    fn multi_rank_plan(
        structure: MatrixStructureConfig,
        eligibility: CommissionEligibility,
    ) -> CompensationPlan {
        let mut plan = build_test_plan(
            eligibility,
            StructureConfig::Matrix(structure),
            "Test Matrix",
        );
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                    tenure: None,
                },
                qualified_structures: vec!["Test Matrix".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "silver".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                    tenure: None,
                },
                qualified_structures: vec!["Test Matrix".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    fn eligible_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "associate".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    #[test]
    fn basic_walk_single_level() {
        // root(0) -> child(1). Volume from child. Root earns at level 1.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(0));
        assert_eq!(result[0].source_id, test_uuid(1));
        assert_eq!(result[0].level, 1);
        assert_eq!(result[0].rate, 0.05);
        // 100.0 * 0.40 * 1.0 * 0.05 = 2.0
        assert!((result[0].dollar_amount - 2.0).abs() < 1e-10);
    }

    #[test]
    fn walk_multiple_levels() {
        // Chain: root(0) -> 1 -> 2 -> 3. Volume from 3.
        // Expect earnings at levels 1, 2, 3.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..4 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 3);

        // Verify each earner got the correct level, rate, and dollar amount
        let e2 = result.iter().find(|e| e.earner_id == test_uuid(2)).unwrap();
        assert_eq!(e2.level, 1);
        assert_eq!(e2.rate, 0.05);
        // 100.0 * 0.40 * 1.0 * 0.05 = 2.0
        assert!((e2.dollar_amount - 2.0).abs() < 1e-10);

        let e1 = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(e1.level, 2);
        assert_eq!(e1.rate, 0.04);
        // 100.0 * 0.40 * 1.0 * 0.04 = 1.6
        assert!((e1.dollar_amount - 1.6).abs() < 1e-10);

        let e0 = result.iter().find(|e| e.earner_id == test_uuid(0)).unwrap();
        assert_eq!(e0.level, 3);
        assert_eq!(e0.rate, 0.03);
        // 100.0 * 0.40 * 1.0 * 0.03 = 1.2
        assert!((e0.dollar_amount - 1.2).abs() < 1e-10);
    }

    #[test]
    fn depth_limited_by_height() {
        // height=2, max_depth=5. Effective depth = 2.
        // Chain: root(0) -> 1 -> 2 -> 3. Volume from 3.
        // Only levels 1 and 2 should earn.
        let structure = test_matrix_structure(3, 2, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..4 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);
        for earning in &result {
            assert!(earning.level <= 2);
        }
        // Verify the correct earners received earnings
        let earner_ids: Vec<_> = result.iter().map(|e| e.earner_id).collect();
        assert!(earner_ids.contains(&test_uuid(2))); // level 1 from source
        assert!(earner_ids.contains(&test_uuid(1))); // level 2 from source
    }

    #[test]
    fn depth_limited_by_max_depth() {
        // height=9, max_depth=2. Effective depth = 2.
        // Chain: root(0) -> 1 -> 2 -> 3. Volume from 3.
        // Only levels 1 and 2 should earn.
        let structure = test_matrix_structure(3, 9, 2);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 3).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..4 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);
        for earning in &result {
            assert!(earning.level <= 2);
        }
        // Verify the correct earners received earnings
        let earner_ids: Vec<_> = result.iter().map(|e| e.earner_id).collect();
        assert!(earner_ids.contains(&test_uuid(2))); // level 1 from source
        assert!(earner_ids.contains(&test_uuid(1))); // level 2 from source
    }

    #[test]
    fn ineligible_distributor_does_not_earn() {
        // root(0, ineligible) -> child(1). Volume from 1.
        // Root is ineligible (low PV). No earnings.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(0),
            DistributorSnapshot {
                rank: "associate".to_string(),
                personal_volume: 10.0, // below min 100
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn compression_skip_inactive() {
        // Chain: root(0) -> 1(ineligible) -> 2. Volume from 2.
        // With compression, node 1 is skipped. Root earns at level 1.
        use crate::config::commission::{CompressionConfig, CompressionMode};

        let mut structure = test_matrix_structure(3, 9, 5);
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "associate".to_string(),
                personal_volume: 10.0, // ineligible
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(0));
        assert_eq!(result[0].level, 1); // level 1, not 2 — compression preserved the level
    }

    #[test]
    fn without_compression_ineligible_forfeits_level() {
        // Chain: root(0) -> 1(ineligible) -> 2. Volume from 2.
        // Without compression, node 1 forfeits level 1. Root earns at level 2.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "associate".to_string(),
                personal_volume: 10.0, // ineligible
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(0));
        assert_eq!(result[0].level, 2); // level 2 — node 1 consumed level 1
    }

    #[test]
    fn source_is_root_no_earnings() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(0),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn empty_volume_returns_empty() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();

        let snapshots = HashMap::new();
        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &[]).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn source_not_in_tree_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();

        let snapshots = HashMap::new();
        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(result, Err(CalculationError::SourceNotInTree(_))));
    }

    #[test]
    fn source_not_in_snapshot_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        // test_uuid(1) intentionally missing from snapshots

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::SourceNotInSnapshot(_))
        ));
    }

    #[test]
    fn invalid_cv_amount_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: -50.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    #[test]
    fn missing_rank_in_rate_table_no_earning() {
        // Distributor has rank "gold" which is not in rate table.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(0),
            DistributorSnapshot {
                rank: "gold".to_string(), // not in rate table
                personal_volume: 150.0,
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn multiplier_fallback_to_plan_level() {
        // Structure has no volume_to_dollar_multiplier. Falls back to plan level (1.0).
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 200.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        // 200.0 * 0.40 * 1.0 * 0.05 = 4.0
        assert!((result[0].dollar_amount - 4.0).abs() < 1e-10);
    }

    #[test]
    fn structure_multiplier_overrides_plan() {
        let mut structure = test_matrix_structure(3, 9, 5);
        structure.level_commission.volume_to_dollar_multiplier = Some(2.0);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        // 100.0 * 0.40 * 2.0 * 0.05 = 4.0
        assert!((result[0].dollar_amount - 4.0).abs() < 1e-10);
    }

    #[test]
    fn multiple_volume_sources_independent_walks() {
        // root(0) -> 1, root(0) -> 2. Volume from both 1 and 2.
        // Root earns twice — once from each source.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(0), 2).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![
            VolumeSource {
                source_id: test_uuid(1),
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: test_uuid(2),
                cv_amount: 200.0,
            },
        ];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);
        let total: f64 = result.iter().map(|e| e.dollar_amount).sum();
        // (100 + 200) * 0.40 * 1.0 * 0.05 = 6.0
        assert!((total - 6.0).abs() < 1e-10);
    }

    #[test]
    fn walk_follows_placement_not_sponsor() {
        // 3-wide matrix. root(0) sponsors 1, 2, 3, 4.
        // Nodes 1-3 fill root's slots. Node 4 spills under node 1.
        // Volume from 4. The placement upline is 4 -> 1 -> 0.
        // Node 0 earns at level 2, node 1 earns at level 1.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(0), 2).unwrap();
        tree.add_node(test_uuid(3), test_uuid(0), 3).unwrap();
        // Node 4 sponsored by root, but root's slots are full.
        // BFS spillover places under node 1.
        tree.add_node(test_uuid(4), test_uuid(0), 4).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..5 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Placement walk: 4 -> 1 (level 1) -> 0 (level 2)
        assert_eq!(result.len(), 2);

        let node1 = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(node1.level, 1);

        let root = result.iter().find(|e| e.earner_id == test_uuid(0)).unwrap();
        assert_eq!(root.level, 2);
    }

    #[test]
    fn zero_cv_amount_produces_zero_earnings() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 0.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        // Zero CV passes validation but produces zero dollar amounts.
        // The rate > 0.0 check passes, so an earning is emitted with dollar_amount = 0.
        assert_eq!(result.len(), 1);
        assert!((result[0].dollar_amount).abs() < 1e-10);
    }

    #[test]
    fn nan_cv_amount_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::NAN,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    #[test]
    fn positive_infinity_cv_amount_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::INFINITY,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    #[test]
    fn negative_infinity_cv_amount_returns_error() {
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: f64::NEG_INFINITY,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::InvalidCvAmount(_, _))
        ));
    }

    #[test]
    fn compression_skip_below_rank() {
        // Chain: root(0, silver) -> mid(1, associate) -> leaf(2).
        // Threshold is "silver" (ordinal 2). Mid is "associate" (ordinal 1),
        // below threshold, so mid gets compressed out. Root earns at level 1.
        let mut structure = test_matrix_structure(3, 9, 5);
        structure.level_commission.rate_table = multi_rank_rate_table();
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });
        let eligibility = crate::commission::test_helpers::default_eligibility();
        let plan = multi_rank_plan(structure.clone(), eligibility);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), 2).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(0),
            DistributorSnapshot {
                rank: "silver".to_string(),
                ..eligible_snapshot()
            },
        );
        snapshots.insert(
            test_uuid(1),
            DistributorSnapshot {
                rank: "associate".to_string(), // below threshold
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Mid compressed out. Root earns at level 1 (not level 2).
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(0));
        assert_eq!(result[0].level, 1);
        assert_eq!(result[0].rate, 0.07); // silver rate at level 1
    }

    #[test]
    fn active_leg_tiers_count_sponsored_not_placed() {
        // 3-wide matrix. Root(0) sponsors 1, 2, 3, 4.
        // Nodes 1-3 fill root's placement slots. Node 4 spills under node 1.
        // Root has 4 sponsored recruits but only 3 placement children.
        //
        // Active leg tier: need 4 active legs for depth 3, otherwise depth 1.
        // If counting placement children (3), root would be limited to depth 1.
        // If counting sponsored recruits (4), root earns up to depth 3.
        //
        // Volume from node 4. Placement walk: 4 -> 1 (level 1) -> 0 (level 2).
        // Root should earn at level 2 (within depth 3 from sponsored count).
        let structure = test_matrix_structure(3, 9, 5);
        let eligibility = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier {
                    min_active_legs: 0,
                    max_commission_depth: 1,
                },
                ActiveLegTier {
                    min_active_legs: 4,
                    max_commission_depth: 3,
                },
            ],
        };
        let plan = multi_rank_plan(structure.clone(), eligibility);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();
        tree.add_node(test_uuid(2), test_uuid(0), 2).unwrap();
        tree.add_node(test_uuid(3), test_uuid(0), 3).unwrap();
        // Node 4: sponsored by root, but placed under node 1 (spillover).
        tree.add_node(test_uuid(4), test_uuid(0), 4).unwrap();

        let mut snapshots = HashMap::new();
        for i in 0..5 {
            snapshots.insert(test_uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: test_uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Node 1 earns at level 1. Root earns at level 2 (within depth 3).
        assert_eq!(result.len(), 2);

        let node1 = result.iter().find(|e| e.earner_id == test_uuid(1)).unwrap();
        assert_eq!(node1.level, 1);

        let root = result.iter().find(|e| e.earner_id == test_uuid(0)).unwrap();
        assert_eq!(root.level, 2);
    }

    #[test]
    fn width_mismatch_returns_error() {
        // Config declares width 3; tree is built 2-wide.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());
        let tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();

        let snapshots = HashMap::new();
        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &[]);

        assert!(matches!(
            result,
            Err(CalculationError::TreeConfigMismatch {
                expected_width: 3,
                actual_width: 2,
                ..
            })
        ));
    }

    #[test]
    fn spillover_mismatch_returns_error() {
        // Config declares DepthFirst; tree is BreadthFirst (widths match).
        // A config may carry DepthFirst even though MatrixTree::new rejects it.
        let mut structure = test_matrix_structure(3, 9, 5);
        structure.matrix_params.spillover = SpilloverDirection::DepthFirst;
        let plan = test_plan(structure.clone());
        let tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();

        let snapshots = HashMap::new();
        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &[]);

        // Pin the expected/actual spillover direction (expected = config's
        // DepthFirst, actual = tree's BreadthFirst) so a future field swap in
        // the guard is caught — the operator-facing message would otherwise
        // reverse silently on this money seam.
        assert!(matches!(
            result,
            Err(CalculationError::TreeConfigMismatch {
                expected_spillover: SpilloverDirection::DepthFirst,
                actual_spillover: SpilloverDirection::BreadthFirst,
                ..
            })
        ));
    }

    #[test]
    fn matching_topology_calculates() {
        // Width and spillover both match — the guard does not fire.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(
            !matches!(result, Err(CalculationError::TreeConfigMismatch { .. })),
            "matching topology must not be a mismatch"
        );
        assert!(
            result.is_ok(),
            "matching topology should calculate: {result:?}"
        );
    }

    #[test]
    fn mismatch_precedes_volume_errors() {
        // Width mismatch AND an invalid cv_amount. The guard runs first, so
        // the topology error wins over InvalidCvAmount.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());

        let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(test_uuid(0), eligible_snapshot());
        snapshots.insert(test_uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: -50.0, // would be InvalidCvAmount if the walk ran
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(
            result,
            Err(CalculationError::TreeConfigMismatch { .. })
        ));
    }

    #[test]
    fn mismatch_is_deterministic() {
        // NFR2 (Reliability): identical inputs yield an identical error, and the
        // guard mutates nothing — reusing `&tree`/`&structure` across two calls
        // only compiles because they are borrowed immutably.
        let structure = test_matrix_structure(3, 9, 5);
        let plan = test_plan(structure.clone());
        let tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
        let snapshots = HashMap::new();

        let first = calculate_matrix(&tree, &plan, &structure, &snapshots, &[]);
        let second = calculate_matrix(&tree, &plan, &structure, &snapshots, &[]);

        assert!(matches!(
            first,
            Err(CalculationError::TreeConfigMismatch { .. })
        ));
        // CalculationError and CommissionEarning both derive PartialEq.
        assert_eq!(
            first, second,
            "guard must be deterministic for identical inputs"
        );
    }
}
