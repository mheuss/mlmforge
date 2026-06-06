//! Generation commission calculator and counting utility.
//!
//! Walks upward through a unilevel tree from a starting node, counting
//! generation boundaries. A "generation" increments each time we encounter
//! a breakaway distributor who passes a boundary check (e.g., meets a
//! rank threshold). This module is shared by the stairstep calculator
//! and the standalone generation calculator.

use std::collections::{HashMap, HashSet};

use uuid::Uuid;

use crate::config::generation::GenerationBoundaryMode;
use crate::config::{CompensationPlan, GenerationStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
use super::walk;

/// Returns the generation depth for an earner with the given rank.
///
/// Looks up the rank in `max_generations_per_rank` first; falls back to
/// the default `max_generations` if the rank is not in the map.
fn earner_max_generations(
    rank: &str,
    cfg: &crate::config::generation::GenerationCommissionConfig,
) -> u8 {
    cfg.max_generations_per_rank
        .get(rank)
        .copied()
        .unwrap_or(cfg.max_generations)
}

/// Returns the maximum walk depth needed to satisfy any earner's per-rank
/// generation cap. Used by the ThresholdRank walk to decide how deep the
/// shared per-source walk must go. Per-earner filtering trims results to
/// each earner's own cap after the walk.
fn walk_depth(cfg: &crate::config::generation::GenerationCommissionConfig) -> u8 {
    cfg.max_generations_per_rank
        .values()
        .copied()
        .max()
        .map(|m| m.max(cfg.max_generations))
        .unwrap_or(cfg.max_generations)
}

/// A single generation entry produced by the upward walk.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GenerationEntry {
    /// The distributor who represents this generation boundary.
    pub earner_id: Uuid,
    /// The generation number (1-based).
    pub generation: u8,
}

/// Walk upward from `start_id` through the unilevel tree, counting
/// generation boundaries among breakaway distributors.
///
/// For each ancestor (excluding `start_id`):
/// - If not in `breakaway_set`, skip it entirely.
/// - If in `breakaway_set` and passes `boundary_check`: increment generation,
///   add to results.
/// - If in `breakaway_set` but fails `boundary_check`: a non-boundary
///   breakaway. When `empty_generation_consumes_number` is true, increment
///   the generation counter without adding to results. When false, skip.
///
/// Stops when `current_gen >= max_generations` or the upline is exhausted.
///
/// # Errors
///
/// Returns an empty Vec if `start_id` is not in the tree or has no upline.
pub fn count_generations_upward(
    tree: &UnilevelTree,
    start_id: Uuid,
    breakaway_set: &HashSet<Uuid>,
    boundary_check: &dyn Fn(Uuid) -> bool,
    max_generations: u8,
    empty_generation_consumes_number: bool,
) -> Vec<GenerationEntry> {
    let upline = match tree.get_upline(start_id, 0) {
        Ok(nodes) => nodes,
        Err(_) => return Vec::new(),
    };

    let mut results = Vec::new();
    let mut current_gen: u8 = 0;

    for node in &upline {
        if current_gen >= max_generations {
            break;
        }

        if !breakaway_set.contains(&node.user_id) {
            continue;
        }

        if boundary_check(node.user_id) {
            current_gen += 1;
            results.push(GenerationEntry {
                earner_id: node.user_id,
                generation: current_gen,
            });
        } else if empty_generation_consumes_number {
            current_gen += 1;
        }
    }

    results
}

/// Convert generation entries into commission earnings, filtering by
/// eligibility. Shared by ThresholdRank and SameRank modes.
///
/// For SameRank mode, callers pre-filter `gen_entries` to earners at
/// exactly the target ordinal before calling this function.
fn emit_generation_earnings(
    gen_entries: &[GenerationEntry],
    source: &VolumeSource,
    gen_config: &crate::config::generation::GenerationCommissionConfig,
    eligibility_cache: &HashMap<Uuid, walk::EligibilityResult>,
    multiplier: f64,
    earnings: &mut Vec<CommissionEarning>,
) {
    for entry in gen_entries {
        // Ineligible earners are in the boundary set (they define structure)
        // but must not earn.
        let earner_eligible = eligibility_cache
            .get(&entry.earner_id)
            .is_some_and(|e| e.eligible);
        if !earner_eligible {
            continue;
        }

        let rate = gen_config
            .rates
            .get(&entry.generation)
            .copied()
            .unwrap_or(0.0);

        if rate <= 0.0 {
            continue;
        }

        earnings.push(CommissionEarning {
            earner_id: entry.earner_id,
            source_id: source.source_id,
            level: entry.generation,
            rate,
            cv_amount: source.cv_amount,
            dollar_amount: source.cv_amount * multiplier * rate,
        });
    }
}

