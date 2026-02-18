use network_engine::config::CompensationPlan;
use network_engine::tree::unilevel::UnilevelTree;

#[derive(Default)]
pub struct WorkerState {
    pub plan: Option<CompensationPlan>,
    pub unilevel_tree: Option<UnilevelTree>,
}
