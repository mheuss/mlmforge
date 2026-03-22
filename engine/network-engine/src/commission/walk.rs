//! Shared level-commission walk logic.
//!
//! Extracts the common prep phase and walk loop used by unilevel,
//! matrix, and stairstep (Walk 1) commission calculators. Each
//! calculator remains a standalone public function that delegates
//! to these shared internals.
//!
//! Binary uses pairing mechanics, not level-based walks. It is
//! not a consumer of this module.

// All items are pub(crate) and consumed by unilevel, matrix, and
// stairstep calculators.

use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

use crate::config::CompensationPlan;
use crate::config::commission::{CompressionConfig, CompressionMode};
use crate::config::eligibility::{ActiveLegTier, CommissionEligibility};
use crate::tree::navigator::TreeNavigator;

use super::is_eligible;
use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/// Cached eligibility result for a single distributor.
///
/// Built during the prep phase and consumed by the walk phase via
/// O(1) HashMap lookups.
pub(crate) struct EligibilityResult {
    pub eligible: bool,
    /// Per-distributor earning depth limit from active leg tiers.
    /// None means no tier restriction (use config max_depth).
    pub max_earning_depth: Option<u8>,
}

/// Configuration for a level-based commission walk.
///
/// Built once per calculation from the structure config. Bundles
/// parameters that are identical across unilevel, matrix, and
/// stairstep Walk 1. The caller computes any plan-specific values
/// (e.g., matrix height ceiling) before constructing this struct.
pub(crate) struct LevelWalkConfig<'a> {
    /// u8 is sufficient because realistic max_depth values are 1-20. A
    /// 255-level commission plan doesn't exist. If level saturates at
    /// u8::MAX due to a pathological config, the walk would emit extra
    /// earnings at level 255. The Go validation pipeline enforces
    /// reasonable max_depth values upstream, so we accept this as a
    /// known non-risk rather than widening to u16.
    pub max_depth: u8,
    pub broad_pct: f64,
    pub multiplier: f64,
    pub compression: Option<&'a CompressionConfig>,
    pub threshold_ordinal: Option<u16>,
    pub rank_ordinals: &'a HashMap<&'a str, u16>,
    pub rate_table: &'a BTreeMap<String, BTreeMap<u8, f64>>,
}

// ---------------------------------------------------------------------------
// Prep utilities (tree-agnostic)
// ---------------------------------------------------------------------------

/// Build rank name -> ordinal map from the plan's rank definitions.
///
/// Used for SkipBelowRank compression comparison and breakaway
/// threshold detection.
pub(crate) fn build_rank_ordinals(plan: &CompensationPlan) -> HashMap<&str, u16> {
    plan.ranks
        .iter()
        .map(|r| (r.name.as_str(), r.ordinal))
        .collect()
}

/// Resolve the SkipBelowRank threshold ordinal from compression config.
///
/// Returns `Some(ordinal)` when compression is configured with
/// SkipBelowRank mode and the threshold rank exists in the plan.
/// Returns `None` otherwise (with warnings for misconfiguration).
pub(crate) fn resolve_threshold_ordinal(
    compression: Option<&CompressionConfig>,
    rank_ordinals: &HashMap<&str, u16>,
) -> Option<u16> {
    compression.and_then(|c| {
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
    })
}

