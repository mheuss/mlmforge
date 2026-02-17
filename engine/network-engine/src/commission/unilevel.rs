//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::commission::CompressionMode;
use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

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
    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let compression_enabled = compression.is_some_and(|c| c.enabled);

    let threshold_ordinal = compression.and_then(|c| {
        if matches!(c.mode, CompressionMode::SkipBelowRank) {
            c.rank_threshold
                .as_ref()
                .and_then(|name| rank_ordinals.get(name.as_str()).copied())
        } else {
            None
        }
    });

    let mut all_earnings = Vec::new();

    for source in volume {
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
            let should_compress = if compression_enabled {
                let compress = compression.unwrap();
                match compress.mode {
                    CompressionMode::SkipInactive => !node_eligible,
                    CompressionMode::SkipBelowRank => {
                        let dist_ordinal = rank_ordinals
                            .get(snapshot.rank.as_str())
                            .copied()
                            .unwrap_or(0);
                        threshold_ordinal.map(|t| dist_ordinal < t).unwrap_or(false)
                    }
                }
            } else {
                false
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

/// Check if a distributor meets basic commission eligibility.
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
                debug_assert!(
                    tier.max_commission_depth <= u8::MAX as u16,
                    "max_commission_depth {} exceeds u8 range",
                    tier.max_commission_depth
                );
                Some(tier.max_commission_depth as u8)
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
    use crate::config::bonus::BonusConfig;
    use crate::config::commission::{CompressionConfig, CompressionMode, LevelCommissionConfig};
    use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
    use crate::config::payout::{CapEnforcement, CapsConfig, PaymentMethod, PayoutConfig};
    use crate::config::period::{PeriodConfig, PeriodLength};
    use crate::config::placement::PlacementConfig;
    use crate::config::rank::{
        DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
    };
    use crate::config::volume::VolumeConfig;
    use crate::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
    use std::collections::BTreeMap;

    fn test_uuid(n: u8) -> Uuid {
        Uuid::from_bytes([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, n])
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
        CompensationPlan {
            name: "Test Plan".to_string(),
            version: 1,
            structures: vec![StructureConfig::Unilevel(structure)],
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
            ranks: vec![
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
            ],
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
                payment_methods: vec![PaymentMethod {
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
        tree.add_node(test_uuid(2), root, 0).unwrap(); // eligible child
        tree.add_node(test_uuid(3), root, 0).unwrap(); // ineligible child
        tree.add_node(test_uuid(4), root, 0).unwrap(); // eligible child

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
        tree.add_node(test_uuid(2), root, 0).unwrap();
        tree.add_node(test_uuid(3), root, 0).unwrap();

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
        tree.add_node(test_uuid(2), root, 0).unwrap();
        tree.add_node(test_uuid(3), root, 0).unwrap();
        tree.add_node(test_uuid(4), root, 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(2), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(3), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(4), 0).unwrap();

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
        tree.add_node(test_uuid(2), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(4), test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(5), test_uuid(2), 0).unwrap();

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
}
