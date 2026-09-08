use std::collections::HashMap;

use network_engine::board_plan::BoardPlanEngine;
use network_engine::commission::PlanIdentity;
use network_engine::config::CompensationPlan;
use network_engine::streamline::StreamlineEngine;
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
    BoardPlan(BoardPlanEngine),
    Streamline(StreamlineEngine),
}

impl TreeInstance {
    /// Returns a reference to the tree as a `dyn TreeNavigator`.
    ///
    /// Returns `None` for board plan and streamline structures because
    /// they do not implement `TreeNavigator`.
    pub fn as_navigator(&self) -> Option<&dyn TreeNavigator> {
        match self {
            TreeInstance::Unilevel(t) => Some(t),
            TreeInstance::Binary(t) => Some(t),
            TreeInstance::Matrix(t) => Some(t),
            TreeInstance::BoardPlan(_) => None,
            TreeInstance::Streamline(_) => None,
        }
    }
}

#[derive(Default)]
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,

    /// Identity of the plan in `plan`. Set together with it and never apart:
    /// a calculation reports this to the caller, who compares it against the
    /// hash its run recorded before persisting any payout. An identity that
    /// described a different plan than the one loaded would defeat that check
    /// rather than fail it.
    pub plan_identity: Option<PlanIdentity>,

    pub trees: HashMap<String, TreeInstance>,
}
