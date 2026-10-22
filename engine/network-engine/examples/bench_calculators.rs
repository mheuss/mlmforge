//! Times rank evaluation and the unilevel, binary and stairstep calculators on
//! generated trees.
//!
//! Run: `cargo run --release -p network-engine --example bench_calculators -- 100000`

use std::collections::HashMap;
use std::hint::black_box;
use std::time::Instant;

use network_engine::commission::types::{DistributorSnapshot, LegVolumes, VolumeSource};
use network_engine::commission::{
    calculate_binary_pairing, calculate_stairstep, calculate_unilevel,
};
use network_engine::config::{CompensationPlan, StructureConfig};
use network_engine::rank::{EvaluationInputs, evaluate_ranks};
use network_engine::test_support::{test_plan_identity, uuid_from_index};
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;
use serde_json::{Value, json};

const TESTDATA: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata");

/// A small deterministic generator for tree shape and volume.
struct Lcg(u64);

impl Lcg {
    fn below(&mut self, n: u64) -> u64 {
        self.0 = self
            .0
            .wrapping_mul(6_364_136_223_846_793_005)
            .wrapping_add(1_442_695_040_888_963_407);
        (self.0 >> 33) % n
    }
}

fn money(cents: u64) -> Value {
    json!(cents as f64 / 100.0)
}

fn read_json(path: &str) -> Value {
    let text = std::fs::read_to_string(format!("{TESTDATA}/{path}")).expect("fixture exists");
    serde_json::from_str(&text).expect("fixture is JSON")
}

fn plan_from_contract(path: &str) -> Value {
    let fixture = read_json(path);
    let setup = fixture["setup_raw"].as_array().expect("setup_raw");
    setup
        .iter()
        .map(|line| serde_json::from_str::<Value>(line.as_str().unwrap()).unwrap())
        .find(|request| request["op"] == "load_plan")
        .expect("a load_plan setup line")["params"]
        .clone()
}

fn parse<T: serde::de::DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).expect("bench input deserializes")
}

fn timed<R>(label: &str, n: usize, iters: usize, mut run: impl FnMut() -> R) -> R {
    let mut samples = Vec::with_capacity(iters);
    let mut last = None;
    for _ in 0..iters {
        let start = Instant::now();
        let out = run();
        samples.push(start.elapsed().as_secs_f64() * 1e3);
        last = Some(black_box(out));
    }
    samples.sort_by(|a, b| a.total_cmp(b));
    println!(
        "n={n} {label}: median {:.2} ms, min {:.2} ms ({iters} runs)",
        samples[samples.len() / 2],
        samples[0]
    );
    last.expect("at least one run")
}

fn cents_per_node(n: usize, rng: &mut Lcg) -> Vec<u64> {
    (0..n).map(|_| rng.below(20_001)).collect()
}

fn volume_json(cents: &[u64]) -> Value {
    Value::Array(
        cents
            .iter()
            .enumerate()
            .map(|(i, c)| json!({"source_id": uuid_from_index(i), "cv_amount": money(*c)}))
            .collect(),
    )
}

fn snapshots_json(cents: &[u64], rank: impl Fn(usize) -> String) -> Value {
    let mut map = serde_json::Map::new();
    for (i, c) in cents.iter().enumerate() {
        map.insert(
            uuid_from_index(i).to_string(),
            json!({"rank": rank(i), "personal_volume": money(*c), "status": "active", "has_order_in_period": true}),
        );
    }
    Value::Object(map)
}

fn unilevel_tree(n: usize, rng: &mut Lcg) -> UnilevelTree {
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for i in 1..n {
        let parent = uuid_from_index(rng.below(i as u64) as usize);
        tree.add_node(uuid_from_index(i), parent, parent, i as i64)
            .unwrap();
    }
    tree
}

fn binary_tree(n: usize) -> BinaryTree {
    let mut tree = BinaryTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for i in 1..n {
        let parent = uuid_from_index((i - 1) / 2);
        tree.add_node(uuid_from_index(i), parent, (i - 1) % 2, parent, i as i64)
            .unwrap();
    }
    tree
}

