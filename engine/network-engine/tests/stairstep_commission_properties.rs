mod common;
use common::{build_stairstep_plan, uuid_from_index};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_stairstep};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::HashMap;

proptest! {
    /// No earning should have a level exceeding max_depth.
    #[test]
    fn no_earning_beyond_max_depth(
        tree_size in 3..50usize,
        max_depth in 1..10u8,
    ) {
        let (plan, structure) = build_stairstep_plan(max_depth);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                earning.level <= max_depth,
                "Earning at level {} exceeds max_depth {}",
                earning.level, max_depth
            );
        }
    }

    /// All earnings have non-negative dollar_amount.
    #[test]
    fn all_earnings_non_negative(
        tree_size in 2..30usize,
        cv in 0.0..10000.0f64,
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                earning.dollar_amount >= 0.0,
                "Negative dollar_amount: {}",
                earning.dollar_amount
            );
        }
    }

    /// No duplicate (source_id, earner_id) pairs.
    #[test]
    fn no_duplicate_earners(
        tree_size in 2..30usize,
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert((earning.source_id, earning.earner_id)),
                "Duplicate earning for source {:?} earner {:?}",
                earning.source_id, earning.earner_id
            );
        }
    }

    /// Every earner exists in the tree.
    #[test]
    fn every_earner_in_tree(
        tree_size in 2..30usize,
    ) {
        let (plan, structure) = build_stairstep_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                tree.contains(earning.earner_id),
                "Earner {:?} not in tree",
                earning.earner_id
            );
        }
    }

    /// Total payout is bounded.
    #[test]
    fn total_payout_bounded(
        tree_size in 2..30usize,
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_stairstep_plan(5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let max_rate = 0.10; // max of level rate (0.05) and override rate (0.10)
        let max_depth = structure.level_commission.max_depth;

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let total: f64 = result.iter().map(|e| e.dollar_amount).sum();
        // Level comms: up to max_depth levels * 0.05 each
        // Override: up to 0.10 on group volume (bounded by total CV)
        let upper_bound = cv * broad_pct * multiplier * max_rate * (max_depth as f64 + 1.0);
        prop_assert!(
            total <= upper_bound + 1e-10,
            "Total payout {} exceeds upper bound {}",
            total, upper_bound
        );
    }

    /// Total payout is bounded even with breakaway overrides active.
    #[test]
    fn total_payout_bounded_with_overrides(
        tree_size in 3..30usize,
        cv in 1.0..10000.0f64,
    ) {
        use network_engine::config::stairstep::{
            BreakawayConfig, DifferentialConfig, OverrideMode,
        };

        let (plan, mut structure) = build_stairstep_plan(5);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let max_depth = structure.level_commission.max_depth;

        structure.breakaway = Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            override_mode: OverrideMode::Differential(DifferentialConfig {
                rank_rates: {
                    let mut m = std::collections::BTreeMap::new();
                    m.insert("member".to_string(), 0.05);
                    m.insert("director".to_string(), 0.10);
                    m
                },
                min_override: 0.02,
            }),
            generation_overrides: None,
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 3 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let total: f64 = result.iter().map(|e| e.dollar_amount).sum();
        // Level bound: up to max_depth levels at 0.05 each, based on CV.
        let level_bound = cv * broad_pct * multiplier * 0.05 * max_depth as f64;
        // Override bound: overrides use group volume (personal_volume sums),
        // not the CV from volume sources. Total group volume across all
        // breakaways equals total personal volume = tree_size * 100.
        let total_pv = tree_size as f64 * 100.0;
        let override_bound = total_pv * broad_pct * multiplier * 0.10;
        let upper_bound = level_bound + override_bound;
        prop_assert!(
            total <= upper_bound + 1e-10,
            "Total payout {} exceeds upper bound {}",
            total, upper_bound
        );
    }

    /// Level and override earnings partition cleanly: no earner receives
    /// both a level commission and an override on the same volume source.
    #[test]
    fn level_and_override_partition_cleanly(
        tree_size in 3..30usize,
    ) {
        use network_engine::config::stairstep::{
            BreakawayConfig, DifferentialConfig, OverrideMode,
        };

        let (plan, mut structure) = build_stairstep_plan(5);

        // Set up breakaway config so overrides can happen.
        // Threshold rank = "director". Give every other node "director" rank.
        structure.breakaway = Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            override_mode: OverrideMode::Differential(DifferentialConfig {
                rank_rates: {
                    let mut m = std::collections::BTreeMap::new();
                    m.insert("member".to_string(), 0.05);
                    m.insert("director".to_string(), 0.10);
                    m
                },
                min_override: 0.02,
            }),
            generation_overrides: None,
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            let parent = i - 1;
            tree.add_node(uuid_from_index(i), uuid_from_index(parent), uuid_from_index(parent), i as i64).unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            let rank = if i % 2 == 0 { "director" } else { "member" };
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: rank.to_string(),
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

        let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Partition level earners and override earners.
        // Level earnings: source_id == original volume source.
        // Override earnings: source_id == a breakaway distributor.
        let mut level_earners = std::collections::HashSet::new();
        let mut override_earners = std::collections::HashSet::new();

        for earning in &result {
            if earning.source_id == uuid_from_index(source_idx) {
                level_earners.insert(earning.earner_id);
            } else {
                // Override source_id should be a breakaway
                let is_breakaway = snapshots
                    .get(&earning.source_id)
                    .map(|s| s.rank == "director")
                    .unwrap_or(false);
                prop_assert!(
                    is_breakaway,
                    "Override source {:?} is not a breakaway distributor",
                    earning.source_id
                );
                override_earners.insert(earning.earner_id);
            }
        }

        // The partition property: no earner receives both a level
        // commission and an override commission. Walk 1 stays within
        // the source's personal group while Walk 2 pays ancestors
        // above breakaway boundaries.
        let overlap: Vec<_> = level_earners.intersection(&override_earners).collect();
        prop_assert!(
            overlap.is_empty(),
            "Earners received both level and override commissions: {:?}",
            overlap
        );
    }
}

/// Single-node tree produces no earnings.
#[test]
fn single_node_no_earnings() {
    let (plan, structure) = build_stairstep_plan(5);

    let mut tree = UnilevelTree::new();
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

    let result = calculate_stairstep(&tree, &plan, &structure, &snapshots, &volume).unwrap();
    assert!(result.is_empty());
}
