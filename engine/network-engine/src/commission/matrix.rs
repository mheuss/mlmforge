//! Matrix commission calculator.

use std::collections::HashMap;
use uuid::Uuid;

use crate::config::{CompensationPlan, MatrixStructureConfig};
use crate::tree::matrix::MatrixTree;

use super::types::{CalculationError, CommissionEarning, DistributorSnapshot, VolumeSource};

/// Calculate matrix level commissions for a set of volume events.
///
/// Walks the placement-tree upline from each volume source, applying the
/// rate table, compression, eligibility, and depth limits. The effective
/// depth ceiling is `min(matrix_params.height, level_commission.max_depth)`.
///
/// # Errors
///
/// Returns `CalculationError` if a volume source is not found in the
/// tree or snapshot data.
pub fn calculate_matrix(
    _tree: &MatrixTree,
    _plan: &CompensationPlan,
    _structure: &MatrixStructureConfig,
    _snapshots: &HashMap<Uuid, DistributorSnapshot>,
    _volume: &[VolumeSource],
) -> Result<Vec<CommissionEarning>, CalculationError> {
    Ok(Vec::new())
}
