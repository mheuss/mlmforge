//! Rank and earning results do not depend on the order volume arrives in.

use std::collections::HashMap;

use network_engine::commission::calculate_unilevel;
use network_engine::commission::types::{DistributorSnapshot, VolumeSource};
use network_engine::config::{CompensationPlan, StructureConfig};
use network_engine::decimal::{AuthoredDec, Dec};
use network_engine::rank::{
    DistributorPrimitives, EvaluatedRank, EvaluationInputs, evaluate_ranks,
};
use network_engine::test_support::{test_plan_identity, uuid_from_index};
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use uuid::Uuid;

fn plan() -> CompensationPlan {
    let text = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../testdata/config_contract/fixtures/unilevel.json"
    ))
    .unwrap();
    serde_json::from_str(&text).unwrap()
}

fn cents(c: u64) -> AuthoredDec {
    format!("{}.{:02}", c / 100, c % 100).parse().unwrap()
}

fn build_tree(parents: &[usize]) -> UnilevelTree {
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for (i, p) in parents.iter().enumerate() {
        let parent = uuid_from_index(p % (i + 1));
        tree.add_node(uuid_from_index(i + 1), parent, parent, 0)
            .unwrap();
    }
    tree
}

type EarningKey = (Uuid, Uuid, u8, Option<Dec>, Dec, Dec);

fn run(
    plan: &CompensationPlan,
    tree: &UnilevelTree,
    pv: &[u64],
    volume: &[(usize, u64)],
) -> (HashMap<Uuid, EvaluatedRank>, Vec<EarningKey>) {
    let StructureConfig::Unilevel(structure) = &plan.structures[0] else {
        panic!("the unilevel fixture's first structure is unilevel");
    };
    let n = pv.len();
    let sources: Vec<VolumeSource> = volume
        .iter()
        .map(|(node, c)| VolumeSource {
            source_id: uuid_from_index(node % n),
            cv_amount: cents(*c),
        })
        .collect();
    let inputs = EvaluationInputs {
        distributors: (0..n)
            .map(|i| {
                (
                    uuid_from_index(i),
                    DistributorPrimitives {
                        personal_volume: cents(pv[i]),
                        retail_volume: cents(pv[i] / 4),
                        status: "active".to_string(),
                        has_order_in_period: true,
                        active_products: vec!["starter-pack".to_string()],
                    },
                )
            })
            .collect(),
        volume_sources: sources.clone(),
        history_window: vec![],
        history: HashMap::new(),
    };
    let mut trees: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    trees.insert(structure.name.clone(), tree);
    let ranks: HashMap<Uuid, EvaluatedRank> = evaluate_ranks(plan, &trees, &inputs)
        .unwrap()
        .ranks
        .into_iter()
        .collect();
    let lowest = plan.ranks[0].name.clone();
    let snapshots: HashMap<Uuid, DistributorSnapshot> = (0..n)
        .map(|i| {
            let id = uuid_from_index(i);
            let rank = match ranks.get(&id) {
                Some(EvaluatedRank::Qualified { rank, .. }) => rank.clone(),
                _ => lowest.clone(),
            };
            (
                id,
                DistributorSnapshot {
                    rank,
                    personal_volume: cents(pv[i]),
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            )
        })
        .collect();
    let result = calculate_unilevel(
        tree,
        plan,
        structure,
        &snapshots,
        &sources,
        &test_plan_identity(),
    )
    .unwrap();
    let mut earnings: Vec<EarningKey> = result
        .earnings
        .iter()
        .map(|e| {
            (
                e.earner_id,
                e.source_id,
                e.level,
                e.rate,
                e.cv_amount,
                e.dollar_amount,
            )
        })
        .collect();
    earnings.sort();
    (ranks, earnings)
}

proptest! {
    #[test]
    fn ranks_and_earnings_do_not_depend_on_volume_order(
        parents in prop::collection::vec(any::<usize>(), 1..40),
        pv in prop::collection::vec(0u64..500_000, 41),
        orders in prop::collection::vec((any::<usize>(), 0u64..2_000_000), 1..120)
            .prop_flat_map(|v| (Just(v.clone()), Just(v).prop_shuffle())),
    ) {
        let (volume, shuffled) = orders;
        let plan = plan();
        let tree = build_tree(&parents);
        let pv = &pv[..=parents.len()];
        prop_assert_eq!(run(&plan, &tree, pv, &volume), run(&plan, &tree, pv, &shuffled));
    }
}
