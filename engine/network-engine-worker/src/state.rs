use std::collections::HashMap;

use network_engine::config::CompensationPlan;
use network_engine::tree::binary::BinaryTree;
use network_engine::tree::navigator::TreeNavigator;
use network_engine::tree::unilevel::UnilevelTree;

/// A named tree instance stored in the worker.
///
/// The worker supports multiple trees of different types, each
/// identified by a string name. Operations specify which tree
/// to target via the "structure" parameter.
pub enum TreeInstance {
    Unilevel(UnilevelTree),
    Binary(BinaryTree),
}

impl TreeInstance {
    /// Returns a reference to the tree as a `dyn TreeNavigator`.
    /// Used by query handlers to avoid matching on each tree type.
    pub fn as_navigator(&self) -> &dyn TreeNavigator {
        match self {
            TreeInstance::Unilevel(t) => t,
            TreeInstance::Binary(t) => t,
        }
    }
}

#[derive(Default)]
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,
    pub trees: HashMap<String, TreeInstance>,
}
