mod common;
use common::uuid_from_index;

use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_binary_pairing};
use network_engine::config::binary::{
    BinaryCommissionConfig, BinaryCommissionMode, PairingCalculation, PairingConfig,
    VolumeAfterPayout,
};
use network_engine::config::bonus::BonusConfig;
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::payout::{CapEnforcement, CapsConfig, PayoutConfig, PayoutMethod};
use network_engine::config::period::{PeriodConfig, PeriodLength};
use network_engine::config::placement::PlacementConfig;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
};
use network_engine::config::volume::VolumeConfig;
use network_engine::config::{BinaryStructureConfig, CompensationPlan, StructureConfig};
use network_engine::tree::binary::BinaryTree;
use proptest::prelude::*;
use std::collections::HashMap;

/// Build a test binary plan through the public API.
fn build_binary_plan(pairing: PairingConfig) -> (CompensationPlan, BinaryStructureConfig) {
    let structure = BinaryStructureConfig {
        name: "Test Binary".to_string(),
        binary_commission: BinaryCommissionConfig {
            volume_to_dollar_multiplier: None,
            mode: BinaryCommissionMode::Pairing(pairing),
        },
    };

    let plan = CompensationPlan {
        name: "Binary Property Test Plan".to_string(),
        version: 1,
        structures: vec![StructureConfig::Binary(structure.clone())],
        period: PeriodConfig {
            length: PeriodLength::Month,
            start_date: chrono::NaiveDate::from_ymd_opt(2026, 3, 1).unwrap(),
            payout_lag_days: 14,
        },
        volume: VolumeConfig {
            inhibit_signup_volume: false,
            base_currency: "USD".to_string(),
            volume_to_dollar_multiplier: 1.0,
            deduct_qualifying_volume: false,
        },
        ranks: vec![RankDefinition {
            name: "member".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![],
                required_products: vec![],
            },
            qualified_structures: vec!["Test Binary".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }],
        rank_tracking: RankTrackingConfig {
            track_achieved_rank: false,
        },
        rank_features: RankFeaturesConfig {
            constraints_enabled: false,
            overrides_enabled: false,
        },
        eligibility: CommissionEligibility {
            minimum_pv: 0.0,
            require_order_in_period: false,
            eligible_statuses: vec![],
            active_leg_tiers: vec![],
        },
        bonuses: BonusConfig {
            matching: None,
            sponsor: None,
            fast_start: None,
            rank_advancement: None,
            leadership_development: None,
            infinity: None,
            lifestyle: None,
            pool: None,
            matrix_completion: None,
            position: None,
            board_cycling: None,
            pass_up: None,
        },
        payout: PayoutConfig {
            currency: "USD".to_string(),
            minimum_payout: 50.0,
            allow_partial_payout: true,
            payment_methods: vec![PayoutMethod {
                method_type: "bank_transfer".to_string(),
                fee: 2.50,
            }],
        },
        caps: CapsConfig {
            per_distributor_cap: None,
            company_payout_cap_percent: 0.42,
            enforcement: CapEnforcement::ProRata,
            enable_clawback: false,
        },
        placement: PlacementConfig {
            donated_placement: None,
            holding_tank: None,
            binary_placement: None,
        },
    };

    (plan, structure)
}

fn default_pairing() -> PairingConfig {
    PairingConfig {
        percent: 0.10,
        calculation: PairingCalculation::WeakerLeg,
        cap_per_period: None,
        volume_after_payout: VolumeAfterPayout::FullFlush,
        carry_forward_cap: None,
    }
}

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
}
