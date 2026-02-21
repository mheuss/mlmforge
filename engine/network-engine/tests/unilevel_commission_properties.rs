mod common;
use common::{
    build_two_rank_unilevel_plan, build_unilevel_plan, build_unilevel_plan_with_eligibility,
    permissive_eligibility, uuid_from_index,
};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_unilevel};
use network_engine::config::commission::{CompressionConfig, CompressionMode};
use network_engine::config::eligibility::{ActiveLegTier, CommissionEligibility};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::HashMap;

/// Like build_unilevel_plan but with min_personal_volume set to create
/// eligible/ineligible distributors based on PV.
fn build_test_plan_with_min_pv(
    max_depth: u8,
    min_pv: f64,
) -> (
    network_engine::config::CompensationPlan,
    network_engine::config::UnilevelStructureConfig,
) {
    let eligibility = CommissionEligibility {
        minimum_pv: min_pv,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![],
    };
    build_unilevel_plan_with_eligibility(max_depth, eligibility)
}

proptest! {
    /// No earning should have a level exceeding the configured max_depth.
    ///
    /// Builds a linear chain of random size and verifies the calculator
    /// respects the depth ceiling regardless of tree depth.
    #[test]
    fn no_earning_beyond_max_depth(
        tree_size in 3..50usize,
        max_depth in 1..10u8,
    ) {
        let (plan, structure) = build_unilevel_plan(max_depth);

        // Build a chain: 0 -> 1 -> 2 -> ... -> tree_size-1
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
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

        // Volume from the deepest node
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(tree_size - 1),
            cv_amount: 100.0,
        }];

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        for earning in &result {
            prop_assert!(
                earning.level <= max_depth,
                "Earning at level {} exceeds max_depth {}",
                earning.level,
                max_depth
            );
        }
    }

    /// The dollar_amount on every earning must exactly match the formula:
    /// cv * broad_commission_percent * volume_to_dollar_multiplier * rate.
    ///
    /// Uses a two-node tree (root + child) so there is exactly one earning.
    /// The CV amount is randomized to cover a wide range of inputs.
    #[test]
    fn dollar_amount_matches_formula(
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_unilevel_plan(3);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let rate = 0.05; // all levels use 0.05

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        tree.add_node(uuid_from_index(1), uuid_from_index(0), uuid_from_index(0), 1).unwrap();

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

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

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

    /// Compression must never produce duplicate earnings for the same
    /// earner-source pair. Each earner appears at most once per source.
    ///
    /// Builds a chain with alternating eligible/ineligible nodes and
    /// verifies no earner is paid twice.
    #[test]
    fn compression_no_duplicate_earners(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        let (plan, mut structure) = build_test_plan_with_min_pv(max_depth, 50.0);
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: "member".to_string(),
                    // Alternate: even nodes eligible, odd nodes ineligible
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

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert(earning.earner_id),
                "Duplicate earning for earner {:?}",
                earning.earner_id
            );
        }
    }

    /// With compression enabled, eligible nodes earn at least as many
    /// commissions as without compression. Compression preserves levels
    /// by not consuming them for skipped nodes.
    ///
    /// Builds the same chain with and without compression and verifies
    /// the compressed run produces >= the uncompressed count.
    #[test]
    fn compression_preserves_or_increases_earnings(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        let (plan, structure_no_compress) = build_test_plan_with_min_pv(max_depth, 50.0);
        let mut structure_compress = structure_no_compress.clone();
        structure_compress.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipInactive,
            rank_threshold: None,
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
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

        let without =
            calculate_unilevel(&tree, &plan, &structure_no_compress, &snapshots, &volume)
                .unwrap();
        let with =
            calculate_unilevel(&tree, &plan, &structure_compress, &snapshots, &volume)
                .unwrap();

        prop_assert!(
            with.len() >= without.len(),
            "Compressed earnings ({}) < uncompressed ({}) — compression should preserve or increase count",
            with.len(),
            without.len()
        );
    }

    // --- SkipBelowRank compression ---

    /// SkipBelowRank compression: distributors below the rank threshold
    /// are skipped without consuming a level. Levels are preserved for
    /// the next qualified upline.
    ///
    /// Builds a chain where alternating nodes are associate (below
    /// threshold) and silver (at or above threshold). Verifies that
    /// compressed earnings only go to silver-ranked nodes.
    #[test]
    fn skip_below_rank_only_pays_qualified_rank(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        let (plan, mut structure) = build_two_rank_unilevel_plan(max_depth, permissive_eligibility());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    // Alternate: even=silver (qualifies), odd=associate (compressed)
                    rank: if i % 2 == 0 { "silver".to_string() } else { "associate".to_string() },
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

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // All earners must be silver-ranked. Associates are compressed out.
        for earning in &result {
            let snap = snapshots.get(&earning.earner_id).unwrap();
            prop_assert_eq!(
                snap.rank.as_str(), "silver",
                "SkipBelowRank should not pay associate-ranked node {}",
                earning.earner_id
            );
        }
    }

    /// SkipBelowRank compression preserves levels. With compression,
    /// at least as many earnings should be produced as without.
    #[test]
    fn skip_below_rank_preserves_or_increases_earnings(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        let (plan, structure_no_compress) =
            build_two_rank_unilevel_plan(max_depth, permissive_eligibility());
        let mut structure_compress = structure_no_compress.clone();
        structure_compress.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: if i % 2 == 0 { "silver".to_string() } else { "associate".to_string() },
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

        let without =
            calculate_unilevel(&tree, &plan, &structure_no_compress, &snapshots, &volume).unwrap();
        let with =
            calculate_unilevel(&tree, &plan, &structure_compress, &snapshots, &volume).unwrap();

        // Filter to only silver earners from the uncompressed run for a fair comparison.
        let without_silver: Vec<_> = without.iter()
            .filter(|e| snapshots.get(&e.earner_id).is_some_and(|s| s.rank == "silver"))
            .collect();

        prop_assert!(
            with.len() >= without_silver.len(),
            "SkipBelowRank compressed ({}) < uncompressed silver earners ({}) — compression should preserve or increase count",
            with.len(),
            without_silver.len()
        );
    }

    /// SkipBelowRank compression: no duplicate earners. Each earner
    /// appears at most once per source.
    #[test]
    fn skip_below_rank_no_duplicate_earners(
        tree_size in 5..30usize,
        max_depth in 3..10u8,
    ) {
        let (plan, mut structure) = build_two_rank_unilevel_plan(max_depth, permissive_eligibility());
        structure.compression = Some(CompressionConfig {
            enabled: true,
            mode: CompressionMode::SkipBelowRank,
            rank_threshold: Some("silver".to_string()),
        });

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..tree_size {
            snapshots.insert(
                uuid_from_index(i),
                DistributorSnapshot {
                    rank: if i % 2 == 0 { "silver".to_string() } else { "associate".to_string() },
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

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        let mut seen = std::collections::HashSet::new();
        for earning in &result {
            prop_assert!(
                seen.insert(earning.earner_id),
                "Duplicate earning for earner {:?}",
                earning.earner_id
            );
        }
    }

    // --- active_leg_tiers qualification ---

    /// When active_leg_tiers are configured, a distributor with fewer
    /// active frontline legs than the lowest tier threshold earns nothing
    /// beyond the config max_depth (no tier match means no tier restriction,
    /// falling back to config max_depth).
    ///
    /// A distributor who matches a tier has their earning depth limited
    /// to that tier's max_commission_depth.
    #[test]
    fn active_leg_tiers_limits_earning_depth(
        chain_len in 5..20usize,
    ) {
        let max_depth: u8 = 10;
        let tier_depth: u16 = 2;

        let eligibility = CommissionEligibility {
            minimum_pv: 0.0,
            require_order_in_period: false,
            eligible_statuses: vec![],
            active_leg_tiers: vec![
                ActiveLegTier {
                    min_active_legs: 1,
                    max_commission_depth: tier_depth,
                },
            ],
        };
        let (plan, structure) = build_unilevel_plan_with_eligibility(max_depth, eligibility);

        // Build a linear chain. Each node has exactly 1 active child
        // (the next node in the chain).
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..chain_len {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), uuid_from_index(i - 1), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..chain_len {
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

        let source_idx = chain_len - 1;
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(source_idx),
            cv_amount: 100.0,
        }];

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        // Each earner should have a level <= tier_depth because every
        // node in a linear chain has exactly 1 active leg.
        for earning in &result {
            prop_assert!(
                earning.level <= tier_depth as u8,
                "Active leg tier limits depth to {} but earning at level {} for node {}",
                tier_depth, earning.level, earning.earner_id
            );
        }
    }

    // --- Empty and single-node tree edge cases ---

    /// A single-node tree (root only) produces no earnings. The root
    /// has no upline to pay.
    #[test]
    fn single_node_tree_no_earnings(
        cv in 1.0..10000.0f64,
    ) {
        let (plan, structure) = build_unilevel_plan(5);
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
            cv_amount: cv,
        }];

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        prop_assert!(
            result.is_empty(),
            "Single-node tree should produce no earnings"
        );
    }

    /// A tree where root has children but no grandchildren. Volume from
    /// a child pays only the root at level 1. No deeper earnings.
    #[test]
    fn root_with_children_only_level_one(
        cv in 1.0..10000.0f64,
        num_children in 1..10usize,
    ) {
        let (plan, structure) = build_unilevel_plan(5);

        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..=num_children {
            tree.add_node(uuid_from_index(i), uuid_from_index(0), uuid_from_index(0), i as i64)
                .unwrap();
        }

        let mut snapshots = HashMap::new();
        for i in 0..=num_children {
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

        // Volume from the first child.
        let volume = vec![VolumeSource {
            source_id: uuid_from_index(1),
            cv_amount: cv,
        }];

        let result =
            calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

        prop_assert_eq!(result.len(), 1);
        prop_assert_eq!(result[0].earner_id, uuid_from_index(0));
        prop_assert_eq!(result[0].level, 1);
    }
}

