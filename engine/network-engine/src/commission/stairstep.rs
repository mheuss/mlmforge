//! Stairstep breakaway commission calculator.
//!
//! In a stairstep plan, downline groups "break away" when their leader
//! reaches a threshold rank. Walk 1 pays level commissions within each
//! group, stopping at breakaway boundaries. Walk 2 pays differential
//! and generation overrides on breakaway group volume.

use std::collections::{HashMap, HashSet};
use uuid::Uuid;

use crate::config::{CompensationPlan, StairstepStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::generation::count_generations_upward;
use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
use super::walk;

// ---------------------------------------------------------------------------
// Prep phase internal types
// ---------------------------------------------------------------------------

/// Aggregated prep phase results used by the walk phases.
struct PrepResult {
    eligibility: HashMap<Uuid, walk::EligibilityResult>,
    breakaways: HashSet<Uuid>,
    group_volumes: HashMap<Uuid, f64>,
    group_leaders: HashMap<Uuid, Uuid>,
}

// ---------------------------------------------------------------------------
// Prep phase functions
// ---------------------------------------------------------------------------

/// Identify distributors whose rank meets or exceeds the breakaway threshold.
///
/// A distributor is "broken away" when their rank ordinal is greater than
/// or equal to the threshold rank's ordinal. This ensures that a
/// senior_director is detected as breakaway when the threshold is director.
///
/// Returns an empty set if the structure has no breakaway config.
fn identify_breakaways(
    structure: &StairstepStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    rank_ordinals: &HashMap<&str, u16>,
) -> HashSet<Uuid> {
    let threshold = match &structure.breakaway {
        Some(b) => &b.threshold_rank,
        None => return HashSet::new(),
    };

    let threshold_ordinal = match rank_ordinals.get(threshold.as_str()).copied() {
        Some(ord) => ord,
        None => {
            log::warn!(
                "breakaway threshold_rank '{}' not found in plan ranks; \
                 no distributors will be treated as breakaway",
                threshold
            );
            return HashSet::new();
        }
    };

    snapshots
        .iter()
        .filter(|(_, snap)| {
            rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0) >= threshold_ordinal
        })
        .map(|(id, _)| *id)
        .collect()
}

/// Build group volume aggregations and group leader assignments.
///
/// For each distributor, the "group leader" is the nearest breakaway
/// ancestor (or themselves if they are breakaway). If no breakaway
/// ancestor exists, the root of the tree (or the distributor itself
/// if it has no upline) is the leader.
///
/// Each distributor's personal volume is accumulated into their group
/// leader's entry in the group volumes map.
///
/// Note: `BreakawayConfig::exclude_breakaway_gv` controls whether
/// breakaway group volume is excluded from the upline's totals for
/// rank qualification. Rank qualification happens upstream of the
/// calculator. By the time we run, snapshots already contain final
/// ranks. This function does not use that flag.
fn build_group_volumes(
    tree: &UnilevelTree,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    breakaways: &HashSet<Uuid>,
) -> (HashMap<Uuid, f64>, HashMap<Uuid, Uuid>) {
    let mut group_volumes: HashMap<Uuid, f64> = HashMap::new();
    let mut group_leaders: HashMap<Uuid, Uuid> = HashMap::new();

    for (user_id, snapshot) in snapshots {
        let leader = find_group_leader(tree, *user_id, breakaways);
        group_leaders.insert(*user_id, leader);
        *group_volumes.entry(leader).or_insert(0.0) += snapshot.personal_volume;
    }

    (group_volumes, group_leaders)
}

/// Find the group leader for a distributor.
///
/// If the distributor is breakaway, they are their own group leader.
/// Otherwise, walk upline to find the nearest breakaway ancestor.
/// If no breakaway ancestor exists, use the root (last node in upline,
/// or self if no upline).
fn find_group_leader(tree: &UnilevelTree, user_id: Uuid, breakaways: &HashSet<Uuid>) -> Uuid {
    if breakaways.contains(&user_id) {
        return user_id;
    }

    let upline = match tree.get_upline(user_id, 0) {
        Ok(nodes) => nodes,
        Err(_) => return user_id,
    };

    for node in &upline {
        if breakaways.contains(&node.user_id) {
            return node.user_id;
        }
    }

    // No breakaway ancestor. Root is the last in upline, or self.
    upline.last().map(|n| n.user_id).unwrap_or(user_id)
}

