mod common;

use std::collections::HashMap;
use std::time::Instant;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankQualification, StructureQualification,
};
use network_engine::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
use network_engine::rank::{DistributorPrimitives, EvaluationInputs, evaluate_ranks};
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;

fn build_seven_rank_plan() -> CompensationPlan {
    let mut plan = network_engine::test_support::build_test_plan(
        network_engine::config::eligibility::CommissionEligibility {
            minimum_pv: 0.0,
            require_order_in_period: false,
            eligible_statuses: vec![],
            active_leg_tiers: vec![],
        },
        StructureConfig::Unilevel(UnilevelStructureConfig {
            name: "Test".to_string(),
            level_commission: network_engine::config::commission::LevelCommissionConfig {
                broad_commission_percent: 0.4,
                volume_to_dollar_multiplier: None,
                max_depth: 7,
                rate_table: Default::default(),
            },
            compression: None,
            pass_up: None,
        }),
        "Test",
    );
    let names = [
        "associate",
        "bronze",
        "silver",
        "gold",
        "platinum",
        "diamond",
        "blue_diamond",
    ];
    plan.ranks = names
        .iter()
        .enumerate()
        .map(|(i, name)| RankDefinition {
            name: (*name).to_string(),
            ordinal: (i as u16) + 1,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Test".to_string(),
                    personal_volume: (i as f64) * 50.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        })
        .collect();
    plan
}

#[test]
fn evaluate_ranks_completes_in_reasonable_time_on_10k_distributors() {
    const N: usize = 10_000;

    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    for i in 1..N {
        // Wide-but-shallow shape — every node parents under user 0 except
        // the first 100 which form a chain. Keeps the test tractable.
        let parent = if i < 100 { i - 1 } else { i % 100 };
        tree.add_node(
            uuid_from_index(i),
            uuid_from_index(parent),
            uuid_from_index(parent),
            i as i64,
        )
        .unwrap();
    }

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    for i in 0..N {
        distributors.insert(
            uuid_from_index(i),
            DistributorPrimitives {
                personal_volume: (i % 500) as f64,
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            },
        );
    }

    let plan = build_seven_rank_plan();
    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    let started = Instant::now();
    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();
    let elapsed = started.elapsed();

    assert_eq!(result.ranks.len(), N);
    assert!(
        elapsed.as_secs_f64() < 5.0,
        "evaluation of {} distributors took {:?}; budget is 5s in debug",
        N,
        elapsed,
    );
}
