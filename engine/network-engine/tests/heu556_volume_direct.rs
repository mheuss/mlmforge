//! HEU-556 volume spike. Throwaway.
//!
//! Same fixture as `heu556_volume`, serialized straight to a `String` with no
//! `serde_json::Value` intermediate. Separate test binary so the peak RSS
//! high-water mark is not inherited from the other run.
#![cfg(feature = "spike_heu556_volume")]

use std::collections::{BTreeMap, HashMap};

use network_engine::commission::calculate_generation;
use network_engine::commission::types::VolumeSource;
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::generation::{GenerationBoundaryMode, GenerationCommissionConfig};
use network_engine::config::{GenerationStructureConfig, StructureConfig};
use network_engine::test_support::{
    build_test_plan, make_rank, snapshot_with_rank, test_plan_identity, uuid_from_index,
};
use network_engine::tree::unilevel::UnilevelTree;

const TOTAL_NODES: usize = 100_000;
const SPINE_DEPTH: usize = 200;
const SOURCE_COUNT: usize = 1_000;
const RANK_COUNT: usize = 10;

/// Peak resident set size in bytes, from `VmHWM`.
///
/// High-water mark, so it survives the drop of whatever produced it.
fn peak_rss_bytes() -> u64 {
    let status = std::fs::read_to_string("/proc/self/status").expect("procfs status");
    for line in status.lines() {
        if let Some(rest) = line.strip_prefix("VmHWM:") {
            let kb: u64 = rest
                .trim()
                .trim_end_matches(" kB")
                .trim()
                .parse()
                .expect("VmHWM is a number");
            return kb * 1024;
        }
    }
    panic!("VmHWM absent from /proc/self/status");
}

fn rank_name(ordinal: usize) -> String {
    format!("rank{ordinal:02}")
}

#[test]
fn deep_sparse_generation_same_rank_volume_direct() {
    let rss_before = peak_rss_bytes();

    // Spine of SPINE_DEPTH nodes under the root, then SOURCE_COUNT leaves on
    // the deepest spine node, then padding on the root to reach TOTAL_NODES.
    // A source's upline is therefore the whole spine plus the root.
    let mut tree = UnilevelTree::new();
    let root = uuid_from_index(0);
    tree.add_root(root, 0).expect("root");

    let mut parent = root;
    for i in 1..SPINE_DEPTH {
        let id = uuid_from_index(i);
        tree.add_node(id, parent, parent, i as i64).expect("spine");
        parent = id;
    }
    let spine_tip = parent;

    let source_base = SPINE_DEPTH;
    for i in 0..SOURCE_COUNT {
        let id = uuid_from_index(source_base + i);
        tree.add_node(id, spine_tip, spine_tip, i as i64)
            .expect("source");
    }

    let pad_base = source_base + SOURCE_COUNT;
    for i in pad_base..TOTAL_NODES {
        let id = uuid_from_index(i);
        tree.add_node(id, root, root, i as i64).expect("pad");
    }

    // Sparse boundaries. Everyone is rank01 except nine spine nodes near the
    // root carrying rank02 through rank10. For a SameRank pass at ordinal k
    // above 1, the boundary set is the (11 - k) nodes at or above k, all near
    // the root, so the walk runs the full chain without the generation
    // counter reaching its limit.
    let mut snapshots = HashMap::with_capacity(TOTAL_NODES);
    for i in 0..TOTAL_NODES {
        snapshots.insert(uuid_from_index(i), snapshot_with_rank(&rank_name(1)));
    }
    for k in 2..=RANK_COUNT {
        snapshots.insert(uuid_from_index(k - 1), snapshot_with_rank(&rank_name(k)));
    }

    let gen_config = GenerationCommissionConfig {
        max_generations: 20,
        max_generations_per_rank: BTreeMap::new(),
        rates: (1u8..=20).map(|g| (g, 0.05)).collect(),
        boundary_mode: GenerationBoundaryMode::SameRank,
        boundary_rank: rank_name(2),
        empty_generation_consumes_number: false,
        volume_to_dollar_multiplier: None,
        ineligible_creates_boundary: true,
    };

    let structure = GenerationStructureConfig {
        name: "spike".to_string(),
        level_commission: None,
        compression: None,
        generation_commission: gen_config,
        level_commissions_enabled: false,
    };

    let eligibility = CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec!["active".to_string()],
        active_leg_tiers: vec![],
    };

    let mut plan = build_test_plan(
        eligibility,
        StructureConfig::Generation(structure.clone()),
        "spike",
    );
    plan.ranks = (1..=RANK_COUNT)
        .map(|k| make_rank(&rank_name(k), k as u16, vec!["spike".to_string()]))
        .collect();

    let volume: Vec<VolumeSource> = (0..SOURCE_COUNT)
        .map(|i| VolumeSource {
            source_id: uuid_from_index(source_base + i),
            cv_amount: 100.0,
        })
        .collect();

    let rss_fixture = peak_rss_bytes();

    let result = calculate_generation(
        &tree,
        &plan,
        &structure,
        &snapshots,
        &volume,
        &test_plan_identity(),
    )
    .expect("calculation");

    let walk_count = result.walks.len();
    let step_count: usize = result.walks.iter().map(|w| w.steps.len()).sum();
    let earning_count = result.earnings.len();
    let rss_after_calc = peak_rss_bytes();

    // No Value intermediate. Straight from the typed result to a String.
    let json = serde_json::to_string(&result).expect("to_string");
    let wire_bytes = json.len();
    let rss_peak = peak_rss_bytes();

    assert!(!json.is_empty());

    const MIB: f64 = 1024.0 * 1024.0;
    const HARD_CEILING_MIB: f64 = 64.0;
    const WARN_CEILING_MIB: f64 = 16.0;

    let wire_mib = wire_bytes as f64 / MIB;

    println!("--- HEU-556 deep-sparse, direct to_string, no Value ---");
    println!("nodes                {TOTAL_NODES}");
    println!("spine depth          {SPINE_DEPTH}");
    println!("sources              {SOURCE_COUNT}");
    println!("distinct ranks       {RANK_COUNT}");
    println!("walks                {walk_count}");
    println!("steps                {step_count}");
    println!("earnings             {earning_count}");
    println!(
        "bytes per step       {:.1}",
        wire_bytes as f64 / step_count as f64
    );
    println!("wire bytes           {wire_bytes} ({wire_mib:.1} MiB)");
    println!(
        "wire vs 64 MiB       {:.2}x",
        wire_mib / HARD_CEILING_MIB
    );
    println!("wire vs 16 MiB warn  {:.2}x", wire_mib / WARN_CEILING_MIB);
    println!(
        "peak RSS baseline    {:.1} MiB",
        rss_before as f64 / MIB
    );
    println!(
        "peak RSS post-fixture {:.1} MiB",
        rss_fixture as f64 / MIB
    );
    println!(
        "peak RSS post-calc   {:.1} MiB",
        rss_after_calc as f64 / MIB
    );
    println!("peak RSS final       {:.1} MiB", rss_peak as f64 / MIB);
    println!(
        "peak RSS attributable to response {:.1} MiB",
        (rss_peak.saturating_sub(rss_fixture)) as f64 / MIB
    );
}