/// Run the full prep phase: eligibility, breakaway detection, group volumes.
fn prep(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &StairstepStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    rank_ordinals: &HashMap<&str, u16>,
) -> PrepResult {
    let eligibility = walk::evaluate_eligibility(snapshots, tree, &plan.eligibility);
    let breakaways = identify_breakaways(structure, snapshots, rank_ordinals);
    let (group_volumes, group_leaders) = build_group_volumes(tree, snapshots, &breakaways);

    PrepResult {
        eligibility,
        breakaways,
        group_volumes,
        group_leaders,
    }
}

// ---------------------------------------------------------------------------
// Walk 2: Differential and generation overrides
// ---------------------------------------------------------------------------

/// Walk 2 produces override earnings on breakaway group volume.
///
/// For each breakaway distributor, walk upline to find ancestors who
/// earn differential overrides. The differential is the ancestor's rank
/// rate minus the highest rate already paid in the walk. When the
/// differential is zero or negative but the ancestor has a rank rate,
/// `min_override` applies as a floor.
///
/// When generation overrides are configured, the generation counting
/// utility finds additional earners beyond the first. Generation 1 uses
/// the differential rate. Generations 2+ use rates from the generation
/// override table.
///
/// Dollar amounts use `broad_pct` (broad commission percent) because
/// overrides pay from the same commission pool as level commissions.
/// The pool is `cv * broad_pct * multiplier`, then split by rate.
fn walk_overrides(
    tree: &UnilevelTree,
    structure: &StairstepStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    prep: &PrepResult,
    rank_ordinals: &HashMap<&str, u16>,
    broad_pct: f64,
    multiplier: f64,
) -> Vec<CommissionEarning> {
    let breakaway_cfg = match &structure.breakaway {
        Some(b) => b,
        None => return Vec::new(),
    };

    // FixedOverride uses flat per-rank percentages, not differential
    // rates. That mode is not yet implemented (out of scope for HEU-32).
    if matches!(
        breakaway_cfg.override_calculation,
        crate::config::stairstep::OverrideCalculation::FixedOverride
    ) {
        return Vec::new();
    }

    let differential_cfg = match &breakaway_cfg.differential {
        Some(d) => d,
        None => return Vec::new(),
    };

    let mut earnings = Vec::new();

    // Pre-build generation override candidates if configured. This set
    // is the same for every breakaway, so we build it once rather than
    // per-iteration.
    let gen_override_candidates = breakaway_cfg
        .generation_overrides
        .as_ref()
        .and_then(|gen_cfg| {
            let boundary_ordinal = match rank_ordinals.get(gen_cfg.boundary_rank.as_str()).copied()
            {
                Some(ord) => ord,
                None => {
                    log::warn!(
                        "generation override boundary_rank '{}' not found in plan ranks; \
                                 generation overrides will be skipped",
                        gen_cfg.boundary_rank
                    );
                    return None;
                }
            };

            Some(
                snapshots
                    .iter()
                    .filter(|(_, snap)| {
                        rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0)
                            >= boundary_ordinal
                    })
                    .map(|(id, _)| *id)
                    .collect::<HashSet<Uuid>>(),
            )
        });

    for &breakaway_id in &prep.breakaways {
        let group_vol = prep
            .group_volumes
            .get(&breakaway_id)
            .copied()
            .unwrap_or(0.0);

        if group_vol <= 0.0 {
            continue;
        }

        let breakaway_rank = match snapshots.get(&breakaway_id) {
            Some(s) => &s.rank,
            None => continue,
        };

        let breakaway_rate = differential_cfg
            .rank_rates
            .get(breakaway_rank)
            .copied()
            .unwrap_or(0.0);

        if let (Some(gen_cfg), Some(override_candidates)) = (
            &breakaway_cfg.generation_overrides,
            gen_override_candidates.as_ref(),
        ) {
            // --- Generation override mode ---
            // The candidate set contains all distributors at or above the
            // boundary rank. This is broader than the prep breakaway set
            // because a senior_director (above the director threshold)
            // should count as a generation boundary.

            // boundary_check always returns true because the candidate
            // set already filters by rank. Every candidate is a boundary.
            let boundary_check = |_: Uuid| -> bool { true };

            let gen_entries = count_generations_upward(
                tree,
                breakaway_id,
                override_candidates,
                &boundary_check,
                gen_cfg.max_generations,
                false, // stairstep doesn't use empty_generation_consumes_number
            );

            for entry in &gen_entries {
                let ancestor_eligible = prep
                    .eligibility
                    .get(&entry.earner_id)
                    .is_some_and(|e| e.eligible);
                if !ancestor_eligible {
                    continue;
                }

                if entry.generation == 1 {
                    // Generation 1 uses the differential rate: the gap
                    // between the ancestor's rank rate and the breakaway's
                    // rank rate. If the gap is zero or negative, fall back
                    // to min_override. If neither applies, this ancestor
                    // earns nothing on this breakaway.
                    let ancestor_rank = snapshots
                        .get(&entry.earner_id)
                        .map(|s| s.rank.as_str())
                        .unwrap_or("");
                    let ancestor_rate = differential_cfg
                        .rank_rates
                        .get(ancestor_rank)
                        .copied()
                        .unwrap_or(0.0);

                    let diff = ancestor_rate - breakaway_rate;
                    let rate = if diff > 0.0 {
                        diff
                    } else if ancestor_rate > 0.0 && differential_cfg.min_override > 0.0 {
                        differential_cfg.min_override
                    } else {
                        continue;
                    };

                    earnings.push(CommissionEarning {
                        earner_id: entry.earner_id,
                        source_id: breakaway_id,
                        level: entry.generation,
                        rate,
                        cv_amount: group_vol,
                        dollar_amount: group_vol * broad_pct * multiplier * rate,
                    });
                } else {
                    // Generation 2+: rate from generation override table
                    let rate = gen_cfg.rates.get(&entry.generation).copied().unwrap_or(0.0);

                    if rate > 0.0 {
                        earnings.push(CommissionEarning {
                            earner_id: entry.earner_id,
                            source_id: breakaway_id,
                            level: entry.generation,
                            rate,
                            cv_amount: group_vol,
                            dollar_amount: group_vol * broad_pct * multiplier * rate,
                        });
                    }
                }
            }
        } else {
            // --- Differential-only mode (no generation overrides) ---
            // Walk upline from breakaway. First qualifying ancestor earns
            // the differential. Stop after one earner.
            let upline = match tree.get_upline(breakaway_id, 0) {
                Ok(nodes) => nodes,
                Err(_) => continue,
            };

            let mut highest_rate_paid = breakaway_rate;

            for node in &upline {
                let ancestor_eligible = prep
                    .eligibility
                    .get(&node.user_id)
                    .is_some_and(|e| e.eligible);
                if !ancestor_eligible {
                    continue;
                }

                let ancestor_rank = match snapshots.get(&node.user_id) {
                    Some(s) => &s.rank,
                    None => continue,
                };

                let ancestor_rate = differential_cfg
                    .rank_rates
                    .get(ancestor_rank)
                    .copied()
                    .unwrap_or(0.0);

                // Differential rate: the ancestor earns the gap between
                // their rank rate and the highest rate already paid in
                // this override chain. If the gap is zero or negative
                // (same or lower rank), fall back to min_override when
                // configured. If neither applies, skip this ancestor
                // but track their rate so the next ancestor computes
                // against the correct baseline.
                let diff = ancestor_rate - highest_rate_paid;
                let rate = if diff > 0.0 {
                    diff
                } else if ancestor_rate > 0.0 && differential_cfg.min_override > 0.0 {
                    differential_cfg.min_override
                } else {
                    if ancestor_rate > highest_rate_paid {
                        highest_rate_paid = ancestor_rate;
                    }
                    continue;
                };

                earnings.push(CommissionEarning {
                    earner_id: node.user_id,
                    source_id: breakaway_id,
                    level: 1,
                    rate,
                    cv_amount: group_vol,
                    dollar_amount: group_vol * broad_pct * multiplier * rate,
                });

                // Without generation overrides, stop after first qualifying ancestor.
                break;
            }
        }
    }

    earnings
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Calculate stairstep commissions for a set of volume events.
///
/// Three-phase approach (ADR-017):
/// 1. **Prep** — evaluate eligibility, detect breakaways by rank ordinal,
///    aggregate personal volume into group buckets, assign group leaders.
/// 2. **Walk 1** — level commissions within each group. Walks upline from
///    each volume source, stopping at breakaway boundaries. Supports
///    SkipInactive and SkipBelowRank compression.
/// 3. **Walk 2** — differential and generation overrides on breakaway group
///    volume. Each breakaway's qualified upline earns the rate differential
///    between their rank and the breakaway's rank.
///
/// # Parameters
///
/// * `tree` — the unilevel placement tree.
/// * `plan` — the full compensation plan (used for rank ordinals,
///   eligibility rules, and volume multiplier).
/// * `structure` — stairstep-specific config (level rates, breakaway
///   thresholds, differential rates, generation overrides).
/// * `snapshots` — current-period distributor data (rank, PV, status).
/// * `volume` — volume events to process. Each produces level earnings
///   and may trigger override earnings on breakaway groups.
///
/// # Returns
///
/// A flat `Vec<CommissionEarning>` sorted by `(earner_id, source_id)`.
/// Level earnings have `source_id` equal to the original volume source.
/// Override earnings have `source_id` equal to the breakaway distributor.
///
/// # Errors
///
/// Returns [`CalculationError`] if a volume source is not found in the
/// tree or snapshot data, or has a non-finite or negative cv_amount.
pub fn calculate_stairstep(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &StairstepStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    let rank_ordinals = walk::build_rank_ordinals(plan);
    let prep_result = prep(tree, plan, structure, snapshots, &rank_ordinals);

    let broad_pct = structure.level_commission.broad_commission_percent;
    walk::validate_broad_pct(broad_pct);

    let multiplier = structure
        .level_commission
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let compression = structure.compression.as_ref();
    let threshold_ordinal = walk::resolve_threshold_ordinal(compression, &rank_ordinals);

    let config = walk::LevelWalkConfig {
        max_depth: structure.level_commission.max_depth,
        broad_pct,
        multiplier,
        compression,
        compression_enabled: compression.is_some_and(|c| c.enabled),
        threshold_ordinal,
        rank_ordinals: &rank_ordinals,
        rate_table: &structure.level_commission.rate_table,
    };

    // Walk 1: level commissions, stopping at breakaway boundaries.
    //
    // The breakaway boundary check depends on which source is being
    // walked (each source has its own group leader). We call
    // walk_level_commissions per-source so the stop closure captures
    // the correct source_leader for each iteration.
    let mut all_earnings = Vec::new();

    for source in volume {
        let source_leader = prep_result
            .group_leaders
            .get(&source.source_id)
            .copied()
            .unwrap_or(source.source_id);

        let source_earnings = walk::walk_level_commissions(
            tree,
            &config,
            &prep_result.eligibility,
            snapshots,
            std::slice::from_ref(source),
            |id| {
                prep_result
                    .group_leaders
                    .get(&id)
                    .map(|&leader| leader != source_leader)
                    .unwrap_or(false)
            },
        )?;
        all_earnings.extend(source_earnings);
    }

    // Walk 2: differential and generation overrides (stairstep-specific)
    let override_earnings = walk_overrides(
        tree,
        structure,
        snapshots,
        &prep_result,
        &rank_ordinals,
        broad_pct,
        multiplier,
    );
    all_earnings.extend(override_earnings);

    walk::sort_earnings(&mut all_earnings);
    Ok(all_earnings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::{build_test_plan, default_eligibility};
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::eligibility::CommissionEligibility;
    use crate::config::rank::{DemotionPolicy, RankDefinition, RankQualification};
    use crate::config::stairstep::{
        BreakawayConfig, BreakawayGenerationConfig, DifferentialConfig, OverrideCalculation,
    };
    use crate::config::{StairstepStructureConfig, StructureConfig};
    use std::collections::BTreeMap;

    use crate::commission::test_helpers::uuid_from_index as uuid;

    fn build_chain(len: usize) -> UnilevelTree {
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();
        for i in 1..len {
            tree.add_node(uuid(i), uuid(i - 1), uuid(i - 1), i as i64)
                .unwrap();
        }
        tree
    }

    fn snapshot_with_rank(rank: &str, pv: f64) -> DistributorSnapshot {
        DistributorSnapshot {
            rank: rank.to_string(),
            personal_volume: pv,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    fn test_stairstep_rate_table() -> BTreeMap<String, BTreeMap<u8, f64>> {
        let mut table = BTreeMap::new();

        let mut associate = BTreeMap::new();
        for level in 1..=5 {
            associate.insert(level, 0.05);
        }
        table.insert("associate".to_string(), associate);

        let mut director = BTreeMap::new();
        for level in 1..=5 {
            director.insert(level, 0.05);
        }
        table.insert("director".to_string(), director);

        table
    }

    fn test_stairstep_structure() -> StairstepStructureConfig {
        StairstepStructureConfig {
            name: "Test".to_string(),
            level_commission: LevelCommissionConfig {
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth: 5,
                rate_table: test_stairstep_rate_table(),
            },
            compression: None,
            breakaway: Some(BreakawayConfig {
                threshold_rank: "director".to_string(),
                exclude_breakaway_gv: true,
                override_calculation: OverrideCalculation::Differential,
                differential: Some(DifferentialConfig {
                    rank_rates: {
                        let mut m = BTreeMap::new();
                        m.insert("director".to_string(), 0.10);
                        m.insert("senior_director".to_string(), 0.15);
                        m
                    },
                    min_override: 0.02,
                }),
                generation_overrides: None,
            }),
        }
    }

    fn build_test_stairstep_plan(
        eligibility: CommissionEligibility,
        structure: StairstepStructureConfig,
    ) -> CompensationPlan {
        let mut plan = build_test_plan(eligibility, StructureConfig::Stairstep(structure), "Test");
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "director".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "senior_director".to_string(),
                ordinal: 3,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                },
                qualified_structures: vec!["Test".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    // --- Prep phase tests ---

    fn test_rank_ordinals() -> HashMap<&'static str, u16> {
        let mut m = HashMap::new();
        m.insert("associate", 1);
        m.insert("director", 2);
        m.insert("senior_director", 3);
        m
    }

    #[test]
    fn identify_breakaways_finds_threshold_rank() {
        let structure = test_stairstep_structure();
        let rank_ordinals = test_rank_ordinals();
        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(3), snapshot_with_rank("director", 150.0));

        let breakaways = identify_breakaways(&structure, &snapshots, &rank_ordinals);

        assert_eq!(breakaways.len(), 2);
        assert!(breakaways.contains(&uuid(1)));
        assert!(breakaways.contains(&uuid(3)));
        assert!(!breakaways.contains(&uuid(0)));
        assert!(!breakaways.contains(&uuid(2)));
    }

    #[test]
    fn identify_breakaways_includes_ranks_above_threshold() {
        let structure = test_stairstep_structure();
        let rank_ordinals = test_rank_ordinals();
        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("senior_director", 150.0));

        let breakaways = identify_breakaways(&structure, &snapshots, &rank_ordinals);

        assert_eq!(breakaways.len(), 2);
        assert!(breakaways.contains(&uuid(1)), "director meets threshold");
        assert!(
            breakaways.contains(&uuid(2)),
            "senior_director exceeds threshold"
        );
        assert!(!breakaways.contains(&uuid(0)), "associate below threshold");
    }

    #[test]
    fn identify_breakaways_empty_when_no_breakaway_config() {
        let mut structure = test_stairstep_structure();
        structure.breakaway = None;
        let rank_ordinals = test_rank_ordinals();

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("director", 150.0));

        let breakaways = identify_breakaways(&structure, &snapshots, &rank_ordinals);

        assert!(breakaways.is_empty());
    }

    #[test]
    fn group_volumes_no_breakaway() {
        // Tree: 0 -> 1 -> 2 -> 3
        // No breakaways. All volume flows to root.
        let tree = build_chain(4);
        let breakaways = HashSet::new();

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 100.0));
        snapshots.insert(uuid(1), snapshot_with_rank("associate", 200.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 300.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 400.0));

        let (group_volumes, group_leaders) = build_group_volumes(&tree, &snapshots, &breakaways);

        // All distributors should have root (uuid(0)) as their group leader
        for i in 0..4 {
            assert_eq!(
                group_leaders[&uuid(i)],
                uuid(0),
                "distributor {} should have root as group leader",
                i
            );
        }

        // Root's group volume should be the sum of all PV
        assert!(
            (group_volumes[&uuid(0)] - 1000.0).abs() < f64::EPSILON,
            "root group volume should be 1000.0, got {}",
            group_volumes[&uuid(0)]
        );
    }

    #[test]
    fn group_volumes_with_breakaway() {
        // Tree: 0 -> 1 -> 2 -> 3
        // uuid(1) is breakaway (director).
        // uuid(0) is root, not breakaway.
        // Expected:
        //   uuid(0)'s group: just uuid(0) = 100 PV
        //   uuid(1)'s group: uuid(1), uuid(2), uuid(3) = 200+300+400 = 900 PV
        let tree = build_chain(4);
        let mut breakaways = HashSet::new();
        breakaways.insert(uuid(1));

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 100.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 200.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 300.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 400.0));

        let (group_volumes, group_leaders) = build_group_volumes(&tree, &snapshots, &breakaways);

        // uuid(0) is its own leader (root, no breakaway ancestor)
        assert_eq!(group_leaders[&uuid(0)], uuid(0));
        // uuid(1) is breakaway, so its own leader
        assert_eq!(group_leaders[&uuid(1)], uuid(1));
        // uuid(2) and uuid(3) belong to uuid(1)'s group
        assert_eq!(group_leaders[&uuid(2)], uuid(1));
        assert_eq!(group_leaders[&uuid(3)], uuid(1));

        assert!(
            (group_volumes[&uuid(0)] - 100.0).abs() < f64::EPSILON,
            "root group volume = {}",
            group_volumes[&uuid(0)]
        );
        assert!(
            (group_volumes[&uuid(1)] - 900.0).abs() < f64::EPSILON,
            "breakaway group volume = {}",
            group_volumes[&uuid(1)]
        );
    }

    #[test]
    fn group_volumes_multiple_breakaways() {
        // Tree: 0 -> 1 -> 2 -> 3 -> 4
        // uuid(1) and uuid(3) are breakaway.
        // Expected groups:
        //   uuid(0): just uuid(0) = 100
        //   uuid(1): uuid(1), uuid(2) = 200 + 300 = 500
        //   uuid(3): uuid(3), uuid(4) = 400 + 500 = 900
        let tree = build_chain(5);
        let mut breakaways = HashSet::new();
        breakaways.insert(uuid(1));
        breakaways.insert(uuid(3));

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 100.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 200.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 300.0));
        snapshots.insert(uuid(3), snapshot_with_rank("director", 400.0));
        snapshots.insert(uuid(4), snapshot_with_rank("associate", 500.0));

        let (group_volumes, group_leaders) = build_group_volumes(&tree, &snapshots, &breakaways);

        assert_eq!(group_leaders[&uuid(0)], uuid(0));
        assert_eq!(group_leaders[&uuid(1)], uuid(1));
        assert_eq!(group_leaders[&uuid(2)], uuid(1));
        assert_eq!(group_leaders[&uuid(3)], uuid(3));
        assert_eq!(group_leaders[&uuid(4)], uuid(3));

        assert!((group_volumes[&uuid(0)] - 100.0).abs() < f64::EPSILON);
        assert!((group_volumes[&uuid(1)] - 500.0).abs() < f64::EPSILON);
        assert!((group_volumes[&uuid(3)] - 900.0).abs() < f64::EPSILON);
    }

    // --- Walk 1 tests ---

    #[test]
    fn no_breakaways_behaves_like_unilevel() {
        // Tree: 0 -> 1 -> 2
        // No breakaway config. Should behave exactly like unilevel.
        let tree = build_chain(3);
        let mut structure = test_stairstep_structure();
        structure.breakaway = None;
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);

        // uuid(1) at level 1: 100 * 0.40 * 1.0 * 0.05 = 2.0
        let e1 = result.iter().find(|e| e.earner_id == uuid(1)).unwrap();
        assert_eq!(e1.level, 1);
        assert!((e1.dollar_amount - 2.0).abs() < f64::EPSILON);

        // uuid(0) at level 2: 100 * 0.40 * 1.0 * 0.05 = 2.0
        let e0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(e0.level, 2);
        assert!((e0.dollar_amount - 2.0).abs() < f64::EPSILON);
    }

    #[test]
    fn level_commissions_stop_at_breakaway() {
        // Tree: 0(assoc) -> 1(director) -> 2(assoc) -> 3(assoc)
        // uuid(1) is breakaway (director rank = threshold).
        // Volume from uuid(3).
        // Walk from uuid(3): uuid(2) is in uuid(1)'s group (level 1).
        // uuid(1) is breakaway, its own group leader. Source uuid(3)
        // belongs to uuid(1)'s group, so uuid(1) is also in the same
        // group. uuid(0) belongs to a different group -> stop.
        let tree = build_chain(4);
        let structure = test_stairstep_structure();
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // uuid(2) earns at level 1, uuid(1) earns at level 2.
        // uuid(0) is in a different group, so the walk stops before it.
        assert_eq!(result.len(), 2);

        let earner_ids: Vec<Uuid> = result.iter().map(|e| e.earner_id).collect();
        assert!(earner_ids.contains(&uuid(1)));
        assert!(earner_ids.contains(&uuid(2)));
        assert!(!earner_ids.contains(&uuid(0)));
    }

    #[test]
    fn missing_snapshot_ancestor_does_not_block_walk() {
        // Tree: 0 -> 1 -> 2 -> 3
        // No breakaway config. Node 1 has no snapshot.
        // Walk from node 3: node 2 earns at level 1, node 1 is skipped
        // (no snapshot = ineligible), node 0 earns at level 3.
        // The walk must not treat node 1 as a group boundary.
        let tree = build_chain(4);
        let mut structure = test_stairstep_structure();
        structure.breakaway = None;
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));
        // uuid(1) intentionally omitted — no snapshot
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Node 0 should still earn despite node 1 missing a snapshot.
        let earner_ids: Vec<Uuid> = result.iter().map(|e| e.earner_id).collect();
        assert!(
            earner_ids.contains(&uuid(0)),
            "node 0 should earn — missing snapshot on node 1 must not block the walk"
        );
        assert!(earner_ids.contains(&uuid(2)));
    }

    #[test]
    fn single_node_empty_result() {
        // Single root, volume from root. No upline -> no earnings.
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();

        let structure = test_stairstep_structure();
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(0),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert!(result.is_empty());
    }

    // --- Walk 2: Differential override tests ---

    /// Tolerance for floating-point comparisons involving differential
    /// rates. Subtraction of f64 values (e.g., 0.15 - 0.10) produces
    /// rounding error beyond f64::EPSILON when multiplied through the
    /// dollar_amount formula.
    const FP_TOL: f64 = 1e-10;

    #[test]
    fn single_breakaway_differential_override() {
        // Tree: 0(senior_director) -> 1(director) -> 2(assoc) -> 3(assoc)
        // Node 1 is breakaway (director = threshold_rank).
        // Node 0 is senior_director, earns override on node 1's group volume.
        // Differential = senior_director rate (0.15) - director rate (0.10) = 0.05
        // Group volume for node 1 = 200 + 300 + 400 = 900
        // Dollar amount = 900 * 0.40 * 1.0 * 0.05 = 18.0
        let tree = build_chain(4);
        let structure = test_stairstep_structure();
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("senior_director", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 200.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 300.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 400.0));

        // Volume from node 3 to trigger Walk 1 and Walk 2.
        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Walk 2 should produce an override earning for node 0
        let override_earning = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.source_id == uuid(1))
            .expect("node 0 should earn override on breakaway node 1");

        assert_eq!(override_earning.level, 1);
        assert!((override_earning.rate - 0.05).abs() < FP_TOL);
        assert!((override_earning.cv_amount - 900.0).abs() < FP_TOL);
        assert!((override_earning.dollar_amount - 18.0).abs() < FP_TOL);
    }

    #[test]
    fn zero_differential_skipped() {
        // Tree: 0(director) -> 1(director) -> 2(assoc)
        // Both nodes 0 and 1 are director. Differential = 0. min_override = 0.0.
        // No override earnings should be produced.
        let tree = build_chain(3);
        let mut structure = test_stairstep_structure();
        structure
            .breakaway
            .as_mut()
            .unwrap()
            .differential
            .as_mut()
            .unwrap()
            .min_override = 0.0;
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // No override earnings with source_id == uuid(1)
        let override_earnings: Vec<_> = result.iter().filter(|e| e.source_id == uuid(1)).collect();
        assert!(
            override_earnings.is_empty(),
            "zero differential with zero min_override should produce no earnings"
        );
    }

    #[test]
    fn min_override_floor_applied() {
        // Tree: 0(director) -> 1(director) -> 2(assoc)
        // Both are director. Differential = 0. min_override = 0.02.
        // Node 0 should earn override at min_override rate.
        // Group volume for node 1 = 150 + 150 = 300
        // Dollar amount = 300 * 0.40 * 1.0 * 0.02 = 2.40
        let tree = build_chain(3);
        let structure = test_stairstep_structure();
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let override_earning = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.source_id == uuid(1))
            .expect("node 0 should earn min_override on breakaway node 1");

        assert!((override_earning.rate - 0.02).abs() < FP_TOL);
        assert!((override_earning.cv_amount - 300.0).abs() < FP_TOL);
        assert!((override_earning.dollar_amount - 2.40).abs() < FP_TOL);
    }

    #[test]
    fn breakaway_at_root_no_override() {
        // Tree: 0(director) only. Node 0 is breakaway, no ancestors above.
        // No override earnings.
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();

        let structure = test_stairstep_structure();
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("director", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(0),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // No override earnings (source_id == uuid(0) as breakaway)
        let override_earnings: Vec<_> = result.iter().filter(|e| e.source_id == uuid(0)).collect();
        assert!(
            override_earnings.is_empty(),
            "breakaway at root should have no override earnings"
        );
    }

    #[test]
    fn compression_applies_to_levels_not_overrides() {
        // Tree: 0(senior_director) -> 1(inactive assoc) -> 2(director) -> 3(assoc)
        // SkipInactive compression is enabled.
        // Walk 1: node 1 is compressed out.
        // Walk 2: node 0 should still earn override on breakaway node 2.
        // Node 1 being inactive should not block the override walk.
        use crate::config::commission::CompressionConfig;

        let tree = build_chain(4);
        let mut structure = test_stairstep_structure();
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: crate::config::commission::CompressionMode::SkipInactive,
            rank_threshold: None,
        });
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("senior_director", 150.0));
        // Node 1 is inactive (PV below minimum 100)
        snapshots.insert(uuid(1), snapshot_with_rank("associate", 50.0));
        snapshots.insert(uuid(2), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(3), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Node 0 should earn override on breakaway node 2's group volume
        // Group volume for node 2: 150 + 150 = 300 (node 2 + node 3)
        // Differential = 0.15 - 0.10 = 0.05
        // Dollar amount = 300 * 0.40 * 1.0 * 0.05 = 6.0
        let override_earning = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.source_id == uuid(2))
            .expect("node 0 should earn override despite inactive node 1 in between");

        assert!((override_earning.rate - 0.05).abs() < FP_TOL);
        assert!((override_earning.cv_amount - 300.0).abs() < FP_TOL);
        assert!((override_earning.dollar_amount - 6.0).abs() < FP_TOL);
    }

    // --- Walk 2: Generation override tests ---

    #[test]
    fn generation_overrides_multi_breakaway() {
        // Tree: 0(senior_director) -> 1(director) -> 2(assoc) -> 3(director) -> 4(assoc)
        // Breakaways: nodes 1 and 3 (both director = threshold_rank).
        // Generation overrides configured with boundary_rank = "director".
        //
        // For breakaway node 3:
        //   Generation 1: node 1 (next breakaway up, director = boundary)
        //     Differential: director rate (0.10) - director rate (0.10) = 0.
        //     min_override = 0.02, so rate = 0.02.
        //   Generation 2: node 0 (senior_director, boundary check: rank >= director)
        //     Rate from generation_overrides.rates[2] = 0.03
        //
        // For breakaway node 1:
        //   Generation 1: node 0 (senior_director, boundary)
        //     Differential: 0.15 - 0.10 = 0.05
        //
        //   No generation 2 (no more ancestors).
        let tree = build_chain(5);
        let mut structure = test_stairstep_structure();
        structure.breakaway.as_mut().unwrap().generation_overrides =
            Some(BreakawayGenerationConfig {
                max_generations: 3,
                rates: {
                    let mut m = BTreeMap::new();
                    m.insert(1, 0.05);
                    m.insert(2, 0.03);
                    m.insert(3, 0.01);
                    m
                },
                boundary_rank: "director".to_string(),
            });
        let plan = build_test_stairstep_plan(default_eligibility(), structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), snapshot_with_rank("senior_director", 150.0));
        snapshots.insert(uuid(1), snapshot_with_rank("director", 200.0));
        snapshots.insert(uuid(2), snapshot_with_rank("associate", 300.0));
        snapshots.insert(uuid(3), snapshot_with_rank("director", 150.0));
        snapshots.insert(uuid(4), snapshot_with_rank("associate", 150.0));

        let volume = vec![VolumeSource {
            source_id: uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // --- Override earnings for breakaway node 1 (source_id = uuid(1)) ---
        // Node 0 earns differential override on node 1's group volume
        // Group volume for node 1: 200 + 300 = 500
        // Differential = 0.15 - 0.10 = 0.05
        // Dollar = 500 * 0.40 * 1.0 * 0.05 = 10.0
        let node0_on_node1 = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.source_id == uuid(1))
            .expect("node 0 should earn differential override on breakaway node 1");
        assert_eq!(node0_on_node1.level, 1);
        assert!((node0_on_node1.rate - 0.05).abs() < FP_TOL);
        assert!((node0_on_node1.cv_amount - 500.0).abs() < FP_TOL);
        assert!((node0_on_node1.dollar_amount - 10.0).abs() < FP_TOL);

        // --- Override earnings for breakaway node 3 (source_id = uuid(3)) ---
        // Group volume for node 3: 150 + 150 = 300

        // Generation 1: node 1 earns differential on node 3
        // Differential = 0.10 - 0.10 = 0, min_override = 0.02
        // Dollar = 300 * 0.40 * 1.0 * 0.02 = 2.40
        let node1_on_node3 = result
            .iter()
            .find(|e| e.earner_id == uuid(1) && e.source_id == uuid(3))
            .expect("node 1 should earn gen-1 differential on breakaway node 3");
        assert_eq!(node1_on_node3.level, 1);
        assert!((node1_on_node3.rate - 0.02).abs() < FP_TOL);
        assert!((node1_on_node3.cv_amount - 300.0).abs() < FP_TOL);
        assert!((node1_on_node3.dollar_amount - 2.40).abs() < FP_TOL);

        // Generation 2: node 0 earns generation override on node 3
        // Rate from generation_overrides.rates[2] = 0.03
        // Dollar = 300 * 0.40 * 1.0 * 0.03 = 3.60
        let node0_on_node3 = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.source_id == uuid(3))
            .expect("node 0 should earn gen-2 override on breakaway node 3");
        assert_eq!(node0_on_node3.level, 2);
        assert!((node0_on_node3.rate - 0.03).abs() < FP_TOL);
        assert!((node0_on_node3.cv_amount - 300.0).abs() < FP_TOL);
        assert!((node0_on_node3.dollar_amount - 3.60).abs() < FP_TOL);
    }
}
