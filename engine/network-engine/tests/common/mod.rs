// Each integration test compiles this module independently. Functions used
// only by some test files appear unused in others.
#![allow(dead_code)]

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
use network_engine::config::{
    BinaryStructureConfig, CompensationPlan, StructureConfig, UnilevelStructureConfig,
};
use std::collections::BTreeMap;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// UUID helpers
// ---------------------------------------------------------------------------

/// Generates a deterministic UUID from an index.
///
/// Uses little-endian byte representation so that values below 256 produce
/// the same UUID as the unit-test helper `tree::test_helpers::test_uuid`.
/// Shared across all integration/property test files.
pub fn uuid_from_index(i: usize) -> Uuid {
    let bytes = (i as u128).to_le_bytes();
    Uuid::from_bytes(bytes)
}

// ---------------------------------------------------------------------------
// CompensationPlan builders
// ---------------------------------------------------------------------------

/// Build a base `CompensationPlan` with sensible test defaults.
///
/// Callers provide eligibility rules, a pre-wrapped `StructureConfig`, and
/// the structure name used in `qualified_structures`. Ranks, bonuses,
/// payout, caps, and placement use minimal values suitable for property
/// tests.
pub fn build_base_plan(
    eligibility: CommissionEligibility,
    structure: StructureConfig,
    structure_name: &str,
) -> CompensationPlan {
    CompensationPlan {
        name: "Test Plan".to_string(),
        version: 1,
        structures: vec![structure],
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
            qualified_structures: vec![structure_name.to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        }],
        rank_tracking: RankTrackingConfig {
            track_achieved_rank: false,
        },
        rank_features: RankFeaturesConfig {
            constraints_enabled: false,
            overrides_enabled: false,
        },
        eligibility,
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
    }
}

/// Default eligibility for property tests.
///
/// Minimum PV = 0 (everyone eligible), no order requirement, empty
/// eligible_statuses (all statuses accepted).
pub fn permissive_eligibility() -> CommissionEligibility {
    CommissionEligibility {
        minimum_pv: 0.0,
        require_order_in_period: false,
        eligible_statuses: vec![],
        active_leg_tiers: vec![],
    }
}

// --- Unilevel plan builder ---

/// Build a unilevel plan for property tests.
///
/// Rate table: single rank "member" with the given rate at every level up to
/// `max_depth`. Eligibility is fully permissive (everyone qualifies).
pub fn build_unilevel_plan(max_depth: u8) -> (CompensationPlan, UnilevelStructureConfig) {
    build_unilevel_plan_with_eligibility(max_depth, permissive_eligibility())
}

/// Build a unilevel plan with custom eligibility for property tests.
pub fn build_unilevel_plan_with_eligibility(
    max_depth: u8,
    eligibility: CommissionEligibility,
) -> (CompensationPlan, UnilevelStructureConfig) {
    use network_engine::config::commission::LevelCommissionConfig;

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

    let plan = build_base_plan(
        eligibility,
        StructureConfig::Unilevel(structure.clone()),
        "Test",
    );

    (plan, structure)
}

// --- Binary plan builder ---

/// Build a binary plan with default pairing config for property tests.
pub fn build_binary_plan(pairing: PairingConfig) -> (CompensationPlan, BinaryStructureConfig) {
    let structure = BinaryStructureConfig {
        name: "Test Binary".to_string(),
        binary_commission: BinaryCommissionConfig {
            volume_to_dollar_multiplier: None,
            mode: BinaryCommissionMode::Pairing(pairing),
        },
    };

    let plan = build_base_plan(
        permissive_eligibility(),
        StructureConfig::Binary(structure.clone()),
        "Test Binary",
    );

    (plan, structure)
}

/// Default pairing config for binary property tests.
///
/// 10% on weaker leg, full flush, no cap.
pub fn default_pairing() -> PairingConfig {
    PairingConfig {
        percent: 0.10,
        calculation: PairingCalculation::WeakerLeg,
        cap_per_period: None,
        volume_after_payout: VolumeAfterPayout::FullFlush,
        carry_forward_cap: None,
    }
}

// --- Multi-rank unilevel plan builder ---

/// Build a unilevel plan with two ranks (associate ordinal 1, silver ordinal 2)
/// and custom eligibility. Both ranks use the same 5% rate at every level.
///
/// Useful for testing SkipBelowRank compression where rank ordinals matter.
pub fn build_two_rank_unilevel_plan(
    max_depth: u8,
    eligibility: CommissionEligibility,
) -> (CompensationPlan, UnilevelStructureConfig) {
    use network_engine::config::commission::LevelCommissionConfig;

    let mut rate_table = BTreeMap::new();
    for rank_name in &["associate", "silver"] {
        let mut rates = BTreeMap::new();
        for level in 1..=max_depth {
            rates.insert(level, 0.05);
        }
        rate_table.insert(rank_name.to_string(), rates);
    }

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

    let mut plan = build_base_plan(
        eligibility,
        StructureConfig::Unilevel(structure.clone()),
        "Test",
    );

    plan.ranks = vec![
        RankDefinition {
            name: "associate".to_string(),
            ordinal: 1,
            qualification: RankQualification {
                structures: vec![],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
        RankDefinition {
            name: "silver".to_string(),
            ordinal: 2,
            qualification: RankQualification {
                structures: vec![],
                required_products: vec![],
            },
            qualified_structures: vec!["Test".to_string()],
            demotion_policy: DemotionPolicy::PromotionOnly,
        },
    ];

    (plan, structure)
}