fn rank_and_unilevel(n: usize, iters: usize) {
    let mut plan_json = read_json("config_contract/fixtures/unilevel.json");
    plan_json["ranks"][1]["qualification"]["structures"][0]["max_group_volume_per_leg_fraction"] =
        json!(0.6);
    let plan: CompensationPlan = parse(plan_json);
    let StructureConfig::Unilevel(structure) = &plan.structures[0] else {
        panic!("the unilevel fixture's first structure is unilevel");
    };
    let mut rng = Lcg(969);
    let tree = unilevel_tree(n, &mut rng);
    let cents = cents_per_node(n, &mut rng);

    let mut distributors = serde_json::Map::new();
    for (i, c) in cents.iter().enumerate() {
        distributors.insert(
            uuid_from_index(i).to_string(),
            json!({"personal_volume": money(*c), "retail_volume": money(*c / 4), "status": "active", "has_order_in_period": true, "active_products": ["starter-pack"]}),
        );
    }
    let inputs: EvaluationInputs = parse(json!({
        "distributors": distributors,
        "volume_sources": volume_json(&cents),
    }));
    let mut trees: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    trees.insert(structure.name.clone(), &tree);
    let ranks = timed("evaluate_ranks", n, iters, || {
        evaluate_ranks(&plan, &trees, &inputs).unwrap()
    });
    let mut qualified = std::collections::BTreeMap::<String, usize>::new();
    for evaluated in ranks.ranks.values() {
        if let network_engine::rank::EvaluatedRank::Qualified { rank, .. } = evaluated {
            *qualified.entry(rank.clone()).or_default() += 1;
        }
    }
    println!("n={n} evaluate_ranks qualified: {qualified:?}");

    let lowest = plan.ranks[0].name.clone();
    let snapshots: HashMap<_, DistributorSnapshot> = parse(snapshots_json(&cents, |i| match ranks
        .ranks
        .get(&uuid_from_index(i))
    {
        Some(network_engine::rank::EvaluatedRank::Qualified { rank, .. }) => rank.clone(),
        _ => lowest.clone(),
    }));
    let volume: Vec<VolumeSource> = parse(volume_json(&cents));
    let identity = test_plan_identity();
    let result = timed("calculate_unilevel", n, iters, || {
        calculate_unilevel(&tree, &plan, structure, &snapshots, &volume, &identity).unwrap()
    });
    println!("n={n} unilevel earnings: {}", result.earnings.len());
}

fn binary(n: usize, iters: usize, mode: Value, owned_in_threes: bool, label: &str) {
    let mut plan_json = plan_from_contract("contracts/calculate_binary_pairing.json");
    plan_json["structures"][0]["config"]["binary_commission"]["mode"] = mode;
    let plan: CompensationPlan = parse(plan_json);
    let StructureConfig::Binary(structure) = &plan.structures[0] else {
        panic!("the binary contract plan's first structure is binary");
    };
    let mut rng = Lcg(970);
    let tree = binary_tree(n);
    let cents = cents_per_node(n, &mut rng);
    let rank = plan.ranks[0].name.clone();
    let snapshots: HashMap<_, DistributorSnapshot> =
        parse(snapshots_json(&cents, |_| rank.clone()));
    let volume: Vec<VolumeSource> = parse(volume_json(&cents));
    let carry: HashMap<_, LegVolumes> = HashMap::new();
    let ownership: HashMap<_, _> = (0..n)
        .map(|i| (uuid_from_index(i), uuid_from_index(i - i % 3)))
        .collect();
    let ownership = owned_in_threes.then_some(&ownership);
    let result = timed(label, n, iters, || {
        calculate_binary_pairing(
            &tree, &plan, structure, &snapshots, &volume, &carry, ownership,
        )
        .unwrap()
    });
    println!("n={n} {label} earnings: {}", result.earnings.len());
}

fn stairstep(n: usize, iters: usize) {
    let plan: CompensationPlan = parse(read_json("config_contract/fixtures/stairstep.json"));
    let StructureConfig::Stairstep(structure) = &plan.structures[0] else {
        panic!("the stairstep fixture's first structure is stairstep");
    };
    let mut rng = Lcg(971);
    let tree = unilevel_tree(n, &mut rng);
    let cents = cents_per_node(n, &mut rng);
    let ranks: Vec<String> = plan.ranks.iter().map(|r| r.name.clone()).collect();
    let snapshots: HashMap<_, DistributorSnapshot> =
        parse(snapshots_json(&cents, |i| ranks[i % ranks.len()].clone()));
    let volume: Vec<VolumeSource> = parse(volume_json(&cents));
    let identity = test_plan_identity();
    let result = timed("calculate_stairstep", n, iters, || {
        calculate_stairstep(&tree, &plan, structure, &snapshots, &volume, &identity).unwrap()
    });
    println!("n={n} stairstep earnings: {}", result.earnings.len());
}

fn main() {
    let sizes: Vec<usize> = std::env::args()
        .skip(1)
        .map(|s| s.parse().expect("a node count"))
        .collect();
    let sizes = if sizes.is_empty() {
        vec![100_000]
    } else {
        sizes
    };
    for n in sizes {
        let iters = if n <= 10_000 { 51 } else { 21 };
        rank_and_unilevel(n, iters);
        binary(
            n,
            iters,
            json!({"pairing": {"percent": 0.1, "calculation": "volume_ratio", "cap_per_period": 50.0, "volume_after_payout": "carry_forward", "carry_forward_cap": null}}),
            false,
            "calculate_binary_pairing (volume ratio, cap)",
        );
        binary(
            n,
            iters,
            json!({"pairing": {"percent": 0.1, "calculation": "weaker_leg", "cap_per_period": 50.0, "volume_after_payout": "carry_forward", "carry_forward_cap": null, "multi_position_cap_mode": "aggregate"}}),
            true,
            "calculate_binary_pairing (aggregate cap)",
        );
        binary(
            n,
            iters,
            json!({"cycle_step": {"steps": [{"threshold": 500.0, "amount": 40.0}, {"threshold": 1000.0, "amount": 60.0}], "volume_after_cycle": "carry_forward", "cap_per_period": 500.0}}),
            false,
            "calculate_binary_pairing (cycle step, cap)",
        );
        stairstep(n, iters);
    }
}
