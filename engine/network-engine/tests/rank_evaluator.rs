mod common;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, LegPredicate, LegQualityRequirement, RankDefinition, RankQualification,
    RankQualificationWindow, StructureQualification,
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
                window: None,
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
                window: None,
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
        history_window: Vec::new(),
        history: HashMap::new(),
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
                window: None,
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
                window: None,
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
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    // subject_a: both reqs satisfied (2 associate legs + 1 leg with PV >= 100).
    // evaluate_ranks iterates to a fixpoint, so nodes 4-7 are ranked
    // as associate and counted toward subjects 2 and 3 regardless of
    // evaluation order. ContainsRank reads those results from the
    // `already` map — exactly the interaction this test exercises
    // end-to-end.
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
        history_window: Vec::new(),
        history: HashMap::new(),
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

/// HEU-460 regression. `subject` is the root of the `uni` tree (depth 0) but
/// sits at depth 2 in the `bin` tree. The max-depth heuristic therefore orders
/// `subject` (effective depth 2) before its own `uni` child `child` (depth 1).
/// The old single-pass evaluator evaluated `subject` while `child` was still
/// absent from `already`, so `subject`'s distributor_count came back zero and
/// `subject` was undercounted to `associate`. The fixpoint loop re-evaluates
/// until stable, so `subject` reaches `manager`.
///
/// uni:  subject(0) -> child(1)
/// bin:  f1(0) -> f2(1) -> subject(2)        f1, f2 exist only to deepen bin
#[test]
fn evaluate_ranks_counts_descendant_across_trees() {
    use network_engine::config::rank::{DistributorCountRequirement, SearchMode};

    let subject = uuid_from_index(1);
    let child = uuid_from_index(2);
    let f1 = uuid_from_index(10);
    let f2 = uuid_from_index(11);

    let mut uni = UnilevelTree::new();
    uni.add_root(subject, 0).unwrap();
    uni.add_node(child, subject, subject, 0).unwrap();

    let mut bin = UnilevelTree::new();
    bin.add_root(f1, 0).unwrap();
    bin.add_node(f2, f1, f1, 0).unwrap();
    bin.add_node(subject, f2, f2, 0).unwrap();

    // The rank ladder qualifies only on "uni". The "bin" tree is registered in
    // `nav` to model a hybrid plan: it carries no qualification of its own
    // here, and its only effect is that subject's depth in it perturbs the
    // global evaluation order. That perturbation is the HEU-460 condition.
    let mut plan = linear_plan();
    plan.ranks[0].qualification.structures[0].structure = "uni".to_string();
    plan.ranks[0].qualified_structures = vec!["uni".to_string()];
    plan.ranks[0].name = "associate".to_string();
    plan.ranks[1].qualification.structures[0].structure = "uni".to_string();
    plan.ranks[1].qualified_structures = vec!["uni".to_string()];
    plan.ranks[1].name = "manager".to_string();
    // associate stays trivial (PV >= 0). manager additionally needs one
    // downline distributor of associate-or-better.
    plan.ranks[1].qualification.structures[0].personal_volume = 0.0;
    plan.ranks[1].qualification.structures[0].distributor_count =
        Some(DistributorCountRequirement {
            count: 1,
            min_rank: "associate".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 1,
            min_leg_group_volume: 0.0,
        });

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("uni".to_string(), &uni);
    nav.insert("bin".to_string(), &bin);

    let mut distributors = HashMap::new();
    distributors.insert(subject, primitives(0.0));
    distributors.insert(child, primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    // The fix: subject reaches manager because child is counted.
    assert_eq!(
        result.ranks.get(&subject),
        Some(&EvaluatedRank::Qualified {
            rank: "manager".to_string(),
            ordinal: 2,
        }),
        "subject must reach manager: its uni child counts toward distributor_count"
    );
    // child has an empty uni downline, so it cannot reach manager.
    assert_eq!(
        result.ranks.get(&child),
        Some(&EvaluatedRank::Qualified {
            rank: "associate".to_string(),
            ordinal: 1,
        })
    );
}

/// HEU-460 cycle case. `a` and `b` have inverted ancestry across trees:
/// `a` is above `b` in `uni`, `b` is above `a` in `bin`. `manager_u` qualifies
/// on a uni distributor_count and `manager_b` on a bin distributor_count, so
/// `a`'s rank depends on `b`'s and `b`'s depends on `a`'s. No single order
/// resolves that. The fixpoint loop converges to a stable, order-independent result.
///
/// uni:  a(0) -> b(1)
/// bin:  b(0) -> a(1)
#[test]
fn evaluate_ranks_converges_on_cyclic_ancestry() {
    use network_engine::config::rank::{DistributorCountRequirement, SearchMode};

    let a = uuid_from_index(1);
    let b = uuid_from_index(2);

    let mut uni = UnilevelTree::new();
    uni.add_root(a, 0).unwrap();
    uni.add_node(b, a, a, 0).unwrap();

    let mut bin = UnilevelTree::new();
    bin.add_root(b, 0).unwrap();
    bin.add_node(a, b, b, 0).unwrap();

    // A count requirement reused for both manager ranks. count: 1 of
    // associate-or-better, total_count: 1, no group-volume floor.
    let count_req = || DistributorCountRequirement {
        count: 1,
        min_rank: "associate".to_string(),
        search_mode: SearchMode::AnyLevel,
        search_depth: None,
        total_count: 1,
        min_leg_group_volume: 0.0,
    };
    let structure_qual =
        |structure: &str, dc: Option<DistributorCountRequirement>| StructureQualification {
            structure: structure.to_string(),
            personal_volume: 0.0,
            group_volume: 0.0,
            max_group_volume_per_leg: f64::MAX,
            min_retail_volume: 0.0,
            distributor_count: dc,
            leg_quality: vec![],
        };
    let rank =
        |name: &str, ordinal: u16, structure: &str, dc: Option<DistributorCountRequirement>| {
            RankDefinition {
                name: name.to_string(),
                ordinal,
                qualification: RankQualification {
                    structures: vec![structure_qual(structure, dc)],
                    required_products: vec![],
                    window: None,
                },
                qualified_structures: vec![structure.to_string()],
                demotion_policy: DemotionPolicy::PromotionOnly,
            }
        };

    let mut plan = linear_plan();
    plan.ranks = vec![
        rank("associate", 1, "uni", None),
        rank("manager_u", 2, "uni", Some(count_req())),
        rank("manager_b", 3, "bin", Some(count_req())),
    ];

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("uni".to_string(), &uni);
    nav.insert("bin".to_string(), &bin);

    let mut distributors = HashMap::new();
    distributors.insert(a, primitives(0.0));
    distributors.insert(b, primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    // Must not hang and must not error: the cycle is legitimate input.
    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    // `a` reaches manager_u (its uni child `b` is counted); `b` reaches
    // manager_b (its bin child `a` is counted). The single-pass evaluator
    // undercounted whichever of the two was evaluated first.
    assert_eq!(
        result.ranks.get(&a),
        Some(&EvaluatedRank::Qualified {
            rank: "manager_u".to_string(),
            ordinal: 2,
        })
    );
    assert_eq!(
        result.ranks.get(&b),
        Some(&EvaluatedRank::Qualified {
            rank: "manager_b".to_string(),
            ordinal: 3,
        })
    );
}

/// Recursive senior-title ladder: `director`'s `leg_quality` references
/// `manager`, and `president`'s references `director` — a rank whose own
/// qualification is itself leg-structural. This is the recursive case in
/// HEU-427's original title, and the reason rank evaluation iterates to a
/// fixpoint (design-rationale 026): managers must be ranked before directors
/// can be, and directors before presidents.
///
/// Every node has PV = 50, which clears each rank's PV floor, so leg
/// structure is the only differentiator.
///
/// ```text
/// president(1)
/// ├── director(2) ── manager(4), manager(5)
/// └── director(3) ── manager(6), manager(7)
/// ```
///
/// Node 1 reaches `president` only because its two legs each contain a
/// `director`; nodes 2 and 3 reach `director` only because their two legs
/// each contain a `manager`. The leaf managers stay at `manager`, which also
/// proves `leg_quality` gates the senior ranks: were it ignored, every PV-50
/// node would climb the whole ladder.
#[test]
fn evaluate_ranks_resolves_recursive_leg_quality_ladder() {
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

    // Each rank requires PV >= 50 plus the given leg-quality rules.
    let rank = |name: &str, ordinal: u16, leg_quality: Vec<LegQualityRequirement>| RankDefinition {
        name: name.to_string(),
        ordinal,
        qualification: RankQualification {
            structures: vec![StructureQualification {
                structure: "Test".to_string(),
                personal_volume: 50.0,
                group_volume: 0.0,
                max_group_volume_per_leg: f64::MAX,
                min_retail_volume: 0.0,
                distributor_count: None,
                leg_quality,
            }],
            required_products: vec![],
            window: None,
        },
        qualified_structures: vec!["Test".to_string()],
        demotion_policy: DemotionPolicy::PromotionOnly,
    };
    // A senior rank needs 2 frontline legs that each contain the rank below it.
    let legs_containing = |min_rank: &str| {
        vec![LegQualityRequirement {
            count: 2,
            predicate: LegPredicate::ContainsRank {
                min_rank: min_rank.to_string(),
            },
        }]
    };
    plan.ranks = vec![
        rank("manager", 1, vec![]),
        rank("director", 2, legs_containing("manager")),
        rank("president", 3, legs_containing("director")),
    ];

    // president(1) -> director(2), director(3); each director -> two managers.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        0,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(3),
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
    .unwrap();
    tree.add_node(
        uuid_from_index(5),
        uuid_from_index(2),
        uuid_from_index(2),
        0,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(6),
        uuid_from_index(3),
        uuid_from_index(3),
        0,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(7),
        uuid_from_index(3),
        uuid_from_index(3),
        0,
    )
    .unwrap();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    for i in 1..=7usize {
        distributors.insert(uuid_from_index(i), primitives(50.0));
    }

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

    // Top of the ladder: two legs each contain a director.
    assert_eq!(
        result.ranks.get(&uuid_from_index(1)),
        Some(&EvaluatedRank::Qualified {
            rank: "president".to_string(),
            ordinal: 3,
        }),
        "node 1 should reach president: 2 legs each contain a director",
    );
    // Middle tier: two legs each contain a manager. They cannot reach
    // president — their legs hold managers, not directors.
    for mid in [2usize, 3] {
        assert_eq!(
            result.ranks.get(&uuid_from_index(mid)),
            Some(&EvaluatedRank::Qualified {
                rank: "director".to_string(),
                ordinal: 2,
            }),
            "node {} should reach director: 2 legs each contain a manager",
            mid,
        );
    }
    // Leaf managers: PV alone clears every rank's PV floor, so staying at
    // manager proves leg_quality gates the senior ranks.
    for leaf in [4usize, 5, 6, 7] {
        assert_eq!(
            result.ranks.get(&uuid_from_index(leaf)),
            Some(&EvaluatedRank::Qualified {
                rank: "manager".to_string(),
                ordinal: 1,
            }),
            "node {} should stay at manager: it has no legs",
            leaf,
        );
    }
}

/// Plan with a single gated rank "QD" whose base criteria are trivial but
/// which carries a windowed gate (achieved >= "associate" in 1 of the last 2
/// periods). "associate" is also in the ladder so the threshold resolves. The
/// gated rank keeps a non-empty `structures` entry referencing "Test" so the
/// distributor is actually evaluated (empty structures would skip it).
fn windowed_plan() -> CompensationPlan {
    let mut plan = linear_plan();
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
                window: None,
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
        RankDefinition {
            name: "QD".to_string(),
            ordinal: 2,
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
                window: Some(RankQualificationWindow {
                    threshold_rank: "associate".to_string(),
                    qualifying_periods: 1,
                    window_periods: 2,
                }),
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];
    plan
}

#[test]
fn evaluate_ranks_errors_when_windowed_rank_has_empty_history_window() {
    // BR9: a rank declares a window but the caller supplied an empty
    // history_window. evaluate_ranks must fail loud with TimeGateWithoutHistory
    // naming the offending rank, rather than silently treating the gate as
    // unmet.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();

    let plan = windowed_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(0.0));

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: Vec::new(),
        history: HashMap::new(),
    };

    let err = evaluate_ranks(&plan, &nav, &inputs).unwrap_err();
    assert_eq!(
        err,
        EvaluationError::TimeGateWithoutHistory {
            rank: "QD".to_string(),
        }
    );
}

