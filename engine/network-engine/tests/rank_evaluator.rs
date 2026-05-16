mod common;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankQualification, StructureQualification,
};
use network_engine::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
use network_engine::rank::{
    DistributorPrimitives, EvaluatedRank, EvaluationError, EvaluationInputs, evaluate_ranks,
};
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;
use std::collections::HashMap;

fn primitives(pv: f64) -> DistributorPrimitives {
    DistributorPrimitives {
        personal_volume: pv,
        retail_volume: 0.0,
        status: "active".to_string(),
        has_order_in_period: true,
        active_products: vec![],
    }
}

fn linear_plan() -> CompensationPlan {
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
                broad_commission_percent: 0.40,
                volume_to_dollar_multiplier: None,
                max_depth: 3,
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
                    leg_quality: vec![],
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
                    leg_quality: vec![],
                }],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];
    plan
}

#[test]
fn evaluate_ranks_returns_per_distributor_rank() {
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        0,
    )
    .unwrap();

    let plan = linear_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(250.0));
    distributors.insert(uuid_from_index(2), primitives(50.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    assert_eq!(
        result.ranks.get(&uuid_from_index(1)),
        Some(&EvaluatedRank::Qualified {
            rank: "silver".to_string(),
            ordinal: 2
        })
    );
    assert_eq!(
        result.ranks.get(&uuid_from_index(2)),
        Some(&EvaluatedRank::Qualified {
            rank: "associate".to_string(),
            ordinal: 1
        })
    );
}

#[test]
fn evaluate_ranks_returns_unranked_for_distributor_below_lowest_tier() {
    // Reproduces business requirement #3.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();

    let mut plan = linear_plan();
    // Make the lowest rank require PV > 0.
    plan.ranks[0].qualification.structures[0].personal_volume = 50.0;

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();
    assert_eq!(
        result.ranks.get(&uuid_from_index(1)),
        Some(&EvaluatedRank::Unranked)
    );
}

#[test]
fn evaluate_ranks_omitting_primitives_treats_distributor_as_unranked() {
    // Reproduces design risk #3 mitigation.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        0,
    )
    .unwrap();

    let plan = linear_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    // Only distributor 1 has primitives.
    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(250.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();
    // Distributor 2 must NOT appear in the result — they have no primitives.
    assert!(!result.ranks.contains_key(&uuid_from_index(2)));
}

#[test]
fn evaluate_ranks_errors_on_unknown_structure() {
    // Plan ranks reference structure "Test", but the only tree is registered
    // under "Other". The user is in "Other" so they enter the evaluation
    // order, which forces satisfies() to look up "Test" and surface
    // UnknownStructure.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();

    let plan = linear_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Other".to_string(), &tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let err = evaluate_ranks(&plan, &nav, &inputs).unwrap_err();
    assert_eq!(
        err,
        EvaluationError::UnknownStructure {
            rank: "associate".to_string(),
            structure: "Test".to_string(),
        }
    );
}

#[test]
fn evaluate_ranks_errors_on_distributor_not_in_referenced_tree() {
    // Rank references structure "Test"; distributor is in a different
    // structure "Other" so they're in the evaluation order, but
    // satisfies() reports them as not-in-tree for "Test".
    let test_tree = UnilevelTree::new();
    let mut other_tree = UnilevelTree::new();
    other_tree.add_root(uuid_from_index(1), 0).unwrap();

    let plan = linear_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &test_tree);
    nav.insert("Other".to_string(), &other_tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let err = evaluate_ranks(&plan, &nav, &inputs).unwrap_err();
    assert_eq!(
        err,
        EvaluationError::DistributorNotInTree(uuid_from_index(1), "Test".to_string(),)
    );
}

#[test]
fn evaluate_ranks_errors_on_unknown_min_rank() {
    use network_engine::config::rank::{DistributorCountRequirement, SearchMode};

    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();

    // Build a plan whose silver rank carries a DistributorCountRequirement
    // referencing a min_rank that isn't in the rank ladder.
    let mut plan = linear_plan();
    plan.ranks[1].qualification.structures[0].distributor_count =
        Some(DistributorCountRequirement {
            count: 1,
            min_rank: "phantom_rank".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 0,
            min_leg_group_volume: 0.0,
        });

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    // PV high enough that satisfies() reaches the distributor_count check
    // for silver. associate has no distributor_count, so it passes first.
    distributors.insert(uuid_from_index(1), primitives(250.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let err = evaluate_ranks(&plan, &nav, &inputs).unwrap_err();
    assert_eq!(
        err,
        EvaluationError::UnknownMinRank {
            rank: "silver".to_string(),
            referenced: "phantom_rank".to_string(),
        }
    );
}
