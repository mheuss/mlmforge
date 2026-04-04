use crate::config::eligibility::CommissionEligibility;
pub use crate::test_support::{build_test_plan, uuid_from_index};

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
