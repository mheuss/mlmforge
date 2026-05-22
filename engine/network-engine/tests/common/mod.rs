// Each integration test compiles this module independently. Functions used
// only by some test files appear unused in others.
#![allow(dead_code)]

use network_engine::config::binary::{
    BinaryCommissionConfig, BinaryCommissionMode, CycleStepConfig, MultiPositionCapMode,
    PairingCalculation, PairingConfig, VolumeAfterPayout,
};
use network_engine::config::eligibility::CommissionEligibility;
use network_engine::config::stairstep::{
    BreakawayConfig, BreakawayTier, DifferentialConfig, MultiTierConfig, OverrideMode,
    OverrideStrategy,
};
use network_engine::config::{
    BinaryStructureConfig, CompensationPlan, StairstepStructureConfig, StructureConfig,
    UnilevelStructureConfig,
};
use network_engine::test_support::{build_test_plan, make_rank};
#[allow(unused_imports)]
pub use network_engine::test_support::{member_snapshot, snapshot_with_rank, uuid_from_index};
use std::collections::BTreeMap;

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
    let mut plan = build_test_plan(eligibility, structure, structure_name);
    plan.ranks = vec![make_rank("member", 1, vec![structure_name.to_string()])];
    plan
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

// --- Binary CycleStep plan builder ---

/// Build a binary plan with CycleStep config for property tests.
pub fn build_binary_cycle_step_plan(
    config: CycleStepConfig,
) -> (CompensationPlan, BinaryStructureConfig) {
    let structure = BinaryStructureConfig {
        name: "Test Binary".to_string(),
        binary_commission: BinaryCommissionConfig {
            volume_to_dollar_multiplier: None,
            mode: BinaryCommissionMode::CycleStep(config),
        },
    };
    let plan = build_base_plan(
        permissive_eligibility(),
        StructureConfig::Binary(structure.clone()),
        "Test Binary",
    );
    (plan, structure)
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
        make_rank("associate", 1, vec!["Test".to_string()]),
        make_rank("silver", 2, vec!["Test".to_string()]),
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
            overrides: OverrideStrategy::SingleWalk {
                mode: OverrideMode::Differential(DifferentialConfig {
                    rank_rates: {
                        let mut m = BTreeMap::new();
                        m.insert("director".to_string(), 0.10);
                        m
                    },
                    min_override: 0.0,
                }),
                generation_overrides: None,
            },
        }),
    };

    let mut plan = build_base_plan(
        eligibility,
        StructureConfig::Stairstep(structure.clone()),
        "Test",
    );

    plan.ranks
        .push(make_rank("director", 2, vec!["Test".to_string()]));

    (plan, structure)
}

/// Build a stairstep plan with a multi-tier breakaway override ladder.
///
/// Mirrors `build_stairstep_plan`: two ranks ("member" and "director"),
/// "director" as `threshold_rank`, 5% level rates, max depth 5, fully
/// permissive eligibility. Caller supplies the tier ladder.
pub fn build_multi_tier_stairstep_plan(
    tiers: Vec<BreakawayTier>,
) -> (
    CompensationPlan,
    network_engine::config::StairstepStructureConfig,
) {
    use network_engine::config::commission::LevelCommissionConfig;

    let max_depth = 5;
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
            overrides: OverrideStrategy::MultiTier(MultiTierConfig { tiers }),
        }),
    };

    let mut plan = build_base_plan(
        permissive_eligibility(),
        StructureConfig::Stairstep(structure.clone()),
        "Test",
    );

    plan.ranks
        .push(make_rank("director", 2, vec!["Test".to_string()]));

    (plan, structure)
}

// --- Generation plan builder ---

