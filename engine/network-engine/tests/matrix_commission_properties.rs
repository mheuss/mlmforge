mod common;
use common::{build_matrix_plan, build_matrix_plan_with_eligibility, uuid_from_index};

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
            DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 100.0,
                status: "active".to_string(),
                has_order_in_period: true,
            },
        );
        snapshots.insert(
            uuid_from_index(1),
            DistributorSnapshot {
                rank: "member".to_string(),
                personal_volume: 100.0,
                status: "active".to_string(),
                has_order_in_period: true,
            },
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
                DistributorSnapshot {
                    rank: "member".to_string(),
                    personal_volume: if i % 2 == 0 { 100.0 } else { 0.0 },
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

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert(earning.earner_id),
                "Duplicate earning for earner {:?}",
                earning.earner_id
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
