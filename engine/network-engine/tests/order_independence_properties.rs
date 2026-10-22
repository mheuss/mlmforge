//! Rank and earning results do not depend on the order volume arrives in.

use std::collections::HashMap;

use network_engine::commission::calculate_unilevel;
use network_engine::commission::types::{
    CommissionEarning, DistributorSnapshot, VolumeSource, Walk,
};
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

fn to_cents(value: AuthoredDec) -> u64 {
    let hundred: Dec = "100".parse().unwrap();
    let scaled = value.dec().try_mul(hundred).unwrap();
    scaled
        .to_string()
        .parse()
        .unwrap_or_else(|e| panic!("expected {value} to be a whole number of cents: {e}"))
}

/// The personal and group volume, in cents, that `plan().ranks[1]` requires
/// on its only structure, when nothing else gates that rank.
fn second_rank_thresholds() -> (u64, u64) {
    let rank = &plan().ranks[1];
    let gates = &rank.qualification;
    assert!(
        gates.structures.len() == 1
            && gates.required_products.is_empty()
            && gates.window.is_none()
            && gates.tenure.is_none(),
        "expected ranks[1] to have one structure and no products, window or tenure gate, got {gates:?}"
    );
    let q = &gates.structures[0];
    assert!(
        q.max_group_volume_per_leg.dec().is_zero()
            && q.max_group_volume_per_leg_fraction.dec().is_zero()
            && q.min_retail_volume.dec().is_zero()
            && q.distributor_count.is_none()
            && q.leg_quality.is_empty(),
        "expected ranks[1] to gate on personal and group volume only, got {q:?}"
    );
    let (pv, gv) = (to_cents(q.personal_volume), to_cents(q.group_volume));
    assert!(
        pv + 1 < gv,
        "expected personal volume at least 2 cents below group volume, got {pv} and {gv}"
    );
    (pv, gv)
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

/// An earning without its walk index.
type EarningKey = (Uuid, Uuid, u8, Option<Dec>, Dec, Dec);

fn earning_key(e: &CommissionEarning) -> EarningKey {
    (
        e.earner_id,
        e.source_id,
        e.level,
        e.rate,
        e.cv_amount,
        e.dollar_amount,
    )
}

/// A walk's JSON with its index removed.
fn walk_content(walk: &Walk) -> String {
    let mut value = serde_json::to_value(walk).unwrap();
    value.as_object_mut().unwrap().remove("index");
    value.to_string()
}

/// Ranks, each walk's content with the earnings that cite it, and the
/// earnings that cite no walk.
type Outcome = (
    HashMap<Uuid, EvaluatedRank>,
    Vec<(String, Vec<EarningKey>)>,
    Vec<EarningKey>,
);

fn run(
    plan: &CompensationPlan,
    tree: &UnilevelTree,
    pv: &[u64],
    volume: &[(usize, u64)],
) -> Outcome {
    let StructureConfig::Unilevel(structure) = &plan.structures[0] else {
        panic!(
            "expected plan.structures[0] to be unilevel, got {:?}",
            plan.structures[0]
        );
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
    let mut walks: Vec<(String, Vec<EarningKey>)> = result
        .walks
        .iter()
        .map(|w| {
            let mut cited: Vec<EarningKey> = result
                .earnings
                .iter()
                .filter(|e| e.walk == Some(w.index))
                .map(earning_key)
                .collect();
            cited.sort();
            (walk_content(w), cited)
        })
        .collect();
    walks.sort();
    let mut walkless: Vec<EarningKey> = result
        .earnings
        .iter()
        .filter(|e| e.walk.is_none())
        .map(earning_key)
        .collect();
    walkless.sort();
    let placed = walks.iter().map(|(_, cited)| cited.len()).sum::<usize>() + walkless.len();
    assert_eq!(
        placed,
        result.earnings.len(),
        "earnings placed under a walk or as walkless, against all earnings"
    );
    (ranks, walks, walkless)
}

type Case = (Vec<usize>, Vec<u64>, Vec<(usize, u64)>);

/// Any tree of up to 40 nodes, with any cent-valued volume, and up to two
/// entries repeated exactly.
fn any_case() -> impl Strategy<Value = Case> {
    (
        prop::collection::vec(any::<usize>(), 1..40),
        prop::collection::vec(0u64..500_000, 41),
        prop::collection::vec((any::<usize>(), 0u64..2_000_000), 1..120),
    )
        .prop_flat_map(|(parents, mut pv, volume)| {
            pv.truncate(parents.len() + 1);
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
}

/// A root whose personal volume plus its child's volume entries sum to the
/// second rank's group-volume threshold, or one cent under it.
fn threshold_case() -> impl Strategy<Value = Case> {
    let (pv_min, gv) = second_rank_thresholds();
    (
        prop_oneof![Just(gv), Just(gv - 1)],
        pv_min..gv - 1,
        0u64..500_000,
        any::<u64>(),
        2u64..4,
        prop::collection::vec(any::<u64>(), 1..8),
    )
        .prop_map(|(target, root_pv, child_pv, seed, count, cuts)| {
            let rest = target - root_pv;
            let repeated = seed % (rest / count + 1);
            let split = rest - repeated * count;
            let mut points: Vec<u64> = cuts.iter().map(|c| c % (split + 1)).collect();
            points.push(0);
            points.push(split);
            points.sort_unstable();
            let mut volume: Vec<(usize, u64)> =
                points.windows(2).map(|w| (1, w[1] - w[0])).collect();
            for _ in 0..count {
                volume.push((1, repeated));
            }
            (vec![0], vec![root_pv, child_pv], volume)
        })
}

/// A case, and the same volume shuffled.
fn ordered_and_shuffled() -> impl Strategy<Value = (Case, Vec<(usize, u64)>)> {
    prop_oneof![any_case(), threshold_case()].prop_flat_map(|case| {
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