/// Calculate generation commissions for a set of volume events.
///
/// Generation plans pay commissions on generations of qualified leaders,
/// not tree depth. A generation boundary is created when a downline leader
/// meets the boundary rank criteria. The formula for each earning is:
///
/// `dollar_amount = cv_amount * multiplier * generation_rate`
///
/// Generation rates are already effective percentages. There is no broad
/// commission percent like in level commission calculators.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or has an invalid CV amount.
pub fn calculate_generation(
    tree: &UnilevelTree,
    plan: &CompensationPlan,
    structure: &GenerationStructureConfig,
    snapshots: &HashMap<Uuid, DistributorSnapshot>,
    volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    let gen_config = &structure.generation_commission;
    let rank_ordinals = walk::build_rank_ordinals(plan);
    let eligibility_cache = walk::evaluate_eligibility(snapshots, tree, &plan.eligibility);

    let multiplier = gen_config
        .volume_to_dollar_multiplier
        .unwrap_or(plan.volume.volume_to_dollar_multiplier);

    let mut earnings = Vec::new();

    // Optional level commissions. When enabled, these run alongside
    // generation commissions on the same volume. Level earnings use
    // broad_commission_percent; generation earnings do not.
    if structure.level_commissions_enabled {
        if let Some(ref lc) = structure.level_commission {
            let lc_multiplier = lc
                .volume_to_dollar_multiplier
                .unwrap_or(plan.volume.volume_to_dollar_multiplier);
            let threshold_ordinal =
                walk::resolve_threshold_ordinal(structure.compression.as_ref(), &rank_ordinals);

            let level_config = walk::LevelWalkConfig {
                max_depth: lc.max_depth,
                broad_pct: lc.broad_commission_percent,
                multiplier: lc_multiplier,
                compression: structure.compression.as_ref(),
                threshold_ordinal,
                rank_ordinals: &rank_ordinals,
                rate_table: &lc.rate_table,
                pass_up: None,
                dynamic_thresholds: None,
            };

            let level_earnings = walk::walk_level_commissions(
                tree,
                &level_config,
                &eligibility_cache,
                snapshots,
                volume,
                |_| false, // no breakaway boundaries in generation plans
            )?;
            earnings.extend(level_earnings);
        }
    }

    match gen_config.boundary_mode {
        GenerationBoundaryMode::ThresholdRank => {
            // Resolve boundary rank ordinal. If the boundary rank doesn't
            // exist in the plan's rank ladder, no one can be a boundary.
            let threshold = match rank_ordinals
                .get(gen_config.boundary_rank.as_str())
                .copied()
            {
                Some(ord) => ord,
                None => {
                    log::warn!(
                        "generation boundary_rank '{}' not found in plan ranks; \
                         no generation commissions will be paid",
                        gen_config.boundary_rank
                    );
                    // Return any level earnings already collected. Don't
                    // discard them just because the generation boundary
                    // rank is misconfigured.
                    walk::sort_earnings(&mut earnings);
                    return Ok(earnings);
                }
            };

            let boundary_set: HashSet<Uuid> = snapshots
                .iter()
                .filter(|(_, snap)| {
                    rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0) >= threshold
                })
                .map(|(id, _)| *id)
                .collect();

            let boundary_check: Box<dyn Fn(Uuid) -> bool + '_> =
                if gen_config.ineligible_creates_boundary {
                    Box::new(|_| true)
                } else {
                    Box::new(|id: Uuid| eligibility_cache.get(&id).is_some_and(|e| e.eligible))
                };

            // walk_depth is loop-invariant: it depends only on gen_config, not
            // on the source. Compute once before the per-source loop.
            let walk_max = walk_depth(gen_config);

            for source in volume {
                walk::validate_cv(source)?;
                tree.get_upline(source.source_id, 0)
                    .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;

                let gen_entries = count_generations_upward(
                    tree,
                    source.source_id,
                    &boundary_set,
                    &boundary_check,
                    walk_max,
                    gen_config.empty_generation_consumes_number,
                );

                // Filter: each earner receives at most their per-rank generation
                // count. The shared walk extends to the deepest configured depth
                // so every earner's cap can be honored from a single walk; this
                // filter trims emitted entries to each earner's own cap.
                let filtered: Vec<_> = gen_entries
                    .into_iter()
                    .filter(|entry| {
                        let earner_rank = snapshots
                            .get(&entry.earner_id)
                            .map(|s| s.rank.as_str())
                            .unwrap_or("");
                        entry.generation <= earner_max_generations(earner_rank, gen_config)
                    })
                    .collect();

                emit_generation_earnings(
                    &filtered,
                    source,
                    gen_config,
                    &eligibility_cache,
                    multiplier,
                    &mut earnings,
                );
            }
        }

        GenerationBoundaryMode::SameRank => {
            // Collect unique (rank_name, ordinal) pairs from snapshots. Each
            // pair gets its own boundary set and walk pass. The rank name is
            // preserved so each per-rank walk can use earner_max_generations
            // for its termination depth.
            let mut unique_ranks: Vec<(&str, u16)> = snapshots
                .values()
                .filter_map(|snap| {
                    rank_ordinals
                        .get(snap.rank.as_str())
                        .copied()
                        .map(|ord| (snap.rank.as_str(), ord))
                })
                .collect::<HashSet<_>>()
                .into_iter()
                .collect();
            unique_ranks.sort_by_key(|(_, ord)| *ord);

            // Validate sources once before the per-rank loops.
            for source in volume {
                walk::validate_cv(source)?;
                tree.get_upline(source.source_id, 0)
                    .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;
            }

            // The boundary check is rank-independent, so build it once and
            // reuse it across every per-rank walk.
            let boundary_check: Box<dyn Fn(Uuid) -> bool + '_> =
                if gen_config.ineligible_creates_boundary {
                    Box::new(|_| true)
                } else {
                    Box::new(|id: Uuid| eligibility_cache.get(&id).is_some_and(|e| e.eligible))
                };

            for &(rank_name, ordinal) in &unique_ranks {
                let walk_max = earner_max_generations(rank_name, gen_config);

                // Boundary set for this rank level: all nodes at or above ordinal.
                let boundary_set: HashSet<Uuid> = snapshots
                    .iter()
                    .filter(|(_, snap)| {
                        rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0) >= ordinal
                    })
                    .map(|(id, _)| *id)
                    .collect();

                for source in volume {
                    let gen_entries = count_generations_upward(
                        tree,
                        source.source_id,
                        &boundary_set,
                        &boundary_check,
                        walk_max,
                        gen_config.empty_generation_consumes_number,
                    );

                    // Pre-filter to earners at exactly this rank ordinal.
                    let filtered: Vec<_> = gen_entries
                        .into_iter()
                        .filter(|entry| {
                            snapshots
                                .get(&entry.earner_id)
                                .and_then(|snap| rank_ordinals.get(snap.rank.as_str()).copied())
                                .unwrap_or(0)
                                == ordinal
                        })
                        .collect();

                    emit_generation_earnings(
                        &filtered,
                        source,
                        gen_config,
                        &eligibility_cache,
                        multiplier,
                        &mut earnings,
                    );
                }
            }
        }
    }

    walk::sort_earnings(&mut earnings);
    Ok(earnings)
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;
    use crate::commission::test_helpers::uuid_from_index as uuid;
    use crate::config::generation::GenerationCommissionConfig;
    use crate::tree::unilevel::UnilevelTree;

    fn gen_config_with(default: u8, per_rank: BTreeMap<String, u8>) -> GenerationCommissionConfig {
        GenerationCommissionConfig {
            max_generations: default,
            max_generations_per_rank: per_rank,
            rates: BTreeMap::from([(1u8, 0.10)]),
            boundary_mode: GenerationBoundaryMode::ThresholdRank,
            boundary_rank: "director".to_string(),
            empty_generation_consumes_number: false,
            volume_to_dollar_multiplier: None,
            ineligible_creates_boundary: true,
        }
    }

    #[test]
    fn earner_max_generations_uses_per_rank_when_present() {
        let mut per_rank = BTreeMap::new();
        per_rank.insert("diamond".to_string(), 8u8);
        let cfg = gen_config_with(4, per_rank);

        assert_eq!(earner_max_generations("diamond", &cfg), 8);
    }

    #[test]
    fn earner_max_generations_falls_back_to_default_for_missing_rank() {
        let mut per_rank = BTreeMap::new();
        per_rank.insert("diamond".to_string(), 8u8);
        let cfg = gen_config_with(4, per_rank);

        assert_eq!(earner_max_generations("silver", &cfg), 4);
    }

    #[test]
    fn earner_max_generations_falls_back_to_default_when_map_empty() {
        let cfg = gen_config_with(4, BTreeMap::new());
        assert_eq!(earner_max_generations("anything", &cfg), 4);
    }

    #[test]
    fn walk_depth_returns_default_when_per_rank_empty() {
        let cfg = gen_config_with(4, BTreeMap::new());
        assert_eq!(walk_depth(&cfg), 4);
    }

    #[test]
    fn walk_depth_returns_max_of_default_and_per_rank() {
        let mut per_rank = BTreeMap::new();
        per_rank.insert("diamond".to_string(), 8u8);
        per_rank.insert("silver".to_string(), 2u8);
        let cfg = gen_config_with(4, per_rank);
        assert_eq!(walk_depth(&cfg), 8);
    }

    #[test]
    fn walk_depth_returns_default_when_per_rank_all_smaller() {
        let mut per_rank = BTreeMap::new();
        per_rank.insert("silver".to_string(), 2u8);
        let cfg = gen_config_with(5, per_rank);
        assert_eq!(walk_depth(&cfg), 5);
    }

    /// Build a linear chain: 0 -> 1 -> 2 -> ... -> (len-1).
    /// Each node's parent and sponsor are the previous node.
    fn build_chain(len: usize) -> UnilevelTree {
        assert!(len >= 1, "chain must have at least one node");
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();
        for i in 1..len {
            tree.add_node(uuid(i), uuid(i - 1), uuid(i - 1), i as i64)
                .unwrap();
        }
        tree
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 2}. All breakaways pass boundary_check.
    /// Start from 4. Expect gen 1 at node 2, gen 2 at node 0.
    #[test]
    fn threshold_rank_boundary_mode() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(2)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Breakaways: {0, 1, 2}. boundary_check returns true only for {0, 2}.
    /// Start from 3. Node 1 is breakaway but not a boundary.
    /// flag=false, so node 1 doesn't consume a generation number.
    /// Expect gen 1 at node 2, gen 2 at node 0.
    #[test]
    fn same_rank_boundary_mode() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(2)].into_iter().collect();
        let boundaries: HashSet<Uuid> = [uuid(0), uuid(2)].into_iter().collect();
        let boundary_check = move |id: Uuid| boundaries.contains(&id);

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Breakaways: {0, 1, 2}. All pass boundary_check.
    /// flag=true. Start from 3.
    /// All three breakaways are boundaries, so we get gen 1, 2, 3.
    #[test]
    fn empty_generation_consumed() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(2)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, true);

        assert_eq!(result.len(), 3);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(1),
                generation: 2
            }
        );
        assert_eq!(
            result[2],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 3
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 1, 3}. boundary_check returns true for {0, 3}.
    /// flag=false. Start from 4.
    /// Node 1 is breakaway but not boundary. flag=false means it doesn't
    /// consume a generation number. Node 2 is not breakaway at all.
    /// Expect gen 1 at node 3, gen 2 at node 0.
    #[test]
    fn empty_generation_not_consumed() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> = [uuid(0), uuid(1), uuid(3)].into_iter().collect();
        let boundaries: HashSet<Uuid> = [uuid(0), uuid(3)].into_iter().collect();
        let boundary_check = move |id: Uuid| boundaries.contains(&id);

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 10, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(3),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(0),
                generation: 2
            }
        );
    }

    /// Chain: 0 -> 1 -> 2 -> 3
    /// Empty breakaway_set. Start from 3.
    /// No breakaways means no generations.
    #[test]
    fn no_breakaways_zero_generations() {
        let tree = build_chain(4);
        let breakaway_set: HashSet<Uuid> = HashSet::new();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(3), &breakaway_set, &boundary_check, 10, false);

        assert!(result.is_empty());
    }

    /// Chain: 0 -> 1 -> 2 -> 3 -> 4
    /// Breakaways: {0, 1, 2, 3}. All pass boundary_check.
    /// max_generations=2. Start from 4.
    /// Should stop after 2 generations even though more breakaways exist.
    #[test]
    fn max_generations_caps_result() {
        let tree = build_chain(5);
        let breakaway_set: HashSet<Uuid> =
            [uuid(0), uuid(1), uuid(2), uuid(3)].into_iter().collect();
        let boundary_check = |_: Uuid| true;

        let result =
            count_generations_upward(&tree, uuid(4), &breakaway_set, &boundary_check, 2, false);

        assert_eq!(result.len(), 2);
        assert_eq!(
            result[0],
            GenerationEntry {
                earner_id: uuid(3),
                generation: 1
            }
        );
        assert_eq!(
            result[1],
            GenerationEntry {
                earner_id: uuid(2),
                generation: 2
            }
        );
    }
}

