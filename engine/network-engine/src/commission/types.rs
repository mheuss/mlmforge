//! Types for commission calculation.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use thiserror::Error;
use uuid::Uuid;

/// Point-in-time facts about a distributor for a commission period.
///
/// Contains only observable data. The calculator derives all eligibility
/// and depth decisions from the compensation plan config.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DistributorSnapshot {
    /// Current rank name. Must match a rank in the plan's rank ladder.
    pub rank: String,

    /// Personal volume generated this period.
    pub personal_volume: f64,

    /// Distributor's current status (e.g., "active", "grace", "suspended").
    pub status: String,

    /// Whether the distributor placed at least one order this period.
    pub has_order_in_period: bool,
}

/// A volume event that triggers commission calculation.
///
/// Each volume source produces one upline walk. The walk pays
/// commissions to eligible ancestors based on the rate table.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VolumeSource {
    /// The distributor who generated this volume.
    pub source_id: Uuid,

    /// Commission volume points generated.
    pub cv_amount: f64,
}

/// A single commission earning. One entry per earner per volume source.
///
/// The dollar amount formula:
/// `cv_amount * broad_commission_percent * volume_to_dollar_multiplier * rate`
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CommissionEarning {
    /// The distributor who earned this commission.
    pub earner_id: Uuid,

    /// The distributor whose volume triggered the earning.
    pub source_id: Uuid,

    /// Level in the (possibly compressed) upline walk. 1-indexed.
    pub level: u8,

    /// Rate table value applied at this level for this rank.
    pub rate: f64,

    /// Input commission volume from the source.
    pub cv_amount: f64,

    /// Final payout amount in the plan's base currency.
    pub dollar_amount: f64,
}

/// Errors that halt the entire commission calculation.
///
/// These indicate data integrity problems in the caller's input.
/// Recoverable issues (missing upline snapshots) are handled
/// defensively within the calculation.
#[derive(Debug, PartialEq, Error)]
pub enum CalculationError {
    /// A volume source references a distributor not in the tree.
    #[error("volume source {0} not found in tree")]
    SourceNotInTree(Uuid),

    /// A volume source references a distributor with no snapshot data.
    #[error("volume source {0} not found in snapshot data")]
    SourceNotInSnapshot(Uuid),

    /// A volume source has a non-finite or negative cv_amount.
    #[error("volume source {0} has invalid cv_amount: {1}")]
    InvalidCvAmount(Uuid, f64),
}

/// Per-distributor leg volumes carried from the previous period.
///
/// Used as both input (carry-forward from prior period) and output
/// (post-payout state for the next period) of binary commission
/// calculation.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct LegVolumes {
    pub left: f64,
    pub right: f64,
}

/// A single binary pairing commission earning.
///
/// One entry per distributor who earned a pairing bonus. No entry for
/// zero matched volume or ineligible distributors.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct BinaryCommissionEarning {
    /// The distributor (tree position) who earned this commission.
    pub earner_id: Uuid,

    /// Total volume in the left leg (current period + carry-forward).
    pub left_volume: f64,

    /// Total volume in the right leg (current period + carry-forward).
    pub right_volume: f64,

    /// Volume matched between legs: min(left, right).
    pub matched_volume: f64,

    /// Balance ratio applied. 1.0 for WeakerLeg, min/max for VolumeRatio.
    pub ratio: f64,

    /// The pairing percent from config.
    pub percent: f64,

    /// Final payout amount after multiplier and cap.
    pub dollar_amount: f64,

    /// True if cap_per_period reduced this earning.
    pub capped: bool,
}

/// Result of a binary pairing commission calculation.
///
/// Contains earnings for distributors who earned pairing bonuses
/// and updated carry-forward state for every distributor in the tree.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BinaryCalculationResult {
    /// Earnings for distributors who earned a pairing bonus.
    pub earnings: Vec<BinaryCommissionEarning>,

    /// Post-payout leg volumes for every distributor in the tree.
    /// Keyed by user_id. Includes non-earners (they accumulate volume).
    pub carry_forward: HashMap<Uuid, LegVolumes>,
}
