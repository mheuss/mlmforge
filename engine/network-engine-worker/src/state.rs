use std::collections::HashMap;

use network_engine::board_plan::BoardPlanEngine;
use network_engine::config::CompensationPlan;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::matrix::MatrixTree;
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;

/// A named tree instance stored in the worker.
///
/// The worker supports multiple structure types, each identified by a
/// string name. Operations specify which structure to target via the
/// "structure" parameter.
pub enum TreeInstance {
    Unilevel(UnilevelTree),
    Binary(BinaryTree),
    Matrix(MatrixTree),
    #[allow(dead_code)] // Constructed in board plan handlers (added in next commit).
    BoardPlan(BoardPlanEngine),
}

impl TreeInstance {
    /// Returns a reference to the tree as a `dyn TreeNavigator`.
    ///
    /// Returns `None` for board plan structures because `BoardPlanEngine`
    /// does not implement `TreeNavigator`. Board plans use their own
    /// query interface instead of tree traversals.
    pub fn as_navigator(&self) -> Option<&dyn TreeNavigator> {
        match self {
            TreeInstance::Unilevel(t) => Some(t),
            TreeInstance::Binary(t) => Some(t),
            TreeInstance::Matrix(t) => Some(t),
            TreeInstance::BoardPlan(_) => None,
        }
    }
}

#[derive(Default)]
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,
    pub trees: HashMap<String, TreeInstance>,
}
