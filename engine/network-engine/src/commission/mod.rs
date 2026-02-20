//! Commission calculation.

pub mod binary;
pub mod types;
pub mod unilevel;

pub use binary::calculate_binary_pairing;
pub use types::{
    BinaryCalculationResult, BinaryCommissionEarning, CalculationError, CommissionEarning,
    DistributorSnapshot, LegVolumes, VolumeSource,
};
pub use unilevel::calculate_unilevel;
