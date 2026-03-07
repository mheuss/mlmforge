//! Matrix commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::commission::CompressionMode;
use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::config::{CompensationPlan, MatrixStructureConfig};
use crate::tree::matrix::MatrixTree;

use super::is_eligible;
use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

/// Calculate matrix level commissions for a set of volume events.
///
/// Walks the placement-tree upline from each volume source, applying the
/// rate table, compression, eligibility, and depth limits. The effective
/// depth ceiling is `min(matrix_params.height, level_commission.max_depth)`.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data.
pub fn calculate_matrix(
    tree: &MatrixTree,
    plan: &CompensationPlan,
    structure: &MatrixStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    // Build rank name -> ordinal map for SkipBelowRank comparison
    let rank_ordinals: HashMap<&str, u16> = plan
        .ranks
        .iter()
        .map(|r| (r.name.as_str(), r.ordinal))
        .collect();

    // Prep phase: evaluate eligibility for all distributors
    let eligibility_cache = evaluate_eligibility(snapshots, tree, &plan.eligibility);

    // Walk config — effective depth is min(height, max_depth)
    let max_depth = structure
        .level_commission
        .max_depth
        .min(structure.matrix_params.height);
    let broad_pct = structure.level_commission.broad_commission_percent;
    debug_assert!(
        (0.0..=1.0).contains(&broad_pct),
        "broad_commission_percent out of range: {}",
        broad_pct
    );
    if !(0.0..=1.0).contains(&broad_pct) {
        log::warn!(
            "broad_commission_percent {} is outside [0.0, 1.0]; commissions may be overstated",
            broad_pct
        );
    }
    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let compression_enabled = compression.is_some_and(|c| c.enabled);

    let threshold_ordinal = compression.and_then(|c| {
        if matches!(c.mode, CompressionMode::SkipBelowRank) {
            match &c.rank_threshold {
                None => {
                    log::warn!(
                        "SkipBelowRank compression enabled but rank_threshold is not set; \
                         compression will have no effect"
                    );
                    None
                }
                Some(name) => {
                    let ordinal = rank_ordinals.get(name.as_str()).copied();
                    if ordinal.is_none() {
                        log::warn!(
                            "SkipBelowRank compression rank_threshold '{}' not found in \
                             plan ranks; compression will have no effect",
                            name
                        );
                    }
                    ordinal
                }
            }
        } else {
            None
        }
    });

    let mut all_earnings = Vec::new();

    for source in volume {
        // Validate cv_amount is non-negative and finite
        if !source.cv_amount.is_finite() || source.cv_amount < 0.0 {
            return Err(CalculationError::InvalidCvAmount(
                source.source_id,
                source.cv_amount,
            ));
        }

        // Validate source exists in tree and get upline in one call
        let upline = tree
            .get_upline(source.source_id, 0)
            .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;

        // Validate source exists in snapshots
        if !snapshots.contains_key(&source.source_id) {
            return Err(CalculationError::SourceNotInSnapshot(source.source_id));
        }

        let mut level: u8 = 1;

        for node in &upline {
            if level > max_depth {
                break;
            }

            let snapshot = match snapshots.get(&node.user_id) {
                Some(s) => s,
                None => {
                    // Missing snapshot: treat as ineligible
                    if compression_enabled {
                        continue; // compressed out, no level consumed
                    }
                    level = level.saturating_add(1);
                    continue;
                }
            };

            let elig = eligibility_cache.get(&node.user_id);
            let node_eligible = elig.is_some_and(|e| e.eligible);

            // Compression check
            let should_compress = match compression.filter(|c| c.enabled) {
                Some(compress) => match compress.mode {
                    CompressionMode::SkipInactive => !node_eligible,
                    CompressionMode::SkipBelowRank => {
                        let dist_ordinal = rank_ordinals
                            .get(snapshot.rank.as_str())
                            .copied()
                            .unwrap_or(0);
                        threshold_ordinal.map(|t| dist_ordinal < t).unwrap_or(false)
                    }
                },
                None => false,
            };

            if should_compress {
                continue; // skip without consuming level
            }

            // Not compressed. Check if eligible.
            if !node_eligible {
                level = level.saturating_add(1); // forfeit level
                continue;
            }

            // Check per-distributor depth limit from active leg tiers
            if let Some(max_personal) = elig.and_then(|e| e.max_earning_depth) {
                if level > max_personal {
                    level = level.saturating_add(1);
                    continue;
                }
            }

            // Rate table lookup
            let rate = structure
                .level_commission
                .rate_table
                .get(&snapshot.rank)
                .and_then(|levels| levels.get(&level))
                .copied()
                .unwrap_or(0.0);

            if rate > 0.0 {
                all_earnings.push(CommissionEarning {
                    earner_id: node.user_id,
                    source_id: source.source_id,
                    level,
                    rate,
                    cv_amount: source.cv_amount,
                    dollar_amount: source.cv_amount * broad_pct * multiplier * rate,
                });
            }

            level = level.saturating_add(1);
        }
    }

    // Sort earnings for deterministic output. Without sorting, the order
    // depends on BFS traversal and volume source iteration, both of which
    // can vary across runs. Primary sort by earner_id, secondary by
    // source_id so multi-source earnings are also stable.
    all_earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.source_id.cmp(&b.source_id))
    });

    Ok(all_earnings)
}

