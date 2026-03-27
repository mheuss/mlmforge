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

    // Resolve the boundary rank ordinal. If the boundary rank doesn't exist
    // in the plan's rank ladder, no one can be a boundary and we return empty.
    let boundary_ordinal = match gen_config.boundary_mode {
        GenerationBoundaryMode::ThresholdRank => {
            match rank_ordinals
                .get(gen_config.boundary_rank.as_str())
                .copied()
            {
                Some(ord) => Some(ord),
                None => {
                    log::warn!(
                        "generation boundary_rank '{}' not found in plan ranks; \
                         no generation commissions will be paid",
                        gen_config.boundary_rank
                    );
                    return Ok(Vec::new());
                }
            }
        }
        // SameRank mode resolves per-earner, handled in a future task.
        GenerationBoundaryMode::SameRank => None,
    };

    // Build boundary set: nodes whose rank ordinal meets or exceeds the
    // boundary threshold. For ThresholdRank, this is computed once for all
    // volume sources. For SameRank, this would be per-earner (future task).
    let boundary_set: HashSet<Uuid> = if let Some(threshold) = boundary_ordinal {
        snapshots
            .iter()
            .filter(|(_, snap)| {
                rank_ordinals.get(snap.rank.as_str()).copied().unwrap_or(0) >= threshold
            })
            .map(|(id, _)| *id)
            .collect()
    } else {
        HashSet::new()
    };

    // Build boundary_check closure. When ineligible_creates_boundary is true,
    // all boundary-rank nodes count as boundaries (they're filtered at earning
    // time). When false, only eligible boundary-rank nodes create boundaries.
    let boundary_check: Box<dyn Fn(Uuid) -> bool + '_> = if gen_config.ineligible_creates_boundary {
        Box::new(|_| true)
    } else {
        Box::new(|id: Uuid| eligibility_cache.get(&id).is_some_and(|e| e.eligible))
    };

    let mut earnings = Vec::new();

    for source in volume {
        walk::validate_cv(source)?;

        // Verify the source exists in the tree. get_upline returns Err
        // for unknown nodes. For the root (no parents), it returns Ok
        // with an empty Vec, so this check is safe.
        tree.get_upline(source.source_id, 0)
            .map_err(|_| CalculationError::SourceNotInTree(source.source_id))?;

        let gen_entries = count_generations_upward(
            tree,
            source.source_id,
            &boundary_set,
            &boundary_check,
            gen_config.max_generations,
            gen_config.empty_generation_consumes_number,
        );

        for entry in &gen_entries {
            // When ineligible_creates_boundary is true, ineligible earners
            // are in the boundary set but must be filtered at earning time.
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

    walk::sort_earnings(&mut earnings);
    Ok(earnings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::test_helpers::uuid_from_index as uuid;
    use crate::tree::unilevel::UnilevelTree;

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
    use crate::commission::types::{DistributorSnapshot, VolumeSource};
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
}