/// When a distributor has zero active frontline legs and the only
/// tier requires at least 1 active leg, their depth is unrestricted
/// (tier doesn't match, falls back to config max_depth). This tests
/// that a leaf node with no children gets no tier restriction.
#[test]
fn active_leg_tiers_no_match_uses_config_depth() {
    let max_depth: u8 = 5;
    let eligibility = CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![ActiveLegTier {
            min_active_legs: 3,
            max_commission_depth: 2,
        }],
    };
    let (plan, structure) = build_unilevel_plan_with_eligibility(max_depth, eligibility);

    // root -> child -> grandchild
    // root has 1 active leg (child). Tier requires 3. No tier match.
    // root falls back to config max_depth=5. Should earn at level 2.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    tree.add_node(
        uuid_from_index(1),
        uuid_from_index(0),
        uuid_from_index(0),
        1,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(1),
        uuid_from_index(1),
        2,
    )
    .unwrap();

    let mut snapshots = HashMap::new();
    for i in 0..3 {
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
        source_id: uuid_from_index(2),
        cv_amount: 100.0,
    }];

    let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

    // Both node 1 (level 1) and node 0 (level 2) should earn.
    // Node 0 has 1 active leg but needs 3 for the tier. No restriction.
    assert_eq!(
        result.len(),
        2,
        "Both upline nodes should earn when no tier matches"
    );
}

