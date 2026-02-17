use network_engine::commission::{DistributorSnapshot, VolumeSource, calculate_unilevel};
use network_engine::config::bonus::BonusConfig;
use network_engine::config::commission::{
    CompressionConfig, CompressionMode, LevelCommissionConfig,
};
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::payout::{CapEnforcement, CapsConfig, PaymentMethod, PayoutConfig};
use network_engine::config::period::{PeriodConfig, PeriodLength};
use network_engine::config::placement::PlacementConfig;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
};
use network_engine::config::volume::VolumeConfig;
use network_engine::config::{CompensationPlan, StructureConfig, UnilevelStructureConfig};
use network_engine::tree::unilevel::UnilevelTree;
use proptest::prelude::*;
use std::collections::{BTreeMap, HashMap};
use uuid::Uuid;

/// Generates a deterministic UUID from an index.
fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_be_bytes();
    Uuid::from_bytes(bytes)
}

/// Build a minimal compensation plan and unilevel structure for property tests.
///
/// Rate table: single rank "member" with 0.05 at every level up to max_depth.
/// Eligibility: minimum_pv = 0.0 (everyone eligible), no order requirement,
/// empty eligible_statuses (all statuses eligible).
fn build_test_plan(max_depth: u8) -> (CompensationPlan, UnilevelStructureConfig) {
    let mut rate_table = BTreeMap::new();
    let mut rates = BTreeMap::new();
    for level in 1..=max_depth {
        rates.insert(level, 0.05);
    }
    rate_table.insert("member".to_string(), rates);

    let structure = UnilevelStructureConfig {
        name: "Test".to_string(),
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth,
            rate_table,
        },
        compression: None,
    };

    let plan = CompensationPlan {
        name: "Property Test Plan".to_string(),
        version: 1,
        structures: vec![StructureConfig::Unilevel(structure.clone())],
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
            qualified_structures: vec!["Test".to_string()],
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
            payment_methods: vec![PaymentMethod {
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

/// Like build_test_plan but with min_personal_volume set to create
/// eligible/ineligible distributors based on PV.
fn build_test_plan_with_min_pv(
    max_depth: u8,
    min_pv: f64,
) -> (CompensationPlan, UnilevelStructureConfig) {
    let (mut plan, structure) = build_test_plan(max_depth);
    plan.eligibility.minimum_pv = min_pv;
    (plan, structure)
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
        let (plan, structure) = build_test_plan(max_depth);

        // Build a chain: 0 -> 1 -> 2 -> ... -> tree_size-1
        let mut tree = UnilevelTree::new();
        tree.add_root(uuid_from_index(0), 0).unwrap();
        for i in 1..tree_size {
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), i as i64)
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
        let (plan, structure) = build_test_plan(3);
        let broad_pct = structure.level_commission.broad_commission_percent;
        let multiplier = plan.volume.volume_to_dollar_multiplier;
        let rate = 0.05; // all levels use 0.05

        let mut tree = UnilevelTree::new();
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
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), i as i64)
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
            tree.add_node(uuid_from_index(i), uuid_from_index(i - 1), i as i64)
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
}
