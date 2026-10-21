use crate::config::eligibility::CommissionEligibility;
use crate::d;
pub use crate::test_support::{build_test_plan, make_rank, uuid_from_index};

/// Default eligibility config for tests.
///
/// Minimum PV = 100, no order requirement, only "active" status eligible.
pub fn default_eligibility() -> CommissionEligibility {
    CommissionEligibility {
        minimum_pv: d!(100.0),
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
        personal_volume: d!(150.0),
        status: "active".to_string(),
        has_order_in_period: true,
    }
}
