//! Bottom-up walk driver and ladder ascent loop.

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

/// Compute evaluation order across all trees: deepest-first, tiebroken by user_id.
///
/// For each user in `users`, look up their depth in every tree they appear in
/// and keep the maximum. Users not present in any tree are skipped. Sort by
/// depth descending, then by user_id ascending so the output is deterministic.
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
/// `ranks` MUST be sorted by ordinal ascending. Iterates lowest to highest;
/// the last passing rank is the result. A failed rank does NOT short-circuit
/// — higher ranks may still pass and be selected. This handles ladder gaps
/// where a distributor satisfies rank N+1 but not rank N (e.g., missing a
/// required product unique to N).
pub(crate) fn evaluate_distributor(
    user_id: Uuid,
    primitives: &DistributorPrimitives,
    ranks: &[RankDefinition],
    trees: &HashMap<String, &dyn TreeNavigator>,
    volume_index: &VolumeIndex,
    rank_ordinals: &HashMap<String, u16>,
    already: &HashMap<Uuid, EvaluatedRank>,
) -> Result<EvaluatedRank, EvaluationError> {
    let mut current: Option<(String, u16)> = None;
    for rank in ranks {
        if satisfies(
            rank,
            user_id,
            primitives,
            trees,
            volume_index,
            already,
            rank_ordinals,
        )? {
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commission::types::VolumeSource;
    use uuid::Uuid;

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
        let result = evaluate_distributor(
            Uuid::from_u128(1),
            &primitives,
            &ranks,
            &nav,
            &idx,
            &ordinals,
            &already,
        )
        .unwrap();

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

        let result = evaluate_distributor(
            Uuid::from_u128(1),
            &primitives,
            &ranks,
            &nav,
            &idx,
            &ordinals,
            &HashMap::new(),
        )
        .unwrap();

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

        let result = evaluate_distributor(
            Uuid::from_u128(1),
            &primitives,
            &ranks,
            &nav,
            &idx,
            &ordinals,
            &HashMap::new(),
        )
        .unwrap();

        assert_eq!(
            result,
            EvaluatedRank::Qualified {
                rank: "gold".to_string(),
                ordinal: 3
            }
        );
    }
}
