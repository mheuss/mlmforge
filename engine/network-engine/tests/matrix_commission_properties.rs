mod common;
use common::{
    build_matrix_plan, build_matrix_plan_with_eligibility, build_two_rank_matrix_plan,
    member_snapshot, uuid_from_index,
};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_matrix};
use network_engine::config::commission::{CompressionConfig, CompressionMode};
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::matrix::SpilloverDirection;
use network_engine::tree::matrix::MatrixTree;
use proptest::prelude::*;
use std::collections::HashMap;

proptest! {
    /// No earning should have a level exceeding min(height, max_depth).
    #[test]
    fn no_earning_beyond_effective_depth(
        tree_size in 3..50usize,
        max_depth in 1..10u8,
        height in 1..10u8,
    ) {
        let effective = max_depth.min(height);
        let (plan, structure) = build_matrix_plan(3, height, max_depth);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                earning.level <= effective,
                "Earning at level {} exceeds effective depth {} (height={}, max_depth={})",
                earning.level, effective, height, max_depth
            );
        }
    }

    /// Dollar amount matches the formula exactly.
    #[test]
    fn dollar_amount_matches_formula(
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_matrix_plan(3, 9, 5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let rate = 0.05;

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        tree.add_node(uuid_from_index(1), uuid_from_index(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(
            uuid_from_index(0),
            member_snapshot(),
        );
        snapshots.insert(
            uuid_from_index(1),
            member_snapshot(),
        );

        let volume = vec![VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: cv,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        prop_assert_eq!(result.len(), 1);
        let expected = cv * broad_pct * multiplier * rate;
        let diff = (result[0].dollar_amount - expected).abs();
        prop_assert!(
            diff < 1e-10,
            "Dollar amount {} != expected {}",
            result[0].dollar_amount,
            expected
        );
    }

    /// Compression never produces duplicate earners per source.
    #[test]
    fn compression_no_duplicate_earners(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        // Use stricter eligibility (min PV 50) so nodes with PV 0 are
        // actually ineligible and SkipInactive compression fires.
        let eligibility = CommissionEligibility {
            minimum_pv: 50.0,
            require_order_in_period: false,
            eligible_statuses: vec![],
            active_leg_tiers: vec![],
        };
        let (plan, mut structure) = build_matrix_plan_with_eligibility(3, 9, max_depth, eligibility);
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                network_engine::commission::DistributorSnapshot {
                    personal_volume: if i % 2 == 0 { 100.0 } else { 0.0 },
                    ..member_snapshot()
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert((earning.source_id, earning.earner_id)),
                "Duplicate earning for source {:?} earner {:?}",
                earning.source_id, earning.earner_id
            );
        }
    }

    /// All earnings have non-negative dollar_amount.
    #[test]
    fn all_earnings_non_negative(
        tree_size in 2..30usize,
        cv in 0.0..10000.0f64,
    ) {
        let (plan, structure) = build_matrix_plan(3, 9, 5);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                member_snapshot(),
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                earning.dollar_amount >= 0.0,
                "Negative dollar_amount: {}",
                earning.dollar_amount
            );
        }
    }

    /// Every earner exists in the tree.
    #[test]
    fn every_earner_in_tree(
        tree_size in 2..30usize,
    ) {
        let (plan, structure) = build_matrix_plan(3, 9, 5);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: "member".to_string(),
                    personal_volume: 100.0,
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: 100.0,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                tree.contains(earning.earner_id),
                "Earner {:?} not in tree",
                earning.earner_id
            );
        }
    }

    /// Total payout never exceeds cv * broad_pct * multiplier * max_rate * effective_depth.
    ///
    /// Each volume source produces at most one earner per level, so the
    /// maximum number of earnings is bounded by the effective depth, not
    /// the tree size.
    #[test]
    fn total_payout_bounded(
        tree_size in 2..30usize,
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_matrix_plan(3, 9, 5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let max_rate = 0.05;
        let effective_depth = structure.level_commission.max_depth
            .min(structure.matrix_params.height);

        let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: "member".to_string(),
                    personal_volume: 100.0,
                    status: "active".to_string(),
                    has_order_in_period: true,
                },
            );
        }

        let source_idx = tree_size - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: cv,
        }];

        let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let total: f64 = result.iter().map(|e| e.dollar_amount).sum();
        let upper_bound = cv * broad_pct * multiplier * max_rate * (effective_depth as f64);
        prop_assert!(
            total <= upper_bound + 1e-10,
            "Total payout {} exceeds upper bound {}",
            total, upper_bound
        );
    }
}