/// active_leg_tiers with multiple tiers: the highest qualifying tier
/// determines depth. Distributors with more active legs earn deeper.
#[test]
fn active_leg_tiers_more_legs_earn_deeper() {
    let max_depth: u8 = 10;
    let eligibility = CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![
            ActiveLegTier {
                min_active_legs: 1,
                max_commission_depth: 2,
            },
            ActiveLegTier {
                min_active_legs: 3,
                max_commission_depth: 5,
            },
        ],
    };
    let (plan, structure) = build_unilevel_plan_with_eligibility(max_depth, eligibility);

    // Tree: root(0) has 3 children (1, 2, 3). Child 1 has a child (4).
    // Volume from node 4.
    // root: 3 active legs -> tier 2 (depth 5). Should earn at level 2.
    // child 1: 1 active leg (node 4) -> tier 1 (depth 2). Earns at level 1.
    let mut tree = UnilevelTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    tree.add_node(
        uuid_from_index(1),
        uuid_from_index(0),
        uuid_from_index(0),
        1,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(0),
        uuid_from_index(0),
        2,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(3),
        uuid_from_index(0),
        uuid_from_index(0),
        3,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(4),
        uuid_from_index(1),
        uuid_from_index(1),
        4,
    )
    .unwrap();

    let mut snapshots = HashMap::new();
    for i in 0..5 {
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
        source_id: uuid_from_index(4),
        cv_amount: 100.0,
    }];

    let result = calculate_unilevel(&tree, &plan, &structure, &snapshots, &volume).unwrap();

    // Node 1 earns at level 1, root earns at level 2.
    assert_eq!(result.len(), 2);
    let node1_earning = result.iter().find(|e| e.earner_id == uuid_from_index(1));
    let root_earning = result.iter().find(|e| e.earner_id == uuid_from_index(0));
    assert!(node1_earning.is_some(), "Node 1 should earn");
    assert!(root_earning.is_some(), "Root should earn");
}

/// An empty unilevel tree with no volume produces empty earnings.
#[test]
fn empty_tree_no_volume_no_panic() {
    let (plan, structure) = build_unilevel_plan(5);

    // Empty tree: no root, no nodes.
    // Cannot produce volume for a node that doesn't exist.
    // Passing empty volume should produce empty earnings.
    let tree = UnilevelTree::new();
    let result = calculate_unilevel(&tree, &plan, &structure, &HashMap::new(), &[]);
    assert!(result.is_ok());
    assert!(result.unwrap().is_empty());
}
