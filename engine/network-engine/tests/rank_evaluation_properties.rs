mod common;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankQualification, StructureQualification,
};
use network_engine::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
use network_engine::rank::{DistributorPrimitives, EvaluationInputs, evaluate_ranks};
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::HashMap;

fn build_random_plan() -> CompensationPlan {
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
                max_depth: 5,
                rate_table: Default::default(),
            },
            compression: None,
            pass_up: None,
        }),
        "Test",
    );
    plan.ranks = vec![
        RankDefinition {
            name: "associate".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Test".to_string(),
                    personal_volume: 0.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                }],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
        RankDefinition {
            name: "silver".to_string(),
            ordinal: 2,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Test".to_string(),
                    personal_volume: 100.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                }],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];
    plan
}

proptest! {
    #[test]
    fn evaluate_ranks_is_deterministic(
        size in 2..15usize,
        seed_pv in 0u32..500,
    ) {
        // Build a chain of `size` distributors, each with PV derived from seed_pv.
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64).unwrap();
        }

        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        let mut distributors = HashMap::new();
        for i in 0..size {
            distributors.insert(uuid_from_index(i), DistributorPrimitives {
                personal_volume: (seed_pv as f64) + (i as f64),
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            });
        }

        let inputs = EvaluationInputs { distributors, volume_sources: vec![] };
        let plan = build_random_plan();

        let r1 = evaluate_ranks(&plan, &nav, &inputs).unwrap();
        let r2 = evaluate_ranks(&plan, &nav, &inputs).unwrap();

        let json1 = serde_json::to_string(&r1).unwrap();
        let json2 = serde_json::to_string(&r2).unwrap();
        prop_assert_eq!(json1, json2);
    }
}

proptest! {
    #[test]
    fn increasing_pv_cannot_lower_rank(
        size in 2..10usize,
        base_pv in 0u32..500,
        increase in 1u32..200,
    ) {
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64).unwrap();
        }
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        let plan = build_random_plan();

        let mut distributors_low = HashMap::new();
        let mut distributors_high = HashMap::new();
        for i in 0..size {
            distributors_low.insert(uuid_from_index(i), DistributorPrimitives {
                personal_volume: base_pv as f64,
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            });
            distributors_high.insert(uuid_from_index(i), DistributorPrimitives {
                personal_volume: (base_pv + increase) as f64,
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            });
        }

        let r_low = evaluate_ranks(
            &plan, &nav,
            &EvaluationInputs { distributors: distributors_low, volume_sources: vec![] },
        ).unwrap();
        let r_high = evaluate_ranks(
            &plan, &nav,
            &EvaluationInputs { distributors: distributors_high, volume_sources: vec![] },
        ).unwrap();

        for i in 0..size {
            let id = uuid_from_index(i);
            let low_ord = match r_low.ranks.get(&id) {
                Some(network_engine::rank::EvaluatedRank::Qualified { ordinal, .. }) => *ordinal as i32,
                _ => -1,
            };
            let high_ord = match r_high.ranks.get(&id) {
                Some(network_engine::rank::EvaluatedRank::Qualified { ordinal, .. }) => *ordinal as i32,
                _ => -1,
            };
            prop_assert!(high_ord >= low_ord, "increasing PV must not lower rank");
        }
    }
}
