mod common;
use common::{build_binary_plan, default_pairing, uuid_from_index};

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_binary_pairing};
use network_engine::config::binary::{PairingCalculation, PairingConfig, VolumeAfterPayout};
use network_engine::tree::binary::BinaryTree;
use proptest::prelude::*;
use std::collections::HashMap;

fn member_snapshot() -> DistributorSnapshot {
    DistributorSnapshot {
        rank: "member".to_string(),
        personal_volume: 100.0,
        status: "active".to_string(),
        has_order_in_period: true,
    }
}

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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan_wl, &structure_wl, &snapshots, &volume, &HashMap::new(),
        ).unwrap();
        let result_vr = calculate_binary_pairing(
            &tree, &plan_vr, &structure_vr, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan_wl, &structure_wl, &snapshots, &volume, &HashMap::new(),
        ).unwrap();
        let result_vr = calculate_binary_pairing(
            &tree, &plan_vr, &structure_vr, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
            &tree, &plan, &structure, &snapshots, &volume, &HashMap::new(),
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
    )
    .unwrap();

    assert!(result.earnings.is_empty());
    assert!(result.carry_forward.is_empty());
}
