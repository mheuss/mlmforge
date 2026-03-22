// Each integration test compiles this module independently. Functions used
// only by some test files appear unused in others.
#![allow(dead_code)]

use network_engine::config::binary::{
    BinaryCommissionConfig, BinaryCommissionMode, MultiPositionCapMode, PairingCalculation,
    PairingConfig, VolumeAfterPayout,
};
use network_engine::config::bonus::BonusConfig;
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::payout::{CapEnforcement, CapsConfig, PayoutConfig, PayoutMethod};
use network_engine::config::period::{PeriodConfig, PeriodLength};
use network_engine::config::placement::PlacementConfig;
use network_engine::config::rank::{
    DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
};
use network_engine::config::stairstep::{BreakawayConfig, DifferentialConfig, OverrideMode};
use network_engine::config::volume::VolumeConfig;
use network_engine::config::{
    BinaryStructureConfig, CompensationPlan, StairstepStructureConfig, StructureConfig,
    UnilevelStructureConfig,
};
use std::collections::BTreeMap;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// UUID helpers
// ---------------------------------------------------------------------------

/// Generates a deterministic UUID from an index.
///
/// Uses little-endian byte representation with the high byte set to 0xFF
/// to avoid collisions with `Uuid::nil()`, which is used as the tombstone
/// sentinel in the arena.
pub fn uuid_from_index(i: usize) -> Uuid {
    let mut bytes = (i as u128).to_le_bytes();
    bytes[15] = 0xFF;
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
        pass_up: None,
    };

    let plan = build_base_plan(
        eligibility,
        StructureConfig::Unilevel(structure.clone()),
        "Test",
    );

    (plan, structure)
}

// --- Unilevel plan builder with pass-up ---

/// Build a unilevel plan with pass-up configuration for property tests.
pub fn build_unilevel_plan_with_pass_up(
    max_depth: u8,
    pass_up: network_engine::config::PassUpConfig,
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
        pass_up: Some(pass_up),
    };

    let plan = build_base_plan(
        permissive_eligibility(),
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
        multi_position_cap_mode: MultiPositionCapMode::PerPosition,
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
        pass_up: None,
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

// --- Stairstep plan builder ---

/// Build a stairstep plan for property tests.
///
/// Rate table: two ranks "member" and "director" with 5% at every level.
/// Breakaway threshold: "director". Differential: director=0.10, min_override=0.0.
/// No generation overrides. Eligibility is fully permissive.
pub fn build_stairstep_plan(
    max_depth: u8,
) -> (
    CompensationPlan,
    network_engine::config::StairstepStructureConfig,
) {
    build_stairstep_plan_with_eligibility(max_depth, permissive_eligibility())
}

pub fn build_stairstep_plan_with_eligibility(
    max_depth: u8,
    eligibility: CommissionEligibility,
) -> (
    CompensationPlan,
    network_engine::config::StairstepStructureConfig,
) {
    use network_engine::config::commission::LevelCommissionConfig;

    let mut rate_table = BTreeMap::new();
    for rank_name in &["member", "director"] {
        let mut rates = BTreeMap::new();
        for level in 1..=max_depth {
            rates.insert(level, 0.05);
        }
        rate_table.insert(rank_name.to_string(), rates);
    }

    let structure = StairstepStructureConfig {
        name: "Test".to_string(),
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth,
            rate_table,
        },
        compression: None,
        breakaway: Some(BreakawayConfig {
            threshold_rank: "director".to_string(),
            exclude_breakaway_gv: false,
            override_mode: OverrideMode::Differential(DifferentialConfig {
                rank_rates: {
                    let mut m = BTreeMap::new();
                    m.insert("director".to_string(), 0.10);
                    m
                },
                min_override: 0.0,
            }),
            generation_overrides: None,
        }),
    };

    let mut plan = build_base_plan(
        eligibility,
        StructureConfig::Stairstep(structure.clone()),
        "Test",
    );

    plan.ranks.push(RankDefinition {
        name: "director".to_string(),
        ordinal: 2,
        qualification: RankQualification {
            structures: vec![],
            required_products: vec![],
        },
        qualified_structures: vec!["Test".to_string()],
        demotion_policy: DemotionPolicy::PromotionOnly,
    });

    (plan, structure)
}

// --- Matrix plan builder ---

/// Build a matrix plan for property tests.
///
/// Rate table: single rank "member" with 5% at every level up to `max_depth`.
/// Eligibility is fully permissive.
pub fn build_matrix_plan(
    width: u8,
    height: u8,
    max_depth: u8,
) -> (
    CompensationPlan,
    network_engine::config::MatrixStructureConfig,
) {
    build_matrix_plan_with_eligibility(width, height, max_depth, permissive_eligibility())
}

/// Build a matrix plan with custom eligibility for property tests.
pub fn build_matrix_plan_with_eligibility(
    width: u8,
    height: u8,
    max_depth: u8,
    eligibility: CommissionEligibility,
) -> (
    CompensationPlan,
    network_engine::config::MatrixStructureConfig,
) {
    use network_engine::config::MatrixStructureConfig;
    use network_engine::config::commission::LevelCommissionConfig;
    use network_engine::config::matrix::{MatrixStructureParams, SpilloverDirection};

    let mut rate_table = BTreeMap::new();
    let mut rates = BTreeMap::new();
    for level in 1..=max_depth {
        rates.insert(level, 0.05);
    }
    rate_table.insert("member".to_string(), rates);

    let structure = MatrixStructureConfig {
        name: "Test".to_string(),
        matrix_params: MatrixStructureParams {
            width,
            height,
            spillover: SpilloverDirection::BreadthFirst,
        },
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth,
            rate_table,
        },
        compression: None,
        pruning: None,
    };

    let plan = build_base_plan(
        eligibility,
        StructureConfig::Matrix(structure.clone()),
        "Test",
    );

    (plan, structure)
}

/// Build a matrix plan with two ranks for SkipBelowRank tests.
pub fn build_two_rank_matrix_plan(
    width: u8,
    height: u8,
    max_depth: u8,
    eligibility: CommissionEligibility,
) -> (
    CompensationPlan,
    network_engine::config::MatrixStructureConfig,
) {
    use network_engine::config::MatrixStructureConfig;
    use network_engine::config::commission::LevelCommissionConfig;
    use network_engine::config::matrix::{MatrixStructureParams, SpilloverDirection};

    let mut rate_table = BTreeMap::new();
    for rank_name in &["associate", "silver"] {
        let mut rates = BTreeMap::new();
        for level in 1..=max_depth {
            rates.insert(level, 0.05);
        }
        rate_table.insert(rank_name.to_string(), rates);
    }

    let structure = MatrixStructureConfig {
        name: "Test".to_string(),
        matrix_params: MatrixStructureParams {
            width,
            height,
            spillover: SpilloverDirection::BreadthFirst,
        },
        level_commission: LevelCommissionConfig {
            broad_commission_percent: 0.40,
            volume_to_dollar_multiplier: None,
            max_depth,
            rate_table,
        },
        compression: None,
        pruning: None,
    };

    let mut plan = build_base_plan(
        eligibility,
        StructureConfig::Matrix(structure.clone()),
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
