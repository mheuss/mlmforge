//! Unilevel commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::{CompensationPlan, UnilevelStructureConfig};
use crate::tree::unilevel::UnilevelTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

/// Calculate unilevel commissions for a set of volume events.
///
/// Walks the upline from each volume source, applying the rate table,
/// compression, eligibility, and depth limits from the plan config.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data.
pub fn calculate_unilevel(
    _tree: &UnilevelTree,
    _plan: &CompensationPlan,
    _structure: &UnilevelStructureConfig,
    _snapshots: &HashMap<Uuid, DistributorSnapshot>,
    _volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    todo!()
}