/// Single-node tree produces no earnings.
#[test]
fn single_node_no_earnings() {
    let (plan, structure) = build_matrix_plan(3, 9, 5);

    let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
    tree.add_root(uuid_from_index(0), 0).unwrap();

    let mut snapshots = HashMap::new();
    snapshots.insert(
        uuid_from_index(0),
        DistributorSnapshot {
            rank: "member".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );

    let volume = vec![VolumeSource {
        source_id: uuid_from_index(0),
        cv_amount: 100.0,
    }];

    let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();
    assert!(result.is_empty());
}

/// SkipBelowRank compression skips below-threshold nodes without consuming a level.
///
/// Tree: root(silver) -> A(associate) -> B(silver) -> C(silver)
/// Volume at C. With SkipBelowRank threshold "silver", A is skipped.
/// B earns at level 1, root earns at level 2. A earns nothing.
#[test]
fn skip_below_rank_skips_low_rank_nodes() {
    let eligibility = CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![],
    };
    let (plan, mut structure) = build_two_rank_matrix_plan(3, 9, 5, eligibility);
    structure.compression = Some(CompressionConfig {
        enabled: true,
        mode: CompressionMode::SkipBelowRank,
        rank_threshold: Some("silver".to_string()),
    });

    let mut tree = MatrixTree::new(3, SpilloverDirection::BreadthFirst).unwrap();
    tree.add_root(uuid_from_index(0), 0).unwrap(); // root
    tree.add_node(uuid_from_index(1), uuid_from_index(0), 1)
        .unwrap(); // A
    tree.add_node(uuid_from_index(2), uuid_from_index(1), 2)
        .unwrap(); // B
    tree.add_node(uuid_from_index(3), uuid_from_index(2), 3)
        .unwrap(); // C

    let mut snapshots = HashMap::new();
    snapshots.insert(
        uuid_from_index(0),
        DistributorSnapshot {
            rank: "silver".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );
    snapshots.insert(
        uuid_from_index(1),
        DistributorSnapshot {
            rank: "associate".to_string(), // below threshold
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );
    snapshots.insert(
        uuid_from_index(2),
        DistributorSnapshot {
            rank: "silver".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );
    snapshots.insert(
        uuid_from_index(3),
        DistributorSnapshot {
            rank: "silver".to_string(),
            personal_volume: 100.0,
            status: "active".to_string(),
            has_order_in_period: true,
        },
    );

    let volume = vec![VolumeSource {
        source_id: uuid_from_index(3),
        cv_amount: 100.0,
    }];

    let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

    // A (associate) should be skipped — no earning for uuid_from_index(1)
    assert!(
        !result.iter().any(|e| e.earner_id == uuid_from_index(1)),
        "Associate node should be skipped by SkipBelowRank"
    );

    // B earns at level 1 (A was compressed out, doesn't consume a level)
    let b_earning = result.iter().find(|e| e.earner_id == uuid_from_index(2));
    assert!(b_earning.is_some(), "B (silver) should earn");
    assert_eq!(b_earning.unwrap().level, 1);

    // Root earns at level 2
    let root_earning = result.iter().find(|e| e.earner_id == uuid_from_index(0));
    assert!(root_earning.is_some(), "Root (silver) should earn");
    assert_eq!(root_earning.unwrap().level, 2);
}

/// Active leg tiers use sponsor edges, not placement children.
///
/// In a matrix with spillover, a sponsor's recruit may be placed under
/// a different parent. Active leg counting must use the sponsor relationship
/// to determine frontline legs.
///
/// Tree (placement):
///   root -> A -> B (spillover: C placed under A, not root)
///   root -> C (sponsored by root, placed under A by spillover)
///
/// Root sponsors both A and C, so root has 2 active legs via sponsor edges.
/// A is the placement parent of C, but A only sponsors B.
#[test]
fn active_leg_tiers_use_sponsor_not_placement() {
    use network_engine::config::eligibility::ActiveLegTier;

    let eligibility = CommissionEligibility {
        minimum_pv: 10.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![
            ActiveLegTier {
                min_active_legs: 0,
                max_commission_depth: 1, // 0 legs = depth 1
            },
            ActiveLegTier {
                min_active_legs: 2,
                max_commission_depth: 3, // 2+ legs = depth 3
            },
        ],
    };
    let (plan, structure) = build_matrix_plan_with_eligibility(2, 9, 5, eligibility);

    // Width-2 matrix. Root sponsors A and C.
    // A is placed directly under root. C is also sponsored by root but
    // spillover places C under A (root's slots are full after A and D).
    let mut tree = MatrixTree::new(2, SpilloverDirection::BreadthFirst).unwrap();
    tree.add_root(uuid_from_index(0), 0).unwrap(); // root
    // Root sponsors A, placed under root
    tree.add_node(uuid_from_index(1), uuid_from_index(0), 1)
        .unwrap(); // A
    // Root sponsors D, placed under root (fills root's 2 slots)
    tree.add_node(uuid_from_index(3), uuid_from_index(0), 3)
        .unwrap(); // D
    // Root sponsors C, but root is full — spillover places C under A
    tree.add_node(uuid_from_index(2), uuid_from_index(0), 2)
        .unwrap(); // C (sponsor=root, parent=A)
    // A sponsors B, placed under A's remaining slot or spills further
    tree.add_node(uuid_from_index(4), uuid_from_index(1), 4)
        .unwrap(); // B (sponsor=A)
    // Add a deep node as volume source
    tree.add_node(uuid_from_index(5), uuid_from_index(2), 5)
        .unwrap(); // E (sponsor=C)

    let mut snapshots = HashMap::new();
    for i in 0..6 {
        snapshots.insert(
            uuid_from_index(i),
            DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 100.0,
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
    }

    let volume = vec![VolumeSource {
        source_id: uuid_from_index(5), // E
        cv_amount: 100.0,
    }];

    let result = calculate_matrix(&tree, &plan, &structure, &snapshots, &volume).unwrap();

    // Root has 3 sponsored recruits (A, D, C) — all eligible.
    // That's >= 2 active legs, so root gets max_depth=3.
    // If active legs were counted by placement children (only A, D),
    // root would still have 2 legs and qualify. But A only has 1
    // sponsored recruit (B) via sponsor edge, even though C is placed
    // under A. So A has 1 active leg = max_depth 1.
    //
    // The key assertion: verify earnings exist at the depths the
    // sponsor-based counting allows.

    // Root should be able to earn (has enough active legs for deeper depth).
    let root_earning = result.iter().find(|e| e.earner_id == uuid_from_index(0));
    assert!(
        root_earning.is_some(),
        "Root should earn — has 3 sponsored legs"
    );

    // A has only 1 sponsored recruit (B), so max_depth=1 from tiers.
    // A can only earn at level 1 from volume sources directly below.
    let a_earnings: Vec<_> = result
        .iter()
        .filter(|e| e.earner_id == uuid_from_index(1))
        .collect();
    for earning in &a_earnings {
        assert!(
            earning.level <= 1,
            "A has 1 sponsored leg, should be limited to depth 1, but earned at level {}",
            earning.level
        );
    }
}
