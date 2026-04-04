mod common;
use common::{
    build_binary_cycle_step_plan, build_binary_plan, default_pairing, member_snapshot,
    uuid_from_index,
};

use network_engine::commission::{VolumeSource, calculate_binary_pairing};
use network_engine::config::binary::{
    CycleStep, CycleStepConfig, MultiPositionCapMode, PairingCalculation, PairingConfig,
    VolumeAfterPayout,
};
use network_engine::tree::binary::BinaryTree;
use proptest::prelude::*;
use std::collections::HashMap;

/// Build a 3-node binary tree (root with left and right children).
fn three_node_tree() -> BinaryTree {
    let mut tree = BinaryTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    tree.add_node(
        uuid_from_index(1),
        uuid_from_index(0),
        0,
        uuid_from_index(0),
        1,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(0),
        1,
        uuid_from_index(0),
        2,
    )
    .unwrap();
    tree
}

/// Generate a random volume amount (positive, finite).
fn arb_volume() -> impl Strategy<Value = f64> {
    0.0..10000.0f64
}

proptest! {
    /// Binary pays only the direct parent (level 1). No deeper levels
    /// should receive earnings. In a deeper tree, only nodes whose own
    /// left and right subtrees both have volume should earn.
    ///
    /// This test builds a 4-level tree and verifies each earning
    /// comes from a node that has volume in both legs.
    #[test]
    fn no_earning_beyond_single_level(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        // Build a deeper tree:
        //       0
        //      / \
        //     1   2
        //    / \
        //   3   4
        let mut tree = BinaryTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        tree.add_node(uuid_from_index(1), uuid_from_index(0), 0, uuid_from_index(0), 1).unwrap();
        tree.add_node(uuid_from_index(2), uuid_from_index(0), 1, uuid_from_index(0), 2).unwrap();
        tree.add_node(uuid_from_index(3), uuid_from_index(1), 0, uuid_from_index(1), 3).unwrap();
        tree.add_node(uuid_from_index(4), uuid_from_index(1), 1, uuid_from_index(1), 4).unwrap();

        let (plan, structure) = build_binary_plan(default_pairing());

        let mut snapshots = HashMap::new();
        for i in 0..5 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        // Volume at leaves of node 1's subtree (3 and 4) and at node 2.
        let volume = vec![
            VolumeSource { source_id: uuid_from_index(3), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(4), cv_amount: right_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: 100.0 },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        // Every earner should be one whose own subtrees had volume on both sides.
        // Binary doesn't have "levels" in the unilevel sense. Each node earns
        // based on its own left/right subtree volume. No "pass-through" earnings.
        for earning in &result.earnings {
            prop_assert!(
                earning.matched_volume > 0.0,
                "Earner {} has zero matched volume but got an earning",
                earning.earner_id
            );
            prop_assert!(
                earning.left_volume > 0.0 && earning.right_volume > 0.0,
                "Earner {} earned without volume in both legs (left={}, right={})",
                earning.earner_id, earning.left_volume, earning.right_volume
            );
        }
    }

    /// Carry-forward values must never be negative regardless of volume
    /// distribution or carry-forward mode.
    #[test]
    fn carry_forward_always_non_negative(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = PairingConfig {
            volume_after_payout: VolumeAfterPayout::CarryForward,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for (uid, legs) in &result.carry_forward {
            prop_assert!(
                legs.left >= 0.0,
                "Distributor {} has negative left carry-forward: {}",
                uid, legs.left
            );
            prop_assert!(
                legs.right >= 0.0,
                "Distributor {} has negative right carry-forward: {}",
                uid, legs.right
            );
        }
    }

    /// Dollar amount must equal: matched_volume * percent * multiplier * ratio.
    ///
    /// Uses WeakerLeg mode (ratio=1.0) for simplicity. Two-node test
    /// isolates the formula.
    #[test]
    fn dollar_amount_matches_formula(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = default_pairing(); // percent=0.10, WeakerLeg, multiplier=1.0
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let matched = left_vol.min(right_vol);
        if matched == 0.0 {
            // No earning expected.
            prop_assert!(result.earnings.is_empty());
        } else {
            prop_assert_eq!(result.earnings.len(), 1);
            let earning = &result.earnings[0];
            let expected = matched * 0.10 * 1.0 * 1.0; // matched * percent * multiplier * ratio
            let diff = (earning.dollar_amount - expected).abs();
            prop_assert!(
                diff < 1e-10,
                "Dollar amount {} != expected {}",
                earning.dollar_amount,
                expected
            );
        }
    }

    /// When a cap is set, no earning's dollar_amount should exceed it.
    #[test]
    fn total_payout_never_exceeds_cap(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let cap = 25.0;
        let pairing = PairingConfig {
            cap_per_period: Some(cap),
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for earning in &result.earnings {
            prop_assert!(
                earning.dollar_amount <= cap + 1e-10,
                "Earning {} exceeded cap {}",
                earning.dollar_amount,
                cap
            );
        }
    }

    // --- NetOff carry-forward mode ---

    /// NetOff subtracts matched volume from both legs. The stronger leg
    /// carries forward only the difference. The weaker leg becomes zero.
    #[test]
    fn net_off_carry_forward_equals_difference(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = PairingConfig {
            volume_after_payout: VolumeAfterPayout::NetOff,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let root_cf = result.carry_forward.get(&uuid_from_index(0)).unwrap();
        let matched = left_vol.min(right_vol);

        // NetOff: both legs subtract matched. Stronger keeps the excess.
        let expected_left = (left_vol - matched).max(0.0);
        let expected_right = (right_vol - matched).max(0.0);

        prop_assert!(
            (root_cf.left - expected_left).abs() < 1e-10,
            "NetOff left carry-forward: got {}, expected {}",
            root_cf.left, expected_left
        );
        prop_assert!(
            (root_cf.right - expected_right).abs() < 1e-10,
            "NetOff right carry-forward: got {}, expected {}",
            root_cf.right, expected_right
        );
    }

    /// NetOff carry-forward values are always non-negative.
    #[test]
    fn net_off_carry_forward_non_negative(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = PairingConfig {
            volume_after_payout: VolumeAfterPayout::NetOff,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for (uid, legs) in &result.carry_forward {
            prop_assert!(
                legs.left >= 0.0,
                "NetOff: distributor {} has negative left carry-forward: {}",
                uid, legs.left
            );
            prop_assert!(
                legs.right >= 0.0,
                "NetOff: distributor {} has negative right carry-forward: {}",
                uid, legs.right
            );
        }
    }

    /// NetOff total carry-forward (left + right) never exceeds the
    /// absolute difference between the two legs.
    #[test]
    fn net_off_total_carry_never_exceeds_leg_difference(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = PairingConfig {
            volume_after_payout: VolumeAfterPayout::NetOff,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let root_cf = result.carry_forward.get(&uuid_from_index(0)).unwrap();
        let total_cf = root_cf.left + root_cf.right;
        let leg_diff = (left_vol - right_vol).abs();

        prop_assert!(
            total_cf <= leg_diff + 1e-10,
            "NetOff total carry {} exceeded leg difference {}",
            total_cf, leg_diff
        );
    }

    // --- VolumeRatio calculation mode ---

    /// VolumeRatio commission is always <= WeakerLeg commission for the
    /// same volumes. The ratio factor (min/max) penalizes imbalance.
    #[test]
    fn volume_ratio_never_exceeds_weaker_leg(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let weaker_leg_pairing = default_pairing(); // WeakerLeg
        let ratio_pairing = PairingConfig {
            calculation: PairingCalculation::VolumeRatio,
            ..default_pairing()
        };

        let (plan_wl, structure_wl) = build_binary_plan(weaker_leg_pairing);
        let (plan_vr, structure_vr) = build_binary_plan(ratio_pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result_wl = calculate_binary_pairing(
            &tree, &plan_wl, &structure_wl, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();
        let result_vr = calculate_binary_pairing(
            &tree, &plan_vr, &structure_vr, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let payout_wl: f64 = result_wl.earnings.iter().map(|e| e.dollar_amount).sum();
        let payout_vr: f64 = result_vr.earnings.iter().map(|e| e.dollar_amount).sum();

        prop_assert!(
            payout_vr <= payout_wl + 1e-10,
            "VolumeRatio payout {} exceeded WeakerLeg payout {}",
            payout_vr, payout_wl
        );
    }

    /// VolumeRatio: when legs are balanced (equal), the ratio is 1.0
    /// and the payout equals WeakerLeg payout.
    #[test]
    fn volume_ratio_balanced_equals_weaker_leg(
        vol in 1.0..10000.0f64,
    ) {
        let weaker_leg_pairing = default_pairing();
        let ratio_pairing = PairingConfig {
            calculation: PairingCalculation::VolumeRatio,
            ..default_pairing()
        };

        let (plan_wl, structure_wl) = build_binary_plan(weaker_leg_pairing);
        let (plan_vr, structure_vr) = build_binary_plan(ratio_pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        // Equal volume on both legs.
        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: vol },
        ];

        let result_wl = calculate_binary_pairing(
            &tree, &plan_wl, &structure_wl, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();
        let result_vr = calculate_binary_pairing(
            &tree, &plan_vr, &structure_vr, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        prop_assert_eq!(result_wl.earnings.len(), 1);
        prop_assert_eq!(result_vr.earnings.len(), 1);

        let diff = (result_wl.earnings[0].dollar_amount - result_vr.earnings[0].dollar_amount).abs();
        prop_assert!(
            diff < 1e-10,
            "Balanced VolumeRatio ({}) should equal WeakerLeg ({})",
            result_vr.earnings[0].dollar_amount,
            result_wl.earnings[0].dollar_amount
        );
    }

    /// VolumeRatio: the ratio reported on each earning is min/max of
    /// the two legs, and is always between 0.0 and 1.0 (inclusive).
    #[test]
    fn volume_ratio_ratio_in_unit_range(
        (left_vol, right_vol) in (arb_volume(), arb_volume()),
    ) {
        let pairing = PairingConfig {
            calculation: PairingCalculation::VolumeRatio,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for earning in &result.earnings {
            prop_assert!(
                earning.ratio >= 0.0 && earning.ratio <= 1.0 + 1e-10,
                "VolumeRatio ratio {} outside [0, 1] range",
                earning.ratio
            );
        }
    }

    // --- Empty and single-node tree edge cases ---

    /// A single-node binary tree produces no earnings. Root has no
    /// children so both legs are zero.
    #[test]
    fn single_node_tree_no_earnings(
        vol in arb_volume(),
    ) {
        let (plan, structure) = build_binary_plan(default_pairing());
        let mut tree = BinaryTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid_from_index(0), member_snapshot());

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(0), cv_amount: vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        prop_assert!(
            result.earnings.is_empty(),
            "Single-node tree should produce no earnings"
        );
    }

    /// A tree where root has only one child (no right leg) produces
    /// no earnings. Matched volume is zero because one leg is empty.
    #[test]
    fn root_with_one_child_no_earnings(
        vol in arb_volume(),
    ) {
        let (plan, structure) = build_binary_plan(default_pairing());
        let mut tree = BinaryTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        tree.add_node(uuid_from_index(1), uuid_from_index(0), 0, uuid_from_index(0), 1).unwrap();

        let mut snapshots = HashMap::new();
        snapshots.insert(uuid_from_index(0), member_snapshot());
        snapshots.insert(uuid_from_index(1), member_snapshot());

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        prop_assert!(
            result.earnings.is_empty(),
            "Root with only one child should produce no earnings"
        );
    }
}

/// An empty binary tree produces no earnings and no carry-forward.
#[test]
fn empty_tree_no_panic() {
    let (plan, structure) = build_binary_plan(default_pairing());
    let tree = BinaryTree::new();

    let result = calculate_binary_pairing(
        &tree,
        &plan,
        &structure,
        &HashMap::new(),
        &[],
        &HashMap::new(),
        None,
    )
    .unwrap();

    assert!(result.earnings.is_empty());
    assert!(result.carry_forward.is_empty());
}

// --- Multi-position property tests ---

/// Build a 7-node binary tree for multi-position property tests:
///
/// ```text
///        root (0)
///       /        \
///    pos1 (1)   pos2 (2)
///    /    \       /    \
///  (3)   (4)    (5)   (6)
/// ```
fn multi_position_tree() -> BinaryTree {
    let mut tree = BinaryTree::new();
    tree.add_root(uuid_from_index(0), 0).unwrap();
    tree.add_node(
        uuid_from_index(1),
        uuid_from_index(0),
        0,
        uuid_from_index(0),
        1,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(2),
        uuid_from_index(0),
        1,
        uuid_from_index(0),
        2,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(3),
        uuid_from_index(1),
        0,
        uuid_from_index(1),
        3,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(4),
        uuid_from_index(1),
        1,
        uuid_from_index(1),
        4,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(5),
        uuid_from_index(2),
        0,
        uuid_from_index(2),
        5,
    )
    .unwrap();
    tree.add_node(
        uuid_from_index(6),
        uuid_from_index(2),
        1,
        uuid_from_index(2),
        6,
    )
    .unwrap();
    tree
}

/// Owner UUID that isn't a tree node.
fn owner_uuid() -> uuid::Uuid {
    uuid_from_index(200)
}

proptest! {
    /// With aggregate cap mode and an ownership map, the per-owner total
    /// payout never exceeds the cap regardless of volume distribution.
    #[test]
    fn multi_position_total_payout_respects_aggregate_cap(
        cap in 10.0..1000.0f64,
        left1 in 0.0..5000.0f64,
        right1 in 0.0..5000.0f64,
        left2 in 0.0..5000.0f64,
        right2 in 0.0..5000.0f64,
    ) {
        let pairing = PairingConfig {
            cap_per_period: Some(cap),
            multi_position_cap_mode: MultiPositionCapMode::Aggregate,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = multi_position_tree();

        let owner = owner_uuid();
        let mut ownership = HashMap::new();
        ownership.insert(uuid_from_index(1), owner);
        ownership.insert(uuid_from_index(2), owner);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner, member_snapshot());
        snapshots.insert(uuid_from_index(0), member_snapshot());
        for i in 3..=6 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(3), cv_amount: left1 },
            VolumeSource { source_id: uuid_from_index(4), cv_amount: right1 },
            VolumeSource { source_id: uuid_from_index(5), cv_amount: left2 },
            VolumeSource { source_id: uuid_from_index(6), cv_amount: right2 },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), Some(&ownership),
        ).unwrap();

        let owner_total: f64 = result.earnings.iter()
            .filter(|e| e.earner_id == owner)
            .map(|e| e.dollar_amount)
            .sum();

        prop_assert!(
            owner_total <= cap + 1e-10,
            "owner total {} exceeded aggregate cap {}",
            owner_total, cap
        );
    }

    /// Each position's carry-forward is independent of other positions
    /// owned by the same person.
    #[test]
    fn multi_position_carry_forward_independent_per_position(
        left1 in 0.0..5000.0f64,
        right1 in 0.0..5000.0f64,
        left2 in 0.0..5000.0f64,
        right2 in 0.0..5000.0f64,
    ) {
        let pairing = PairingConfig {
            volume_after_payout: VolumeAfterPayout::CarryForward,
            ..default_pairing()
        };
        let (plan, structure) = build_binary_plan(pairing);
        let tree = multi_position_tree();

        let owner = owner_uuid();
        let mut ownership = HashMap::new();
        ownership.insert(uuid_from_index(1), owner);
        ownership.insert(uuid_from_index(2), owner);

        let mut snapshots = HashMap::new();
        snapshots.insert(owner, member_snapshot());
        snapshots.insert(uuid_from_index(0), member_snapshot());
        for i in 3..=6 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(3), cv_amount: left1 },
            VolumeSource { source_id: uuid_from_index(4), cv_amount: right1 },
            VolumeSource { source_id: uuid_from_index(5), cv_amount: left2 },
            VolumeSource { source_id: uuid_from_index(6), cv_amount: right2 },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), Some(&ownership),
        ).unwrap();

        // Each position must have its own carry-forward entry keyed by position_id.
        let cf1 = result.carry_forward.get(&uuid_from_index(1));
        let cf2 = result.carry_forward.get(&uuid_from_index(2));

        prop_assert!(cf1.is_some(), "pos1 carry-forward missing");
        prop_assert!(cf2.is_some(), "pos2 carry-forward missing");

        let cf1 = cf1.unwrap();
        let cf2 = cf2.unwrap();

        // pos1 carry-forward is derived only from its own legs (left1, right1).
        // CarryForward mode: weaker leg zeroed, stronger keeps excess.
        let matched1 = left1.min(right1);
        if left1 <= right1 {
            prop_assert!(cf1.left.abs() < 1e-10, "pos1 left should be 0, got {}", cf1.left);
            prop_assert!((cf1.right - (right1 - matched1)).abs() < 1e-10,
                "pos1 right carry should be {}, got {}", right1 - matched1, cf1.right);
        } else {
            prop_assert!((cf1.left - (left1 - matched1)).abs() < 1e-10,
                "pos1 left carry should be {}, got {}", left1 - matched1, cf1.left);
            prop_assert!(cf1.right.abs() < 1e-10, "pos1 right should be 0, got {}", cf1.right);
        }

        // pos2 carry-forward is derived only from its own legs (left2, right2).
        let matched2 = left2.min(right2);
        if left2 <= right2 {
            prop_assert!(cf2.left.abs() < 1e-10, "pos2 left should be 0, got {}", cf2.left);
            prop_assert!((cf2.right - (right2 - matched2)).abs() < 1e-10,
                "pos2 right carry should be {}, got {}", right2 - matched2, cf2.right);
        } else {
            prop_assert!((cf2.left - (left2 - matched2)).abs() < 1e-10,
                "pos2 left carry should be {}, got {}", left2 - matched2, cf2.left);
            prop_assert!(cf2.right.abs() < 1e-10, "pos2 right should be 0, got {}", cf2.right);
        }
    }
}

// --- CycleStep property tests ---

/// Generate a random CycleStep config with 1-4 steps.
///
/// Steps are sorted ascending by threshold and deduplicated to avoid
/// near-duplicate thresholds that would fail validation.
fn arb_cycle_step_config() -> impl Strategy<Value = CycleStepConfig> {
    (
        prop::collection::vec((100.0..5000.0f64, 10.0..500.0f64), 1..=4),
        prop::bool::ANY,
    )
        .prop_map(|(pairs, use_flush)| {
            let mut steps: Vec<CycleStep> = pairs
                .into_iter()
                .map(|(threshold, amount)| CycleStep { threshold, amount })
                .collect();
            steps.sort_by(|a, b| a.threshold.total_cmp(&b.threshold));
            // Deduplicate thresholds that are too close together.
            steps.dedup_by(|a, b| (a.threshold - b.threshold).abs() < 1.0);
            CycleStepConfig {
                steps,
                volume_after_cycle: if use_flush {
                    VolumeAfterPayout::FullFlush
                } else {
                    VolumeAfterPayout::CarryForward
                },
                cap_per_period: None,
                carry_forward_cap: None,
                multi_position_cap_mode: MultiPositionCapMode::PerPosition,
            }
        })
}

proptest! {
    /// All CycleStep earnings are non-negative for any valid config and
    /// any non-negative leg volumes.
    #[test]
    fn cycle_step_earnings_never_negative(
        config in arb_cycle_step_config(),
        left_vol in arb_volume(),
        right_vol in arb_volume(),
    ) {
        let (plan, structure) = build_binary_cycle_step_plan(config);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for earning in &result.earnings {
            prop_assert!(
                earning.dollar_amount >= 0.0,
                "CycleStep earning {} is negative",
                earning.dollar_amount
            );
        }
    }

    /// Total earnings never exceed sum_of_all_step_amounts * max_possible_cycles.
    ///
    /// For FullFlush mode, at most 1 cycle can complete because volume is
    /// zeroed after the cycle. For CarryForward, an upper bound on cycles
    /// is floor(min(left, right) / highest_threshold) + 1.
    #[test]
    fn cycle_step_earnings_bounded_by_max_cycles(
        config in arb_cycle_step_config(),
        left_vol in arb_volume(),
        right_vol in arb_volume(),
    ) {
        let is_flush = matches!(config.volume_after_cycle, VolumeAfterPayout::FullFlush);
        let sum_of_amounts: f64 = config.steps.iter().map(|s| s.amount).sum();
        let highest_threshold = config.steps.iter()
            .map(|s| s.threshold)
            .fold(0.0f64, f64::max);

        let (plan, structure) = build_binary_cycle_step_plan(config);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let total_payout: f64 = result.earnings.iter().map(|e| e.dollar_amount).sum();

        let max_cycles = if is_flush {
            1.0
        } else {
            let matched = left_vol.min(right_vol);
            if highest_threshold > 0.0 {
                (matched / highest_threshold).floor() + 1.0
            } else {
                1.0
            }
        };

        let upper_bound = sum_of_amounts * max_cycles;
        prop_assert!(
            total_payout <= upper_bound + 1e-10,
            "Total payout {} exceeded upper bound {} (sum_amounts={}, max_cycles={})",
            total_payout, upper_bound, sum_of_amounts, max_cycles
        );
    }

    /// When volume_after_cycle is FullFlush and a cycle completes (both
    /// legs >= highest threshold), carry-forward legs for root are both 0.
    #[test]
    fn cycle_step_full_flush_zeroes_carry_forward(
        config in arb_cycle_step_config(),
        extra in 0.0..5000.0f64,
    ) {
        let mut config = config;
        config.volume_after_cycle = VolumeAfterPayout::FullFlush;

        let highest_threshold = config.steps.iter()
            .map(|s| s.threshold)
            .fold(0.0f64, f64::max);

        // Ensure both legs meet the highest threshold to guarantee cycle completion.
        let left_vol = highest_threshold + extra;
        let right_vol = highest_threshold + extra;

        let (plan, structure) = build_binary_cycle_step_plan(config);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        let root_cf = result.carry_forward.get(&uuid_from_index(0)).unwrap();
        prop_assert!(
            root_cf.left.abs() < 1e-10,
            "FullFlush: root left carry-forward should be 0, got {}",
            root_cf.left
        );
        prop_assert!(
            root_cf.right.abs() < 1e-10,
            "FullFlush: root right carry-forward should be 0, got {}",
            root_cf.right
        );
    }

    /// When CarryForward mode and a cycle completes, the weaker leg in
    /// carry-forward is less than the highest step threshold. If both
    /// legs were still >= highest threshold, another cycle would have
    /// been processed and subtracted.
    #[test]
    fn cycle_step_carry_forward_legs_below_highest_threshold(
        config in arb_cycle_step_config(),
        extra in 0.0..5000.0f64,
    ) {
        let highest_threshold = config.steps.iter()
            .map(|s| s.threshold)
            .fold(0.0f64, f64::max);

        // Ensure both legs meet the highest threshold.
        let left_vol = highest_threshold + extra;
        let right_vol = highest_threshold + extra;

        // Override to CarryForward mode.
        let cf_config = CycleStepConfig {
            volume_after_cycle: VolumeAfterPayout::CarryForward,
            ..config
        };

        let (plan, structure) = build_binary_cycle_step_plan(cf_config);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        // After all cycles, the weaker leg must be below the highest
        // threshold. Otherwise another cycle would have completed.
        let root_cf = result.carry_forward.get(&uuid_from_index(0)).unwrap();
        let weaker = root_cf.left.min(root_cf.right);
        prop_assert!(
            weaker < highest_threshold + 1e-10,
            "CarryForward: weaker leg {} still >= highest threshold {}",
            weaker, highest_threshold
        );
    }

    /// When carry_forward_cap is set, neither leg in carry-forward
    /// exceeds the cap.
    #[test]
    fn cycle_step_carry_forward_cap_respected(
        config in arb_cycle_step_config(),
        left_vol in arb_volume(),
        right_vol in arb_volume(),
    ) {
        let cap = 100.0;

        // Override to CarryForward mode with a cap.
        let cf_config = CycleStepConfig {
            volume_after_cycle: VolumeAfterPayout::CarryForward,
            carry_forward_cap: Some(cap),
            ..config
        };

        let (plan, structure) = build_binary_cycle_step_plan(cf_config);
        let tree = three_node_tree();

        let mut snapshots = HashMap::new();
        for i in 0..3 {
            snapshots.insert(uuid_from_index(i), member_snapshot());
        }

        let volume = vec![
            VolumeSource { source_id: uuid_from_index(1), cv_amount: left_vol },
            VolumeSource { source_id: uuid_from_index(2), cv_amount: right_vol },
        ];

        let result = calculate_binary_pairing(
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(), None,
        ).unwrap();

        for (uid, legs) in &result.carry_forward {
            prop_assert!(
                legs.left <= cap + 1e-10,
                "Distributor {} left carry-forward {} exceeds cap {}",
                uid, legs.left, cap
            );
            prop_assert!(
                legs.right <= cap + 1e-10,
                "Distributor {} right carry-forward {} exceeds cap {}",
                uid, legs.right, cap
            );
        }
    }
}