/// Build a generation plan for property and integration tests.
///
/// ThresholdRank mode with "director" as boundary rank. Two ranks:
/// "associate" (ordinal 1) and "director" (ordinal 2). No level
/// commissions. Eligibility is fully permissive by default.
pub fn build_generation_plan(
    max_generations: u8,
) -> (
    CompensationPlan,
    network_engine::config::GenerationStructureConfig,
) {
    build_generation_plan_with_eligibility(max_generations, permissive_eligibility())
}

/// Build a generation plan with custom eligibility.
pub fn build_generation_plan_with_eligibility(
    max_generations: u8,
    eligibility: CommissionEligibility,
) -> (
    CompensationPlan,
    network_engine::config::GenerationStructureConfig,
) {
    use network_engine::config::GenerationStructureConfig;
    use network_engine::config::generation::{GenerationBoundaryMode, GenerationCommissionConfig};

    let mut rates = BTreeMap::new();
    for g in 1..=max_generations {
        // Decreasing rates: gen 1 = 0.10, gen 2 = 0.07, gen 3 = 0.05, gen 4 = 0.03
        let rate = match g {
            1 => 0.10,
            2 => 0.07,
            3 => 0.05,
            4 => 0.03,
            _ => 0.02,
        };
        rates.insert(g, rate);
    }

    let structure = GenerationStructureConfig {
        name: "Generation".to_string(),
        level_commission: None,
        compression: None,
        generation_commission: GenerationCommissionConfig {
            max_generations,
            max_generations_per_rank: BTreeMap::new(),
            rates,
            boundary_mode: GenerationBoundaryMode::ThresholdRank,
            boundary_rank: "director".to_string(),
            empty_generation_consumes_number: false,
            volume_to_dollar_multiplier: None,
            ineligible_creates_boundary: true,
        },
        level_commissions_enabled: false,
    };

    let mut plan = build_base_plan(
        eligibility,
        StructureConfig::Generation(structure.clone()),
        "Generation",
    );

    plan.ranks = vec![
        make_rank("associate", 1, vec!["Generation".to_string()]),
        make_rank("director", 2, vec!["Generation".to_string()]),
    ];

    (plan, structure)
}

/// Build a SameRank generation plan for property tests.
///
/// Three ranks: "associate" (1), "silver" (2), "director" (3).
/// SameRank boundary mode. Eligibility is fully permissive.
pub fn build_same_rank_generation_plan(
    max_generations: u8,
) -> (
    CompensationPlan,
    network_engine::config::GenerationStructureConfig,
) {
    use network_engine::config::GenerationStructureConfig;
    use network_engine::config::generation::{GenerationBoundaryMode, GenerationCommissionConfig};

    let mut rates = BTreeMap::new();
    for g in 1..=max_generations {
        let rate = match g {
            1 => 0.10,
            2 => 0.07,
            3 => 0.05,
            4 => 0.03,
            _ => 0.02,
        };
        rates.insert(g, rate);
    }

    let structure = GenerationStructureConfig {
        name: "Generation".to_string(),
        level_commission: None,
        compression: None,
        generation_commission: GenerationCommissionConfig {
            max_generations,
            max_generations_per_rank: BTreeMap::new(),
            rates,
            boundary_mode: GenerationBoundaryMode::SameRank,
            boundary_rank: "unused_in_same_rank_mode".to_string(),
            empty_generation_consumes_number: false,
            volume_to_dollar_multiplier: None,
            ineligible_creates_boundary: true,
        },
        level_commissions_enabled: false,
    };

    let mut plan = build_base_plan(
        permissive_eligibility(),
        StructureConfig::Generation(structure.clone()),
        "Generation",
    );

    plan.ranks = vec![
        make_rank("associate", 1, vec!["Generation".to_string()]),
        make_rank("silver", 2, vec!["Generation".to_string()]),
        make_rank("director", 3, vec!["Generation".to_string()]),
    ];

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
        make_rank("associate", 1, vec!["Test".to_string()]),
        make_rank("silver", 2, vec!["Test".to_string()]),
    ];

    (plan, structure)
}
