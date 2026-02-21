//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::commission::CompressionMode;
use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::is_eligible;
use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

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
    // Build rank name -> ordinal map for SkipBelowRank comparison
    let rank_ordinals: HashMap<&str, u16> = plan
        .ranks
        .iter()
        .map(|r| (r.name.as_str(), r.ordinal))
        .collect();

    // Prep phase: evaluate eligibility for all distributors
    let eligibility_cache = evaluate_eligibility(snapshots, tree, &plan.eligibility);

    // Walk config
    let max_depth = structure.level_commission.max_depth;
    let broad_pct = structure.level_commission.broad_commission_percent;
    debug_assert!(
        (0.0..=1.0).contains(&broad_pct),
        "broad_commission_percent out of range: {}",
        broad_pct
    );
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

    Ok(all_earnings)
}

/// Count how many direct children of a distributor are commission-eligible.
fn count_active_legs(
    tree: &UnilevelTree,
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
    tree: &UnilevelTree,
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

    // --- count_active_legs tests ---

    #[test]
    fn count_active_legs_with_mixed_children() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, root, 0).unwrap(); // eligible child
        tree.add_node(test_uuid(3), root, root, 0).unwrap(); // ineligible child
        tree.add_node(test_uuid(4), root, root, 0).unwrap(); // eligible child

        let elig = default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(root, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                personal_volume: 0.0, // below min
                ..eligible_snapshot()
            },
        );
        snapshots.insert(test_uuid(4), eligible_snapshot());

        assert_eq!(count_active_legs(&tree, root, &snapshots, &elig), 2);
    }

    #[test]
    fn count_active_legs_missing_child_snapshot() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, root, 0).unwrap();
        tree.add_node(test_uuid(3), root, root, 0).unwrap();

        let elig = default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(root, eligible_snapshot());
        snapshots.insert(test_uuid(2), eligible_snapshot());
        // test_uuid(3) has no snapshot — counts as not active

        assert_eq!(count_active_legs(&tree, root, &snapshots, &elig), 1);
    }

    #[test]
    fn count_active_legs_user_not_in_tree() {
        let tree = UnilevelTree::new();
        let elig = default_eligibility();
        let snapshots = HashMap::new();

        assert_eq!(
            count_active_legs(&tree, test_uuid(99), &snapshots, &elig),
            0
        );
    }

    // --- determine_max_depth tests ---

    #[test]
    fn determine_max_depth_no_tiers() {
        assert_eq!(determine_max_depth(5, &[]), None);
    }

    #[test]
    fn determine_max_depth_matches_highest_qualifying_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 2,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 5,
                max_commission_depth: 5,
            },
            ActiveLegTier {
                min_active_legs: 8,
                max_commission_depth: 0,
            },
        ];

        // 6 legs qualifies for tier 2 (min 5) but not tier 3 (min 8)
        assert_eq!(determine_max_depth(6, &tiers), Some(5));
    }

    #[test]
    fn determine_max_depth_unlimited_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 2,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 8,
                max_commission_depth: 0,
            },
        ];

        // 10 legs qualifies for unlimited tier (depth 0)
        assert_eq!(determine_max_depth(10, &tiers), None);
    }

    #[test]
    fn determine_max_depth_no_tier_matches() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 5,
            max_commission_depth: 3,
        }];

        // 2 legs doesn't meet min 5
        assert_eq!(determine_max_depth(2, &tiers), None);
    }

    #[test]
    fn determine_max_depth_exact_match() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 3,
            max_commission_depth: 4,
        }];

        assert_eq!(determine_max_depth(3, &tiers), Some(4));
    }

    // --- evaluate_eligibility tests ---

    #[test]
    fn evaluate_eligibility_builds_cache_for_all_distributors() {
        let mut tree = UnilevelTree::new();
        let root = test_uuid(1);
        tree.add_root(root, 0).unwrap();
        tree.add_node(test_uuid(2), root, root, 0).unwrap();
        tree.add_node(test_uuid(3), root, root, 0).unwrap();
        tree.add_node(test_uuid(4), root, root, 0).unwrap();

        let elig = CommissionEligibility {
            minimum_pv: 100.0,
            require_order_in_period: false,
            eligible_statuses: vec!["active".to_string()],
            active_leg_tiers: vec![
                ActiveLegTier {
                    min_active_legs: 1,
                    max_commission_depth: 3,
                },
                ActiveLegTier {
                    min_active_legs: 2,
                    max_commission_depth: 0,
                },
            ],
        };

        let mut snapshots = HashMap::new();
        // Root has 2 eligible children (uuid(2), uuid(4)) -> tier 2 (unlimited)
        snapshots.insert(root, eligible_snapshot());
        // Child 2 is eligible, no children -> 0 legs -> no tier
        snapshots.insert(test_uuid(2), eligible_snapshot());
        // Child 3 is ineligible (low PV)
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                personal_volume: 10.0,
                ..eligible_snapshot()
            },
        );
        // Child 4 is eligible, no children -> 0 legs -> no tier
        snapshots.insert(test_uuid(4), eligible_snapshot());

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);

        let root_elig = &cache[&root];
        assert!(root_elig.eligible);
        assert_eq!(root_elig.max_earning_depth, None); // unlimited tier

        let child2_elig = &cache[&test_uuid(2)];
        assert!(child2_elig.eligible);
        assert_eq!(child2_elig.max_earning_depth, None); // no legs, no tier

        let child3_elig = &cache[&test_uuid(3)];
        assert!(!child3_elig.eligible);
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
}