/// Determine per-distributor max earning depth from active leg tiers.
///
/// Returns `Some(depth)` if a tier limits the distributor, or `None`
/// if no tier restriction applies (use config max_depth as ceiling).
///
/// Tiers must be sorted ascending by `min_active_legs`. The caller
/// (Go validation pipeline) enforces this via business rules requiring
/// a base tier with min_active_legs=0.
pub(crate) fn determine_max_depth(active_leg_count: u16, tiers: &[ActiveLegTier]) -> Option<u8> {
    if tiers.is_empty() {
        return None;
    }

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

/// Validate that a volume source's CV amount is finite and non-negative.
pub(crate) fn validate_cv(source: &VolumeSource) -> Result<(), CalculationError> {
    if !source.cv_amount.is_finite() || source.cv_amount < 0.0 {
        return Err(CalculationError::InvalidCvAmount(
            source.source_id,
            source.cv_amount,
        ));
    }
    Ok(())
}

/// Sort earnings by (earner_id, source_id) for deterministic output.
///
/// Without sorting, the order depends on BFS traversal and volume
/// source iteration, both of which can vary across runs.
pub(crate) fn sort_earnings(earnings: &mut [CommissionEarning]) {
    earnings.sort_by(|a, b| {
        a.earner_id
            .cmp(&b.earner_id)
            .then_with(|| a.source_id.cmp(&b.source_id))
    });
}

// ---------------------------------------------------------------------------
// Prep functions (generic over TreeNavigator)
// ---------------------------------------------------------------------------

/// Count how many personally sponsored distributors are commission-eligible.
///
/// Active leg tiers are defined in terms of personally sponsored frontline
/// legs, not placement children. In a matrix with spillover, placement
/// parent can differ from sponsor. Uses `get_sponsored` (sponsor edges)
/// per decision 021.
pub(crate) fn count_active_legs<T: TreeNavigator>(
    tree: &T,
    user_id: Uuid,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    eligibility: &CommissionEligibility,
) -> u16 {
    let sponsored = match tree.get_sponsored(user_id) {
        Ok(s) => s,
        Err(_) => return 0,
    };

    sponsored
        .iter()
        .filter(|node| {
            snapshots
                .get(&node.user_id)
                .map(|s| is_eligible(s, eligibility))
                .unwrap_or(false)
        })
        .count() as u16
}

/// Evaluate eligibility for all distributors in the snapshot map.
///
/// Builds a cache of eligibility + depth limits used by the walk phase.
/// Runs once before any upline walks begin.
pub(crate) fn evaluate_eligibility<T: TreeNavigator>(
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    tree: &T,
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

// ---------------------------------------------------------------------------
// Walk function
// ---------------------------------------------------------------------------

/// Log a warning if broad_commission_percent is outside [0.0, 1.0].
///
/// Called once during config construction. The debug_assert catches
/// this in development; the log::warn surfaces it in production.
pub(crate) fn validate_broad_pct(broad_pct: f64) {
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
}

/// Walk the upline from each volume source, producing level commission earnings.
///
/// This is the shared walk loop used by unilevel, matrix, and stairstep
/// (Walk 1). Each calculator builds a `LevelWalkConfig` with its own
/// parameters and delegates to this function.
///
/// The `should_stop` callback is checked for each upline node before any
/// other logic. If it returns `true`, the walk breaks immediately for
/// this volume source. Unilevel and matrix pass `|_| false`. Stairstep
/// passes a closure that checks breakaway set membership.
///
/// Returns earnings unsorted. The caller is responsible for calling
/// `sort_earnings` after combining results from multiple walk phases
/// (e.g., stairstep combines Walk 1 and Walk 2 before sorting).
pub(crate) fn walk_level_commissions<T: TreeNavigator>(
    tree: &T,
    config: &LevelWalkConfig,
    eligibility_cache: &HashMap<Uuid, EligibilityResult>,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
    // Takes Uuid, not &Node. The closure only needs the ID for set
    // membership checks (e.g., breakaway detection). Passing Uuid
    // keeps captures simple — just &HashSet<Uuid>. If a future use
    // case needs node data in the stop decision, widen then.
    should_stop: impl Fn(Uuid) -> bool,
) -> Result<Vec<CommissionEarning>, CalculationError> {
    let mut all_earnings = Vec::new();

    for source in volume {
        validate_cv(source)?;

        let upline = tree
            .get_upline(source.source_id, 0)
            .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;

        if !snapshots.contains_key(&source.source_id) {
            return Err(CalculationError::SourceNotInSnapshot(source.source_id));
        }

        let mut level: u8 = 1;

        for node in &upline {
            if level > config.max_depth {
                break;
            }

            if should_stop(node.user_id) {
                break;
            }

            let snapshot = match snapshots.get(&node.user_id) {
                Some(s) => s,
                None => {
                    // Missing snapshot: treat as ineligible.
                    // With compression, skip without consuming a level.
                    // Without compression, forfeit the level.
                    if config.compression.is_some_and(|c| c.enabled) {
                        continue;
                    }
                    level = level.saturating_add(1);
                    continue;
                }
            };

            let elig = eligibility_cache.get(&node.user_id);
            let node_eligible = elig.is_some_and(|e| e.eligible);

            // Compression check
            let should_compress = match config.compression.filter(|c| c.enabled) {
                Some(compress) => match compress.mode {
                    CompressionMode::SkipInactive => !node_eligible,
                    CompressionMode::SkipBelowRank => {
                        let dist_ordinal = config
                            .rank_ordinals
                            .get(snapshot.rank.as_str())
                            .copied()
                            .unwrap_or(0);
                        config
                            .threshold_ordinal
                            .map(|t| dist_ordinal < t)
                            .unwrap_or(false)
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
            let rate = config
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
                    dollar_amount: source.cv_amount * config.broad_pct * config.multiplier * rate,
                });
            }

            level = level.saturating_add(1);
        }
    }

    Ok(all_earnings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::uuid_from_index;
    use crate::config::commission::{CompressionConfig, CompressionMode};
    use crate::config::eligibility::ActiveLegTier;
    use crate::tree::test_helpers::test_uuid;
    use crate::tree::unilevel::UnilevelTree;

    // --- build_rank_ordinals ---

    #[test]
    fn build_rank_ordinals_maps_names_to_ordinals() {
        let plan = crate::commission::test_helpers::build_test_plan(
            crate::commission::test_helpers::default_eligibility(),
            crate::config::StructureConfig::Unilevel(crate::config::UnilevelStructureConfig {
                name: "Test".to_string(),
                level_commission: crate::config::commission::LevelCommissionConfig {
                    broad_commission_percent: 0.40,
                    volume_to_dollar_multiplier: None,
                    max_depth: 5,
                    rate_table: std::collections::BTreeMap::new(),
                },
                compression: None,
                pass_up: None,
            }),
            "Test",
        );

        let ordinals = build_rank_ordinals(&plan);
        assert_eq!(ordinals.get("associate"), Some(&1));
    }

    // --- resolve_threshold_ordinal ---

    #[test]
    fn resolve_threshold_ordinal_none_when_no_compression() {
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(None, &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_none_for_skip_inactive() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_returns_ordinal_for_skip_below_rank() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        };
        let mut ordinals = HashMap::new();
        ordinals.insert("silver", 2u16);
        assert_eq!(resolve_threshold_ordinal(Some(&config), &ordinals), Some(2));
    }

    #[test]
    fn resolve_threshold_ordinal_none_when_rank_not_found() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("gold".to_string()),
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    #[test]
    fn resolve_threshold_ordinal_none_when_no_rank_threshold_set() {
        let config = CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: None,
        };
        let ordinals = HashMap::new();
        assert!(resolve_threshold_ordinal(Some(&config), &ordinals).is_none());
    }

    // --- determine_max_depth ---

    #[test]
    fn determine_max_depth_no_tiers() {
        assert_eq!(determine_max_depth(5, &[]), None);
    }

    #[test]
    fn determine_max_depth_matches_highest_qualifying_tier() {
        let tiers = vec![
            ActiveLegTier {
                min_active_legs: 0,
                max_commission_depth: 3,
            },
            ActiveLegTier {
                min_active_legs: 3,
                max_commission_depth: 5,
            },
        ];
        assert_eq!(determine_max_depth(3, &tiers), Some(5));
        assert_eq!(determine_max_depth(2, &tiers), Some(3));
    }

    #[test]
    fn determine_max_depth_zero_means_unlimited() {
        let tiers = vec![ActiveLegTier {
            min_active_legs: 0,
            max_commission_depth: 0,
        }];
        assert_eq!(determine_max_depth(0, &tiers), None);
    }

    // --- validate_cv ---

    #[test]
    fn validate_cv_accepts_positive() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: 100.0,
        };
        assert!(validate_cv(&source).is_ok());
    }

    #[test]
    fn validate_cv_accepts_zero() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: 0.0,
        };
        assert!(validate_cv(&source).is_ok());
    }

    #[test]
    fn validate_cv_rejects_negative() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: -1.0,
        };
        assert!(validate_cv(&source).is_err());
    }

    #[test]
    fn validate_cv_rejects_nan() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: f64::NAN,
        };
        assert!(validate_cv(&source).is_err());
    }

    #[test]
    fn validate_cv_rejects_infinity() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: f64::INFINITY,
        };
        assert!(validate_cv(&source).is_err());
    }

    // --- sort_earnings ---

    #[test]
    fn sort_earnings_by_earner_then_source() {
        let mut earnings = vec![
            CommissionEarning {
                earner_id: uuid_from_index(2),
                source_id: uuid_from_index(1),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
            CommissionEarning {
                earner_id: uuid_from_index(1),
                source_id: uuid_from_index(2),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
            CommissionEarning {
                earner_id: uuid_from_index(1),
                source_id: uuid_from_index(1),
                level: 1,
                rate: 0.05,
                cv_amount: 100.0,
                dollar_amount: 2.0,
            },
        ];

        sort_earnings(&mut earnings);

        assert_eq!(earnings[0].earner_id, uuid_from_index(1));
        assert_eq!(earnings[0].source_id, uuid_from_index(1));
        assert_eq!(earnings[1].earner_id, uuid_from_index(1));
        assert_eq!(earnings[1].source_id, uuid_from_index(2));
        assert_eq!(earnings[2].earner_id, uuid_from_index(2));
    }

    // --- count_active_legs ---

    #[test]
    fn count_active_legs_counts_eligible_sponsored() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();
        tree.add_node(test_uuid(3), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let elig = crate::commission::test_helpers::default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            crate::commission::test_helpers::eligible_snapshot(),
        );
        snapshots.insert(
            test_uuid(2),
            crate::commission::test_helpers::eligible_snapshot(),
        );
        snapshots.insert(
            test_uuid(3),
            DistributorSnapshot {
                personal_volume: 0.0, // below min
                ..crate::commission::test_helpers::eligible_snapshot()
            },
        );

        assert_eq!(count_active_legs(&tree, test_uuid(1), &snapshots, &elig), 1);
    }

    #[test]
    fn count_active_legs_user_not_in_tree() {
        let tree = UnilevelTree::new();
        let elig = crate::commission::test_helpers::default_eligibility();
        let snapshots = HashMap::new();
        assert_eq!(
            count_active_legs(&tree, test_uuid(99), &snapshots, &elig),
            0
        );
    }

    // --- evaluate_eligibility ---

    #[test]
    fn evaluate_eligibility_caches_all_distributors() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(1), 0).unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 0)
            .unwrap();

        let elig = crate::commission::test_helpers::default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(1),
            crate::commission::test_helpers::eligible_snapshot(),
        );
        snapshots.insert(
            test_uuid(2),
            DistributorSnapshot {
                personal_volume: 0.0, // ineligible
                ..crate::commission::test_helpers::eligible_snapshot()
            },
        );

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);

        assert!(cache.get(&test_uuid(1)).unwrap().eligible);
        assert!(!cache.get(&test_uuid(2)).unwrap().eligible);
    }

    // --- Additional tests from code quality review ---

    #[test]
    fn validate_cv_rejects_negative_infinity() {
        let source = VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: f64::NEG_INFINITY,
        };
        assert!(validate_cv(&source).is_err());
    }

    #[test]
    fn determine_max_depth_no_tier_matches_with_nonempty_tiers() {
        // Tiers exist but active_leg_count is below all thresholds
        let tiers = vec![ActiveLegTier {
            min_active_legs: 5,
            max_commission_depth: 10,
        }];
        assert_eq!(determine_max_depth(2, &tiers), None);
    }

    // --- walk_level_commissions ---

    fn test_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();
        let mut rates = BTreeMap::new();
        rates.insert(1, 0.05);
        rates.insert(2, 0.04);
        rates.insert(3, 0.03);
        table.insert("associate".to_string(), rates);
        table
    }

    fn test_walk_config<'a>(
        rank_ordinals: &'a HashMap<&'a str, u16>,
        rate_table: &'a BTreeMap<String, BTreeMap<u8, f64>>,
    ) -> LevelWalkConfig<'a> {
        LevelWalkConfig {
            max_depth: 5,
            broad_pct: 0.40,
            multiplier: 1.0,
            compression: None,
            threshold_ordinal: None,
            rank_ordinals,
            rate_table,
        }
    }

    #[test]
    fn walk_basic_single_level() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), test_uuid(0), 1)
            .unwrap();

        let elig = crate::commission::test_helpers::default_eligibility();
        let mut snapshots = HashMap::new();
        snapshots.insert(
            test_uuid(0),
            crate::commission::test_helpers::eligible_snapshot(),
        );
        snapshots.insert(
            test_uuid(1),
            crate::commission::test_helpers::eligible_snapshot(),
        );

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);
        let rank_ordinals = HashMap::from([("associate", 1u16)]);
        let rate_table = test_rate_table();
        let config = test_walk_config(&rank_ordinals, &rate_table);

        let volume = vec![VolumeSource {
            source_id: test_uuid(1),
            cv_amount: 100.0,
        }];

        let result =
            walk_level_commissions(&tree, &config, &cache, &snapshots, &volume, |_| false).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(0));
        assert_eq!(result[0].level, 1);
        assert_eq!(result[0].rate, 0.05);
        assert!((result[0].dollar_amount - 2.0).abs() < 1e-10); // 100 * 0.40 * 1.0 * 0.05
    }

    #[test]
    fn walk_should_stop_breaks_at_callback() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(0), 0).unwrap();
        tree.add_node(test_uuid(1), test_uuid(0), test_uuid(0), 1)
            .unwrap();
        tree.add_node(test_uuid(2), test_uuid(1), test_uuid(1), 2)
            .unwrap();

        let elig = crate::commission::test_helpers::default_eligibility();
        let mut snapshots = HashMap::new();
        for i in 0..3u8 {
            snapshots.insert(
                test_uuid(i),
                crate::commission::test_helpers::eligible_snapshot(),
            );
        }

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);
        let rank_ordinals = HashMap::from([("associate", 1u16)]);
        let rate_table = test_rate_table();
        let config = test_walk_config(&rank_ordinals, &rate_table);

        let volume = vec![VolumeSource {
            source_id: test_uuid(2),
            cv_amount: 100.0,
        }];

        // Stop at node 0 (the root). Only node 1 should earn.
        let stop_set: std::collections::HashSet<Uuid> = [test_uuid(0)].into_iter().collect();
        let result = walk_level_commissions(&tree, &config, &cache, &snapshots, &volume, |id| {
            stop_set.contains(&id)
        })
        .unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, test_uuid(1));
        assert_eq!(result[0].level, 1);
    }

    #[test]
    fn walk_source_not_in_tree_returns_error() {
        let tree = UnilevelTree::new();
        let rank_ordinals = HashMap::new();
        let rate_table = BTreeMap::new();
        let config = test_walk_config(&rank_ordinals, &rate_table);
        let snapshots = HashMap::new();
        let cache = HashMap::new();

        let volume = vec![VolumeSource {
            source_id: test_uuid(99),
            cv_amount: 100.0,
        }];

        let result = walk_level_commissions(&tree, &config, &cache, &snapshots, &volume, |_| false);
        assert!(matches!(result, Err(CalculationError::SourceNotInTree(_))));
    }

    #[test]
    fn walk_respects_max_depth() {
        let mut tree = UnilevelTree::new();
        tree.add_root(test_uuid(0), 0).unwrap();
        for i in 1..=5u8 {
            tree.add_node(test_uuid(i), test_uuid(i - 1), test_uuid(i - 1), i as i64)
                .unwrap();
        }

        let elig = crate::commission::test_helpers::default_eligibility();
        let mut snapshots = HashMap::new();
        for i in 0..=5u8 {
            snapshots.insert(
                test_uuid(i),
                crate::commission::test_helpers::eligible_snapshot(),
            );
        }

        let cache = evaluate_eligibility(&snapshots, &tree, &elig);
        let rank_ordinals = HashMap::from([("associate", 1u16)]);
        let rate_table = test_rate_table(); // only has levels 1-3

        let mut config = test_walk_config(&rank_ordinals, &rate_table);
        config.max_depth = 2;

        let volume = vec![VolumeSource {
            source_id: test_uuid(5),
            cv_amount: 100.0,
        }];

        let result =
            walk_level_commissions(&tree, &config, &cache, &snapshots, &volume, |_| false).unwrap();

        // max_depth=2, so only levels 1 and 2 earn
        assert_eq!(result.len(), 2);
        for earning in &result {
            assert!(earning.level <= 2);
        }
    }
}