/// Count how many direct children of a distributor are commission-eligible.
fn count_active_legs(
    tree: &MatrixTree,
    user_id: Uuid,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    eligibility: &CommissionEligibility,
) -> u16 {
    let children = match tree.get_children(user_id) {
        Ok(children) => children,
        Err(_) => return 0,
    };

    children
        .iter()
        .filter(|child| {
            snapshots
                .get(&child.user_id)
                .map(|s| is_eligible(s, eligibility))
                .unwrap_or(false)
        })
        .count() as u16
}

/// Determine per-distributor max earning depth from active leg tiers.
///
/// Returns `Some(depth)` if a tier limits the distributor, or `None`
/// if no tier restriction applies (use config max_depth as ceiling).
///
/// Tiers must be sorted ascending by `min_active_legs`. The caller (Go validation
/// pipeline) enforces this via business rules requiring a base tier with min_active_legs=0.
fn determine_max_depth(active_leg_count: u16, tiers: &[ActiveLegTier]) -> Option<u8> {
    if tiers.is_empty() {
        return None;
    }

    // Tiers are sorted ascending by min_active_legs.
    // Walk in reverse to find the highest qualifying tier.
    for tier in tiers.iter().rev() {
        if active_leg_count >= tier.min_active_legs {
            return if tier.max_commission_depth == 0 {
                None // unlimited
            } else {
                Some(u8::try_from(tier.max_commission_depth).unwrap_or(u8::MAX))
            };
        }
    }

    None // no tier matched, use config max_depth
}

/// Cached eligibility result for a single distributor.
struct EligibilityResult {
    eligible: bool,
    /// Per-distributor earning depth limit from active leg tiers.
    /// None means no tier restriction (use config max_depth).
    max_earning_depth: Option<u8>,
}

/// Evaluate eligibility for all distributors in the snapshot map.
///
/// Builds an internal cache used during the walk phase. Runs once
/// before any upline walks begin.
fn evaluate_eligibility(
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    tree: &MatrixTree,
    eligibility: &CommissionEligibility,
) -> HashMap<Uuid, EligibilityResult> {
    let mut results = HashMap::with_capacity(snapshots.len());

    for (user_id, snapshot) in snapshots {
        let eligible = is_eligible(snapshot, eligibility);

        let active_leg_count = if eligible && !eligibility.active_leg_tiers.is_empty() {
            count_active_legs(tree, *user_id, snapshots, eligibility)
        } else {
            0
        };

        let max_earning_depth =
            determine_max_depth(active_leg_count, &eligibility.active_leg_tiers);

        results.insert(
            *user_id,
            EligibilityResult {
                eligible,
                max_earning_depth,
            },
        );
    }

    results
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::build_test_plan;
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::matrix::{MatrixStructureParams, SpilloverDirection};
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
}
