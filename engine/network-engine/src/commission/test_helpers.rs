//! Shared test helpers for commission calculators.

use crate::config::bonus::BonusConfig;
use crate::config::eligibility::CommissionEligibility;
use crate::config::payout::{CapEnforcement, CapsConfig, PayoutConfig, PayoutMethod};
use crate::config::period::{PeriodConfig, PeriodLength};
use crate::config::placement::PlacementConfig;
use crate::config::rank::{
    DemotionPolicy, RankDefinition, RankFeaturesConfig, RankQualification, RankTrackingConfig,
};
use crate::config::volume::VolumeConfig;
use crate::config::{CompensationPlan, StructureConfig};

/// Build a minimal `CompensationPlan` for testing.
///
/// Takes a pre-wrapped `StructureConfig` and eligibility rules.
/// Ranks, bonuses, payout, caps, and placement use sensible test defaults.
/// The structure name in `qualified_structures` defaults to "Test".
/// Callers needing specific rank names should modify the returned plan.
pub fn build_test_plan(
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
            name: "associate".to_string(),
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

/// Default eligibility config for tests.
///
/// Minimum PV = 100, no order requirement, only "active" status eligible.
pub fn default_eligibility() -> CommissionEligibility {
    CommissionEligibility {
        minimum_pv: 100.0,
        require_order_in_period: false,
        eligible_statuses: vec!["active".to_string()],
        active_leg_tiers: vec![],
    }
}

/// Default eligible distributor snapshot.
///
/// Rank "associate", PV 150, status "active", has order.
pub fn eligible_snapshot() -> super::types::DistributorSnapshot {
    super::types::DistributorSnapshot {
        rank: "associate".to_string(),
        personal_volume: 150.0,
        status: "active".to_string(),
        has_order_in_period: true,
    }
}
