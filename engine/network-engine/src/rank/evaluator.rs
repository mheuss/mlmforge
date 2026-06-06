//! Fixpoint evaluation driver and ladder ascent loop.

use std::collections::HashMap;

use uuid::Uuid;

use crate::commission::types::VolumeSource;
use crate::config::rank::RankDefinition;
use crate::rank::predicates::satisfies;
use crate::rank::types::{DistributorPrimitives, EvaluatedRank, EvaluationError};
use crate::tree::navigator::TreeNavigator;

/// Per-distributor CV total, derived from `EvaluationInputs.volume_sources`.
///
/// Built once per `evaluate_ranks` call and reused across predicate evaluations.
pub(crate) struct VolumeIndex {
    by_source: HashMap<Uuid, f64>,
}

impl VolumeIndex {
    pub(crate) fn build(sources: &[VolumeSource]) -> Self {
        let mut by_source: HashMap<Uuid, f64> = HashMap::new();
        for src in sources {
            *by_source.entry(src.source_id).or_insert(0.0) += src.cv_amount;
        }
        Self { by_source }
    }

    pub(crate) fn cv_for(&self, user_id: Uuid) -> f64 {
        self.by_source.get(&user_id).copied().unwrap_or(0.0)
    }
}

/// Read-only ambient inputs for one evaluation pass. Bundled to keep
/// `satisfies`/`evaluate_distributor` under the clippy arg limit and to
/// carry the windowed/tenure history without widening every signature.
/// All references are confined to a single `evaluate_ranks` call.
pub(crate) struct EvalCtx<'a, 'b> {
    pub distributors: &'a HashMap<Uuid, DistributorPrimitives>,
    pub ranks: &'a [RankDefinition],
    pub trees: &'a HashMap<String, &'b dyn TreeNavigator>,
    pub volume_index: &'a VolumeIndex,
    pub rank_ordinals: &'a HashMap<String, u16>,
    #[allow(dead_code)] // Read by the windowed/tenure gates (HEU-446 tasks 5/6).
    pub history_window: &'a [String],
    #[allow(dead_code)] // Read by the windowed/tenure gates (HEU-446 tasks 5/6).
    pub history: &'a HashMap<Uuid, HashMap<String, Option<u16>>>,
}

/// Compute a heuristic evaluation order across all trees: deepest-first,
/// tiebroken by user_id.
///
/// For each user in `users`, look up their depth in every tree they appear in
/// and keep the maximum. Users not present in any tree are skipped. Sort by
/// depth descending, then by user_id ascending so the output is deterministic.
///
/// This order is a performance heuristic for the fixpoint loop in
/// `iterate_to_fixpoint`, not a correctness guarantee. The fixpoint converges
/// to the same result for any order. A deepest-first order simply lets the
/// common single-tree, acyclic case settle in one effective pass. See
/// design-rationale 026.
pub(crate) fn evaluation_order_for_users(
    trees: &HashMap<String, &dyn TreeNavigator>,
    users: &[Uuid],
) -> Vec<Uuid> {
    let mut depth_by_user: HashMap<Uuid, u32> = HashMap::new();
    for u in users {
        for tree in trees.values() {
            if !tree.contains(*u) {
                continue;
            }
            if let Ok(pos) = tree.get_position(*u) {
                let entry = depth_by_user.entry(*u).or_insert(0);
                if pos.depth > *entry {
                    *entry = pos.depth;
                }
            }
        }
    }
    let mut order: Vec<(Uuid, u32)> = depth_by_user.into_iter().collect();
    order.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    order.into_iter().map(|(u, _)| u).collect()
}

