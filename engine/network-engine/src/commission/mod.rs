//! Commission calculation.

pub mod binary;
pub mod generation;
pub mod matrix;
pub mod stairstep;
pub mod types;
pub mod unilevel;

#[cfg(test)]
pub(crate) mod test_helpers;

pub use binary::calculate_binary_pairing;
pub use matrix::calculate_matrix;
pub use stairstep::calculate_stairstep;
pub use types::{
    BinaryCalculationResult, BinaryCommissionEarning, CalculationError, CommissionEarning,
    DistributorSnapshot, LegVolumes, VolumeSource,
};
pub use unilevel::calculate_unilevel;

use crate::config::eligibility::CommissionEligibility;
use types::DistributorSnapshot as Snapshot;

/// Check if a distributor meets basic commission eligibility.
///
/// Evaluates minimum PV, order-in-period requirement, and status
/// against the plan's eligibility config. Used by both unilevel
/// and binary commission calculators.
pub(crate) fn is_eligible(snapshot: &Snapshot, eligibility: &CommissionEligibility) -> bool {
    if snapshot.personal_volume < eligibility.minimum_pv {
        return false;
    }

    if eligibility.require_order_in_period && !snapshot.has_order_in_period {
        return false;
    }

    if !eligibility.eligible_statuses.is_empty()
        && !eligibility.eligible_statuses.contains(&snapshot.status)
    {
        return false;
    }

    true
}
