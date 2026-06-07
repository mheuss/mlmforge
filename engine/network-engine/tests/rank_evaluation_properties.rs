mod common;

use common::uuid_from_index;
use network_engine::config::rank::{
    DemotionPolicy, DistributorCountRequirement, LegPredicate, LegQualityRequirement,
    RankDefinition, RankQualification, RankQualificationWindow, SearchMode, StructureQualification,
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
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
                tenure: None,
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
                tenure: None,
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

        let inputs = EvaluationInputs {
            distributors,
            volume_sources: vec![],
            history_window: Vec::new(),
            history: HashMap::new(),
        };
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
            &EvaluationInputs {
                distributors: distributors_low,
                volume_sources: vec![],
                history_window: Vec::new(),
                history: HashMap::new(),
            },
        ).unwrap();
        let r_high = evaluate_ranks(
            &plan, &nav,
            &EvaluationInputs {
                distributors: distributors_high,
                volume_sources: vec![],
                history_window: Vec::new(),
                history: HashMap::new(),
            },
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

proptest! {
    #[test]
    fn evaluation_is_invariant_to_input_iteration_order(
        size in 2..12usize,
        seed_pv in 0u32..400,
    ) {
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64).unwrap();
        }
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);
        let plan = build_random_plan();

        // Build the same logical input twice with different HashMap seeds.
        // serde_json roundtrip via two independent HashMap instances is
        // enough to exercise different iteration orders in practice.
        let mut a = HashMap::new();
        let mut b = HashMap::new();
        for i in 0..size {
            let p = DistributorPrimitives {
                personal_volume: (seed_pv as f64) + (i as f64),
                retail_volume: 0.0,
                status: "active".to_string(),
                has_order_in_period: true,
                active_products: vec![],
            };
            a.insert(uuid_from_index(i), p.clone());
            b.insert(uuid_from_index(i), p);
        }

        let r_a = evaluate_ranks(
            &plan, &nav,
            &EvaluationInputs {
                distributors: a,
                volume_sources: vec![],
                history_window: Vec::new(),
                history: HashMap::new(),
            },
        ).unwrap();
        let r_b = evaluate_ranks(
            &plan, &nav,
            &EvaluationInputs {
                distributors: b,
                volume_sources: vec![],
                history_window: Vec::new(),
                history: HashMap::new(),
            },
        ).unwrap();

        // The map-equality assertion below is the operative check. The JSON
        // assertion catches future regressions where EvaluationResult gains a
        // non-canonical field (anything but a BTreeMap or Vec with deterministic order).
        let json_a = serde_json::to_string(&r_a).unwrap();
        let json_b = serde_json::to_string(&r_b).unwrap();
        prop_assert_eq!(json_a, json_b);

        // Catches divergence on every key in either map (missing, extra, or mismatched).
        prop_assert_eq!(r_a.ranks, r_b.ranks);
    }
}