/// Walk the rank ladder for one distributor and return their evaluated rank.
///
/// `ctx.ranks` MUST be sorted by ordinal ascending. Iterates lowest to
/// highest; the last passing rank is the result. A failed rank does NOT
/// short-circuit — higher ranks may still pass and be selected. This handles
/// ladder gaps where a distributor satisfies rank N+1 but not rank N (e.g.,
/// missing a required product unique to N).
pub(crate) fn evaluate_distributor(
    user_id: Uuid,
    already: &HashMap<Uuid, EvaluatedRank>,
    ctx: &EvalCtx<'_, '_>,
) -> Result<EvaluatedRank, EvaluationError> {
    let mut current: Option<(String, u16)> = None;
    for rank in ctx.ranks {
        if satisfies(rank, user_id, already, ctx)? {
            current = Some((rank.name.clone(), rank.ordinal));
        }
    }
    Ok(match current {
        Some((name, ordinal)) => EvaluatedRank::Qualified {
            rank: name,
            ordinal,
        },
        None => EvaluatedRank::Unranked,
    })
}

/// Run rank evaluation passes until the rank map reaches a fixpoint.
///
/// Each pass evaluates every user in `order` against the accumulating
/// `already` map. Rank evaluation is monotone in `already` — every
/// descendant-reading predicate is a "rank >= X" threshold — so iterating
/// from the empty map converges to the least fixpoint. The result is the
/// same for any `order`; `order` only affects how many passes it takes.
///
/// `max_passes` is `order.len() * (ctx.ranks.len() + 1) + 1`. A distributor's
/// evaluated rank only rises, through at most `ctx.ranks.len() + 1` distinct
/// values, so no run can need more passes. Reaching the bound means a
/// non-monotone descendant-reading predicate was introduced — a bug — and
/// yields `RankEvaluationDidNotConverge`.
pub(crate) fn iterate_to_fixpoint(
    order: &[Uuid],
    ctx: &EvalCtx<'_, '_>,
) -> Result<HashMap<Uuid, EvaluatedRank>, EvaluationError> {
    let max_passes = order
        .len()
        .saturating_mul(ctx.ranks.len().saturating_add(1))
        .saturating_add(1);

    let mut already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
    for _ in 0..max_passes {
        let mut changed = false;
        for &user_id in order {
            let evaluated = evaluate_distributor(user_id, &already, ctx)?;
            // `already` is updated in place, so later users in this pass see
            // earlier users' fresh ranks. That speeds convergence and does
            // not change the fixpoint. A user never reads its own entry —
            // a node is not its own descendant within a single tree.
            if already.get(&user_id) != Some(&evaluated) {
                already.insert(user_id, evaluated);
                changed = true;
            }
        }
        if !changed {
            return Ok(already);
        }
    }
    Err(EvaluationError::RankEvaluationDidNotConverge { passes: max_passes })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use proptest::prelude::*;
    use uuid::Uuid;

    #[test]
    fn eval_ctx_exposes_history_fields() {
        let distributors = HashMap::new();
        let ranks: Vec<RankDefinition> = vec![];
        let trees: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        let vi = VolumeIndex::build(&[]);
        let ords = HashMap::new();
        let window = vec!["2026-05".to_string()];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &trees,
            volume_index: &vi,
            rank_ordinals: &ords,
            history_window: &window,
            history: &history,
        };
        assert_eq!(ctx.history_window.len(), 1);
    }

    #[test]
    fn volume_index_sums_cv_per_source() {
        let a = Uuid::from_u128(1);
        let b = Uuid::from_u128(2);
        let sources = vec![
            VolumeSource {
                source_id: a,
                cv_amount: 100.0,
            },
            VolumeSource {
                source_id: a,
                cv_amount: 50.0,
            },
            VolumeSource {
                source_id: b,
                cv_amount: 200.0,
            },
        ];
        let idx = VolumeIndex::build(&sources);
        assert!((idx.cv_for(a) - 150.0).abs() < 1e-9);
        assert!((idx.cv_for(b) - 200.0).abs() < 1e-9);
        assert_eq!(idx.cv_for(Uuid::from_u128(99)), 0.0);
    }

    use std::collections::HashMap as StdHashMap;

    use crate::tree::navigator::TreeNavigator;
    use crate::tree::unilevel::UnilevelTree;

    #[test]
    fn evaluation_order_returns_deepest_first() {
        let mut tree = UnilevelTree::new();
        tree.add_root(Uuid::from_u128(1), 0).unwrap();
        tree.add_node(
            Uuid::from_u128(2),
            Uuid::from_u128(1),
            Uuid::from_u128(1),
            0,
        )
        .unwrap();
        tree.add_node(
            Uuid::from_u128(3),
            Uuid::from_u128(2),
            Uuid::from_u128(2),
            0,
        )
        .unwrap();

        let mut nav_map: StdHashMap<String, &dyn TreeNavigator> = StdHashMap::new();
        nav_map.insert("S1".to_string(), &tree);

        let users = vec![Uuid::from_u128(1), Uuid::from_u128(2), Uuid::from_u128(3)];
        let order = evaluation_order_for_users(&nav_map, &users);

        let pos = |id: u128| {
            order
                .iter()
                .position(|u| *u == Uuid::from_u128(id))
                .unwrap()
        };
        assert!(pos(3) < pos(2));
        assert!(pos(2) < pos(1));
        assert_eq!(order.len(), 3);
    }

    #[test]
    fn evaluation_order_uses_max_depth_across_trees() {
        let mut t1 = UnilevelTree::new();
        t1.add_root(Uuid::from_u128(1), 0).unwrap();
        t1.add_node(
            Uuid::from_u128(2),
            Uuid::from_u128(1),
            Uuid::from_u128(1),
            0,
        )
        .unwrap();
        // user 2 is at depth 1 in t1.

        let mut t2 = UnilevelTree::new();
        t2.add_root(Uuid::from_u128(99), 0).unwrap();
        t2.add_node(
            Uuid::from_u128(98),
            Uuid::from_u128(99),
            Uuid::from_u128(99),
            0,
        )
        .unwrap();
        t2.add_node(
            Uuid::from_u128(2),
            Uuid::from_u128(98),
            Uuid::from_u128(98),
            0,
        )
        .unwrap();
        // user 2 is at depth 2 in t2.

        let mut nav: StdHashMap<String, &dyn TreeNavigator> = StdHashMap::new();
        nav.insert("t1".to_string(), &t1);
        nav.insert("t2".to_string(), &t2);

        let users = vec![
            Uuid::from_u128(1),
            Uuid::from_u128(2),
            Uuid::from_u128(98),
            Uuid::from_u128(99),
        ];
        let order = evaluation_order_for_users(&nav, &users);

        // User 2's effective depth = max(1, 2) = 2. They must come before
        // user 99 (depth 0 in t2) and user 1 (depth 0 in t1).
        let pos = |id: u128| {
            order
                .iter()
                .position(|u| *u == Uuid::from_u128(id))
                .unwrap()
        };
        assert!(pos(2) < pos(99));
        assert!(pos(2) < pos(1));
    }

    #[test]
    fn evaluation_order_skips_users_not_in_any_tree() {
        let mut tree = UnilevelTree::new();
        tree.add_root(Uuid::from_u128(1), 0).unwrap();

        let mut nav: StdHashMap<String, &dyn TreeNavigator> = StdHashMap::new();
        nav.insert("S1".to_string(), &tree);

        let users = vec![Uuid::from_u128(1), Uuid::from_u128(99)];
        let order = evaluation_order_for_users(&nav, &users);

        assert_eq!(order, vec![Uuid::from_u128(1)]);
    }

    use crate::config::rank::{
        DemotionPolicy, RankDefinition, RankQualification, StructureQualification,
    };

    fn linear_rank(name: &str, ord: u16, structure: &str, pv: f64) -> RankDefinition {
        RankDefinition {
            name: name.to_string(),
            ordinal: ord,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: structure.to_string(),
                    personal_volume: pv,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
            },
            qualified_structures: vec![structure.to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }
    }

    #[test]
    fn evaluate_distributor_picks_highest_qualifying_rank() {
        use crate::rank::types::{DistributorPrimitives, EvaluatedRank};

        let mut tree = UnilevelTree::new();
        tree.add_root(Uuid::from_u128(1), 0).unwrap();
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);
        let idx = VolumeIndex::build(&[]);

        let primitives = DistributorPrimitives {
            personal_volume: 250.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };

        let ranks = vec![
            linear_rank("associate", 1, "Test", 0.0),
            linear_rank("silver", 2, "Test", 100.0),
            linear_rank("gold", 3, "Test", 200.0),
            linear_rank("diamond", 4, "Test", 500.0),
        ];
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        for r in &ranks {
            ordinals.insert(r.name.clone(), r.ordinal);
        }

        let already: HashMap<Uuid, EvaluatedRank> = HashMap::new();
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(Uuid::from_u128(1), primitives);
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };
        let result = evaluate_distributor(Uuid::from_u128(1), &already, &ctx).unwrap();

        assert_eq!(
            result,
            EvaluatedRank::Qualified {
                rank: "gold".to_string(),
                ordinal: 3
            }
        );
    }

    #[test]
    fn evaluate_distributor_returns_unranked_when_no_rank_passes() {
        use crate::rank::types::{DistributorPrimitives, EvaluatedRank};

        let mut tree = UnilevelTree::new();
        tree.add_root(Uuid::from_u128(1), 0).unwrap();
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);
        let idx = VolumeIndex::build(&[]);

        let primitives = DistributorPrimitives {
            personal_volume: 0.0,
            retail_volume: 0.0,
            status: "inactive".to_string(),
            has_order_in_period: false,
            active_products: vec![],
        };

        let ranks = vec![linear_rank("silver", 2, "Test", 100.0)];
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        ordinals.insert("silver".to_string(), 2);

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(Uuid::from_u128(1), primitives);
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };
        let result = evaluate_distributor(Uuid::from_u128(1), &HashMap::new(), &ctx).unwrap();

        assert_eq!(result, EvaluatedRank::Unranked);
    }

    #[test]
    fn evaluate_distributor_handles_ladder_gap() {
        // Plan should pick rank 3 (gold) because rank 2 (silver) fails on
        // missing product, but rank 3's requirements are still met.
        use crate::rank::types::{DistributorPrimitives, EvaluatedRank};

        let mut tree = UnilevelTree::new();
        tree.add_root(Uuid::from_u128(1), 0).unwrap();
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);
        let idx = VolumeIndex::build(&[]);

        let primitives = DistributorPrimitives {
            personal_volume: 100.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec!["kit-A".to_string()],
        };

        // rank 2 requires product kit-B which the distributor lacks.
        let mut rank_two = linear_rank("silver", 2, "Test", 50.0);
        rank_two.qualification.required_products = vec!["kit-B".to_string()];

        let ranks = vec![
            linear_rank("associate", 1, "Test", 0.0),
            rank_two,
            linear_rank("gold", 3, "Test", 50.0),
        ];
        let mut ordinals: HashMap<String, u16> = HashMap::new();
        for r in &ranks {
            ordinals.insert(r.name.clone(), r.ordinal);
        }

        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(Uuid::from_u128(1), primitives);
        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &nav,
            volume_index: &idx,
            rank_ordinals: &ordinals,
            history_window: &window,
            history: &history,
        };
        let result = evaluate_distributor(Uuid::from_u128(1), &HashMap::new(), &ctx).unwrap();

        assert_eq!(
            result,
            EvaluatedRank::Qualified {
                rank: "gold".to_string(),
                ordinal: 3
            }
        );
    }

    #[test]
    fn iterate_to_fixpoint_same_result_for_any_order() {
        use crate::config::rank::{DistributorCountRequirement, SearchMode};
        use crate::rank::types::DistributorPrimitives;

        // Two structure trees. `subject` is the root of `uni` (depth 0) but
        // sits at depth 2 in `bin`, so the max-depth heuristic orders
        // `subject` before its own `uni` child `child`. `child` qualifies
        // `subject` for `manager` via the distributor_count predicate.
        //
        // uni:  subject(0) -> child(1)
        // bin:  f1(0) -> f2(1) -> subject(2)
        let subject = Uuid::from_u128(1);
        let child = Uuid::from_u128(2);
        let f1 = Uuid::from_u128(10);
        let f2 = Uuid::from_u128(11);

        let mut uni = UnilevelTree::new();
        uni.add_root(subject, 0).unwrap();
        uni.add_node(child, subject, subject, 0).unwrap();

        let mut bin = UnilevelTree::new();
        bin.add_root(f1, 0).unwrap();
        bin.add_node(f2, f1, f1, 0).unwrap();
        bin.add_node(subject, f2, f2, 0).unwrap();

        let mut trees: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        trees.insert("uni".to_string(), &uni);
        trees.insert("bin".to_string(), &bin);

        // Ladder: associate (trivial) and manager (needs 1 associate downline).
        let associate = RankDefinition {
            name: "associate".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "uni".to_string(),
                    personal_volume: 0.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
            },
            qualified_structures: vec!["uni".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        };
        let manager = RankDefinition {
            name: "manager".to_string(),
            ordinal: 2,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "uni".to_string(),
                    personal_volume: 0.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: Some(DistributorCountRequirement {
                        count: 1,
                        min_rank: "associate".to_string(),
                        search_mode: SearchMode::AnyLevel,
                        search_depth: None,
                        total_count: 1,
                        min_leg_group_volume: 0.0,
                    }),
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
            },
            qualified_structures: vec!["uni".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        };
        let ranks = vec![associate, manager];
        let rank_ordinals: HashMap<String, u16> =
            ranks.iter().map(|r| (r.name.clone(), r.ordinal)).collect();

        let prim = DistributorPrimitives {
            personal_volume: 0.0,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };
        let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
        distributors.insert(subject, prim.clone());
        distributors.insert(child, prim);

        let volume_index = VolumeIndex::build(&[]);

        // The heuristic order places `subject` first. Its exact reverse places
        // `subject` last. The fixpoint result must be identical for both.
        let heuristic = evaluation_order_for_users(&trees, &[subject, child]);
        let mut reversed = heuristic.clone();
        reversed.reverse();

        let window: Vec<String> = vec![];
        let history = HashMap::new();
        let ctx = EvalCtx {
            distributors: &distributors,
            ranks: &ranks,
            trees: &trees,
            volume_index: &volume_index,
            rank_ordinals: &rank_ordinals,
            history_window: &window,
            history: &history,
        };

        let from_heuristic = iterate_to_fixpoint(&heuristic, &ctx).unwrap();
        let from_reversed = iterate_to_fixpoint(&reversed, &ctx).unwrap();

        // Order-independent, and the fixpoint counts `child` for `subject`.
        assert_eq!(from_heuristic, from_reversed);
        assert_eq!(
            from_heuristic.get(&subject),
            Some(&EvaluatedRank::Qualified {
                rank: "manager".to_string(),
                ordinal: 2,
            })
        );
        assert_eq!(
            from_heuristic.get(&child),
            Some(&EvaluatedRank::Qualified {
                rank: "associate".to_string(),
                ordinal: 1,
            })
        );
    }

    proptest! {
        /// `iterate_to_fixpoint` produces the same result for any within-pass
        /// order. Two trees with different ancestry (`uni`: a->b->c, `bin`:
        /// c->b->a) make some evaluation orders visit an ancestor before its
        /// descendants, which forces the loop to run more than one pass. Only
        /// `uni` carries qualifications; `bin` exists only to skew tree depth.
        /// PV values are randomized; for every draw, all six orderings of the
        /// three distributors must yield the same fixpoint rank map.
        #[test]
        fn prop_fixpoint_order_independent(
            pv_a in 0u32..300,
            pv_b in 0u32..300,
            pv_c in 0u32..300,
        ) {
            use crate::config::rank::{DistributorCountRequirement, SearchMode};
            use crate::rank::types::DistributorPrimitives;

            let a = Uuid::from_u128(1);
            let b = Uuid::from_u128(2);
            let c = Uuid::from_u128(3);

            // uni: a(0) -> b(1) -> c(2)
            let mut uni = UnilevelTree::new();
            uni.add_root(a, 0).unwrap();
            uni.add_node(b, a, a, 0).unwrap();
            uni.add_node(c, b, b, 0).unwrap();
            // bin: c(0) -> b(1) -> a(2). Different ancestry from uni; it skews
            // the depth heuristic but carries no qualification of its own.
            let mut bin = UnilevelTree::new();
            bin.add_root(c, 0).unwrap();
            bin.add_node(b, c, c, 0).unwrap();
            bin.add_node(a, b, b, 0).unwrap();

            let mut trees: HashMap<String, &dyn TreeNavigator> = HashMap::new();
            trees.insert("uni".to_string(), &uni);
            trees.insert("bin".to_string(), &bin);

            let sq = |dc: Option<DistributorCountRequirement>| StructureQualification {
                structure: "uni".to_string(),
                personal_volume: 100.0,
                group_volume: 0.0,
                max_group_volume_per_leg: f64::MAX,
                min_retail_volume: 0.0,
                distributor_count: dc,
                leg_quality: vec![],
            };
            let ranks = vec![
                RankDefinition {
                    name: "associate".to_string(),
                    ordinal: 1,
                    qualification: RankQualification {
                        structures: vec![sq(None)],
                        required_products: vec![],
                        window: None,
                    },
                    qualified_structures: vec!["uni".to_string()],
                    demotion_policy: DemotionPolicy::PromotionOnly,
                },
                RankDefinition {
                    name: "manager".to_string(),
                    ordinal: 2,
                    qualification: RankQualification {
                        structures: vec![sq(Some(DistributorCountRequirement {
                            count: 1,
                            min_rank: "associate".to_string(),
                            search_mode: SearchMode::AnyLevel,
                            search_depth: None,
                            total_count: 1,
                            min_leg_group_volume: 0.0,
                        }))],
                        required_products: vec![],
                        window: None,
                    },
                    qualified_structures: vec!["uni".to_string()],
                    demotion_policy: DemotionPolicy::PromotionOnly,
                },
            ];
            let rank_ordinals: HashMap<String, u16> =
                ranks.iter().map(|r| (r.name.clone(), r.ordinal)).collect();

            // Only personal volume is randomized; status and order activity
            // are held constant so order is the lone varying dimension.
            let prim = |pv: u32| DistributorPrimitives {
                personal_volume: pv as f64,
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            };
            let mut distributors: HashMap<Uuid, DistributorPrimitives> = HashMap::new();
            distributors.insert(a, prim(pv_a));
            distributors.insert(b, prim(pv_b));
            distributors.insert(c, prim(pv_c));

            let volume_index = VolumeIndex::build(&[]);

            // Every ordering of the three distributors must converge to the
            // same fixpoint. Six permutations is exhaustive for three users.
            let orders = [
                [a, b, c],
                [a, c, b],
                [b, a, c],
                [b, c, a],
                [c, a, b],
                [c, b, a],
            ];
            let window: Vec<String> = vec![];
            let history = HashMap::new();
            let ctx = EvalCtx {
                distributors: &distributors,
                ranks: &ranks,
                trees: &trees,
                volume_index: &volume_index,
                rank_ordinals: &rank_ordinals,
                history_window: &window,
                history: &history,
            };
            let baseline = iterate_to_fixpoint(&orders[0], &ctx).unwrap();
            for order in &orders {
                let result = iterate_to_fixpoint(order, &ctx).unwrap();
                prop_assert_eq!(&result, &baseline);
            }
        }
    }
}
