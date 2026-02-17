//! Commission calculation.

pub mod types;
pub mod unilevel;

pub use types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};
pub use unilevel::calculate_unilevel;
