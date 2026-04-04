//! Shared test helpers for unit and integration tests.
//!
//! This module is intentionally public so `tests/` can reuse a single set of
//! constructors. It is not part of the stable runtime API for production
//! callers. Keep it narrow and biased toward obvious defaults.

use uuid::Uuid;

use crate::commission::DistributorSnapshot;
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
/// Takes a pre-wrapped `StructureConfig` and eligibility rules. Ranks, bonuses,
/// payout, caps, and placement use sensible test defaults.
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
        ranks: vec![make_rank("associate", 1, vec![structure_name.to_string()])],
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

/// Deterministic UUID from a test index.
///
/// Byte 15 is set to 0xFF to avoid collisions with the arena tombstone
/// sentinel (`Uuid::nil`). The index is encoded as a little-endian u128
/// in the remaining bytes.
pub fn uuid_from_index(i: usize) -> Uuid {
    let mut bytes = (i as u128).to_le_bytes();
    bytes[15] = 0xFF;
    Uuid::from_bytes(bytes)
}

/// Default "member" snapshot used by property tests.
pub fn member_snapshot() -> DistributorSnapshot {
    DistributorSnapshot {
        rank: "member".to_string(),
        personal_volume: 100.0,
        status: "active".to_string(),
        has_order_in_period: true,
    }
}

/// Snapshot with the standard active/test defaults but a custom rank.
pub fn snapshot_with_rank(rank: &str) -> DistributorSnapshot {
    DistributorSnapshot {
        rank: rank.to_string(),
        ..member_snapshot()
    }
}

/// Rank with empty qualification requirements and promotion-only demotion.
pub fn make_rank(name: &str, ordinal: u16, qualified_structures: Vec<String>) -> RankDefinition {
    RankDefinition {
        name: name.to_string(),
        ordinal,
        qualification: RankQualification {
            structures: vec![],
            required_products: vec![],
        },
        qualified_structures,
        demotion_policy: DemotionPolicy::PromotionOnly,
    }
}
