mod common;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, LegPredicate, LegQualityRequirement, RankDefinition, RankQualification,
    StructureQualification,
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

/// End-to-end guard: a populated `leg_quality` with two AND-combined requirements
/// — one `ContainsRank` and one `ContainsPersonalVolume` — is enforced through
/// the public `evaluate_ranks` entry point.
///
/// Tree layout (root 1 has no primitives and is not evaluated):
/// ```text
/// root(1)
/// ├── subject_a(2)  PV=50
/// │   ├── node(4)   PV=100 → evaluates to associate
/// │   └── node(5)   PV=50  → evaluates to associate
/// └── subject_b(3)  PV=50
///     ├── node(6)   PV=50  → evaluates to associate
///     └── node(7)   PV=50  → evaluates to associate
/// ```
///
/// Both subjects have PV=50 (meets silver's PV requirement) and two associate
/// legs (meets leg_quality requirement 1). Only subject A has a leg containing
/// PV >= 100 (requirement 2). Leg quality is the sole differentiator.
#[test]
fn evaluate_ranks_with_populated_leg_quality_gates_rank() {
    // Build the plan: associate (ord 1, PV >= 50) and silver (ord 2, PV >= 50)
    // with two AND-combined leg_quality requirements on silver.
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
                    personal_volume: 50.0,
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
                    personal_volume: 50.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    // Two AND-combined requirements.
                    // req 1: at least 2 legs each containing an associate node.
                    // req 2: at least 1 leg containing a node with PV >= 100.
                    leg_quality: vec![
                        LegQualityRequirement {
                            count: 2,
                            predicate: LegPredicate::ContainsRank {
                                min_rank: "associate".to_string(),
                            },
                        },
                        LegQualityRequirement {
                            count: 1,
                            predicate: LegPredicate::ContainsPersonalVolume {
                                min_personal_volume: 100.0,
                            },
                        },
                    ],
                }],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];

    // Tree: root(1) is the structural parent. It has no primitives so it is
    // not evaluated. subject_a(2) and subject_b(3) are root's children; each
    // has two children of their own.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();
    // subject_a and its two legs
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        0,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(4),
        uuid_from_index(2),
        uuid_from_index(2),
        0,
    )
    .unwrap(); // PV=100 leg
    tree.add_node(
        uuid_from_index(5),
        uuid_from_index(2),
        uuid_from_index(2),
        0,
    )
    .unwrap(); // PV=50 leg
    // subject_b and its two legs
    tree.add_node(
        uuid_from_index(3),
        uuid_from_index(1),
        uuid_from_index(1),
        0,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(6),
        uuid_from_index(3),
        uuid_from_index(3),
        0,
    )
    .unwrap(); // PV=50 leg
    tree.add_node(
        uuid_from_index(7),
        uuid_from_index(3),
        uuid_from_index(3),
        0,
    )
    .unwrap(); // PV=50 leg

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    // subject_a(2): PV=50. Its children: node 4 (PV=100), node 5 (PV=50).
    // subject_b(3): PV=50. Its children: node 6 (PV=50), node 7 (PV=50).
    // Root(1) has no primitives — not evaluated.
    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(2), primitives(50.0));
    distributors.insert(uuid_from_index(3), primitives(50.0));
    distributors.insert(uuid_from_index(4), primitives(100.0));
    distributors.insert(uuid_from_index(5), primitives(50.0));
    distributors.insert(uuid_from_index(6), primitives(50.0));
    distributors.insert(uuid_from_index(7), primitives(50.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    // subject_a: both reqs satisfied (2 associate legs + 1 leg with PV >= 100).
    // evaluate_ranks evaluates deepest-first, so nodes 4-7 are ranked as
    // associate before subjects 2 and 3 are evaluated. ContainsRank reads
    // those results from the `already` map — exactly the interaction this test
    // exercises end-to-end.
    assert_eq!(
        result.ranks.get(&uuid_from_index(2)),
        Some(&EvaluatedRank::Qualified {
            rank: "silver".to_string(),
            ordinal: 2,
        }),
        "subject_a should reach silver: 2 associate legs and 1 leg with PV >= 100"
    );

    // subject_b: req 1 passes (2 associate legs) but req 2 fails (no leg has
    // PV >= 100). Both subjects have identical PV=50; leg structure is the
    // only differentiator.
    assert_eq!(
        result.ranks.get(&uuid_from_index(3)),
        Some(&EvaluatedRank::Qualified {
            rank: "associate".to_string(),
            ordinal: 1,
        }),
        "subject_b should stay at associate: no leg contains PV >= 100"
    );
}

/// BR5 regression guard: a plan whose qualifications carry an empty
/// `leg_quality` evaluates identically to the pre-feature behaviour.
///
/// Absent `leg_quality` deserializes to an empty Vec via `#[serde(default)]`
/// — proven by `structure_qualification_leg_quality_defaults_to_empty_when_absent`
/// in config/rank.rs — so empty, absent, and pre-feature configs all evaluate
/// identically.
#[test]
fn evaluate_ranks_with_empty_leg_quality_matches_baseline() {
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
    // Guard: linear_plan's qualifications carry an empty leg_quality.
    for rank in &plan.ranks {
        for sq in &rank.qualification.structures {
            assert!(sq.leg_quality.is_empty());
        }
    }

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

    // Identical to the pre-feature baseline: distributor 1 reaches silver,
    // distributor 2 stays at associate.
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