proptest! {
    /// `evaluate_ranks` always converges on a multi-tree plan with circular
    /// cross-tree rank dependencies. `a` is above `b` in `uni`, `b` is above
    /// `a` in `bin`. `manager_u` counts a `uni` downline and `manager_b`
    /// counts a `bin` downline, so `a`'s rank depends on `b`'s and `b`'s on
    /// `a`'s — a genuine cycle that no single evaluation order resolves. Every
    /// rank requires PV >= 100, and PV is randomized across 0..400. A
    /// distributor below the threshold is `Unranked`, so the cross-tree
    /// distributor_count predicate genuinely flips between draws: some count a
    /// qualifying downline, others do not. For every draw `evaluate_ranks`
    /// must return Ok — it must never fail to converge.
    ///
    /// uni: a(0) -> b(1)
    /// bin: b(0) -> a(1)
    #[test]
    fn evaluate_ranks_always_converges_on_cyclic_multi_tree_plan(
        pv_a in 0u32..400,
        pv_b in 0u32..400,
    ) {
        let a = uuid_from_index(1);
        let b = uuid_from_index(2);

        let mut uni = UnilevelTree::new();
        uni.add_root(a, 0).unwrap();
        uni.add_node(b, a, a, 0).unwrap();

        let mut bin = UnilevelTree::new();
        bin.add_root(b, 0).unwrap();
        bin.add_node(a, b, b, 0).unwrap();

        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("uni".to_string(), &uni);
        nav.insert("bin".to_string(), &bin);

        // 1 associate-or-better downline distributor.
        let count_req = || DistributorCountRequirement {
            count: 1,
            min_rank: "associate".to_string(),
            search_mode: SearchMode::AnyLevel,
            search_depth: None,
            total_count: 1,
            min_leg_group_volume: 0.0,
        };
        let rank = |name: &str,
                    ordinal: u16,
                    structure: &str,
                    pv: f64,
                    dc: Option<DistributorCountRequirement>| RankDefinition {
            name: name.to_string(),
            ordinal,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: structure.to_string(),
                    personal_volume: pv,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: dc,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec![structure.to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        };

        let mut plan = build_random_plan();
        plan.ranks = vec![
            rank("associate", 1, "uni", 100.0, None),
            rank("manager_u", 2, "uni", 100.0, Some(count_req())),
            rank("manager_b", 3, "bin", 100.0, Some(count_req())),
        ];

        let prim = |pv: u32| DistributorPrimitives {
            personal_volume: pv as f64,
            retail_volume: 0.0,
            status: "active".to_string(),
            has_order_in_period: true,
            active_products: vec![],
        };
        let mut distributors = HashMap::new();
        distributors.insert(a, prim(pv_a));
        distributors.insert(b, prim(pv_b));

        let inputs = EvaluationInputs {
            distributors,
            volume_sources: vec![],
            history_window: Vec::new(),
            history: HashMap::new(),
        };

        let result = evaluate_ranks(&plan, &nav, &inputs);
        prop_assert!(
            result.is_ok(),
            "evaluate_ranks must converge on a cyclic multi-tree plan: {:?}",
            result.err()
        );
    }
}

/// Builds `DistributorPrimitives` with the given personal volume. The other
/// fields are fixed: active, with an order in the period.
fn prim(pv: f64) -> DistributorPrimitives {
    DistributorPrimitives {
        personal_volume: pv,
        retail_volume: 0.0,
        status: "active".to_string(),
        has_order_in_period: true,
        active_products: vec![],
    }
}

/// A two-rank plan on the "Test" structure: `base` (ordinal 1, PV >= `base_pv`)
/// and `gated` (ordinal 2, PV >= 0) which additionally requires
/// `gated_leg_quality`. Reuses `build_random_plan`'s structure config.
fn leg_quality_plan(
    base_pv: f64,
    gated_leg_quality: Vec<LegQualityRequirement>,
) -> CompensationPlan {
    let mut plan = build_random_plan();
    plan.ranks = vec![
        RankDefinition {
            name: "base".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Test".to_string(),
                    personal_volume: base_pv,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: vec![],
                }],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
        RankDefinition {
            name: "gated".to_string(),
            ordinal: 2,
            qualification: RankQualification {
                structures: vec![StructureQualification {
                    structure: "Test".to_string(),
                    personal_volume: 0.0,
                    group_volume: 0.0,
                    max_group_volume_per_leg: f64::MAX,
                    min_retail_volume: 0.0,
                    distributor_count: None,
                    leg_quality: gated_leg_quality,
                }],
                required_products: vec![],
                window: None,
                tenure: None,
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];
    plan
}

proptest! {
    /// Oracle check for `leg_quality` gating. The subject has one leaf child
    /// per drawn PV, so each leg contains a PV-matching node exactly when that
    /// child clears the threshold. The subject must reach `gated` if and only
    /// if at least `required` legs qualify — checked against an independent
    /// count over the same PVs.
    #[test]
    fn leg_quality_gates_rank_iff_enough_legs_match(
        child_pvs in prop::collection::vec(0u32..300, 1..8usize),
        threshold in 1u32..300,
        required in 1u16..8,
    ) {
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 0..child_pvs.len() {
            tree.add_node(
                uuid_from_index(i + 1),
                uuid_from_index(0),
                uuid_from_index(0),
                i as i64,
            ).unwrap();
        }
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        let plan = leg_quality_plan(
            0.0,
            vec![LegQualityRequirement {
                count: required,
                predicate: LegPredicate::ContainsPersonalVolume {
                    min_personal_volume: threshold as f64,
                },
            }],
        );

        let mut distributors = HashMap::new();
        distributors.insert(uuid_from_index(0), prim(50.0));
        for (i, pv) in child_pvs.iter().enumerate() {
            distributors.insert(uuid_from_index(i + 1), prim(*pv as f64));
        }
        let inputs = EvaluationInputs {
            distributors,
            volume_sources: vec![],
            history_window: Vec::new(),
            history: HashMap::new(),
        };

        let result = evaluate_ranks(&plan, &nav, &inputs).unwrap();

        // Oracle: each leg is a single leaf, so it qualifies exactly when that
        // child's PV clears the threshold.
        let qualifying_legs = child_pvs.iter().filter(|pv| **pv >= threshold).count();
        let expected = if qualifying_legs >= required as usize {
            "gated"
        } else {
            "base"
        };
        let subject_rank = match result.ranks.get(&uuid_from_index(0)) {
            Some(network_engine::rank::EvaluatedRank::Qualified { rank, .. }) => rank.as_str(),
            _ => "<unranked>",
        };
        prop_assert_eq!(
            subject_rank, expected,
            "child PVs {:?}, threshold {}, required {}: {} legs qualify",
            child_pvs, threshold, required, qualifying_legs,
        );
    }
}

proptest! {
    /// Monotonicity of `ContainsRank` leg quality. Raising every child's PV
    /// can only raise children into the `base` rank, never out of it, so it
    /// can only add qualifying legs — never remove them. The subject's
    /// evaluated rank must therefore never decrease. This is the per-predicate
    /// monotonicity that design-rationale 026 relies on for fixpoint
    /// convergence.
    #[test]
    fn leg_quality_is_monotone_in_descendant_rank(
        child_pvs in prop::collection::vec(0u32..250, 1..8usize),
        increase in 1u32..200,
        base_threshold in 1u32..250,
        required in 1u16..6,
    ) {
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 0..child_pvs.len() {
            tree.add_node(
                uuid_from_index(i + 1),
                uuid_from_index(0),
                uuid_from_index(0),
                i as i64,
            ).unwrap();
        }
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        // `gated` needs `required` legs each containing a `base`-or-better
        // node; a child reaches `base` once its PV clears `base_threshold`.
        let plan = leg_quality_plan(
            base_threshold as f64,
            vec![LegQualityRequirement {
                count: required,
                predicate: LegPredicate::ContainsRank {
                    min_rank: "base".to_string(),
                },
            }],
        );

        // The subject's PV clears any `base_threshold` draw, so the subject is
        // always Qualified — only its leg structure varies between runs.
        let subject_ordinal = |bump: u32| -> u16 {
            let mut distributors = HashMap::new();
            distributors.insert(uuid_from_index(0), prim(300.0));
            for (i, pv) in child_pvs.iter().enumerate() {
                distributors.insert(uuid_from_index(i + 1), prim((*pv + bump) as f64));
            }
            let result = evaluate_ranks(
                &plan,
                &nav,
                &EvaluationInputs {
                    distributors,
                    volume_sources: vec![],
                    history_window: Vec::new(),
                    history: HashMap::new(),
                },
            ).unwrap();
            match result.ranks.get(&uuid_from_index(0)) {
                Some(network_engine::rank::EvaluatedRank::Qualified { ordinal, .. }) => *ordinal,
                _ => 0,
            }
        };

        prop_assert!(
            subject_ordinal(increase) >= subject_ordinal(0),
            "raising every descendant's PV must not lower the subject's leg-quality rank",
        );
    }
}

/// Ordinal of the windowed threshold rank ("silver"). The window gate counts
/// axis periods whose achieved ordinal is `>= WINDOW_THRESHOLD_ORDINAL`.
const WINDOW_THRESHOLD_ORDINAL: u16 = 2;

/// A three-rank plan on the "Test" structure with a windowed (N-of-M) gate on
/// the top rank:
/// - `associate` (ordinal 1, PV >= 0, no window)
/// - `silver` (ordinal 2, PV >= 0, no window) — the gate's `threshold_rank`
/// - `gold` (ordinal 3, PV >= 0) — additionally requires `qualifying_periods`
///   of the last `window_periods` axis periods to have achieved silver-or-better
///
/// Every rank requires only PV >= 0 on "Test", so any in-tree distributor is
/// always at least `silver`. Reaching `gold` depends solely on the window gate,
/// which isolates the windowed dimension. `gold`'s `structures` references the
/// "Test" tree (non-empty) so the subject is actually evaluated. `threshold_rank`
/// is "silver", a real rank in the plan, so `evaluate_ranks` does not error with
/// `UnknownThresholdRank`. Reuses `build_random_plan`'s structure config.
fn windowed_plan(qualifying_periods: u8, window_periods: u8) -> CompensationPlan {
    let pv_only =
        |name: &str, ordinal: u16, window: Option<RankQualificationWindow>| RankDefinition {
            name: name.to_string(),
            ordinal,
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
                window,
                tenure: None,
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        };
    let mut plan = build_random_plan();
    plan.ranks = vec![
        pv_only("associate", 1, None),
        pv_only("silver", WINDOW_THRESHOLD_ORDINAL, None),
        pv_only(
            "gold",
            3,
            Some(RankQualificationWindow {
                threshold_rank: "silver".to_string(),
                qualifying_periods,
                window_periods,
            }),
        ),
    ];
    plan
}

proptest! {
    /// Determinism (design §5, NFR #4): evaluating the same inputs twice — the
    /// same `history_window` (DESC) and per-distributor `history` — yields
    /// byte-identical serialized output. `evaluate_ranks` is already
    /// deterministic, so this passes with no production change; a failure here
    /// is a real non-determinism bug, not a test gap.
    ///
    /// Non-vacuous: the subject is the root of the "Test" tree and is present in
    /// `distributors`, so it is always evaluated (and, since every rank needs
    /// only PV >= 0, always at least `silver` — see the sanity assertion). The
    /// axis length is `>= window_periods` (BR5), so the gate is satisfiable, and
    /// per-period ordinals are drawn across the threshold (`None`/below/at-or-
    /// above), so the gate both passes and fails across draws.
    #[test]
    fn windowed_gate_is_deterministic(
        // axis_len = ords.len() in 2..=8; ords[i] is the subject's achieved rank
        // in axis period i (None = Unranked). window_periods is clamped to
        // <= axis_len so the axis is always long enough to satisfy the gate
        // (BR5); qualifying_periods is clamped to <= window_periods so the
        // window itself is valid.
        ords in prop::collection::vec(prop::option::of(0u16..=3), 2..=8usize),
        m_raw in 1u8..=8,
        n_raw in 1u8..=8,
    ) {
        let axis_len = ords.len();
        let m = (m_raw as usize).min(axis_len).max(1);
        let n = (n_raw as usize).min(m).max(1);
        let plan = windowed_plan(n as u8, m as u8);

        let subject = uuid_from_index(0);
        let mut tree = UnilevelTree::new();
        tree.add_root(subject, 0).unwrap();
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        // Most-recent-first axis (DESC); labels need only be distinct, the Vec
        // order defines recency.
        let axis: Vec<String> = (0..axis_len)
            .map(|i| format!("2026-{:02}", axis_len - i))
            .collect();
        let mut subject_history: HashMap<String, Option<u16>> = HashMap::new();
        for (i, achieved) in ords.iter().enumerate() {
            subject_history.insert(axis[i].clone(), *achieved);
        }
        let mut history = HashMap::new();
        history.insert(subject, subject_history);

        let mut distributors = HashMap::new();
        distributors.insert(subject, prim(300.0));

        let inputs = EvaluationInputs {
            distributors,
            volume_sources: vec![],
            history_window: axis,
            history,
        };

        let r1 = evaluate_ranks(&plan, &nav, &inputs).unwrap();
        let r2 = evaluate_ranks(&plan, &nav, &inputs).unwrap();

        // The subject is in-tree with PV >= every threshold, so it is always
        // evaluated and at least `silver`. This proves the determinism check is
        // not vacuously comparing two `Unranked`/absent results.
        prop_assert!(
            matches!(
                r1.ranks.get(&subject),
                Some(network_engine::rank::EvaluatedRank::Qualified { .. })
            ),
            "subject must be evaluated (Qualified), got {:?}",
            r1.ranks.get(&subject),
        );

        let json1 = serde_json::to_string(&r1).unwrap();
        let json2 = serde_json::to_string(&r2).unwrap();
        prop_assert_eq!(json1, json2);
    }
}

proptest! {
    /// Fixed-set monotonicity (design §5, NFR #4): holding the axis fixed,
    /// raising some axis periods from below-threshold to at-or-above-threshold
    /// (and lowering none) can only increase the window count, which can only
    /// flip the gate false->true, so the subject's evaluated ordinal must not
    /// decrease. Varying the axis itself is out of scope.
    ///
    /// The "raised" history maps each baseline ordinal `b` to
    /// `max(b, WINDOW_THRESHOLD_ORDINAL)` on masked periods and leaves the rest
    /// at `b`. `max(b, T) >= b` always, so no period is ever lowered; only
    /// below-`T` masked periods move up to `T`.
    ///
    /// Non-vacuous: the subject is the in-tree root and always at least `silver`
    /// (sanity assertion below), so the comparison is between two real evaluated
    /// ordinals. The axis length is `>= window_periods` (BR5), so the gate is
    /// satisfiable and can reach `gold`.
    #[test]
    fn windowed_gate_is_monotone_in_qualifying_periods(
        // (baseline ordinal, raise this period?) per axis period; axis_len in
        // 2..=8. window_periods clamped <= axis_len (BR5 satisfiable),
        // qualifying_periods clamped <= window_periods (valid window).
        base_and_mask in prop::collection::vec((0u16..=3, any::<bool>()), 2..=8usize),
        m_raw in 1u8..=8,
        n_raw in 1u8..=8,
    ) {
        let axis_len = base_and_mask.len();
        let m = (m_raw as usize).min(axis_len).max(1);
        let n = (n_raw as usize).min(m).max(1);
        let plan = windowed_plan(n as u8, m as u8);

        let subject = uuid_from_index(0);
        let mut tree = UnilevelTree::new();
        tree.add_root(subject, 0).unwrap();
        let mut nav: HashMap<String, &dyn TreeNavigator> = HashMap::new();
        nav.insert("Test".to_string(), &tree);

        let axis: Vec<String> = (0..axis_len)
            .map(|i| format!("2026-{:02}", axis_len - i))
            .collect();

        // Evaluate the subject with its history; `raise` toggles the masked
        // periods up to the threshold. Base criteria (PV) are fixed in both
        // runs, so only the windowed dimension varies.
        let subject_ordinal = |raise: bool| -> u16 {
            let mut subject_history: HashMap<String, Option<u16>> = HashMap::new();
            for (i, (base, mask)) in base_and_mask.iter().enumerate() {
                let achieved = if raise && *mask {
                    (*base).max(WINDOW_THRESHOLD_ORDINAL)
                } else {
                    *base
                };
                subject_history.insert(axis[i].clone(), Some(achieved));
            }
            let mut history = HashMap::new();
            history.insert(subject, subject_history);

            let mut distributors = HashMap::new();
            distributors.insert(subject, prim(300.0));

            let result = evaluate_ranks(
                &plan,
                &nav,
                &EvaluationInputs {
                    distributors,
                    volume_sources: vec![],
                    history_window: axis.clone(),
                    history,
                },
            )
            .unwrap();
            match result.ranks.get(&subject) {
                Some(network_engine::rank::EvaluatedRank::Qualified { ordinal, .. }) => *ordinal,
                _ => 0,
            }
        };

        // The subject is always at least `silver` (ordinal 2) since every rank
        // needs only PV >= 0; this proves the subject is evaluated and the
        // comparison is not vacuous.
        prop_assert!(
            subject_ordinal(false) >= WINDOW_THRESHOLD_ORDINAL,
            "subject must be evaluated and at least silver",
        );
        prop_assert!(
            subject_ordinal(true) >= subject_ordinal(false),
            "raising periods to at-or-above-threshold must not lower the subject's windowed rank",
        );
    }
}