#[test]
fn evaluate_ranks_applies_windowed_rank_when_history_window_supplied() {
    // BR9 negative case: with a non-empty axis the empty-axis guard does not
    // fire, and the windowed gate is enforced. The distributor achieved
    // associate in 1 of the last 2 periods, so QD's gate passes and they reach
    // QD.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(1), 0).unwrap();

    let plan = windowed_plan();

    let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
    nav.insert("Test".to_string(), &tree);

    let mut distributors = HashMap::new();
    distributors.insert(uuid_from_index(1), primitives(0.0));

    let mut hist: HashMap<String, Option<u16>> = HashMap::new();
    hist.insert("2026-05".to_string(), Some(1)); // associate
    hist.insert("2026-04".to_string(), None); // Unranked
    let mut history: HashMap<uuid::Uuid, HashMap<String, Option<u16>>> = HashMap::new();
    history.insert(uuid_from_index(1), hist);

    let inputs = EvaluationInputs {
        distributors,
        volume_sources: vec![],
        history_window: vec!["2026-05".to_string(), "2026-04".to_string()],
        history,
    };

    let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();
    assert_eq!(
        result.ranks.get(&uuid_from_index(1)),
        Some(&EvaluatedRank::Qualified {
            rank: "QD".to_string(),
            ordinal: 2,
        })
    );
}
