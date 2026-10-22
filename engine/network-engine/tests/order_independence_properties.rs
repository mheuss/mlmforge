//! Rank and earning results do not depend on the order volume arrives in.

use std::collections::HashMap;

use network_engine::commission::calculate_unilevel;
use network_engine::commission::types::{DistributorSnapshot, VolumeSource, Walk};
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

/// An earning without its walk index, carrying its walk's content instead.
type EarningKey = (Uuid, Uuid, u8, Option<Dec>, Dec, Dec, Option<String>);

/// A walk's JSON with its index removed.
fn walk_content(walk: &Walk) -> String {
    let mut value = serde_json::to_value(walk).unwrap();
    value.as_object_mut().unwrap().remove("index");
    value.to_string()
}

type Outcome = (HashMap<Uuid, EvaluatedRank>, Vec<EarningKey>, Vec<String>);

fn run(
    plan: &CompensationPlan,
    tree: &UnilevelTree,
    pv: &[u64],
    volume: &[(usize, u64)],
) -> Outcome {
    let StructureConfig::Unilevel(structure) = &plan.structures[0] else {
        panic!("expected plan.structures[0] to be unilevel");
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
    let walks: HashMap<u32, String> = result
        .walks
        .iter()
        .map(|w| (w.index, walk_content(w)))
        .collect();
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
                e.walk.map(|i| walks[&i].clone()),
            )
        })
        .collect();
    earnings.sort();
    let mut walk_contents: Vec<String> = walks.into_values().collect();
    walk_contents.sort();
    (ranks, earnings, walk_contents)
}

type Case = (Vec<usize>, Vec<u64>, Vec<(usize, u64)>);

/// Any tree of up to 40 nodes, with any cent-valued volume.
fn any_case() -> impl Strategy<Value = Case> {
    (
        prop::collection::vec(any::<usize>(), 1..40),
        prop::collection::vec(0u64..500_000, 41),
        prop::collection::vec((any::<usize>(), 0u64..2_000_000), 1..120),
    )
        .prop_map(|(parents, mut pv, volume)| {
            pv.truncate(parents.len() + 1);
            (parents, pv, volume)
        })
}

/// A root whose group volume is exactly the Builder threshold of 500: its own
/// personal volume plus a child's volume split into random cent parts.
fn threshold_case() -> impl Strategy<Value = Case> {
    (
        10_000u64..50_000,
        0u64..500_000,
        prop::collection::vec(any::<u64>(), 1..8),
    )
        .prop_map(|(root_pv, child_pv, cuts)| {
            let rest = 50_000 - root_pv;
            let mut points: Vec<u64> = cuts.iter().map(|c| c % (rest + 1)).collect();
            points.push(0);
            points.push(rest);
            points.sort_unstable();
            let volume = points.windows(2).map(|w| (1, w[1] - w[0])).collect();
            (vec![0], vec![root_pv, child_pv], volume)
        })
}

/// A case, with some entries repeated exactly, and the same volume shuffled.
fn ordered_and_shuffled() -> impl Strategy<Value = (Case, Vec<(usize, u64)>)> {
    prop_oneof![any_case(), threshold_case()]
        .prop_flat_map(|(parents, pv, volume)| {
            let len = volume.len();
            (
                Just((parents, pv, volume)),
                prop::collection::vec(0..len, 0..3),
            )
        })
        .prop_map(|((parents, pv, mut volume), repeats)| {
            for i in repeats {
                volume.push(volume[i]);
            }
            (parents, pv, volume)
        })
        .prop_flat_map(|case| {
            let shuffled = Just(case.2.clone()).prop_shuffle();
            (Just(case), shuffled)
        })
}

proptest! {
    #[test]
    fn ranks_and_earnings_do_not_depend_on_volume_order(
        ((parents, pv, volume), shuffled) in ordered_and_shuffled(),
    ) {
        let plan = plan();
        let tree = build_tree(&parents);
        prop_assert_eq!(run(&plan, &tree, &pv, &volume), run(&plan, &tree, &pv, &shuffled));
    }
}