// ===========================================================================
// calculate_generation tests
// ===========================================================================

#[cfg(test)]
mod calculate_tests {
    use std::collections::{BTreeMap, HashMap};

    use crate::commission::test_helpers::{
        build_test_plan, default_eligibility, eligible_snapshot, uuid_from_index as uuid,
    };
    use crate::commission::types::{CalculationError, DistributorSnapshot, VolumeSource};
    use crate::config::commission::LevelCommissionConfig;
    use crate::config::generation::{GenerationBoundaryMode, GenerationCommissionConfig};
    use crate::config::rank::{DemotionPolicy, RankDefinition, RankQualification};
    use crate::config::{GenerationStructureConfig, StructureConfig};
    use crate::tree::unilevel::UnilevelTree;

    use super::calculate_generation;

    /// Build a linear chain: 0 -> 1 -> 2 -> ... -> (len-1).
    fn build_chain(len: usize) -> UnilevelTree {
        assert!(len >= 1, "chain must have at least one node");
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid(0), 0).unwrap();
        for i in 1..len {
            tree.add_node(uuid(i), uuid(i - 1), uuid(i - 1), i as i64)
                .unwrap();
        }
        tree
    }

    /// Build a generation structure config for ThresholdRank mode.
    fn threshold_structure(
        boundary_rank: &str,
        max_generations: u8,
        rates: BTreeMap<u8, f64>,
    ) -> GenerationStructureConfig {
        GenerationStructureConfig {
            name: "Generation".to_string(),
            level_commission: None,
            compression: None,
            generation_commission: GenerationCommissionConfig {
                max_generations,
                max_generations_per_rank: BTreeMap::new(),
                rates,
                boundary_mode: GenerationBoundaryMode::ThresholdRank,
                boundary_rank: boundary_rank.to_string(),
                empty_generation_consumes_number: false,
                volume_to_dollar_multiplier: None,
                ineligible_creates_boundary: true,
            },
            level_commissions_enabled: false,
        }
    }

    /// Build a plan with two ranks: associate (ordinal 1), director (ordinal 2).
    fn two_rank_plan() -> crate::config::CompensationPlan {
        let structure = StructureConfig::Generation(threshold_structure(
            "director",
            3,
            BTreeMap::from([(1, 0.10)]),
        ));
        let mut plan = build_test_plan(default_eligibility(), structure, "Generation");
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "director".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    fn director_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "director".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    /// Chain: root(Director) -> mid(Associate) -> leaf(volume source).
    /// Boundary rank = "director", max_generations = 3, gen 1 rate = 0.10.
    /// Volume = 100 CV, multiplier = 1.0.
    /// Expected: root earns gen 1 with dollar_amount = 100 * 1.0 * 0.10 = 10.0.
    #[test]
    fn calculate_single_boundary_threshold_mode() {
        let tree = build_chain(3);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));

        let mut snapshots: HashMap<_, _> = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot()); // associate
        snapshots.insert(uuid(2), eligible_snapshot()); // associate (leaf)

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].source_id, uuid(2));
        assert_eq!(result[0].level, 1);
        assert_eq!(result[0].rate, 0.10);
        assert_eq!(result[0].cv_amount, 100.0);
        assert!((result[0].dollar_amount - 10.0).abs() < f64::EPSILON);
    }

    /// Chain: 0(Dir) -> 1(Assoc) -> 2(Dir) -> 3(Assoc) -> 4(Dir) -> 5(Assoc) -> 6(leaf).
    /// Three Directors at positions 0, 2, 4. Rates: gen1=0.10, gen2=0.06, gen3=0.04.
    /// Volume = 200 CV. Expected: node 4 earns gen1, node 2 earns gen2, node 0 earns gen3.
    #[test]
    fn multiple_boundaries_threshold_mode() {
        let tree = build_chain(7);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.06), (3, 0.04)]);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, rates);

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());
        snapshots.insert(uuid(2), director_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot());
        snapshots.insert(uuid(4), director_snapshot());
        snapshots.insert(uuid(5), eligible_snapshot());
        snapshots.insert(uuid(6), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(6),
            cv_amount: 200.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 3);

        // Results are sorted by earner_id, so find each by earner.
        let earn_4 = result.iter().find(|e| e.earner_id == uuid(4)).unwrap();
        assert_eq!(earn_4.level, 1);
        assert_eq!(earn_4.rate, 0.10);
        assert!((earn_4.dollar_amount - 20.0).abs() < f64::EPSILON);

        let earn_2 = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_2.level, 2);
        assert_eq!(earn_2.rate, 0.06);
        assert!((earn_2.dollar_amount - 12.0).abs() < f64::EPSILON);

        let earn_0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_0.level, 3);
        assert_eq!(earn_0.rate, 0.04);
        assert!((earn_0.dollar_amount - 8.0).abs() < f64::EPSILON);
    }

    /// Chain: 0(Dir) -> 1(Assoc) -> 2(Dir) -> 3(Assoc) -> 4(Dir) -> 5(leaf).
    /// Three Directors but max_generations=2. Only gen 1 and gen 2 should earn.
    #[test]
    fn max_generations_caps_walk() {
        let tree = build_chain(6);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.06), (3, 0.04)]);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 2, rates);

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());
        snapshots.insert(uuid(2), director_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot());
        snapshots.insert(uuid(4), director_snapshot());
        snapshots.insert(uuid(5), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(5),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Only 2 earners: gen 1 (node 4) and gen 2 (node 2). Node 0 is beyond max.
        assert_eq!(result.len(), 2);
        assert!(
            result
                .iter()
                .any(|e| e.earner_id == uuid(4) && e.level == 1)
        );
        assert!(
            result
                .iter()
                .any(|e| e.earner_id == uuid(2) && e.level == 2)
        );
        assert!(!result.iter().any(|e| e.earner_id == uuid(0)));
    }

    /// Chain: 0(Assoc) -> 1(Assoc) -> 2(Assoc) -> 3(leaf).
    /// All Associates, no Directors. No one meets the boundary rank.
    /// No generation earnings should be produced.
    #[test]
    fn no_boundaries_in_downline() {
        let tree = build_chain(4);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));

        let mut snapshots = HashMap::new();
        for i in 0..4 {
            snapshots.insert(uuid(i), eligible_snapshot());
        }

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();
        assert!(result.is_empty());
    }

    /// Chain: 0(Dir) -> 1(Assoc) -> 2(Dir, leaf/volume source).
    /// Node 2 is the source AND a Director. It should not earn on its own
    /// volume, but it creates a boundary for upline. Node 0 should earn at
    /// generation 1 (node 2 is the boundary between them).
    #[test]
    fn volume_generator_is_boundary_rank() {
        let tree = build_chain(3);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());
        snapshots.insert(uuid(2), director_snapshot()); // source is Director

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Node 0 earns gen 1. Node 2 is excluded because count_generations_upward
        // starts walking from the parent of the source (excludes start_id).
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].level, 1);
    }

    /// Chain: 0(Dir) -> 1(no snapshot) -> 2(Dir) -> 3(leaf).
    /// Node 1 has no snapshot entry. It cannot be in the boundary set
    /// (no rank to evaluate) and cannot be eligible (no snapshot).
    /// Node 2 is a boundary. Node 0 is a boundary. Node 2 earns gen 1,
    /// node 0 earns gen 2.
    #[test]
    fn missing_snapshot_treated_as_ineligible() {
        let tree = build_chain(4);
        let plan = two_rank_plan();
        let rates = BTreeMap::from([(1, 0.10), (2, 0.06)]);
        let structure = threshold_structure("director", 3, rates);

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        // uuid(1) intentionally missing from snapshots
        snapshots.insert(uuid(2), director_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 2);
        let earn_2 = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_2.level, 1);
        assert_eq!(earn_2.rate, 0.10);

        let earn_0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_0.level, 2);
        assert_eq!(earn_0.rate, 0.06);

        // Node 1 should not appear in results.
        assert!(!result.iter().any(|e| e.earner_id == uuid(1)));
    }

    /// Source not in tree returns CalculationError::SourceNotInTree.
    #[test]
    fn source_not_in_tree_returns_error() {
        let tree = UnilevelTree::new();
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));
        let snapshots = HashMap::new();
        let volume = vec![VolumeSource {
            source_id: uuid(99),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(result, Err(CalculationError::SourceNotInTree(_))));
    }

    /// Negative CV amount returns CalculationError::InvalidCvAmount.
    #[test]
    fn negative_cv_returns_error() {
        let tree = build_chain(2);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));
        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(1),
            cv_amount: -50.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume);
        assert!(matches!(result, Err(CalculationError::InvalidCvAmount(..))));
    }

    // -- ineligible_creates_boundary tests --

    fn inactive_director_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "director".to_string(),
            personal_volume: 0.0,
            status: "inactive".to_string(),
            has_order_in_period: false,
        }
    }

    /// Tree: root(Dir, active) -> mid(Dir, inactive) -> leaf(source).
    /// ineligible_creates_boundary = true (default).
    /// mid creates boundary (gen 1) but doesn't earn (ineligible).
    /// Root earns at gen 2.
    #[test]
    fn ineligible_creates_boundary_true() {
        let tree = build_chain(3);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05)]);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, rates);
        // ineligible_creates_boundary defaults to true in threshold_structure

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), inactive_director_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // mid creates boundary but is filtered at earning time.
        // Root earns gen 2 (mid consumed gen 1 as structure-only boundary).
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].level, 2);
        assert_eq!(result[0].rate, 0.05);
        assert!((result[0].dollar_amount - 5.0).abs() < f64::EPSILON);
    }

    /// Tree: root(Dir, active) -> mid(Dir, inactive) -> leaf(source).
    /// ineligible_creates_boundary = false, empty_consumes = false.
    /// mid is invisible to the walk. Root earns at gen 1.
    #[test]
    fn ineligible_creates_boundary_false() {
        let tree = build_chain(3);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05)]);
        let plan = two_rank_plan();
        let mut structure = threshold_structure("director", 3, rates);
        structure.generation_commission.ineligible_creates_boundary = false;

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), inactive_director_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // mid fails boundary_check (ineligible) and empty_consumes=false
        // means no gen number consumed. Root earns gen 1.
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].level, 1);
        assert_eq!(result[0].rate, 0.10);
        assert!((result[0].dollar_amount - 10.0).abs() < f64::EPSILON);
    }

    /// Tree: root(Dir, active) -> mid(Dir, inactive) -> leaf(source).
    /// ineligible_creates_boundary = false, empty_consumes = true.
    /// mid fails boundary_check but consumes a generation number.
    /// Root earns at gen 2.
    #[test]
    fn ineligible_false_empty_consumes_true() {
        let tree = build_chain(3);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05)]);
        let plan = two_rank_plan();
        let mut structure = threshold_structure("director", 3, rates);
        structure.generation_commission.ineligible_creates_boundary = false;
        structure
            .generation_commission
            .empty_generation_consumes_number = true;

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), inactive_director_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // mid fails check, but empty_consumes=true advances the counter.
        // Gen 1 consumed by ineligible mid. Root earns gen 2.
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].level, 2);
        assert_eq!(result[0].rate, 0.05);
        assert!((result[0].dollar_amount - 5.0).abs() < f64::EPSILON);
    }

    /// Tree: root(Dir, active) -> mid(Dir, active) -> leaf(source).
    /// ineligible_creates_boundary = true, empty_consumes = true.
    /// Both are eligible, so boundary_check always returns true and
    /// empty_consumes never triggers. Verifies the flags don't interfere
    /// when everyone is eligible.
    #[test]
    fn empty_consumes_irrelevant_when_all_eligible() {
        let tree = build_chain(3);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05)]);
        let plan = two_rank_plan();
        let mut structure = threshold_structure("director", 3, rates);
        structure
            .generation_commission
            .empty_generation_consumes_number = true;

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), director_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Both Directors pass boundary_check. mid=gen1, root=gen2.
        assert_eq!(result.len(), 2);
        let earn_mid = result.iter().find(|e| e.earner_id == uuid(1)).unwrap();
        assert_eq!(earn_mid.level, 1);
        let earn_root = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_root.level, 2);
    }

    // -- Combined level + generation tests --

    /// Tree: root(Dir) -> mid(Assoc) -> leaf(Assoc, source).
    /// level_commissions_enabled = true with max_depth=3, broad_pct=0.40.
    /// gen rate: gen1=0.10. level rate: associate level1=0.05, level2=0.08.
    /// Volume = 100 CV, multiplier = 1.0.
    ///
    /// Level earnings: mid earns level 1 (100 * 0.40 * 1.0 * 0.05 = 2.0),
    ///   root earns level 2 (100 * 0.40 * 1.0 * 0.08 = 3.2).
    /// Generation earnings: root earns gen 1 (100 * 1.0 * 0.10 = 10.0).
    #[test]
    fn combined_level_and_generation() {
        let tree = build_chain(3);

        let level_config = LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth: 3,
            rate_table: BTreeMap::from([
                (
                    "associate".to_string(),
                    BTreeMap::from([(1, 0.05), (2, 0.08)]),
                ),
                (
                    "director".to_string(),
                    BTreeMap::from([(1, 0.05), (2, 0.08)]),
                ),
            ]),
        };

        let structure = GenerationStructureConfig {
            name: "Generation".to_string(),
            level_commission: Some(level_config),
            compression: None,
            generation_commission: GenerationCommissionConfig {
                max_generations: 3,
                max_generations_per_rank: BTreeMap::new(),
                rates: BTreeMap::from([(1, 0.10)]),
                boundary_mode: GenerationBoundaryMode::ThresholdRank,
                boundary_rank: "director".to_string(),
                empty_generation_consumes_number: false,
                volume_to_dollar_multiplier: None,
                ineligible_creates_boundary: true,
            },
            level_commissions_enabled: true,
        };

        let plan_structure = StructureConfig::Generation(structure.clone());
        let mut plan = build_test_plan(default_eligibility(), plan_structure, "Generation");
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "director".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot()); // associate
        snapshots.insert(uuid(2), eligible_snapshot()); // associate (leaf)

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Level earnings (2) + generation earnings (1) = 3 total
        assert_eq!(result.len(), 3);

        // mid: level 1 from walk = 100 * 0.40 * 1.0 * 0.05 = 2.0
        let mid_level = result
            .iter()
            .find(|e| e.earner_id == uuid(1) && e.rate == 0.05)
            .unwrap();
        assert!((mid_level.dollar_amount - 2.0).abs() < f64::EPSILON);

        // root: level 2 from walk = 100 * 0.40 * 1.0 * 0.08 = 3.2
        let root_level = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.rate == 0.08)
            .unwrap();
        assert!((root_level.dollar_amount - 3.2).abs() < f64::EPSILON);

        // root: gen 1 = 100 * 1.0 * 0.10 = 10.0
        let root_gen = result
            .iter()
            .find(|e| e.earner_id == uuid(0) && e.rate == 0.10)
            .unwrap();
        assert!((root_gen.dollar_amount - 10.0).abs() < f64::EPSILON);
    }

    /// Same tree, but level_commissions_enabled = false.
    /// Only generation earnings should appear.
    #[test]
    fn generation_only_level_disabled() {
        let tree = build_chain(3);
        let plan = two_rank_plan();
        let structure = threshold_structure("director", 3, BTreeMap::from([(1, 0.10)]));
        // level_commissions_enabled defaults to false in threshold_structure

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), director_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Only generation earning: root gen 1
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].earner_id, uuid(0));
        assert_eq!(result[0].level, 1);
    }

    /// Combined level + generation with an invalid generation boundary_rank.
    /// Level earnings should still be returned even though generation
    /// commissions can't be calculated.
    #[test]
    fn missing_boundary_rank_preserves_level_earnings() {
        let tree = build_chain(3);

        let level_config = LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth: 3,
            rate_table: BTreeMap::from([(
                "associate".to_string(),
                BTreeMap::from([(1, 0.05), (2, 0.08)]),
            )]),
        };

        let structure = GenerationStructureConfig {
            name: "Generation".to_string(),
            level_commission: Some(level_config),
            compression: None,
            generation_commission: GenerationCommissionConfig {
                max_generations: 3,
                max_generations_per_rank: BTreeMap::new(),
                rates: BTreeMap::from([(1, 0.10)]),
                boundary_mode: GenerationBoundaryMode::ThresholdRank,
                boundary_rank: "nonexistent_rank".to_string(),
                empty_generation_consumes_number: false,
                volume_to_dollar_multiplier: None,
                ineligible_creates_boundary: true,
            },
            level_commissions_enabled: true,
        };

        let plan_structure = StructureConfig::Generation(structure.clone());
        let mut plan = build_test_plan(default_eligibility(), plan_structure, "Generation");
        plan.ranks = vec![RankDefinition {
            name: "associate".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![],
                required_products: vec![],
                window: None,
            },
            qualified_structures: vec!["Generation".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }];

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), eligible_snapshot());
        snapshots.insert(uuid(1), eligible_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(2),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Level earnings should be preserved despite the invalid boundary_rank.
        // No generation earnings (boundary_rank not found in plan ranks).
        assert_eq!(result.len(), 2, "level earnings should be preserved");

        // mid: level 1 = 100 * 0.40 * 1.0 * 0.05 = 2.0
        let mid = result.iter().find(|e| e.earner_id == uuid(1)).unwrap();
        assert!((mid.dollar_amount - 2.0).abs() < 1e-10);

        // root: level 2 = 100 * 0.40 * 1.0 * 0.08 = 3.2
        let root = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert!((root.dollar_amount - 3.2).abs() < 1e-10);
    }

    // -- SameRank mode helpers and tests --

    fn same_rank_structure(
        max_generations: u8,
        rates: BTreeMap<u8, f64>,
    ) -> GenerationStructureConfig {
        GenerationStructureConfig {
            name: "Generation".to_string(),
            level_commission: None,
            compression: None,
            generation_commission: GenerationCommissionConfig {
                max_generations,
                max_generations_per_rank: BTreeMap::new(),
                rates,
                boundary_mode: GenerationBoundaryMode::SameRank,
                boundary_rank: "unused_in_same_rank_mode".to_string(),
                empty_generation_consumes_number: false,
                volume_to_dollar_multiplier: None,
                ineligible_creates_boundary: true,
            },
            level_commissions_enabled: false,
        }
    }

    fn three_rank_plan(structure: GenerationStructureConfig) -> crate::config::CompensationPlan {
        let structure_config = StructureConfig::Generation(structure);
        let mut plan = build_test_plan(default_eligibility(), structure_config, "Generation");
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "gold".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "diamond".to_string(),
                ordinal: 3,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    fn gold_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "gold".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    fn diamond_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "diamond".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    /// Tree: root(Diamond/3) -> mid(Gold/2) -> low(Associate/1) -> leaf(Associate, source).
    /// SameRank mode. Each earner's own rank determines boundaries.
    ///
    /// Associate walk (ordinal 1): everyone is a boundary. low=gen1.
    /// Gold walk (ordinal 2): {root,mid} are boundaries. mid=gen1.
    /// Diamond walk (ordinal 3): {root} is a boundary. root=gen1.
    #[test]
    fn same_rank_mode_different_earner_ranks() {
        let tree = build_chain(4);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05), (3, 0.03)]);
        let structure = same_rank_structure(5, rates);
        let plan = three_rank_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        snapshots.insert(uuid(1), gold_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        assert_eq!(result.len(), 3);

        let earn_low = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_low.level, 1);
        assert_eq!(earn_low.rate, 0.10);
        assert!((earn_low.dollar_amount - 10.0).abs() < f64::EPSILON);

        let earn_mid = result.iter().find(|e| e.earner_id == uuid(1)).unwrap();
        assert_eq!(earn_mid.level, 1);
        assert_eq!(earn_mid.rate, 0.10);
        assert!((earn_mid.dollar_amount - 10.0).abs() < f64::EPSILON);

        let earn_root = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_root.level, 1);
        assert_eq!(earn_root.rate, 0.10);
        assert!((earn_root.dollar_amount - 10.0).abs() < f64::EPSILON);
    }

    /// Tree: root(Diamond) -> g1(Gold) -> g2(Gold) -> low(Associate) -> leaf(source).
    /// SameRank mode. Higher-ranked earners see fewer boundaries.
    ///
    /// Gold walk: g2=gen1, g1=gen2 (two Gold+ boundaries).
    /// Diamond walk: root=gen1 (one Diamond+ boundary).
    #[test]
    fn same_rank_higher_rank_sees_fewer_boundaries() {
        let tree = build_chain(5);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05), (3, 0.03)]);
        let structure = same_rank_structure(5, rates);
        let plan = three_rank_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        snapshots.insert(uuid(1), gold_snapshot());
        snapshots.insert(uuid(2), gold_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot());
        snapshots.insert(uuid(4), eligible_snapshot());

        let volume = vec![VolumeSource {
            source_id: uuid(4),
            cv_amount: 200.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // g2 (Gold): gen 1 — first Gold+ boundary above source
        let earn_g2 = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_g2.level, 1);
        assert_eq!(earn_g2.rate, 0.10);

        // g1 (Gold): gen 2 — second Gold+ boundary above source
        let earn_g1 = result.iter().find(|e| e.earner_id == uuid(1)).unwrap();
        assert_eq!(earn_g1.level, 2);
        assert_eq!(earn_g1.rate, 0.05);

        // root (Diamond): gen 1 — only Diamond+ boundary
        let earn_root = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_root.level, 1);
        assert_eq!(earn_root.rate, 0.10);

        // low (Associate): gen 1 — first Associate+ boundary above source
        let earn_low = result.iter().find(|e| e.earner_id == uuid(3)).unwrap();
        assert_eq!(earn_low.level, 1);
    }

    fn inactive_gold_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "gold".to_string(),
            personal_volume: 0.0,
            status: "inactive".to_string(),
            has_order_in_period: false,
        }
    }

    /// Tree: root(Diamond) -> mid(Gold, inactive) -> low(Associate) -> leaf(source).
    /// SameRank mode, ineligible_creates_boundary = false.
    ///
    /// Gold walk (ordinal 2): mid is in boundary_set but fails boundary_check
    /// (ineligible). With empty_consumes=false, mid is invisible. root (Diamond,
    /// ordinal 3, >= 2) passes check → gen 1. Filter to ordinal 2: no earners.
    /// Diamond walk (ordinal 3): root → gen 1. Filter to ordinal 3: root earns gen 1.
    /// Associate walk (ordinal 1): mid fails check (invisible), so boundary_set
    /// for ordinal 1 is everyone, but mid fails check. low (ordinal 1) passes → gen 1.
    /// root passes → gen 2. Filter to ordinal 1: low earns gen 1.
    #[test]
    fn same_rank_ineligible_creates_boundary_false() {
        let tree = build_chain(4);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05)]);
        let mut structure = same_rank_structure(5, rates);
        structure.generation_commission.ineligible_creates_boundary = false;
        let plan = three_rank_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        snapshots.insert(uuid(1), inactive_gold_snapshot());
        snapshots.insert(uuid(2), eligible_snapshot()); // associate
        snapshots.insert(uuid(3), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(3),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // mid (Gold, inactive) does not earn and is invisible to boundary check.
        assert!(
            !result.iter().any(|e| e.earner_id == uuid(1)),
            "inactive Gold should not earn"
        );

        // root (Diamond): gen 1 from Diamond walk
        let earn_root = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_root.level, 1);

        // low (Associate): gen 1 from Associate walk
        let earn_low = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_low.level, 1);
    }

    fn silver_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "silver".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    /// Plan with two ranks for the per-rank-depth test: silver (1), diamond (2).
    fn silver_diamond_plan(
        structure: GenerationStructureConfig,
    ) -> crate::config::CompensationPlan {
        let structure_config = StructureConfig::Generation(structure);
        let mut plan = build_test_plan(default_eligibility(), structure_config, "Generation");
        plan.ranks = vec![
            RankDefinition {
                name: "silver".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "diamond".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    /// Chain: 0(Silver) -> 1(Silver) -> 2(Diamond) -> 3(Diamond) -> 4(Associate, source).
    /// SameRank mode. max_generations = 10. Per-rank depth: silver = 2, diamond = 5.
    ///
    /// Silver walk (ordinal 1, walk_max=2):
    ///   boundary_set = all ranked nodes {0, 1, 2, 3}.
    ///   Walking up from source: 3 (diamond, gen 1) -> 2 (diamond, gen 2) -> STOP.
    ///   Per-rank cap of 2 stops the walk before the actual Silver nodes (1, 0).
    ///   Filter to ordinal 1: zero earners.
    ///
    /// Diamond walk (ordinal 2, walk_max=5):
    ///   boundary_set = {2, 3}. Walking up: 3 (gen 1), 2 (gen 2), 1 (skip), 0 (skip).
    ///   Filter to ordinal 2: node 3 earns gen 1, node 2 earns gen 2.
    ///
    /// With the OLD code (no per-rank), every walk uses max_generations=10 and the
    /// Silver walk would produce gen 3 at node 1 and gen 4 at node 0. The new
    /// per-rank cap is the binding constraint: max_generations is loose, and the
    /// tree has more than enough depth to reach those Silver nodes.
    #[test]
    fn same_rank_walk_uses_per_rank_depth_cap() {
        let tree = build_chain(5);
        let rates = BTreeMap::from([(1, 0.10), (2, 0.05), (3, 0.03), (4, 0.02)]);
        let mut structure = same_rank_structure(10, rates);
        structure.generation_commission.max_generations_per_rank =
            BTreeMap::from([("silver".to_string(), 2), ("diamond".to_string(), 5)]);
        let plan = silver_diamond_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), silver_snapshot());
        snapshots.insert(uuid(1), silver_snapshot());
        snapshots.insert(uuid(2), diamond_snapshot());
        snapshots.insert(uuid(3), diamond_snapshot());
        snapshots.insert(uuid(4), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(4),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Silver nodes (0, 1) earn nothing: per-rank cap of 2 caps the silver
        // walk at the two diamond nodes above the source, before reaching them.
        assert!(
            !result.iter().any(|e| e.earner_id == uuid(0)),
            "silver node 0 should not earn (per-rank cap binds)"
        );
        assert!(
            !result.iter().any(|e| e.earner_id == uuid(1)),
            "silver node 1 should not earn (per-rank cap binds)"
        );

        // Diamond walk produces both diamond earners.
        let earn_3 = result.iter().find(|e| e.earner_id == uuid(3)).unwrap();
        assert_eq!(earn_3.level, 1);
        assert_eq!(earn_3.rate, 0.10);

        let earn_2 = result.iter().find(|e| e.earner_id == uuid(2)).unwrap();
        assert_eq!(earn_2.level, 2);
        assert_eq!(earn_2.rate, 0.05);

        // Exactly two earnings total: only the diamond walk produces results.
        assert_eq!(result.len(), 2);
    }

    /// Chain: 0(Diamond) -> 1..7(Silver) -> 8(Associate, source).
    /// ThresholdRank mode. boundary_rank = "silver" (ordinal 1) so all silver
    /// and diamond nodes are boundaries. max_generations = 4 (default).
    /// Per-rank depth: silver = 2, diamond = 8.
    ///
    /// walk_depth = max(4, max(2, 8)) = 8. The shared walk emits eight
    /// generation entries: silver gen 1..=7 at nodes 7..=1, diamond gen 8
    /// at node 0.
    ///
    /// Per-earner filter:
    ///   - Silver entries trimmed at gen > 2: only nodes 7 (gen 1) and
    ///     6 (gen 2) survive. Silvers at gen 3..=7 (nodes 5..=1) are
    ///     trimmed by the silver per-rank cap.
    ///   - Diamond entry at gen 8 (node 0) survives the diamond cap of 8.
    ///
    /// With the OLD code (walk capped at max_generations=4, no per-earner
    /// filter) this test fails twice over:
    ///   - Silver nodes 5 and 4 would earn at gen 3 and gen 4 (no filter).
    ///   - Diamond node 0 would never appear (walk stops at depth 4).
    ///
    /// Both per-rank caps are the binding constraint, and walk_depth must
    /// extend the walk past max_generations to reach the diamond earner.
    #[test]
    fn threshold_rank_walk_uses_per_rank_depth_cap() {
        let tree = build_chain(9);
        let rates = BTreeMap::from([
            (1u8, 0.10),
            (2u8, 0.08),
            (3u8, 0.06),
            (4u8, 0.04),
            (5u8, 0.03),
            (6u8, 0.03),
            (7u8, 0.03),
            (8u8, 0.02),
        ]);
        let mut structure = threshold_structure("silver", 4, rates);
        structure.generation_commission.max_generations_per_rank =
            BTreeMap::from([("silver".to_string(), 2), ("diamond".to_string(), 8)]);
        let plan = silver_diamond_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        for i in 1..=7 {
            snapshots.insert(uuid(i), silver_snapshot());
        }
        snapshots.insert(uuid(8), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(8),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Silver earners beyond their per-rank cap of 2 must not earn. Under
        // the OLD code these nodes earn at gen 3 and gen 4 because no filter
        // trims them.
        assert!(
            !result.iter().any(|e| e.earner_id == uuid(5)),
            "silver node 5 (gen 3) must not earn (silver per-rank cap is 2)"
        );
        assert!(
            !result.iter().any(|e| e.earner_id == uuid(4)),
            "silver node 4 (gen 4) must not earn (silver per-rank cap is 2)"
        );

        // Silver earners within the per-rank cap of 2 must earn.
        let earn_7 = result.iter().find(|e| e.earner_id == uuid(7)).unwrap();
        assert_eq!(earn_7.level, 1);
        assert_eq!(earn_7.rate, 0.10);

        let earn_6 = result.iter().find(|e| e.earner_id == uuid(6)).unwrap();
        assert_eq!(earn_6.level, 2);
        assert_eq!(earn_6.rate, 0.08);

        // Diamond earner at gen 8 must earn. Under the OLD code the walk
        // stops at depth 4 and this earner never appears.
        let earn_0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_0.level, 8);
        assert_eq!(earn_0.rate, 0.02);

        // Exactly three earnings: silvers 7 and 6, plus diamond 0.
        assert_eq!(result.len(), 3);
    }

    // -- End-to-end per-rank generation depth tests --

    fn bronze_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "bronze".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    fn platinum_snapshot() -> DistributorSnapshot {
        DistributorSnapshot {
            rank: "platinum".to_string(),
            personal_volume: 150.0,
            status: "active".to_string(),
            has_order_in_period: true,
        }
    }

    /// Plan with six ranks for the end-to-end per-rank-depth tests:
    /// associate(1), bronze(2), silver(3), gold(4), platinum(5), diamond(6).
    fn six_rank_plan(structure: GenerationStructureConfig) -> crate::config::CompensationPlan {
        let structure_config = StructureConfig::Generation(structure);
        let mut plan = build_test_plan(default_eligibility(), structure_config, "Generation");
        plan.ranks = vec![
            RankDefinition {
                name: "associate".to_string(),
                ordinal: 1,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "bronze".to_string(),
                ordinal: 2,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "silver".to_string(),
                ordinal: 3,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "gold".to_string(),
                ordinal: 4,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "platinum".to_string(),
                ordinal: 5,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
            RankDefinition {
                name: "diamond".to_string(),
                ordinal: 6,
                qualification: RankQualification {
                    structures: vec![],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec!["Generation".to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            },
        ];
        plan
    }

    /// End-to-end ThresholdRank test for per-rank generation depth.
    ///
    /// Plan: 6 ranks, default max_generations=4, per-rank
    /// `{silver: 2, gold: 4, diamond: 7}`. boundary_rank="silver" so all
    /// silver+ nodes are boundaries.
    ///
    /// Chain: 0(diamond) -> 1..=5(silver) -> 6(gold) -> 7(associate, source).
    ///
    /// walk_depth = max(default=4, max(2, 4, 7)) = 7. Walking up from
    /// node 7 produces eight upline nodes but stops at gen 7:
    ///   gen 1 = node 6 (gold), gen 2..=6 = nodes 5..=1 (silver),
    ///   gen 7 = node 0 (diamond).
    ///
    /// Per-earner filter:
    ///   - gold cap = 4: node 6 at gen 1 keeps.
    ///   - silver cap = 2: node 5 at gen 2 keeps; nodes 4,3,2,1 at gens
    ///     3,4,5,6 trim out.
    ///   - diamond cap = 7: node 0 at gen 7 keeps (boundary cap).
    ///
    /// Final earners: node 6 (gen 1), node 5 (gen 2), node 0 (gen 7).
    ///
    /// The walk reaches gen 7, beyond the default max_generations of 4:
    /// the diamond per-rank cap is the binding constraint that drives
    /// walk_depth past max_generations. If the chain were shorter than
    /// 7, the walk would still terminate at the chain's actual depth.
    /// The silver per-rank cap is the binding constraint that trims
    /// four would-be silver earners.
    #[test]
    fn calculate_generation_per_rank_depth_threshold_rank_end_to_end() {
        let tree = build_chain(8);
        let rates = BTreeMap::from([
            (1u8, 0.10),
            (2u8, 0.08),
            (3u8, 0.06),
            (4u8, 0.04),
            (5u8, 0.03),
            (6u8, 0.025),
            (7u8, 0.02),
        ]);
        let mut structure = threshold_structure("silver", 4, rates);
        structure.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("silver".to_string(), 2u8),
            ("gold".to_string(), 4u8),
            ("diamond".to_string(), 7u8),
        ]);
        let plan = six_rank_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        for i in 1..=5 {
            snapshots.insert(uuid(i), silver_snapshot());
        }
        snapshots.insert(uuid(6), gold_snapshot());
        snapshots.insert(uuid(7), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(7),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Silver nodes beyond the per-rank cap of 2 must not earn.
        for capped_node in [1usize, 2, 3, 4] {
            assert!(
                !result.iter().any(|e| e.earner_id == uuid(capped_node)),
                "silver node {capped_node} must not earn (silver per-rank cap is 2)"
            );
        }

        // Gold node 6 earns gen 1.
        let earn_6 = result.iter().find(|e| e.earner_id == uuid(6)).unwrap();
        assert_eq!(earn_6.level, 1);
        assert_eq!(earn_6.rate, 0.10);
        assert!((earn_6.dollar_amount - 10.0).abs() < f64::EPSILON);

        // Silver node 5 earns gen 2 (within silver cap of 2).
        let earn_5 = result.iter().find(|e| e.earner_id == uuid(5)).unwrap();
        assert_eq!(earn_5.level, 2);
        assert_eq!(earn_5.rate, 0.08);
        assert!((earn_5.dollar_amount - 8.0).abs() < f64::EPSILON);

        // Diamond node 0 earns gen 7. Walk reaches the diamond cap of 7,
        // proving walk_depth (not max_generations or tree depth) is the
        // binding constraint here.
        let earn_0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_0.level, 7);
        assert_eq!(earn_0.rate, 0.02);
        assert!((earn_0.dollar_amount - 2.0).abs() < f64::EPSILON);

        // Exactly three earnings: gold 6, silver 5, diamond 0.
        assert_eq!(result.len(), 3);
    }

    /// End-to-end SameRank test for per-rank generation depth.
    ///
    /// Same plan and tree as the ThresholdRank end-to-end test, but with
    /// `boundary_mode = SameRank`. Each earner's own rank determines the
    /// boundary set and walk depth.
    ///
    /// Chain: 0(diamond) -> 1..=5(silver) -> 6(gold) -> 7(associate, source).
    ///
    /// Per-rank walks (each uses the earner's per-rank cap as walk_max):
    ///   - Associate walk (default cap=4): boundary_set = all ranked nodes.
    ///     Walk: 6 gen 1, 5 gen 2, 4 gen 3, 3 gen 4, STOP. Filter to
    ///     associate ordinal: zero earners (no associates above source).
    ///   - Silver walk (cap=2): boundary_set = silver+. Walk: 6 gen 1,
    ///     5 gen 2, STOP. Filter to silver: only node 5 at gen 2.
    ///   - Gold walk (cap=4): boundary_set = {0, 6}. Walk: 6 gen 1, 5..=1
    ///     skip (not in set), 0 gen 2. Filter to gold: only node 6 at gen 1.
    ///   - Diamond walk (cap=7): boundary_set = {0}. Walk: 6..=1 skip,
    ///     0 gen 1. Filter to diamond: only node 0 at gen 1.
    ///
    /// Final earners: node 5 (silver, gen 2), node 6 (gold, gen 1),
    /// node 0 (diamond, gen 1).
    ///
    /// The silver per-rank cap of 2 is the binding constraint: without
    /// it, the silver walk would extend to default max_generations and
    /// silver nodes 4, 3 would also earn at gens 3, 4.
    #[test]
    fn calculate_generation_per_rank_depth_same_rank_end_to_end() {
        let tree = build_chain(8);
        let rates = BTreeMap::from([
            (1u8, 0.10),
            (2u8, 0.08),
            (3u8, 0.06),
            (4u8, 0.04),
            (5u8, 0.03),
            (6u8, 0.025),
            (7u8, 0.02),
        ]);
        let mut structure = same_rank_structure(4, rates);
        structure.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("silver".to_string(), 2u8),
            ("gold".to_string(), 4u8),
            ("diamond".to_string(), 7u8),
        ]);
        let plan = six_rank_plan(structure.clone());

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        for i in 1..=5 {
            snapshots.insert(uuid(i), silver_snapshot());
        }
        snapshots.insert(uuid(6), gold_snapshot());
        snapshots.insert(uuid(7), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(7),
            cv_amount: 100.0,
        }];

        let result = calculate_generation(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Silver nodes beyond the per-rank cap of 2 must not earn. Without
        // the cap the silver walk would extend further and these would
        // surface at gens 3, 4 in the silver walk.
        for capped_node in [1usize, 2, 3, 4] {
            assert!(
                !result.iter().any(|e| e.earner_id == uuid(capped_node)),
                "silver node {capped_node} must not earn (silver per-rank cap is 2)"
            );
        }

        // Gold node 6 earns gen 1 in the gold walk.
        let earn_6 = result.iter().find(|e| e.earner_id == uuid(6)).unwrap();
        assert_eq!(earn_6.level, 1);
        assert_eq!(earn_6.rate, 0.10);
        assert!((earn_6.dollar_amount - 10.0).abs() < f64::EPSILON);

        // Silver node 5 earns gen 2 in the silver walk (gen 2 = silver cap).
        let earn_5 = result.iter().find(|e| e.earner_id == uuid(5)).unwrap();
        assert_eq!(earn_5.level, 2);
        assert_eq!(earn_5.rate, 0.08);
        assert!((earn_5.dollar_amount - 8.0).abs() < f64::EPSILON);

        // Diamond node 0 earns gen 1 in the diamond walk (only diamond+
        // boundary above the source).
        let earn_0 = result.iter().find(|e| e.earner_id == uuid(0)).unwrap();
        assert_eq!(earn_0.level, 1);
        assert_eq!(earn_0.rate, 0.10);
        assert!((earn_0.dollar_amount - 10.0).abs() < f64::EPSILON);

        // Exactly three earnings: silver 5, gold 6, diamond 0.
        assert_eq!(result.len(), 3);
    }

    /// Regression guard: per-rank entries that equal `max_generations`
    /// must produce identical earnings to an empty per-rank map, because
    /// neither walk depth nor per-earner caps differ in that case.
    ///
    /// Three configs are compared:
    ///   (a) empty per-rank map (today's baseline behavior),
    ///   (b) per-rank map populated for every snapshot rank with the
    ///       value equal to `max_generations` — exercises lookup hits
    ///       AND the per-earner filter, but the caps do not bind,
    ///   (c) per-rank map with PARTIAL coverage (only some ranks present,
    ///       value equal to `max_generations`) — exercises both lookup
    ///       hits and lookup misses (fallback) within one walk.
    ///
    /// All three must produce identical earnings vectors. If any future
    /// refactor causes (b) or (c) to diverge from (a), this test fails.
    /// A test that only compared three empty maps (the previous shape)
    /// would assert nothing beyond determinism.
    #[test]
    fn calculate_generation_empty_per_rank_map_preserves_current_behavior() {
        let tree = build_chain(5);
        let rates = BTreeMap::from([(1u8, 0.10), (2u8, 0.06), (3u8, 0.04)]);

        // Snapshots: a mix of ranks above the source so the rank lookup
        // is exercised on multiple values.
        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        snapshots.insert(uuid(1), platinum_snapshot());
        snapshots.insert(uuid(2), bronze_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot()); // associate
        snapshots.insert(uuid(4), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(4),
            cv_amount: 100.0,
        }];

        let max_gen = 3u8;

        // (a) Empty per-rank map (baseline).
        let structure_a = threshold_structure("bronze", max_gen, rates.clone());
        let plan_a = six_rank_plan(structure_a.clone());
        let result_a =
            calculate_generation(&tree, &plan_a, &structure_a, &snapshots, &volume).unwrap();

        // (b) Full population: every snapshot rank present, all caps == max_gen.
        // walk_depth = max(max_gen, max_gen, ...) = max_gen. Per-earner filter
        // looks each earner up successfully, finds cap == max_gen, no entries
        // trimmed. Equivalent to (a) but exercises the lookup-hit branch.
        let mut structure_b = threshold_structure("bronze", max_gen, rates.clone());
        structure_b.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("associate".to_string(), max_gen),
            ("bronze".to_string(), max_gen),
            ("platinum".to_string(), max_gen),
            ("diamond".to_string(), max_gen),
        ]);
        let plan_b = six_rank_plan(structure_b.clone());
        let result_b =
            calculate_generation(&tree, &plan_b, &structure_b, &snapshots, &volume).unwrap();

        // (c) Partial population: only bronze and diamond present, both == max_gen.
        // Earners at platinum and associate fall through to the default in
        // earner_max_generations. Mixes lookup-hit and lookup-miss branches in
        // one walk. Equivalent earnings to (a).
        let mut structure_c = threshold_structure("bronze", max_gen, rates);
        structure_c.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("bronze".to_string(), max_gen),
            ("diamond".to_string(), max_gen),
        ]);
        let plan_c = six_rank_plan(structure_c.clone());
        let result_c =
            calculate_generation(&tree, &plan_c, &structure_c, &snapshots, &volume).unwrap();

        // All three must produce identical earnings. The full vec is compared
        // so any divergence in earner_id, level, rate, cv_amount, or
        // dollar_amount surfaces.
        assert_eq!(result_a, result_b);
        assert_eq!(result_b, result_c);
    }

    /// SameRank counterpart to the threshold regression guard above. The
    /// SameRank refactor changed the per-rank dedup key from `u16` to
    /// `(&str, u16)`, so SameRank is the path most likely to diverge under
    /// future refactors. Same shape as the threshold version: three configs
    /// (empty / fully populated / partially populated), all expected to
    /// produce identical earnings because the per-rank values match
    /// `max_generations` exactly (no-op caps).
    #[test]
    fn calculate_generation_empty_per_rank_map_preserves_same_rank_behavior() {
        let tree = build_chain(5);
        let rates = BTreeMap::from([(1u8, 0.10), (2u8, 0.06), (3u8, 0.04)]);

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid(0), diamond_snapshot());
        snapshots.insert(uuid(1), platinum_snapshot());
        snapshots.insert(uuid(2), bronze_snapshot());
        snapshots.insert(uuid(3), eligible_snapshot()); // associate
        snapshots.insert(uuid(4), eligible_snapshot()); // associate (source)

        let volume = vec![VolumeSource {
            source_id: uuid(4),
            cv_amount: 100.0,
        }];

        let max_gen = 3u8;

        // (a) Empty per-rank map (baseline).
        let structure_a = same_rank_structure(max_gen, rates.clone());
        let plan_a = six_rank_plan(structure_a.clone());
        let result_a =
            calculate_generation(&tree, &plan_a, &structure_a, &snapshots, &volume).unwrap();

        // (b) Full population: every per-rank-ordinal walk uses walk_max == max_gen.
        let mut structure_b = same_rank_structure(max_gen, rates.clone());
        structure_b.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("associate".to_string(), max_gen),
            ("bronze".to_string(), max_gen),
            ("platinum".to_string(), max_gen),
            ("diamond".to_string(), max_gen),
        ]);
        let plan_b = six_rank_plan(structure_b.clone());
        let result_b =
            calculate_generation(&tree, &plan_b, &structure_b, &snapshots, &volume).unwrap();

        // (c) Partial population: only bronze and diamond present, both == max_gen.
        // The platinum and associate per-rank-ordinal walks fall back to the
        // default, exercising both branches of earner_max_generations within
        // one calculation.
        let mut structure_c = same_rank_structure(max_gen, rates);
        structure_c.generation_commission.max_generations_per_rank = BTreeMap::from([
            ("bronze".to_string(), max_gen),
            ("diamond".to_string(), max_gen),
        ]);
        let plan_c = six_rank_plan(structure_c.clone());
        let result_c =
            calculate_generation(&tree, &plan_c, &structure_c, &snapshots, &volume).unwrap();

        assert_eq!(result_a, result_b);
        assert_eq!(result_b, result_c);
    }
}
